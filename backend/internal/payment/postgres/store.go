package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/jjmbeba/lumora/backend/internal/booking"
	"github.com/jjmbeba/lumora/backend/internal/payment"
	"github.com/jjmbeba/lumora/backend/internal/payment/postgres/dbgen"
	"github.com/jjmbeba/lumora/backend/internal/ticket"
)

type PostgresStore struct {
	db                *sql.DB
	leaseDuration     time.Duration
	ticketCredentials *ticket.CredentialCipher
}

func NewPostgresStore(db *sql.DB, leaseDuration time.Duration, ticketCredentials *ticket.CredentialCipher) (*PostgresStore, error) {
	if db == nil {
		return nil, ErrDatabasePoolRequired
	}
	if ticketCredentials == nil {
		return nil, ErrTicketCredentialCipherRequired
	}

	if leaseDuration < time.Microsecond {
		return nil, errors.New("lease duration must be at least one microsecond")
	}

	return &PostgresStore{
		db:                db,
		leaseDuration:     leaseDuration,
		ticketCredentials: ticketCredentials,
	}, nil
}

func (p *PostgresStore) ClaimNext(ctx context.Context) (payment.Claim, bool, error) {
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return payment.Claim{}, false, fmt.Errorf("begin claim transaction: %w", err)
	}
	defer tx.Rollback()

	row, err := dbgen.New(tx).ClaimNextPaymentInitialization(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		if err := tx.Commit(); err != nil {
			return payment.Claim{}, false, fmt.Errorf("commit empty claim transaction: %w", err)
		}
		return payment.Claim{}, false, nil
	}

	if err != nil {
		return payment.Claim{}, false, fmt.Errorf("select next payment initialization: %w", err)
	}

	claim := payment.Claim{InitializationID: int(row.InitializationID), PaystackReference: row.PaystackReference, Currency: row.Currency, AmountMinor: int64(row.Amount), PayerEmail: row.CustomerEmail, PayerPhoneNumber: row.CustomerPhone}

	claim.PreviousStatus, err = parseInitializationStatus(row.PreviousStatus)
	if err != nil {
		return payment.Claim{}, false, err
	}

	claim.PaymentMethod, err = parsePaymentMethod(row.PaymentMethod)
	if err != nil {
		return payment.Claim{}, false, err
	}

	generation, err := dbgen.New(tx).ClaimPaymentInitialization(ctx, dbgen.ClaimPaymentInitializationParams{
		ID:          int32(claim.InitializationID),
		LeaseMicros: float64(p.leaseDuration.Microseconds()),
	})
	if err != nil {
		return payment.Claim{}, false, fmt.Errorf("update claimed payment initialization: %w", err)
	}
	claim.Generation = int(generation)

	if err := tx.Commit(); err != nil {
		return payment.Claim{}, false, fmt.Errorf("commit claim transaction: %w", err)
	}

	return claim, true, nil
}

func (p *PostgresStore) MarkReady(ctx context.Context, claim payment.Claim, checkoutURL string, nextCheckAt time.Time) error {
	if checkoutURL == "" {
		return errors.New("Checkout URL is empty")
	}

	rows, err := dbgen.New(p.db).MarkPaymentInitializationReady(ctx, dbgen.MarkPaymentInitializationReadyParams{
		AuthorizationUrl:   sql.NullString{String: checkoutURL, Valid: true},
		NextAttemptAt:      nextCheckAt,
		ID:                 int32(claim.InitializationID),
		WorkerAttemptCount: int32(claim.Generation),
	})
	if err != nil {
		return fmt.Errorf("mark ready: %w", err)
	}
	if err := checkClaimRows("mark ready", rows); err != nil {
		return err
	}

	return nil
}

func (p *PostgresStore) MarkVerificationPending(ctx context.Context, claim payment.Claim, nextCheckAt time.Time) error {
	return p.finishPendingVerification(ctx, claim, "READY", nextCheckAt)
}

