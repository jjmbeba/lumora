package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/jjmbeba/lumora/backend/internal/payment"
	"github.com/jjmbeba/lumora/backend/internal/payment/postgres/dbgen"
)

type WebhookStore struct {
	db *sql.DB
}

func NewWebhookStore(db *sql.DB) (*WebhookStore, error) {
	if db == nil {
		return nil, ErrDatabasePoolRequired
	}

	return &WebhookStore{
		db: db,
	}, nil
}

func (h *WebhookStore) RecordAndSchedule(ctx context.Context, event payment.ProviderWebhookEvent) error {
	tx, err := h.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin record event transaction err: %w", err)
	}
	defer tx.Rollback()

	queries := dbgen.New(tx)
	if err := queries.RecordWebhookEvent(ctx, dbgen.RecordWebhookEventParams{
		EventType: event.EventType, TransactionID: event.TransactionID, Reference: event.Reference,
		ProviderPaidAt: event.ProviderPaidAt, ProviderStatus: event.ProviderStatus,
	}); err != nil {
		return fmt.Errorf("begin record event to webhook inbox err: %w", err)
	}

	if err := queries.ScheduleVerificationFromWebhook(ctx, event.Reference); err != nil {
		return fmt.Errorf("schedule payment verification from webhook: %w", err)
	}

	// A success notification may arrive after an abandoned checkout was
	// cancelled and its seats released. Quarantine the payment for staff review;
	// never restore claims that may already belong to another booking.
	paymentAttemptID, err := queries.MoveCancelledInitializationToReview(ctx, event.Reference)
	if err == nil {
		if err := queries.MoveBookingToPaymentReview(ctx, paymentAttemptID); err != nil {
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
