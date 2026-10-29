package postgres

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"example.com/cinema-tickets-backend/internal/booking"
	"example.com/cinema-tickets-backend/internal/payment"
)

type InitializationStore struct {
	db *sql.DB
}

var _ payment.InitializationCreator = (*InitializationStore)(nil)

func NewInitializationStore(db *sql.DB) (*InitializationStore, error) {
	if db == nil {
		return nil, errors.New("database pool is required")
	}

	return &InitializationStore{db: db}, nil
}

func (p *InitializationStore) CreateOrReplayInitialization(ctx context.Context, request payment.CreateInitializationRequest) (payment.Initialization, error) {
	request.BookingReference = strings.TrimSpace(request.BookingReference)
	request.IdempotencyKey = strings.TrimSpace(request.IdempotencyKey)
	request.RequestFingerprint = strings.TrimSpace(request.RequestFingerprint)

	if request.BookingReference == "" || request.IdempotencyKey == "" || request.RequestFingerprint == "" {
		return payment.Initialization{}, fmt.Errorf("%w: booking reference, idempotency key, and request fingerprint are required", payment.ErrInvalidInitializationRequest)
	}

	if request.PaymentMethod != payment.PaymentMethodCard && request.PaymentMethod != payment.PaymentMethodMpesa {
		return payment.Initialization{}, fmt.Errorf("%w: unsupported payment method", payment.ErrInvalidInitializationRequest)
	}

	for attempt := 0; attempt < 2; attempt++ {
		initialization, err := p.createOrReplayInitialization(ctx, request)
		if !errors.Is(err, errInitializationReceiptConflict) {
			return initialization, err
		}
	}
	return payment.Initialization{}, errors.New("initialization receipt conflict could not be resolved")
}

func (p *InitializationStore) GetLatestForBooking(ctx context.Context, bookingReference string) (payment.Initialization, error) {
	bookingReference = strings.TrimSpace(bookingReference)
	if bookingReference == "" {
		return payment.Initialization{}, payment.ErrInvalidInitializationRequest
	}

	var initialization payment.Initialization
	var status, method string
	err := p.db.QueryRowContext(ctx, `
		SELECT pi.id, pi.status::text, pi.payment_method::text,
		       pi.paystack_reference, COALESCE(pi.authorization_url, '')
		FROM payment_initializations AS pi
		JOIN payment_attempts AS pa ON pa.id = pi.payment_attempt_id
		JOIN bookings AS b ON b.id = pa.booking_id
		WHERE b.booking_reference = $1
		ORDER BY pi.id DESC
		LIMIT 1
	`, bookingReference).Scan(
		&initialization.ID,
		&status,
		&method,
		&initialization.PaystackReference,
		&initialization.AuthorizationURL,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return payment.Initialization{}, payment.ErrInitializationNotFound
	}
	if err != nil {
		return payment.Initialization{}, fmt.Errorf("load latest payment initialization for booking: %w", err)
	}

	initialization.Status, err = parseInitializationStatus(status)
	if err != nil {
		return payment.Initialization{}, fmt.Errorf("parse payment initialization status: %w", err)
	}

	initialization.PaymentMethod, err = parsePaymentMethod(method)
	if err != nil {
		return payment.Initialization{}, fmt.Errorf("parse payment initialization method: %w", err)
	}

	return initialization, nil
}

var errInitializationReceiptConflict = errors.New("initialization receipt concurrently created")

