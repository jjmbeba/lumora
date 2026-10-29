package payment

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"time"
)

type Worker struct {
	store           Store
	provider        Provider
	pollingInterval time.Duration
}

func NewWorker(store Store, provider Provider, pollingInterval time.Duration) (*Worker, error) {
	if pollingInterval <= 0 {
		return nil, fmt.Errorf("Polling interval must be greater than 0")
	}

	return &Worker{
		store:           store,
		provider:        provider,
		pollingInterval: pollingInterval,
	}, nil
}

func (w *Worker) Run(ctx context.Context) error {
	for {
		claim, found, err := w.store.ClaimNext(ctx)
		if err != nil {
			return err
		}

		if !found {
			timer := time.NewTimer(w.pollingInterval)

			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
				continue
			}
		}

		if err := w.process(ctx, claim); err != nil {
			return err
		}
	}
}

func calculateRetryAt(attempt int) time.Time {
	const (
		baseDelay = 5 * time.Second
		maxDelay  = 5 * time.Minute
	)

	if attempt < 1 {
		attempt = 1
	}

	backoff := baseDelay
	for claimNumber := 1; claimNumber < attempt && backoff < maxDelay; claimNumber++ {
		backoff = min(backoff*2, maxDelay)
	}

	// Equal jitter keeps the delay between half the capped backoff and the
	// full capped backoff, avoiding synchronized retries without retrying early.
	jitter := time.Duration(rand.Int64N(int64(backoff/2) + 1))
	delay := backoff/2 + jitter

	return time.Now().UTC().Add(delay)
}

func (w *Worker) process(ctx context.Context, claim Claim) error {
	switch claim.PreviousStatus {
	case InitializationStatusUnknown, InitializationStatusSubmitting, InitializationStatusReady:
		result, err := w.provider.Verify(ctx, claim.PaystackReference)
		if err != nil {
			if err := w.scheduleRetry(ctx, claim); err != nil {
				return err
			}

			return nil
		}

		switch result.VerificationStatus {
		case VerificationStatusPending:
			if claim.PreviousStatus == InitializationStatusReady {
				nextCheckAt := calculateRetryAt(max(claim.Generation, 1))
				if err := w.store.MarkVerificationPending(ctx, claim, nextCheckAt); err != nil {
					if errors.Is(err, ErrClaimLost) {
						return nil
					}

					return err
				}

				return nil
			}

			if err := w.scheduleRetry(ctx, claim); err != nil {
				return err
			}

			return nil

		case VerificationStatusUnknown, VerificationStatusInvalid:
			if err := w.scheduleRetry(ctx, claim); err != nil {
				return err
			}

			return nil

		case VerificationStatusFailed:
			if err := w.store.MarkFailed(ctx, claim); err != nil {
				if errors.Is(err, ErrClaimLost) {
					return nil
				}

				return err
			}

		case VerificationStatusCancelled:
			if err := w.store.MarkPaymentCancelled(ctx, claim); err != nil {
				if errors.Is(err, ErrClaimLost) {
					return nil
				}

				return err
			}

		case VerificationStatusReversed:
			if err := w.store.MarkReversed(ctx, claim); err != nil {
				if errors.Is(err, ErrClaimLost) {
					return nil
				}

				return err
			}

		case VerificationStatusPaid:
			if err := w.store.MarkPaid(ctx, claim, result); err != nil {
				if errors.Is(err, ErrClaimLost) {
					return nil
				}

				return err
			}

		default:
			if err := w.scheduleRetry(ctx, claim); err != nil {
				return err
			}

			return nil
		}

	case InitializationStatusQueued:
		newRequest := InitRequest{
			AmountMinor:   claim.AmountMinor,
			Email:         claim.PayerEmail,
			PhoneNumber:   claim.PayerPhoneNumber,
			Currency:      claim.Currency,
			Reference:     claim.PaystackReference,
			PaymentMethod: claim.PaymentMethod,
		}

		result, err := w.provider.Initialize(ctx, newRequest)
		if err != nil {
			if err := w.scheduleRetry(ctx, claim); err != nil {
				return err
			}
			return nil
		}

		switch result.Status {
		case ResultStatusCheckoutCreated:
			if result.AuthorizationURL == "" || result.Reference != claim.PaystackReference {
				if err := w.scheduleRetry(ctx, claim); err != nil {
					return err
				}
				return nil
			}

			attempt := max(claim.Generation, 1)
			nextCheckAt := calculateRetryAt(attempt)

			if err := w.store.MarkReady(ctx, claim, result.AuthorizationURL, nextCheckAt); err != nil {
				if errors.Is(err, ErrClaimLost) {
					return nil
				}

				return err
			}

		case ResultStatusRejected:
			if err := w.store.MarkRejected(ctx, claim, result.Message); err != nil {
				if errors.Is(err, ErrClaimLost) {
					return nil
				}

				return err
			}

		case ResultStatusCancelled:
			if err := w.store.MarkCancelled(ctx, claim); err != nil {
				if errors.Is(err, ErrClaimLost) {
					return nil
				}

				return err
			}

		case ResultStatusUnknown, ResultStatusInvalid:
			if err := w.scheduleRetry(ctx, claim); err != nil {
				return err
			}

		default:
			if err := w.scheduleRetry(ctx, claim); err != nil {
				return err
			}
		}

	default:
		return fmt.Errorf("Claim of Initialization ID %d is not eligible for processing", claim.InitializationID)
	}

	return nil
}

func (w *Worker) scheduleRetry(ctx context.Context, claim Claim) error {
	attempt := max(claim.Generation, 1)
	retryAt := calculateRetryAt(attempt)

	if err := w.store.MarkUnknown(ctx, claim, retryAt); err != nil {
		if errors.Is(err, ErrClaimLost) {
			return nil
		}

		return err
	}

	return nil
}