// finishPendingVerification reschedules a still-open payment, or quarantines it
// once its booking hold has expired. The hold decision and claim fence must be
// checked in the same transaction as the transition.
func (p *PostgresStore) finishPendingVerification(ctx context.Context, claim payment.Claim, nextStatus string, nextCheckAt time.Time) error {
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin schedule pending verification transaction: %w", err)
	}
	defer tx.Rollback()

	row, err := dbgen.New(tx).LockClaimForPendingVerification(ctx, dbgen.LockClaimForPendingVerificationParams{ID: int32(claim.InitializationID), WorkerAttemptCount: int32(claim.Generation)})
	if errors.Is(err, sql.ErrNoRows) {
		return payment.ErrClaimLost
	}
	if err != nil {
		return fmt.Errorf("lock payment state before scheduling verification: %w", err)
	}

	bookingID := int(row.BookingID)
	holdExpiresAt, holdExpired, successWebhookRecorded := row.HoldExpiresAt, row.HoldExpired, row.SuccessWebhookRecorded
	parsedPaymentStatus, err := parsePaymentStatus(row.PaymentStatus)
	if err != nil {
		return err
	}
	parsedBookingStatus, err := parseBookingStatus(row.BookingStatus)
	if err != nil {
		return err
	}

	if holdExpired || parsedPaymentStatus != payment.PaymentStatusPending || parsedBookingStatus != booking.BookingStatusReserved {
		if err := updateInitializationStatus(ctx, tx, "REVIEW_REQUIRED", claim); err != nil {
			return fmt.Errorf("move unresolved payment initialization to review: %w", err)
		}
		if parsedBookingStatus != booking.BookingStatusCancelled {
			if err := updateBookingStatus(ctx, tx, "PAYMENT_REVIEW", string(parsedBookingStatus), bookingID, "move unresolved booking to payment review"); err != nil {
				return err
			}
		}
	} else {
		rows, err := dbgen.New(tx).SchedulePaymentVerification(ctx, dbgen.SchedulePaymentVerificationParams{
			Status:             dbgen.PaymentInitializationStatus(nextStatus),
			Immediate:          successWebhookRecorded,
			NextAttemptAt:      nextCheckAt,
			HoldExpiresAt:      holdExpiresAt,
			ID:                 int32(claim.InitializationID),
			WorkerAttemptCount: int32(claim.Generation),
		})
		if err != nil {
			return fmt.Errorf("schedule payment verification: %w", err)
		}
		if err := checkClaimRows("schedule payment verification", rows); err != nil {
			return err
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit pending verification transition: %w", err)
	}
	return nil
}

func (p *PostgresStore) MarkRejected(ctx context.Context, claim payment.Claim, reason string) error {
	rows, err := dbgen.New(p.db).MarkPaymentInitializationRejected(ctx, dbgen.MarkPaymentInitializationRejectedParams{
		RejectionReason:    sql.NullString{String: reason, Valid: true},
		ID:                 int32(claim.InitializationID),
		WorkerAttemptCount: int32(claim.Generation),
	})
	if err != nil {
		return fmt.Errorf("mark rejected: %w", err)
	}
	if err := checkClaimRows("mark rejected", rows); err != nil {
		return err
	}

	return nil
}

func (p *PostgresStore) MarkCancelled(ctx context.Context, claim payment.Claim) error {
	rows, err := dbgen.New(p.db).MarkPaymentInitializationCancelled(ctx, dbgen.MarkPaymentInitializationCancelledParams{
		ID:                 int32(claim.InitializationID),
		WorkerAttemptCount: int32(claim.Generation),
	})
	if err != nil {
		return fmt.Errorf("mark cancelled: %w", err)
	}
	if err := checkClaimRows("mark cancelled", rows); err != nil {
		return err
	}

	return nil
}

func (p *PostgresStore) MarkPaymentCancelled(ctx context.Context, claim payment.Claim) error {
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin mark payment cancelled transaction: %w", err)
	}
	defer tx.Rollback()

	locked, err := dbgen.New(tx).LockClaimPaymentAndBooking(ctx, dbgen.LockClaimPaymentAndBookingParams{ID: int32(claim.InitializationID), WorkerAttemptCount: int32(claim.Generation)})

	if errors.Is(err, sql.ErrNoRows) {
		return payment.ErrClaimLost
	}
	if err != nil {
		return fmt.Errorf("lock claimed initialization, payment attempt, and booking: %w", err)
	}

	paymentAttemptID, bookingID := int(locked.PaymentAttemptID), int(locked.BookingID)
	parsedPaymentStatus, err := parsePaymentStatus(locked.PaymentStatus)
	if err != nil {
		return err
	}
	parsedBookingStatus, err := parseBookingStatus(locked.BookingStatus)
	if err != nil {
		return err
	}

	if parsedPaymentStatus == payment.PaymentStatusPaid || parsedBookingStatus == booking.BookingStatusBooked {
		if err := updateInitializationStatus(ctx, tx, "REVIEW_REQUIRED", claim); err != nil {
			return fmt.Errorf("mark conflicting cancellation for review: %w", err)
		}

		if parsedBookingStatus != booking.BookingStatusCancelled {
			if err := updateBookingStatus(ctx, tx, "PAYMENT_REVIEW", string(parsedBookingStatus), bookingID, "mark booking for payment review"); err != nil {
				return err
			}
		}
	} else {
		if err := updateInitializationStatus(ctx, tx, "CANCELLED", claim); err != nil {
			return fmt.Errorf("mark initialization cancelled: %w", err)
		}

		if parsedPaymentStatus == payment.PaymentStatusPending {
			if err := updatePaymentAttemptStatus(ctx, tx, "CANCELLED", "PENDING", paymentAttemptID, "cancel payment attempt"); err != nil {
				return err
			}
		}

		if parsedBookingStatus != booking.BookingStatusCancelled {
			if err := updateBookingStatus(ctx, tx, "CANCELLED", string(parsedBookingStatus), bookingID, "cancel booking"); err != nil {
				return err
			}
		}

		if err := dbgen.New(tx).ReleaseBookingSeatClaims(ctx, int32(bookingID)); err != nil {
			return fmt.Errorf("release cancelled booking seat claims: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit mark payment cancelled transaction: %w", err)
	}
	return nil
}

