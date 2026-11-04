package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"example.com/cinema-tickets-backend/internal/booking"
	"example.com/cinema-tickets-backend/internal/payment"
	"example.com/cinema-tickets-backend/internal/payment/postgres/dbgen"
	"example.com/cinema-tickets-backend/internal/ticket"
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

const updateInitializationSQL = `
		UPDATE payment_initializations
		SET status = $1,
			lease_expires_at = NULL,
			next_attempt_at = NULL,
			updated_at = clock_timestamp()
		WHERE id = $2
			AND status = 'SUBMITTING'
			AND worker_attempt_count = $3
		RETURNING payment_attempt_id
	`

func (p *PostgresStore) ClaimNext(ctx context.Context) (payment.Claim, bool, error) {
	const claimJob = `
		UPDATE payment_initializations
		SET status = 'SUBMITTING',
			worker_attempt_count = worker_attempt_count + 1,
			next_attempt_at = NULL,
			lease_expires_at = clock_timestamp() + ($2::double precision * interval '1 microsecond'),
			updated_at = clock_timestamp()
		WHERE id = $1
		RETURNING worker_attempt_count
	`

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

	err = tx.QueryRowContext(ctx, claimJob, claim.InitializationID, p.leaseDuration.Microseconds()).Scan(&claim.Generation)
	if err != nil {
		return payment.Claim{}, false, fmt.Errorf("update claimed payment initialization: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return payment.Claim{}, false, fmt.Errorf("commit claim transaction: %w", err)
	}

	return claim, true, nil
}

func (p *PostgresStore) MarkReady(ctx context.Context, claim payment.Claim, checkoutURL string, nextCheckAt time.Time) error {
	if checkoutURL == "" {
		return errors.New("Checkout URL is empty")
	}

	const markReadySQL = `
		UPDATE payment_initializations
		SET status = 'READY',
			authorization_url = $1,
			lease_expires_at = NULL,
			next_attempt_at = CASE WHEN EXISTS (
				SELECT 1
				FROM webhook_inbox AS wi
				WHERE wi.reference = payment_initializations.paystack_reference
					AND wi.event_type = 'charge.success'
			) THEN clock_timestamp() ELSE LEAST(
				$2::timestamptz,
				(SELECT b.hold_expires_at
				 FROM payment_attempts AS pa
				 JOIN bookings AS b ON b.id = pa.booking_id
				 WHERE pa.id = payment_initializations.payment_attempt_id)
			) END,
			updated_at = clock_timestamp()
		WHERE id = $3
			AND status = 'SUBMITTING'
			AND worker_attempt_count = $4
	`

	if err := p.execClaimUpdate(ctx, "mark ready", markReadySQL, checkoutURL, nextCheckAt, claim.InitializationID, claim.Generation); err != nil {
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
	const scheduleSQL = `
		UPDATE payment_initializations
		SET status = $1,
			lease_expires_at = NULL,
			next_attempt_at = CASE WHEN $2 THEN clock_timestamp() ELSE LEAST($3::timestamptz, $4::timestamptz) END,
			updated_at = clock_timestamp()
		WHERE id = $5 AND status = 'SUBMITTING' AND worker_attempt_count = $6
	`
	const markBookingForReviewSQL = `
		UPDATE bookings SET status = 'PAYMENT_REVIEW'
		WHERE id = $1 AND status <> 'CANCELLED'
		RETURNING id
	`

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
		if err := updateInitializationStatus(ctx, tx, updateInitializationSQL, "REVIEW_REQUIRED", claim); err != nil {
			return fmt.Errorf("move unresolved payment initialization to review: %w", err)
		}
		if parsedBookingStatus != booking.BookingStatusCancelled {
			if err := updateBookingStatus(ctx, tx, markBookingForReviewSQL, bookingID, "move unresolved booking to payment review"); err != nil {
				return err
			}
		}
	} else if err := p.execTxClaimUpdate(ctx, tx, "schedule payment verification", scheduleSQL, nextStatus, successWebhookRecorded, nextCheckAt, holdExpiresAt, claim.InitializationID, claim.Generation); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit pending verification transition: %w", err)
	}
	return nil
}

func (p *PostgresStore) MarkRejected(ctx context.Context, claim payment.Claim, reason string) error {
	const markRejectedSQL = `
	UPDATE payment_initializations
	SET status = 'REJECTED',
		rejection_reason = $1,
		lease_expires_at = NULL,
		next_attempt_at = NULL,
		updated_at = clock_timestamp()
	WHERE id = $2
		AND status = 'SUBMITTING'
		AND worker_attempt_count = $3
	`

	if err := p.execClaimUpdate(ctx, "mark rejected", markRejectedSQL, reason, claim.InitializationID, claim.Generation); err != nil {
		return err
	}

	return nil
}

