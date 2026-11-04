package ticket

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"time"
)

type TicketEmailWorkerConfig struct {
	PollingInterval time.Duration
	LeaseDuration   time.Duration
	SendTimeout     time.Duration
	MaxAttempts     int
}

type TicketEmailWorker struct {
	store  TicketEmailStore
	sender TicketEmailSender
	config TicketEmailWorkerConfig
}

func NewTicketEmailWorker(store TicketEmailStore, sender TicketEmailSender, config TicketEmailWorkerConfig) (*TicketEmailWorker, error) {
	if store == nil {
		return nil, errors.New("ticket email store is required")
	}

	if sender == nil {
		return nil, errors.New("ticket email sender is required")
	}

	if config.PollingInterval <= 0 {
		return nil, errors.New("ticket email polling interval must be positive")
	}

	if config.SendTimeout <= 0 {
		return nil, errors.New("ticket email send timeout must be positive")
	}
	if config.SendTimeout < time.Microsecond {
		return nil, errors.New("ticket email send timeout must be at least one microsecond")
	}

	if config.LeaseDuration <= config.SendTimeout {
		return nil, errors.New("ticket email lease duration must exceed the send timeout")
	}
	if config.LeaseDuration < time.Microsecond {
		return nil, errors.New("ticket email lease duration must be at least one microsecond")
	}

	if config.MaxAttempts <= 0 {
		return nil, errors.New("ticket email maximum attempts must be positive")
	}

	return &TicketEmailWorker{store: store, sender: sender, config: config}, nil
}

func (w *TicketEmailWorker) Run(ctx context.Context) error {
	for {
		job, found, err := w.store.ClaimNextTicketEmail(ctx, w.config.LeaseDuration)
		if err != nil {
			return fmt.Errorf("claim ticket email: %w", err)
		}

		if !found {
			if err := waitForTicketEmail(ctx, w.config.PollingInterval); err != nil {
				return err
			}
			continue
		}

		if err := w.process(ctx, job); err != nil {
			return err
		}
	}
}

func (w *TicketEmailWorker) process(ctx context.Context, job TicketEmailJob) error {
	email, err := w.store.LoadTicketEmail(ctx, job)
	if err == nil {
		sendCtx, cancel := context.WithTimeout(ctx, w.config.SendTimeout)
		err = w.sender.SendTicketEmail(sendCtx, email)

		cancel()
	}

	if err == nil {
		if err := w.store.MarkTicketEmailSent(ctx, job); err != nil && !errors.Is(err, ErrEmailClaimLost) {
			return fmt.Errorf("mark ticket email %d sent: %w", job.ID, err)
		}

		return nil
	}

	if ctx.Err() != nil {
		// Leave the row leased; another worker will reclaim it after expiry.
		return ctx.Err()
	}

	terminal := job.AttemptCount >= w.config.MaxAttempts
	retryAt := nextTicketEmailRetry(job.AttemptCount)
	if terminal {
		retryAt = time.Now().UTC()
	}

	if err := w.store.RetryTicketEmail(ctx, job, retryAt, terminal); err != nil && !errors.Is(err, ErrEmailClaimLost) {
		return fmt.Errorf("schedule retry for ticket email %d: %w", job.ID, err)
	}

	return nil
}

func waitForTicketEmail(ctx context.Context, interval time.Duration) error {
	timer := time.NewTimer(interval)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func nextTicketEmailRetry(attempt int) time.Time {
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
