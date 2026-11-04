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
	"example.com/cinema-tickets-backend/internal/payment/postgres/dbgen"
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

	row, err := dbgen.New(p.db).GetLatestInitializationForBooking(ctx, bookingReference)
	if errors.Is(err, sql.ErrNoRows) {
		return payment.Initialization{}, payment.ErrInitializationNotFound
	}
	if err != nil {
		return payment.Initialization{}, fmt.Errorf("load latest payment initialization for booking: %w", err)
	}

	initialization := payment.Initialization{ID: int(row.ID), PaystackReference: row.PaystackReference, AuthorizationURL: row.AuthorizationUrl}
	initialization.Status, err = parseInitializationStatus(row.Status)
	if err != nil {
		return payment.Initialization{}, fmt.Errorf("parse payment initialization status: %w", err)
	}

	initialization.PaymentMethod, err = parsePaymentMethod(row.PaymentMethod)
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

	lockedBooking, err := dbgen.New(tx).LockBookingForInitialization(ctx, request.BookingReference)
	bookingID, bookingStatus, holdActive = int(lockedBooking.ID), lockedBooking.Status, lockedBooking.HoldActive

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

	created, err := dbgen.New(tx).CreatePaymentInitialization(ctx, dbgen.CreatePaymentInitializationParams{
		PaymentAttemptID: int32(paymentAttemptID), PaystackReference: reference, PaymentMethod: dbgen.PaymentMethod(methodName),
	})

	if err != nil {
		return payment.Initialization{}, fmt.Errorf("insert payment initialization: %w", err)
	}

	initialization.ID = int(created.ID)
	initialization.Status, err = parseInitializationStatus(created.Status)
	if err != nil {
		return payment.Initialization{}, err
	}

	if err := saveInitializationReceipt(ctx, tx, request, initialization.ID); err != nil {
		return payment.Initialization{}, err
	}

	initialization.PaymentMethod, err = parsePaymentMethod(created.PaymentMethod)
	if err != nil {
		return payment.Initialization{}, err
	}
	initialization.PaystackReference = created.PaystackReference
	initialization.AuthorizationURL = created.AuthorizationUrl

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

	row, err := dbgen.New(tx).LoadInitializationReceiptByKey(ctx, key)

	if errors.Is(err, sql.ErrNoRows) {
		return initializationReplay{}, false, nil
	}

	if err != nil {
		return initializationReplay{}, false, err
	}
	record.initialization = payment.Initialization{ID: int(row.ID), PaystackReference: row.PaystackReference, AuthorizationURL: row.AuthorizationUrl}
	status, method, requestedMethod = row.Status, row.PaymentMethod, row.RequestedPaymentMethod
	record.fingerprint, record.bookingReference = row.RequestFingerprint, row.BookingReference

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
	count, err := dbgen.New(tx).SavePaymentInitializationReceipt(ctx, dbgen.SavePaymentInitializationReceiptParams{
		IdempotencyKey: request.IdempotencyKey, RequestFingerprint: request.RequestFingerprint,
		RequestedPaymentMethod: dbgen.PaymentMethod(method), PaymentInitializationID: int32(initializationID),
	})
	if err != nil {
		return fmt.Errorf("insert initialization receipt: %w", err)
	}
	if count == 0 {
		return errInitializationReceiptConflict
	}
	return nil
}

func loadUnresolvedInitialization(ctx context.Context, tx *sql.Tx, bookingReference string) (payment.Initialization, bool, error) {
	var initialization payment.Initialization
	var status, method string

	row, err := dbgen.New(tx).LoadUnresolvedInitializationForBooking(ctx, bookingReference)

	if errors.Is(err, sql.ErrNoRows) {
		return payment.Initialization{}, false, nil
	}

	if err != nil {
		return payment.Initialization{}, false, err
	}
	initialization = payment.Initialization{ID: int(row.ID), PaystackReference: row.PaystackReference, AuthorizationURL: row.AuthorizationUrl}
	status, method = row.Status, row.PaymentMethod

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
	queries := dbgen.New(tx)
	attempt, err := queries.LoadPaymentAttemptForBooking(ctx, int32(bookingID))

	if err == nil {
		return int(attempt.ID), attempt.Status, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, "", fmt.Errorf("load payment attempt: %w", err)
	}

	totals, err := queries.GetBookingPriceSnapshotTotals(ctx, int32(bookingID))

	if err != nil {
		return 0, "", fmt.Errorf("calculate payment attempt amount: %w", err)
	}

	if totals.SeatCount == 0 || totals.TotalAmount <= 0 || totals.Currency == "" {
		return 0, "", fmt.Errorf("booking %d has no valid price snapshots", bookingID)
	}

	var paymentAttemptID int
	var status string
	created, err := queries.CreatePaymentAttempt(ctx, dbgen.CreatePaymentAttemptParams{BookingID: int32(bookingID), Amount: int32(totals.TotalAmount), Currency: totals.Currency})

	if err == nil {
		paymentAttemptID, status = int(created.ID), created.Status
	} else if errors.Is(err, sql.ErrNoRows) {
		attempt, err = queries.LoadPaymentAttemptForBooking(ctx, int32(bookingID))
		paymentAttemptID, status = int(attempt.ID), attempt.Status
	}

	if err != nil {
		return 0, "", err
	}

	return paymentAttemptID, status, nil
}

func cancelExpiredBooking(ctx context.Context, tx *sql.Tx, bookingID int) error {
	if err := dbgen.New(tx).CancelExpiredBooking(ctx, int32(bookingID)); err != nil {
		return fmt.Errorf("cancel expired booking: %w", err)
	}

	return releaseExpiredBooking(ctx, tx, bookingID)
}

func cancelExpiredBookingByReference(ctx context.Context, tx *sql.Tx, bookingReference string) error {
	var bookingID int

	lockedID, err := dbgen.New(tx).LockExpiredBookingByReference(ctx, bookingReference)
	bookingID = int(lockedID)

	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}

	if err != nil {
		return fmt.Errorf("lock expired booking after rejected initialization: %w", err)
	}

	_, err = dbgen.New(tx).CancelExpiredBookingAfterRejectedInitialization(ctx, int32(bookingID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("cancel expired booking after rejected initialization: %w", err)
	}

	return releaseExpiredBooking(ctx, tx, bookingID)
}

func releaseExpiredBooking(ctx context.Context, tx *sql.Tx, bookingID int) error {
	if err := dbgen.New(tx).CancelExpiredPaymentAttempt(ctx, int32(bookingID)); err != nil {
		return fmt.Errorf("cancel expired payment attempt after rejected initialization: %w", err)
	}

	if err := dbgen.New(tx).ReleaseBookingSeatClaims(ctx, int32(bookingID)); err != nil {
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

	row, err := dbgen.New(tx).LoadLatestInitializationForAttempt(ctx, int32(paymentAttemptID))

	if errors.Is(err, sql.ErrNoRows) {
		return payment.Initialization{}, false, nil
	}
	if err != nil {
		return payment.Initialization{}, false, err
	}
	initialization = payment.Initialization{ID: int(row.ID), PaystackReference: row.PaystackReference, AuthorizationURL: row.AuthorizationUrl}
	status, method = row.Status, row.PaymentMethod

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