func (p *PostgresStore) MarkReversed(ctx context.Context, claim payment.Claim) error {
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin mark reversed transaction: %w", err)
	}
	defer tx.Rollback()

	locked, err := dbgen.New(tx).LockClaimPaymentAndBooking(ctx, dbgen.LockClaimPaymentAndBookingParams{ID: int32(claim.InitializationID), WorkerAttemptCount: int32(claim.Generation)})
	if errors.Is(err, sql.ErrNoRows) {
		return payment.ErrClaimLost
	}
	if err != nil {
		return fmt.Errorf("lock claimed initialization, payment attempt, and booking: %w", err)
	}
	bookingID := int(locked.BookingID)
	if _, err := parsePaymentStatus(locked.PaymentStatus); err != nil {
		return err
	}
	parsedBookingStatus, err := parseBookingStatus(locked.BookingStatus)
	if err != nil {
		return err
	}

	if err := updateInitializationStatus(ctx, tx, "REVIEW_REQUIRED", claim); err != nil {
		return fmt.Errorf("mark reversed initialization for review: %w", err)
	}
	if parsedBookingStatus != booking.BookingStatusCancelled {
		if err := updateBookingStatus(ctx, tx, "PAYMENT_REVIEW", string(parsedBookingStatus), bookingID, "mark booking for payment review"); err != nil {
			return err
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit mark reversed transaction: %w", err)
	}
	return nil
}

func (p *PostgresStore) MarkFailed(ctx context.Context, claim payment.Claim) error {
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin mark failed transaction: %w", err)
	}
	defer tx.Rollback()

	locked, err := dbgen.New(tx).LockClaimPaymentAndBooking(ctx, dbgen.LockClaimPaymentAndBookingParams{ID: int32(claim.InitializationID), WorkerAttemptCount: int32(claim.Generation)})
	if errors.Is(err, sql.ErrNoRows) {
		return payment.ErrClaimLost
	}
	if err != nil {
		return fmt.Errorf("lock claimed initialization, payment attempt, and booking: %w", err)
	}

	paymentAttemptID, bookingID := int(locked.PaymentAttemptID), int(locked.BookingID)
	parsedPaymentStatus, err := parsePaymentStatus(locked.PaymentStatus)
	if err != nil {
		return err
	}
	parsedBookingStatus, err := parseBookingStatus(locked.BookingStatus)
	if err != nil {
		return err
	}

	markForReview := func() error {
		if err := updateInitializationStatus(ctx, tx, "REVIEW_REQUIRED", claim); err != nil {
			return fmt.Errorf("mark initialization for review: %w", err)
		}
		if parsedBookingStatus != booking.BookingStatusCancelled {
			if err := updateBookingStatus(ctx, tx, "PAYMENT_REVIEW", string(parsedBookingStatus), bookingID, "mark booking for payment review"); err != nil {
				return err
			}
		}
		return nil
	}
	markTerminalFailure := func(markAttemptFailed bool) error {
		if err := updateInitializationStatus(ctx, tx, "FAILED", claim); err != nil {
			return fmt.Errorf("mark initialization failed: %w", err)
		}
		if markAttemptFailed {
			if err := updatePaymentAttemptStatus(ctx, tx, "FAILED", "PENDING", paymentAttemptID, "mark payment attempt failed"); err != nil {
				if errors.Is(err, payment.ErrPaymentStateConflict) {
					return payment.ErrPaymentAttemptNotPending
				}
				return err
			}
		}
		if parsedBookingStatus != booking.BookingStatusCancelled {
			if err := updateBookingStatus(ctx, tx, "CANCELLED", string(parsedBookingStatus), bookingID, "cancel booking after failed payment"); err != nil {
				return err
			}
		}
		if err := dbgen.New(tx).ReleaseBookingSeatClaims(ctx, int32(bookingID)); err != nil {
			return fmt.Errorf("release failed booking seat claims: %w", err)
		}
		return nil
	}

	switch parsedPaymentStatus {
	case payment.PaymentStatusPending:
		if parsedBookingStatus == booking.BookingStatusBooked {
			if err := markForReview(); err != nil {
				return err
			}
		} else if err := markTerminalFailure(true); err != nil {
			return err
		}
	case payment.PaymentStatusFailed:
		if parsedBookingStatus == booking.BookingStatusBooked {
			if err := markForReview(); err != nil {
				return err
			}
		} else if err := markTerminalFailure(false); err != nil {
			return err
		}
	case payment.PaymentStatusPaid, payment.PaymentStatusCancelled:
		if err := markForReview(); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unsupported payment attempt status %d", parsedPaymentStatus)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit mark failed transaction: %w", err)
	}
	return nil
}

