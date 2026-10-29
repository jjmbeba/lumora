package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"testing"

	"example.com/cinema-tickets-backend/internal/payment"
)

func initializationRequest(rows paymentRows) payment.CreateInitializationRequest {
	return payment.CreateInitializationRequest{
		BookingReference:   fmt.Sprintf("integration-booking-%d", rows.auditoriumID),
		IdempotencyKey:     fmt.Sprintf("integration-init-key-%d", rows.auditoriumID),
		RequestFingerprint: "initialization-fingerprint",
		PaymentMethod:      payment.PaymentMethodCard,
	}
}

func rejectSeedInitialization(t *testing.T, db *sql.DB, rows paymentRows) {
	t.Helper()
	if _, err := db.Exec(`UPDATE payment_initializations SET status = 'REJECTED', lease_expires_at = NULL WHERE id = $1`, rows.initializationID); err != nil {
		t.Fatalf("reject seeded initialization: %v", err)
	}
}

func cleanupNewInitializations(t *testing.T, db *sql.DB, rows paymentRows) {
	t.Helper()
	t.Cleanup(func() {
		if _, err := db.Exec(`DELETE FROM payment_initializations WHERE payment_attempt_id = $1 AND id <> $2`, rows.paymentAttemptID, rows.initializationID); err != nil {
			t.Errorf("clean up new initializations: %v", err)
		}
	})
}

func TestInitializationReplayAndConflict(t *testing.T) {
	db, store := integrationStore(t)
	rows := seedPaymentRows(t, db, "PENDING", "RESERVED")
	request := initializationRequest(rows)

	got, err := store.CreateOrReplayInitialization(context.Background(), request)
	if err != nil || got.ID != rows.initializationID || got.Status != payment.InitializationStatusSubmitting {
		t.Fatalf("replay = %+v, %v", got, err)
	}
	request.RequestFingerprint = "different"
	if _, err := store.CreateOrReplayInitialization(context.Background(), request); !errors.Is(err, payment.ErrInitializationIdempotencyConflict) {
		t.Fatalf("changed fingerprint error = %v, want idempotency conflict", err)
	}
}

func TestInitializationRecordsKeyForExistingUnresolvedWork(t *testing.T) {
	db, store := integrationStore(t)
	rows := seedPaymentRows(t, db, "PENDING", "RESERVED")
	request := initializationRequest(rows)
	request.IdempotencyKey += "-mpesa"
	request.RequestFingerprint = "mpesa-request"
	request.PaymentMethod = payment.PaymentMethodMpesa

	got, err := store.CreateOrReplayInitialization(context.Background(), request)
	if err != nil || got.ID != rows.initializationID || got.PaymentMethod != payment.PaymentMethodCard {
		t.Fatalf("existing initialization = %+v, %v; want original card initialization", got, err)
	}
	var fingerprint, method string
	var initializationID int
	if err := db.QueryRow(`SELECT request_fingerprint, requested_payment_method::text, payment_initialization_id FROM payment_initialization_receipts WHERE idempotency_key = $1`, request.IdempotencyKey).Scan(&fingerprint, &method, &initializationID); err != nil {
		t.Fatalf("load receipt: %v", err)
	}
	if fingerprint != request.RequestFingerprint || method != "MPESA" || initializationID != rows.initializationID {
		t.Fatalf("receipt = %s/%s/%d", fingerprint, method, initializationID)
	}

	got, err = store.CreateOrReplayInitialization(context.Background(), request)
	if err != nil || got.ID != rows.initializationID || got.PaymentMethod != payment.PaymentMethodCard {
		t.Fatalf("replay = %+v, %v", got, err)
	}
	for _, tc := range []struct {
		name   string
		mutate func(*payment.CreateInitializationRequest)
	}{
		{"fingerprint", func(r *payment.CreateInitializationRequest) { r.RequestFingerprint = "different" }},
		{"payment method", func(r *payment.CreateInitializationRequest) { r.PaymentMethod = payment.PaymentMethodCard }},
		{"booking", func(r *payment.CreateInitializationRequest) { r.BookingReference = "different-booking" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			changed := request
			tc.mutate(&changed)
			if _, err := store.CreateOrReplayInitialization(context.Background(), changed); !errors.Is(err, payment.ErrInitializationIdempotencyConflict) {
				t.Fatalf("changed %s: error = %v, want idempotency conflict", tc.name, err)
			}
		})
	}

	rejectSeedInitialization(t, db, rows)
	got, err = store.CreateOrReplayInitialization(context.Background(), request)
	if err != nil || got.ID != rows.initializationID || got.Status != payment.InitializationStatusRejected {
		t.Fatalf("replay after rejection = %+v, %v", got, err)
	}
}