func (p *PostgresStore) MarkCancelled(ctx context.Context, claim payment.Claim) error {
	const markCancelledSQL = `
	UPDATE payment_initializations
	SET status = 'CANCELLED',
		lease_expires_at = NULL,
		next_attempt_at = NULL,
		updated_at = clock_timestamp()
	WHERE id = $1
		AND status = 'SUBMITTING'
		AND worker_attempt_count = $2
	`

	if err := p.execClaimUpdate(ctx, "mark cancelled", markCancelledSQL, claim.InitializationID, claim.Generation); err != nil {
		return err
	}

	return nil
}

func (p *PostgresStore) MarkPaymentCancelled(ctx context.Context, claim payment.Claim) error {
	const markPaymentCancelledSQL = `
		UPDATE payment_attempts
		SET status = 'CANCELLED'
		WHERE id = $1 AND status = 'PENDING'
		RETURNING id
	`
	const markBookingCancelledSQL = `
		UPDATE bookings
		SET status = 'CANCELLED'
		WHERE id = $1 AND status <> 'CANCELLED'
		RETURNING id
	`
	const markBookingForReviewSQL = `
		UPDATE bookings
		SET status = 'PAYMENT_REVIEW'
		WHERE id = $1 AND status <> 'CANCELLED'
		RETURNING id
	`
	const releaseSeatClaimsSQL = `DELETE FROM seat_claims WHERE booking_id = $1`

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
		if err := updateInitializationStatus(ctx, tx, updateInitializationSQL, "REVIEW_REQUIRED", claim); err != nil {
			return fmt.Errorf("mark conflicting cancellation for review: %w", err)
		}

		if parsedBookingStatus != booking.BookingStatusCancelled {
			if err := updateBookingStatus(ctx, tx, markBookingForReviewSQL, bookingID, "mark booking for payment review"); err != nil {
				return err
			}
		}
	} else {
		if err := updateInitializationStatus(ctx, tx, updateInitializationSQL, "CANCELLED", claim); err != nil {
			return fmt.Errorf("mark initialization cancelled: %w", err)
		}

		if parsedPaymentStatus == payment.PaymentStatusPending {
			if err := updatePaymentAttemptStatus(ctx, tx, markPaymentCancelledSQL, paymentAttemptID, "cancel payment attempt"); err != nil {
				return err
			}
		}

		if parsedBookingStatus != booking.BookingStatusCancelled {
			if err := updateBookingStatus(ctx, tx, markBookingCancelledSQL, bookingID, "cancel booking"); err != nil {
				return err
			}
		}

		if _, err := tx.ExecContext(ctx, releaseSeatClaimsSQL, bookingID); err != nil {
			return fmt.Errorf("release cancelled booking seat claims: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit mark payment cancelled transaction: %w", err)
	}
	return nil
}