func (p *PostgresStore) MarkPaid(ctx context.Context, claim payment.Claim, verification payment.Verification) error {
	if err := verifyClaimPayment(claim, verification); err != nil {
		return err
	}

	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin mark paid transaction: %w", err)
	}

	defer tx.Rollback()

	locked, err := dbgen.New(tx).LockClaimForPaidTransition(ctx, dbgen.LockClaimForPaidTransitionParams{ID: int32(claim.InitializationID), WorkerAttemptCount: int32(claim.Generation)})
	if errors.Is(err, sql.ErrNoRows) {
		return payment.ErrClaimLost
	}

	if err != nil {
		return fmt.Errorf("lock claim failed: %w", err)
	}

	paymentAttemptID, bookingID := int(locked.PaymentAttemptID), int(locked.BookingID)
	customerEmail := locked.CustomerEmail
	parsedPaymentStatus, err := parsePaymentStatus(locked.PaymentStatus)
	if err != nil {
		return err
	}

	switch parsedPaymentStatus {
	case payment.PaymentStatusPaid:
		parsedBookingStatus, err := parseBookingStatus(locked.BookingStatus)
		if err != nil {
			return err
		}

		if parsedBookingStatus != booking.BookingStatusBooked {
			return payment.ErrPaymentStateConflict
		}

		if err := updateInitializationStatus(ctx, tx, "PAID", claim); err != nil {
			return fmt.Errorf("mark initialization paid: %w", err)
		}

	case payment.PaymentStatusPending:
		parsedBookingStatus, err := parseBookingStatus(locked.BookingStatus)
		if err != nil {
			return err
		}

		if parsedBookingStatus != booking.BookingStatusReserved {
			return payment.ErrPaymentStateConflict
		}

		if err := updateInitializationStatus(ctx, tx, "PAID", claim); err != nil {
			return fmt.Errorf("mark initialization paid: %w", err)
		}

		if err := updatePaymentAttemptStatus(ctx, tx, "PAID", "PENDING", paymentAttemptID, "mark payment paid"); err != nil {
			return err
		}

		if err := updateBookingStatus(ctx, tx, "BOOKED", "RESERVED", bookingID, "mark booking booked"); err != nil {
			return err
		}

	default:
		return payment.ErrPaymentStateConflict
	}

	if err := p.insertTickets(ctx, tx, bookingID); err != nil {
		return fmt.Errorf("create booking tickets: %w", err)
	}
	if err := p.insertEmailTask(ctx, tx, bookingID, customerEmail); err != nil {
		return fmt.Errorf("enqueue booking ticket email: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit mark failed transaction: %w", err)
	}

	return nil
}

func (p *PostgresStore) MarkUnknown(ctx context.Context, claim payment.Claim, retryAt time.Time) error {
	return p.finishPendingVerification(ctx, claim, "UNKNOWN", retryAt)
}

func verifyClaimPayment(claim payment.Claim, verification payment.Verification) error {
	if verification.VerificationStatus != payment.VerificationStatusPaid {
		return fmt.Errorf("Payment status is not PAID")
	}

	if claim.AmountMinor != verification.AmountMinor {
		return fmt.Errorf("Claim amount %d does not match verification amount %d", claim.AmountMinor, verification.AmountMinor)
	}

	if claim.Currency != verification.Currency {
		return fmt.Errorf("Claim currency %s does not match verification currency %s", claim.Currency, verification.Currency)
	}

	if claim.PaystackReference != verification.Reference {
		return fmt.Errorf("Claim paystack reference %s does not match verification paystack reference %s", claim.PaystackReference, verification.Reference)
	}

	return nil
}