func TestInitializationConcurrentSameKeyDifferentBookings(t *testing.T) {
	db, store := integrationStore(t)
	firstRows := seedPaymentRows(t, db, "PENDING", "RESERVED")
	secondRows := seedPaymentRows(t, db, "PENDING", "RESERVED")
	rejectSeedInitialization(t, db, firstRows)
	rejectSeedInitialization(t, db, secondRows)
	cleanupNewInitializations(t, db, firstRows)
	cleanupNewInitializations(t, db, secondRows)
	first := initializationRequest(firstRows)
	second := initializationRequest(secondRows)
	first.IdempotencyKey += "-shared"
	second.IdempotencyKey = first.IdempotencyKey

	type result struct {
		initialization payment.Initialization
		err            error
	}
	start := make(chan struct{})
	results := make(chan result, 2)
	for _, request := range []payment.CreateInitializationRequest{first, second} {
		go func(request payment.CreateInitializationRequest) {
			<-start
			initialization, err := store.CreateOrReplayInitialization(context.Background(), request)
			results <- result{initialization, err}
		}(request)
	}
	close(start)
	one, two := <-results, <-results
	if !((one.err == nil && errors.Is(two.err, payment.ErrInitializationIdempotencyConflict)) || (two.err == nil && errors.Is(one.err, payment.ErrInitializationIdempotencyConflict))) {
		t.Fatalf("concurrent results = %+v and %+v; want one success and one conflict", one, two)
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM payment_initialization_receipts WHERE idempotency_key = $1`, first.IdempotencyKey).Scan(&count); err != nil || count != 1 {
		t.Fatalf("shared-key receipt count = %d, error = %v; want 1", count, err)
	}
	total := 0
	for _, rows := range []paymentRows{firstRows, secondRows} {
		if err := db.QueryRow(`SELECT count(*) FROM payment_initializations WHERE payment_attempt_id = $1`, rows.paymentAttemptID).Scan(&count); err != nil || (count != 1 && count != 2) {
			t.Fatalf("booking %d initialization count = %d, error = %v; want 1 or 2", rows.bookingID, count, err)
		}
		total += count
	}
	if total != 3 {
		t.Fatalf("initialization count across both bookings = %d, want 3 (losing insert rolled back)", total)
	}
}

func TestInitializationCreatesAttemptFromBookingSnapshots(t *testing.T) {
	db, store := integrationStore(t)
	rows := seedPaymentRows(t, db, "PENDING", "RESERVED")
	if _, err := db.Exec(`DELETE FROM payment_initializations WHERE id = $1`, rows.initializationID); err != nil {
		t.Fatalf("remove seeded initialization: %v", err)
	}
	if _, err := db.Exec(`DELETE FROM payment_attempts WHERE id = $1`, rows.paymentAttemptID); err != nil {
		t.Fatalf("remove seeded payment attempt: %v", err)
	}
	t.Cleanup(func() {
		if _, err := db.Exec(`DELETE FROM payment_initializations WHERE payment_attempt_id IN (SELECT id FROM payment_attempts WHERE booking_id = $1)`, rows.bookingID); err != nil {
			t.Errorf("clean up initialization: %v", err)
		}
		if _, err := db.Exec(`DELETE FROM payment_attempts WHERE booking_id = $1`, rows.bookingID); err != nil {
			t.Errorf("clean up payment attempt: %v", err)
		}
	})

	got, err := store.CreateOrReplayInitialization(context.Background(), initializationRequest(rows))
	if err != nil || got.Status != payment.InitializationStatusQueued || got.PaystackReference == "" {
		t.Fatalf("create initialization = %+v, %v", got, err)
	}
	var amount int
	var currency, status string
	if err := db.QueryRow(`SELECT amount, currency, status::text FROM payment_attempts WHERE booking_id = $1`, rows.bookingID).Scan(&amount, &currency, &status); err != nil {
		t.Fatal(err)
	}
	if amount != 2500 || currency != "KES" || status != "PENDING" {
		t.Fatalf("attempt = %d %s %s, want 2500 KES PENDING", amount, currency, status)
	}
}

func TestInitializationConcurrentRetryCreatesOneRecord(t *testing.T) {
	db, store := integrationStore(t)
	rows := seedPaymentRows(t, db, "PENDING", "RESERVED")
	rejectSeedInitialization(t, db, rows)
	cleanupNewInitializations(t, db, rows)
	request := initializationRequest(rows)
	request.IdempotencyKey += "-retry"
	request.RequestFingerprint = "retry-fingerprint"

	type result struct {
		initialization payment.Initialization
		err            error
	}
	start := make(chan struct{})
	results := make(chan result, 2)
	var ready sync.WaitGroup
	ready.Add(2)
	for i := 0; i < 2; i++ {
		go func() {
			ready.Done()
			<-start
			initialization, err := store.CreateOrReplayInitialization(context.Background(), request)
			results <- result{initialization, err}
		}()
	}
	ready.Wait()
	close(start)
	first, second := <-results, <-results
	if first.err != nil || second.err != nil || first.initialization.ID == rows.initializationID || first.initialization.ID != second.initialization.ID || first.initialization.Status != payment.InitializationStatusQueued {
		t.Fatalf("concurrent results = %+v and %+v", first, second)
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM payment_initializations WHERE payment_attempt_id = $1`, rows.paymentAttemptID).Scan(&count); err != nil || count != 2 {
		t.Fatalf("initialization count = %d, error = %v; want 2", count, err)
	}
}

