package staff

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"net"
	"net/url"
	"strings"
	"time"
)

type InvitationEmailJob struct {
	ID           int
	InvitationID int
	Recipient    string
	EventKey     string
	AttemptCount int
}

type InvitationEmailDelivery struct {
	Email           string
	TokenCiphertext []byte
	TokenKeyVersion int16
	ExpiresAt       time.Time
}

type StaffInvitationEmail struct {
	To             string
	SetupURL       string
	IdempotencyKey string
	ExpiresAt      time.Time
}

type InvitationEmailStore interface {
	ClaimNextInvitationEmail(context.Context, time.Duration) (InvitationEmailJob, bool, error)
	LoadInvitationEmail(context.Context, InvitationEmailJob) (InvitationEmailDelivery, error)
	MarkInvitationEmailSent(context.Context, InvitationEmailJob) error
	RetryInvitationEmail(context.Context, InvitationEmailJob, time.Time, bool) error
}

type InvitationEmailSender interface {
	SendStaffInvitationEmail(context.Context, StaffInvitationEmail) error
}

type InvitationEmailWorkerConfig struct {
	SetupURL        string
	PollingInterval time.Duration
	LeaseDuration   time.Duration
	SendTimeout     time.Duration
	MaxAttempts     int
}

type InvitationEmailWorker struct {
	store    InvitationEmailStore
	sender   InvitationEmailSender
	cipher   *InvitationTokenCipher
	config   InvitationEmailWorkerConfig
	setupURL url.URL
}

func NewInvitationEmailWorker(store InvitationEmailStore, sender InvitationEmailSender, cipher *InvitationTokenCipher, config InvitationEmailWorkerConfig) (*InvitationEmailWorker, error) {
	if store == nil || sender == nil || cipher == nil {
		return nil, errors.New("staff invitation email worker requires a store, sender, and token cipher")
	}

	setupURL, err := url.Parse(strings.TrimSpace(config.SetupURL))
	if err != nil || setupURL == nil || setupURL.Host == "" || setupURL.User != nil || setupURL.Fragment != "" {
		return nil, errors.New("staff invitation setup URL must be an absolute URL without credentials or a fragment")
	}

	localHTTP := setupURL.Scheme == "http" && isLoopbackHost(setupURL.Hostname())
	if setupURL.Scheme != "https" && !localHTTP {
		return nil, errors.New("staff invitation setup URL must use HTTPS except for a loopback development URL")
	}

	if config.PollingInterval <= 0 || config.SendTimeout <= 0 || config.SendTimeout < time.Microsecond {
		return nil, errors.New("staff invitation email polling interval and send timeout must be positive")
	}

	if config.LeaseDuration <= config.SendTimeout || config.LeaseDuration < time.Microsecond {
		return nil, errors.New("staff invitation email lease duration must exceed the send timeout")
	}

	if config.MaxAttempts <= 0 {
		return nil, errors.New("staff invitation email maximum attempts must be positive")
	}

	return &InvitationEmailWorker{store: store, sender: sender, cipher: cipher, config: config, setupURL: *setupURL}, nil
}

func (w *InvitationEmailWorker) Run(ctx context.Context) error {
	for {
		job, found, err := w.store.ClaimNextInvitationEmail(ctx, w.config.LeaseDuration)
		if err != nil {
			return fmt.Errorf("claim staff invitation email: %w", err)
		}

		if !found {
			if err := waitForInvitationEmail(ctx, w.config.PollingInterval); err != nil {
				return err
			}
			continue
		}

		if err := w.process(ctx, job); err != nil {
			return err
		}
	}
}

func (w *InvitationEmailWorker) process(ctx context.Context, job InvitationEmailJob) error {
	delivery, err := w.store.LoadInvitationEmail(ctx, job)
	if errors.Is(err, ErrInvitationEmailClaimLost) {
		return nil
	}

	if err == nil {
		var token string

		token, err = w.cipher.Decrypt(delivery.Email, delivery.TokenKeyVersion, delivery.TokenCiphertext)
		if err == nil && !isOpaqueToken(token) {
			err = errors.New("decrypted staff invitation token is invalid")
		}

		if err == nil && !delivery.ExpiresAt.After(time.Now()) {
			err = ErrInvitationExpired
		}

		if err == nil {
			setupURL := w.setupURL
			query := setupURL.Query()
			query.Set("token", token)
			setupURL.RawQuery = query.Encode()
			sendCtx, cancel := context.WithTimeout(ctx, w.config.SendTimeout)
			err = w.sender.SendStaffInvitationEmail(sendCtx, StaffInvitationEmail{
				To: delivery.Email, SetupURL: setupURL.String(), IdempotencyKey: job.EventKey, ExpiresAt: delivery.ExpiresAt,
			})

			cancel()
		}
	}

	if err == nil {
		if err := w.store.MarkInvitationEmailSent(ctx, job); err != nil && !errors.Is(err, ErrInvitationEmailClaimLost) {
			return fmt.Errorf("mark staff invitation email %d sent: %w", job.ID, err)
		}

		return nil
	}

	if ctx.Err() != nil {
		return ctx.Err()
	}

	terminal := errors.Is(err, ErrInvitationExpired) || job.AttemptCount >= w.config.MaxAttempts
	retryAt := nextInvitationEmailRetry(job.AttemptCount)
	if terminal {
		retryAt = time.Now().UTC()
	}

	if err := w.store.RetryInvitationEmail(ctx, job, retryAt, terminal); err != nil && !errors.Is(err, ErrInvitationEmailClaimLost) {
		return fmt.Errorf("schedule retry for staff invitation email %d: %w", job.ID, err)
	}

	return nil
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}

	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func waitForInvitationEmail(ctx context.Context, interval time.Duration) error {
	timer := time.NewTimer(interval)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func nextInvitationEmailRetry(attempt int) time.Time {
	const (
		baseDelay = 5 * time.Second
		maxDelay  = 5 * time.Minute
	)

	if attempt < 1 {
		attempt = 1
	}

	backoff := baseDelay
	for n := 1; n < attempt && backoff < maxDelay; n++ {
		backoff = min(backoff*2, maxDelay)
	}

	jitter := time.Duration(rand.Int64N(int64(backoff/2) + 1))
	return time.Now().UTC().Add(backoff/2 + jitter)
}