func (p *InitializationStore) createOrReplayInitialization(ctx context.Context, request payment.CreateInitializationRequest) (payment.Initialization, error) {
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return payment.Initialization{}, fmt.Errorf("begin create payment initialization transaction: %w", err)
	}

	defer tx.Rollback()

	initialization, found, err := findInitializationReplay(ctx, tx, request)
	if err != nil {
		return payment.Initialization{}, fmt.Errorf("load payment initialization by idempotency key: %w", err)
	}

	if found {
		if err := tx.Commit(); err != nil {
			return payment.Initialization{}, fmt.Errorf("commit payment initialization replay: %w", err)
		}
		return initialization, nil
	}

	var bookingID int
	var bookingStatus string
	var holdActive bool

	err = tx.QueryRowContext(ctx, `
		SELECT id, status::text, hold_expires_at > clock_timestamp()
		FROM bookings
		WHERE booking_reference = $1
		FOR UPDATE
	`, request.BookingReference).Scan(&bookingID, &bookingStatus, &holdActive)

	if errors.Is(err, sql.ErrNoRows) {
		return payment.Initialization{}, payment.ErrBookingNotFound
	}
	if err != nil {
		return payment.Initialization{}, fmt.Errorf("lock booking for payment initialization: %w", err)
	}

	initialization, found, err = findInitializationReplay(ctx, tx, request)
	if err != nil {
		return payment.Initialization{}, fmt.Errorf("reload payment initialization by idempotency key: %w", err)
	}
	if found {
		if err := tx.Commit(); err != nil {
			return payment.Initialization{}, fmt.Errorf("commit payment initialization replay: %w", err)
		}
		return initialization, nil
	}

	initialization, found, err = loadUnresolvedInitialization(ctx, tx, request.BookingReference)
	if err != nil {
		return payment.Initialization{}, fmt.Errorf("load unresolved payment initialization: %w", err)
	}

	if found {
		if err := saveInitializationReceipt(ctx, tx, request, initialization.ID); err != nil {
			return payment.Initialization{}, err
		}
		if err := tx.Commit(); err != nil {
			return payment.Initialization{}, fmt.Errorf("commit payment initialization replay: %w", err)
		}

		return initialization, nil
	}

	parsedBookingStatus, err := parseBookingStatus(bookingStatus)
	if err != nil {
		return payment.Initialization{}, err
	}

	if parsedBookingStatus == booking.BookingStatusReserved && !holdActive {
		if err := cancelExpiredBooking(ctx, tx, bookingID); err != nil {
			return payment.Initialization{}, err
		}

		if err := tx.Commit(); err != nil {
			return payment.Initialization{}, fmt.Errorf("commit expired booking cancellation: %w", err)
		}

		return payment.Initialization{}, payment.ErrBookingNotEligibleForPayment
	}

	if parsedBookingStatus != booking.BookingStatusReserved {
		return payment.Initialization{}, payment.ErrBookingNotEligibleForPayment
	}

	paymentAttemptID, paymentStatus, err := loadOrCreatePaymentAttempt(ctx, tx, bookingID)
	if err != nil {
		return payment.Initialization{}, fmt.Errorf("load or create payment attempt: %w", err)
	}

	parsedPaymentStatus, err := parsePaymentStatus(paymentStatus)
	if err != nil {
		return payment.Initialization{}, err
	}

	if parsedPaymentStatus != payment.PaymentStatusPending {
		return payment.Initialization{}, payment.ErrPaymentAttemptNotRetryable
	}

	latest, found, err := loadLatestInitialization(ctx, tx, paymentAttemptID)
	if err != nil {
		return payment.Initialization{}, fmt.Errorf("load latest payment initialization: %w", err)
	}
	if found {
		if isUnresolvedInitialization(latest.Status) {
			if err := saveInitializationReceipt(ctx, tx, request, latest.ID); err != nil {
				return payment.Initialization{}, err
			}
			if err := tx.Commit(); err != nil {
				return payment.Initialization{}, fmt.Errorf("commit existing initialization result: %w", err)
			}
			return latest, nil
		}
		if latest.Status != payment.InitializationStatusRejected {
			return payment.Initialization{}, payment.ErrPaymentAttemptNotRetryable
		}
	}

	reference, err := newPaystackReference()
	if err != nil {
		return payment.Initialization{}, err
	}

	methodName, err := paymentMethodDatabaseValue(request.PaymentMethod)
	if err != nil {
		return payment.Initialization{}, err
	}

	var latestStatus string
	err = tx.QueryRowContext(ctx, `
		INSERT INTO payment_initializations (
			payment_attempt_id,
			paystack_reference,
			payment_method,
			status,
			created_at,
			updated_at
		)
		VALUES ($1, $2, $3, 'QUEUED', clock_timestamp(), clock_timestamp())
		RETURNING id, status::text, payment_method::text, paystack_reference, COALESCE(authorization_url, '')
	`, paymentAttemptID, reference, methodName).Scan(
		&initialization.ID,
		&latestStatus,
		&methodName,
		&initialization.PaystackReference,
		&initialization.AuthorizationURL,
	)

	if err != nil {
		return payment.Initialization{}, fmt.Errorf("insert payment initialization: %w", err)
	}

	initialization.Status, err = parseInitializationStatus(latestStatus)
	if err != nil {
		return payment.Initialization{}, err
	}

	if err := saveInitializationReceipt(ctx, tx, request, initialization.ID); err != nil {
		return payment.Initialization{}, err
	}

	initialization.PaymentMethod, err = parsePaymentMethod(methodName)
	if err != nil {
		return payment.Initialization{}, err
	}

	if err := tx.Commit(); err != nil {
		return payment.Initialization{}, fmt.Errorf("commit payment initialization: %w", err)
	}
	return initialization, nil
}