func TestInitializationExpiredHoldCancelsAfterRejection(t *testing.T) {
	for _, replay := range []bool{false, true} {
		t.Run(fmt.Sprintf("replay=%t", replay), func(t *testing.T) {
			db, store := integrationStore(t)
			rows := seedPaymentRows(t, db, "PENDING", "RESERVED")
			rejectSeedInitialization(t, db, rows)
			if _, err := db.Exec(`UPDATE bookings SET created_at = clock_timestamp() - interval '2 seconds', hold_expires_at = clock_timestamp() - interval '1 second' WHERE id = $1`, rows.bookingID); err != nil {
				t.Fatalf("expire booking: %v", err)
			}
			request := initializationRequest(rows)
			if !replay {
				request.IdempotencyKey += "-retry"
			}
			got, err := store.CreateOrReplayInitialization(context.Background(), request)
			if replay && (err != nil || got.ID != rows.initializationID) {
				t.Fatalf("replay = %+v, %v", got, err)
			}
			if !replay && !errors.Is(err, payment.ErrBookingNotEligibleForPayment) {
				t.Fatalf("expired retry error = %v", err)
			}
			state := readPaymentState(t, db, rows)
			if state.bookingStatus != "CANCELLED" || state.paymentStatus != "CANCELLED" || state.initializationStatus != "REJECTED" {
				t.Fatalf("expired state = %+v", state)
			}
			assertSeatClaims(t, db, rows, 0)
		})
	}
}

