package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"example.com/cinema-tickets-backend/internal/payment"
)

type WebhookStore struct {
	db *sql.DB
}

func NewWebhookStore(db *sql.DB) (*WebhookStore, error) {
	if db == nil {
		return nil, errors.New("database pool is required")
	}

	return &WebhookStore{
		db: db,
	}, nil
}

func (h *WebhookStore) RecordAndSchedule(ctx context.Context, event payment.ProviderWebhookEvent) error {
	const recordEventSQL = `
	INSERT INTO webhook_inbox (event_type, transaction_id, reference, receipt_time, provider_paid_at, provider_status)
	VALUES($1, $2, $3, clock_timestamp(), $4, $5)
	ON CONFLICT (reference) DO NOTHING
	`

	const scheduleVerification = `
	UPDATE payment_initializations
	SET next_attempt_at = clock_timestamp(),
		updated_at = clock_timestamp()
	WHERE paystack_reference = $1
		AND status IN ('READY', 'UNKNOWN')
	`
	const moveCancelledInitializationToReview = `
		UPDATE payment_initializations
		SET status = 'REVIEW_REQUIRED',
			lease_expires_at = NULL,
			next_attempt_at = NULL,
			updated_at = clock_timestamp()
		WHERE paystack_reference = $1 AND status = 'CANCELLED'
		RETURNING payment_attempt_id
	`
	const moveBookingToReview = `
		UPDATE bookings
		SET status = 'PAYMENT_REVIEW'
		WHERE id = (SELECT booking_id FROM payment_attempts WHERE id = $1)
			AND status <> 'CANCELLED'
	`

	tx, err := h.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin record event transaction err: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, recordEventSQL, event.EventType, event.TransactionID, event.Reference, event.ProviderPaidAt, event.ProviderStatus); err != nil {
		return fmt.Errorf("begin record event to webhook inbox err: %w", err)
	}

	if _, err := tx.ExecContext(ctx, scheduleVerification, event.Reference); err != nil {
		return fmt.Errorf("schedule payment verification from webhook: %w", err)
	}

	// A success notification may arrive after an abandoned checkout was
	// cancelled and its seats released. Quarantine the payment for staff review;
	// never restore claims that may already belong to another booking.
	var paymentAttemptID int
	err = tx.QueryRowContext(ctx, moveCancelledInitializationToReview, event.Reference).Scan(&paymentAttemptID)
	if err == nil {
		if _, err := tx.ExecContext(ctx, moveBookingToReview, paymentAttemptID); err != nil {
			return fmt.Errorf("move late-success booking to payment review: %w", err)
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("move cancelled initialization to payment review: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("transaction commit err: %w", err)
	}

	return nil
}