type initializationReplay struct {
	initialization   payment.Initialization
	fingerprint      string
	bookingReference string
	requestedMethod  payment.PaymentMethod
}

func findInitializationReplay(ctx context.Context, tx *sql.Tx, request payment.CreateInitializationRequest) (payment.Initialization, bool, error) {
	record, found, err := loadInitializationByKey(ctx, tx, request.IdempotencyKey)
	if err != nil || !found {
		return payment.Initialization{}, found, err
	}
	if record.fingerprint != request.RequestFingerprint || record.bookingReference != request.BookingReference || record.requestedMethod != request.PaymentMethod {
		return payment.Initialization{}, true, payment.ErrInitializationIdempotencyConflict
	}

	if record.initialization.Status == payment.InitializationStatusRejected {
		if err := cancelExpiredBookingByReference(ctx, tx, record.bookingReference); err != nil {
			return payment.Initialization{}, true, err
		}
	}
	return record.initialization, true, nil
}

func loadInitializationByKey(
	ctx context.Context,
	tx *sql.Tx,
	key string,
) (initializationReplay, bool, error) {
	var record initializationReplay
	var status, method, requestedMethod string

	err := tx.QueryRowContext(ctx, `
		SELECT pi.id, pi.status::text, pi.payment_method::text,
		       pi.paystack_reference, COALESCE(pi.authorization_url, ''),
		       receipt.request_fingerprint, b.booking_reference, receipt.requested_payment_method::text
		FROM payment_initialization_receipts AS receipt
		JOIN payment_initializations AS pi ON pi.id = receipt.payment_initialization_id
		JOIN payment_attempts AS pa ON pa.id = pi.payment_attempt_id
		JOIN bookings AS b ON b.id = pa.booking_id
		WHERE receipt.idempotency_key = $1
	`, key).Scan(
		&record.initialization.ID,
		&status,
		&method,
		&record.initialization.PaystackReference,
		&record.initialization.AuthorizationURL,
		&record.fingerprint,
		&record.bookingReference,
		&requestedMethod,
	)

	if errors.Is(err, sql.ErrNoRows) {
		return initializationReplay{}, false, nil
	}

	if err != nil {
		return initializationReplay{}, false, err
	}

	record.initialization.Status, err = parseInitializationStatus(status)
	if err != nil {
		return initializationReplay{}, false, err
	}

	record.initialization.PaymentMethod, err = parsePaymentMethod(method)
	if err != nil {
		return initializationReplay{}, false, err
	}
	record.requestedMethod, err = parsePaymentMethod(requestedMethod)
	if err != nil {
		return initializationReplay{}, false, err
	}

	return record, true, nil
}

func saveInitializationReceipt(ctx context.Context, tx *sql.Tx, request payment.CreateInitializationRequest, initializationID int) error {
	method, err := paymentMethodDatabaseValue(request.PaymentMethod)
	if err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `
		INSERT INTO payment_initialization_receipts
			(idempotency_key, request_fingerprint, requested_payment_method, payment_initialization_id)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (idempotency_key) DO NOTHING
	`, request.IdempotencyKey, request.RequestFingerprint, method, initializationID)
	if err != nil {
		return fmt.Errorf("insert initialization receipt: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("check initialization receipt insert: %w", err)
	}
	if count == 0 {
		return errInitializationReceiptConflict
	}
	return nil
}