func TestInitializationOldReceiptDoesNotCancelNewerUnresolvedWork(t *testing.T) {
	db, store := integrationStore(t)
	rows := seedPaymentRows(t, db, "PENDING", "RESERVED")
	rejectSeedInitialization(t, db, rows)
	cleanupNewInitializations(t, db, rows)
	oldRequest := initializationRequest(rows)
	newRequest := oldRequest
	newRequest.IdempotencyKey += "-retry"
	newRequest.RequestFingerprint = "new-request"
	newInitialization, err := store.CreateOrReplayInitialization(context.Background(), newRequest)
	if err != nil || newInitialization.ID == rows.initializationID || newInitialization.Status != payment.InitializationStatusQueued {
		t.Fatalf("create newer initialization = %+v, %v", newInitialization, err)
	}
	if _, err := db.Exec(`UPDATE bookings SET created_at = clock_timestamp() - interval '2 seconds', hold_expires_at = clock_timestamp() - interval '1 second' WHERE id = $1`, rows.bookingID); err != nil {
		t.Fatalf("expire booking: %v", err)
	}
	got, err := store.CreateOrReplayInitialization(context.Background(), oldRequest)
	if err != nil || got.ID != rows.initializationID || got.Status != payment.InitializationStatusRejected {
		t.Fatalf("replay older receipt = %+v, %v", got, err)
	}
	state := readPaymentState(t, db, rows)
	if state.bookingStatus != "RESERVED" || state.paymentStatus != "PENDING" {
		t.Fatalf("newer unresolved work lost booking or attempt: %+v", state)
	}
	assertSeatClaims(t, db, rows, 1)
	var status string
	if err := db.QueryRow(`SELECT status::text FROM payment_initializations WHERE id = $1`, newInitialization.ID).Scan(&status); err != nil || status != "QUEUED" {
		t.Fatalf("newer initialization status = %s, error = %v; want QUEUED", status, err)
	}
}

func TestInitializationUnresolvedStatusAgreesWithIndexAndLookup(t *testing.T) {
	db, _ := integrationStore(t)
	rows := seedPaymentRows(t, db, "PENDING", "RESERVED")
	var predicate string
	if err := db.QueryRow(`SELECT pg_get_expr(indpred, indrelid) FROM pg_index WHERE indexrelid = 'one_unresolved_initialization_per_payment_attempt'::regclass`).Scan(&predicate); err != nil {
		t.Fatalf("read unresolved index predicate: %v", err)
	}
	statusRows, err := db.Query(`SELECT unnest(enum_range(NULL::"PaymentInitializationStatus"))::text`)
	if err != nil {
		t.Fatalf("read initialization statuses: %v", err)
	}
	var statuses []string
	for statusRows.Next() {
		var status string
		if err := statusRows.Scan(&status); err != nil {
			t.Fatal(err)
		}
		statuses = append(statuses, status)
	}
	if err := statusRows.Err(); err != nil {
		t.Fatal(err)
	}
	statusRows.Close()
	for _, status := range statuses {
		t.Run(status, func(t *testing.T) {
			parsed, err := parseInitializationStatus(status)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`UPDATE payment_initializations SET status = $1::"PaymentInitializationStatus", lease_expires_at = CASE WHEN $1 = 'SUBMITTING' THEN clock_timestamp() + interval '1 minute' ELSE NULL END WHERE id = $2`, status, rows.initializationID); err != nil {
				t.Fatalf("set initialization status: %v", err)
			}
			var indexed bool
			query := `SELECT ` + predicate + ` FROM (SELECT $1::"PaymentInitializationStatus" AS status) AS payment_initializations`
			if err := db.QueryRow(query, status).Scan(&indexed); err != nil {
				t.Fatalf("evaluate unresolved index: %v", err)
			}
			tx, err := db.BeginTx(context.Background(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			_, found, err := loadUnresolvedInitialization(context.Background(), tx, initializationRequest(rows).BookingReference)
			if err != nil {
				t.Fatal(err)
			}
			if want := isUnresolvedInitialization(parsed); found != want || indexed != want {
				t.Errorf("status %s: Go=%t, lookup=%t, index=%t", status, want, found, indexed)
			}
		})
	}
}

func TestInitializationUnresolvedStatusClassification(t *testing.T) {
	for _, test := range []struct {
		status     payment.InitializationStatus
		unresolved bool
	}{
		{payment.InitializationStatusQueued, true},
		{payment.InitializationStatusSubmitting, true},
		{payment.InitializationStatusReady, true},
		{payment.InitializationStatusUnknown, true},
		{payment.InitializationStatusReviewRequired, true},
		{payment.InitializationStatusPaid, false},
		{payment.InitializationStatusFailed, false},
		{payment.InitializationStatusRejected, false},
		{payment.InitializationStatusCancelled, false},
		{payment.InitializationStatusInvalid, false},
	} {
		if got := isUnresolvedInitialization(test.status); got != test.unresolved {
			t.Errorf("status %v: unresolved = %t, want %t", test.status, got, test.unresolved)
		}
	}
}