func parseInitializationStatus(status string) (payment.InitializationStatus, error) {
	switch status {
	case "QUEUED":
		return payment.InitializationStatusQueued, nil
	case "SUBMITTING":
		return payment.InitializationStatusSubmitting, nil
	case "READY":
		return payment.InitializationStatusReady, nil
	case "PAID":
		return payment.InitializationStatusPaid, nil
	case "FAILED":
		return payment.InitializationStatusFailed, nil
	case "REJECTED":
		return payment.InitializationStatusRejected, nil
	case "UNKNOWN":
		return payment.InitializationStatusUnknown, nil
	case "CANCELLED":
		return payment.InitializationStatusCancelled, nil
	case "REVIEW_REQUIRED":
		return payment.InitializationStatusReviewRequired, nil
	default:
		return payment.InitializationStatusInvalid, fmt.Errorf("unexpected previous initialization status %q", status)
	}
}

func parsePaymentStatus(status string) (payment.PaymentStatus, error) {
	switch status {
	case "PENDING":
		return payment.PaymentStatusPending, nil
	case "PAID":
		return payment.PaymentStatusPaid, nil
	case "FAILED":
		return payment.PaymentStatusFailed, nil
	case "CANCELLED":
		return payment.PaymentStatusCancelled, nil
	default:
		return payment.PaymentStatusInvalid, fmt.Errorf("unexpected previous payment status %q", status)
	}
}

func parseBookingStatus(status string) (booking.BookingStatus, error) {
	switch status {
	case "RESERVED":
		return booking.BookingStatusReserved, nil
	case "BOOKED":
		return booking.BookingStatusBooked, nil
	case "PAYMENT_REVIEW":
		return booking.BookingStatusPaymentReview, nil
	case "CANCELLED":
		return booking.BookingStatusCancelled, nil
	default:
		return booking.BookingStatusInvalid, fmt.Errorf("unexpected booking status %q", status)
	}
}

func parsePaymentMethod(method string) (payment.PaymentMethod, error) {
	switch method {
	case "CARD":
		return payment.PaymentMethodCard, nil
	case "MPESA":
		return payment.PaymentMethodMpesa, nil
	default:
		return payment.PaymentMethodInvalid, fmt.Errorf("unexpected payment method %q", method)
	}
}

func checkClaimRows(operation string, rows int64) error {
	if rows == 0 {
		return fmt.Errorf("%s: %w", operation, payment.ErrClaimLost)
	}
	if rows != 1 {
		return fmt.Errorf("%s: updated %d rows, want 1", operation, rows)
	}
	return nil
}

func updateInitializationStatus(ctx context.Context, tx *sql.Tx, status string, claim payment.Claim) error {
	_, err := dbgen.New(tx).UpdateClaimedPaymentInitializationStatus(ctx, dbgen.UpdateClaimedPaymentInitializationStatusParams{
		Status:             dbgen.PaymentInitializationStatus(status),
		ID:                 int32(claim.InitializationID),
		WorkerAttemptCount: int32(claim.Generation),
	})
	if errors.Is(err, sql.ErrNoRows) {
		return payment.ErrClaimLost
	}

	if err != nil {
		return err
	}
	return nil
}

func updatePaymentAttemptStatus(ctx context.Context, tx *sql.Tx, status, previousStatus string, paymentAttemptID int, operation string) error {
	_, err := dbgen.New(tx).UpdatePaymentAttemptStatus(ctx, dbgen.UpdatePaymentAttemptStatusParams{
		Status:         dbgen.PaymentStatus(status),
		ID:             int32(paymentAttemptID),
		PreviousStatus: dbgen.PaymentStatus(previousStatus),
	})
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%s: %w", operation, payment.ErrPaymentStateConflict)
	}
	if err != nil {
		return fmt.Errorf("%s: %w", operation, err)
	}
	return nil
}

func updateBookingStatus(ctx context.Context, tx *sql.Tx, status, previousStatus string, bookingID int, operation string) error {
	_, err := dbgen.New(tx).UpdateBookingStatus(ctx, dbgen.UpdateBookingStatusParams{
		Status:         dbgen.BookingStatus(status),
		ID:             int32(bookingID),
		PreviousStatus: dbgen.BookingStatus(previousStatus),
	})
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%s: %w", operation, payment.ErrPaymentStateConflict)
	}
	if err != nil {
		return fmt.Errorf("%s: %w", operation, err)
	}
	return nil
}