func loadUnresolvedInitialization(ctx context.Context, tx *sql.Tx, bookingReference string) (payment.Initialization, bool, error) {
	var initialization payment.Initialization
	var status, method string

	err := tx.QueryRowContext(ctx, `
		SELECT pi.id, pi.status::text, pi.payment_method::text,
		       pi.paystack_reference, COALESCE(pi.authorization_url, '')
		FROM payment_initializations AS pi
		JOIN payment_attempts AS pa ON pa.id = pi.payment_attempt_id
		JOIN bookings AS b ON b.id = pa.booking_id
		WHERE b.booking_reference = $1
		  AND pi.status IN ('QUEUED', 'SUBMITTING', 'READY', 'UNKNOWN', 'REVIEW_REQUIRED')
		ORDER BY pi.id DESC
		LIMIT 1
	`, bookingReference).Scan(
		&initialization.ID,
		&status,
		&method,
		&initialization.PaystackReference,
		&initialization.AuthorizationURL,
	)

	if errors.Is(err, sql.ErrNoRows) {
		return payment.Initialization{}, false, nil
	}

	if err != nil {
		return payment.Initialization{}, false, err
	}

	initialization.Status, err = parseInitializationStatus(status)
	if err != nil {
		return payment.Initialization{}, false, err
	}

	initialization.PaymentMethod, err = parsePaymentMethod(method)
	if err != nil {
		return payment.Initialization{}, false, err
	}

	return initialization, true, nil
}