func (p *PostgresStore) MarkReversed(ctx context.Context, claim payment.Claim) error {
	const markBookingForReviewSQL = `
		UPDATE bookings
		SET status = 'PAYMENT_REVIEW'
		WHERE id = $1 AND status <> 'CANCELLED'
		RETURNING id
	`

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

	if err := updateInitializationStatus(ctx, tx, updateInitializationSQL, "REVIEW_REQUIRED", claim); err != nil {
		return fmt.Errorf("mark reversed initialization for review: %w", err)
	}
	if parsedBookingStatus != booking.BookingStatusCancelled {
		if err := updateBookingStatus(ctx, tx, markBookingForReviewSQL, bookingID, "mark booking for payment review"); err != nil {
			return err
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit mark reversed transaction: %w", err)
	}
	return nil
}

func (p *PostgresStore) MarkFailed(ctx context.Context, claim payment.Claim) error {
	const markPaymentFailedSQL = `
		UPDATE payment_attempts
		SET status = 'FAILED'
		WHERE id = $1 AND status = 'PENDING'
		RETURNING id
	`
	const markBookingForReviewSQL = `
		UPDATE bookings
		SET status = 'PAYMENT_REVIEW'
		WHERE id = $1 AND status <> 'CANCELLED'
		RETURNING id
	`
	const markBookingCancelledSQL = `
		UPDATE bookings
		SET status = 'CANCELLED'
		WHERE id = $1 AND status <> 'CANCELLED'
		RETURNING id
	`
	const releaseSeatClaimsSQL = `DELETE FROM seat_claims WHERE booking_id = $1`

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
		if err := updateInitializationStatus(ctx, tx, updateInitializationSQL, "REVIEW_REQUIRED", claim); err != nil {
			return fmt.Errorf("mark initialization for review: %w", err)
		}
		if parsedBookingStatus != booking.BookingStatusCancelled {
			if err := updateBookingStatus(ctx, tx, markBookingForReviewSQL, bookingID, "mark booking for payment review"); err != nil {
				return err
			}
		}
		return nil
	}
	markTerminalFailure := func(markAttemptFailed bool) error {
		if err := updateInitializationStatus(ctx, tx, updateInitializationSQL, "FAILED", claim); err != nil {
			return fmt.Errorf("mark initialization failed: %w", err)
		}
		if markAttemptFailed {
			var updatedID int
			err := tx.QueryRowContext(ctx, markPaymentFailedSQL, paymentAttemptID).Scan(&updatedID)
			if errors.Is(err, sql.ErrNoRows) {
				return payment.ErrPaymentAttemptNotPending
			}
			if err != nil {
				return fmt.Errorf("mark payment attempt failed: %w", err)
			}
		}
		if parsedBookingStatus != booking.BookingStatusCancelled {
			if err := updateBookingStatus(ctx, tx, markBookingCancelledSQL, bookingID, "cancel booking after failed payment"); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, releaseSeatClaimsSQL, bookingID); err != nil {
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

	const markPaymentPaid = `
	UPDATE payment_attempts
	SET status = 'PAID'
	WHERE status = 'PENDING'
		AND id = $1
	RETURNING id
	`

	const markBookingBooked = `
	UPDATE bookings
	SET status = 'BOOKED'
	WHERE status = 'RESERVED'
		AND id = $1
	RETURNING id
	`

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

		if err := updateInitializationStatus(ctx, tx, updateInitializationSQL, "PAID", claim); err != nil {
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

		if err := updateInitializationStatus(ctx, tx, updateInitializationSQL, "PAID", claim); err != nil {
			return fmt.Errorf("mark initialization paid: %w", err)
		}

		err = tx.QueryRowContext(ctx, markPaymentPaid, paymentAttemptID).Scan(&paymentAttemptID)
		if err != nil {
			return fmt.Errorf("mark payment paid: %w", err)
		}

		err = tx.QueryRowContext(ctx, markBookingBooked, bookingID).Scan(&bookingID)
		if err != nil {
			return fmt.Errorf("mark booking booked: %w", err)
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

func (p *PostgresStore) execClaimUpdate(ctx context.Context, operation string, query string, args ...any) error {
	result, err := p.db.ExecContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("%s: %w", operation, err)
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("%s: get affected rows count: %w", operation, err)
	}

	if rows == 0 {
		return fmt.Errorf("%s: %w", operation, payment.ErrClaimLost)
	}

	if rows != 1 {
		return fmt.Errorf("%s: updated %d rows, want 1", operation, rows)
	}

	return nil
}

func (p *PostgresStore) execTxClaimUpdate(ctx context.Context, tx *sql.Tx, operation, query string, args ...any) error {
	result, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("%s: %w", operation, err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("%s: get affected rows count: %w", operation, err)
	}
	if rows == 0 {
		return fmt.Errorf("%s: %w", operation, payment.ErrClaimLost)
	}
	if rows != 1 {
		return fmt.Errorf("%s: updated %d rows, want 1", operation, rows)
	}
	return nil
}

func updateInitializationStatus(
	ctx context.Context,
	tx *sql.Tx,
	query string,
	status string,
	claim payment.Claim,
) error {
	var paymentAttemptID int

	err := tx.QueryRowContext(ctx, query, status, claim.InitializationID, claim.Generation).Scan(&paymentAttemptID)
	if errors.Is(err, sql.ErrNoRows) {
		return payment.ErrClaimLost
	}

	if err != nil {
		return err
	}

	return nil
}

func updatePaymentAttemptStatus(ctx context.Context, tx *sql.Tx, query string, paymentAttemptID int, operation string) error {
	var updatedID int
	err := tx.QueryRowContext(ctx, query, paymentAttemptID).Scan(&updatedID)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%s: %w", operation, payment.ErrPaymentStateConflict)
	}
	if err != nil {
		return fmt.Errorf("%s: %w", operation, err)
	}
	return nil
}

func updateBookingStatus(ctx context.Context, tx *sql.Tx, query string, bookingID int, operation string) error {
	var updatedID int
	err := tx.QueryRowContext(ctx, query, bookingID).Scan(&updatedID)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%s: %w", operation, payment.ErrPaymentStateConflict)
	}
	if err != nil {
		return fmt.Errorf("%s: %w", operation, err)
	}
	return nil
}