func loadOrCreatePaymentAttempt(ctx context.Context, tx *sql.Tx, bookingID int) (int, string, error) {
	var paymentAttemptID int
	var status string

	err := tx.QueryRowContext(ctx, `
		SELECT id, status::text
		FROM payment_attempts
		WHERE booking_id = $1
		FOR UPDATE
	`, bookingID).Scan(&paymentAttemptID, &status)

	if err == nil {
		return paymentAttemptID, status, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, "", fmt.Errorf("load payment attempt: %w", err)
	}

	var amount int64
	var currency sql.NullString
	var seatCount int

	err = tx.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(unit_price_amount), 0)::bigint, MIN(currency), COUNT(*)
		FROM bookings_seats
		WHERE booking_id = $1
	`, bookingID).Scan(&amount, &currency, &seatCount)

	if err != nil {
		return 0, "", fmt.Errorf("calculate payment attempt amount: %w", err)
	}

	if seatCount == 0 || amount <= 0 || !currency.Valid {
		return 0, "", fmt.Errorf("booking %d has no valid price snapshots", bookingID)
	}

	err = tx.QueryRowContext(ctx, `
		INSERT INTO payment_attempts (booking_id, status, amount, currency, created_at)
		VALUES ($1, 'PENDING', $2, $3, clock_timestamp())
		ON CONFLICT (booking_id) DO NOTHING
		RETURNING id, status::text
	`, bookingID, amount, currency.String).Scan(&paymentAttemptID, &status)

	if errors.Is(err, sql.ErrNoRows) {
		err = tx.QueryRowContext(ctx, `
			SELECT id, status::text
			FROM payment_attempts
			WHERE booking_id = $1
			FOR UPDATE
		`, bookingID).Scan(&paymentAttemptID, &status)
	}

	if err != nil {
		return 0, "", err
	}

	return paymentAttemptID, status, nil
}

func cancelExpiredBooking(ctx context.Context, tx *sql.Tx, bookingID int) error {
	if _, err := tx.ExecContext(ctx, `
		UPDATE bookings
		SET status = 'CANCELLED'
		WHERE id = $1 AND status = 'RESERVED'
	`, bookingID); err != nil {
		return fmt.Errorf("cancel expired booking: %w", err)
	}

	return releaseExpiredBooking(ctx, tx, bookingID)
}

func cancelExpiredBookingByReference(ctx context.Context, tx *sql.Tx, bookingReference string) error {
	var bookingID int

	err := tx.QueryRowContext(ctx, `
		SELECT id FROM bookings
		WHERE booking_reference = $1
		  AND status = 'RESERVED'
		  AND hold_expires_at <= clock_timestamp()
		FOR UPDATE
	`, bookingReference).Scan(&bookingID)

	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}

	if err != nil {
		return fmt.Errorf("lock expired booking after rejected initialization: %w", err)
	}

	err = tx.QueryRowContext(ctx, `
		UPDATE bookings SET status = 'CANCELLED'
		WHERE id = $1 AND EXISTS (
			SELECT 1 FROM payment_attempts AS pa
			WHERE pa.booking_id = $1 AND pa.status = 'PENDING'
			  AND (SELECT pi.status FROM payment_initializations AS pi
			       WHERE pi.payment_attempt_id = pa.id ORDER BY pi.id DESC LIMIT 1) = 'REJECTED'
		)
		RETURNING id
	`, bookingID).Scan(&bookingID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("cancel expired booking after rejected initialization: %w", err)
	}

	return releaseExpiredBooking(ctx, tx, bookingID)
}

func releaseExpiredBooking(ctx context.Context, tx *sql.Tx, bookingID int) error {
	if _, err := tx.ExecContext(ctx, `
		UPDATE payment_attempts
		SET status = 'CANCELLED'
		WHERE booking_id = $1 AND status = 'PENDING'
	`, bookingID); err != nil {
		return fmt.Errorf("cancel expired payment attempt after rejected initialization: %w", err)
	}

	if _, err := tx.ExecContext(ctx, `DELETE FROM seat_claims WHERE booking_id = $1`, bookingID); err != nil {
		return fmt.Errorf("release expired booking claims after rejected initialization: %w", err)
	}

	return nil
}

func loadLatestInitialization(
	ctx context.Context,
	tx *sql.Tx,
	paymentAttemptID int,
) (payment.Initialization, bool, error) {
	var initialization payment.Initialization
	var status, method string

	err := tx.QueryRowContext(ctx, `
		SELECT id, status::text, payment_method::text, paystack_reference, COALESCE(authorization_url, '')
		FROM payment_initializations
		WHERE payment_attempt_id = $1
		ORDER BY id DESC
		LIMIT 1
	`, paymentAttemptID).Scan(
		&initialization.ID,
		&status,
		&method,
		&initialization.PaystackReference,
		&initialization.AuthorizationURL,
	)

	if errors.Is(err, sql.ErrNoRows) {
		return payment.Initialization{}, false, nil
	}
	if err != nil {
		return payment.Initialization{}, false, err
	}

	initialization.Status, err = parseInitializationStatus(status)
	if err != nil {
		return payment.Initialization{}, false, err
	}

	initialization.PaymentMethod, err = parsePaymentMethod(method)
	if err != nil {
		return payment.Initialization{}, false, err
	}

	return initialization, true, nil
}

func isUnresolvedInitialization(status payment.InitializationStatus) bool {
	switch status {
	case payment.InitializationStatusQueued,
		payment.InitializationStatusSubmitting,
		payment.InitializationStatusReady,
		payment.InitializationStatusUnknown,
		payment.InitializationStatusReviewRequired:
		return true
	default:
		return false
	}
}

func paymentMethodDatabaseValue(method payment.PaymentMethod) (string, error) {
	switch method {
	case payment.PaymentMethodCard:
		return "CARD", nil
	case payment.PaymentMethodMpesa:
		return "MPESA", nil
	default:
		return "", fmt.Errorf("%w: unsupported payment method", payment.ErrInvalidInitializationRequest)
	}
}

func newPaystackReference() (string, error) {
	var randomPart [16]byte

	if _, err := rand.Read(randomPart[:]); err != nil {
		return "", fmt.Errorf("generate Paystack reference: %w", err)
	}

	return "cinema_" + hex.EncodeToString(randomPart[:]), nil
}
