package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"example.com/cinema-tickets-backend/internal/payment"
	_ "github.com/jackc/pgx/v5/stdlib"
)

var integrationID atomic.Int64

func init() {
	integrationID.Store(time.Now().UnixNano() % 1_000_000_000)
}

func TestMarkUnknownPersistsRetryAndClearsLease(t *testing.T) {
	db, store := integrationStore(t)
	rows := seedPaymentRows(t, db, "PENDING", "RESERVED")
	claim := rows.claim()
	retryAt := time.Now().UTC().Add(30 * time.Second).Truncate(time.Microsecond)

	if err := store.MarkUnknown(context.Background(), claim, retryAt); err != nil {
		t.Fatalf("MarkUnknown() error = %v", err)
	}

	state := readPaymentState(t, db, rows)
	if state.initializationStatus != "UNKNOWN" {
		t.Errorf("initialization status = %q, want UNKNOWN", state.initializationStatus)
	}
	if state.leaseExpiresAt.Valid {
		t.Errorf("lease_expires_at = %v, want NULL", state.leaseExpiresAt.Time)
	}
	if !state.nextAttemptAt.Valid || !state.nextAttemptAt.Time.Equal(retryAt) {
		t.Errorf("next_attempt_at = %v, want %v", state.nextAttemptAt, retryAt)
	}
	if state.generation != claim.Generation {
		t.Errorf("generation = %d, want unchanged value %d", state.generation, claim.Generation)
	}
}

func TestMarkReadyCapsFirstVerificationAtHoldExpiry(t *testing.T) {
	db, store := integrationStore(t)
	rows := seedPaymentRows(t, db, "PENDING", "RESERVED")
	claim := rows.claim()
	var holdExpiresAt time.Time
	if err := db.QueryRow(`SELECT hold_expires_at FROM bookings WHERE id = $1`, rows.bookingID).Scan(&holdExpiresAt); err != nil {
		t.Fatalf("read booking hold expiry: %v", err)
	}

	if err := store.MarkReady(context.Background(), claim, "https://checkout.example.test/test", holdExpiresAt.Add(time.Hour)); err != nil {
		t.Fatalf("MarkReady() error = %v", err)
	}

	state := readPaymentState(t, db, rows)
	if state.initializationStatus != "READY" || !state.nextAttemptAt.Valid || !state.nextAttemptAt.Time.Equal(holdExpiresAt) {
		t.Errorf("ready schedule = %q/%v, want READY at hold expiry %v", state.initializationStatus, state.nextAttemptAt, holdExpiresAt)
	}
}

func TestMarkVerificationPendingSchedulesNextCheck(t *testing.T) {
	db, store := integrationStore(t)
	rows := seedPaymentRows(t, db, "PENDING", "RESERVED")
	claim := rows.claim()
	nextCheckAt := time.Now().UTC().Add(30 * time.Second).Truncate(time.Microsecond)

	if err := store.MarkVerificationPending(context.Background(), claim, nextCheckAt); err != nil {
		t.Fatalf("MarkVerificationPending() error = %v", err)
	}

	state := readPaymentState(t, db, rows)
	if state.initializationStatus != "READY" || !state.nextAttemptAt.Valid || !state.nextAttemptAt.Time.Equal(nextCheckAt) {
		t.Errorf("scheduled state = %q/%v, want READY/%v", state.initializationStatus, state.nextAttemptAt, nextCheckAt)
	}
	if state.leaseExpiresAt.Valid {
		t.Errorf("lease_expires_at = %v, want NULL", state.leaseExpiresAt.Time)
	}
}

func TestMarkVerificationPendingCapsNextCheckAtHoldExpiry(t *testing.T) {
	db, store := integrationStore(t)
	rows := seedPaymentRows(t, db, "PENDING", "RESERVED")
	var holdExpiresAt time.Time
	if err := db.QueryRow(`SELECT hold_expires_at FROM bookings WHERE id = $1`, rows.bookingID).Scan(&holdExpiresAt); err != nil {
		t.Fatalf("read booking hold expiry: %v", err)
	}

	if err := store.MarkVerificationPending(context.Background(), rows.claim(), holdExpiresAt.Add(time.Hour)); err != nil {
		t.Fatalf("MarkVerificationPending() error = %v", err)
	}

	state := readPaymentState(t, db, rows)
	if !state.nextAttemptAt.Valid || !state.nextAttemptAt.Time.Equal(holdExpiresAt) {
		t.Errorf("next_attempt_at = %v, want capped at hold expiry %v", state.nextAttemptAt, holdExpiresAt)
	}
}

func TestMarkVerificationPendingMovesExpiredHoldToReviewAndPreservesSeats(t *testing.T) {
	db, store := integrationStore(t)
	rows := seedPaymentRows(t, db, "PENDING", "RESERVED")
	if _, err := db.Exec(`UPDATE bookings SET created_at = clock_timestamp() - interval '2 seconds', hold_expires_at = clock_timestamp() - interval '1 second' WHERE id = $1`, rows.bookingID); err != nil {
		t.Fatalf("expire booking hold: %v", err)
	}

	if err := store.MarkVerificationPending(context.Background(), rows.claim(), time.Now().UTC().Add(time.Minute)); err != nil {
		t.Fatalf("MarkVerificationPending() error = %v", err)
	}

	state := readPaymentState(t, db, rows)
	if state.initializationStatus != "REVIEW_REQUIRED" || state.paymentStatus != "PENDING" || state.bookingStatus != "PAYMENT_REVIEW" {
		t.Errorf("unexpected expired-hold state: initialization=%q payment=%q booking=%q", state.initializationStatus, state.paymentStatus, state.bookingStatus)
	}
	if state.leaseExpiresAt.Valid || state.nextAttemptAt.Valid {
		t.Errorf("review initialization retained scheduling: lease=%v next=%v", state.leaseExpiresAt, state.nextAttemptAt)
	}
	assertSeatClaims(t, db, rows, 1)
}

func TestMarkUnknownMovesExpiredHoldToReviewAndPreservesSeats(t *testing.T) {
	db, store := integrationStore(t)
	rows := seedPaymentRows(t, db, "PENDING", "RESERVED")
	if _, err := db.Exec(`UPDATE bookings SET created_at = clock_timestamp() - interval '2 seconds', hold_expires_at = clock_timestamp() - interval '1 second' WHERE id = $1`, rows.bookingID); err != nil {
		t.Fatalf("expire booking hold: %v", err)
	}

	if err := store.MarkUnknown(context.Background(), rows.claim(), time.Now().UTC().Add(time.Minute)); err != nil {
		t.Fatalf("MarkUnknown() error = %v", err)
	}

	state := readPaymentState(t, db, rows)
	if state.initializationStatus != "REVIEW_REQUIRED" || state.paymentStatus != "PENDING" || state.bookingStatus != "PAYMENT_REVIEW" {
		t.Errorf("unexpected expired-hold state: initialization=%q payment=%q booking=%q", state.initializationStatus, state.paymentStatus, state.bookingStatus)
	}
	assertSeatClaims(t, db, rows, 1)
}

func TestWebhookStorePersistsProviderStatusAndTimestamps(t *testing.T) {
	db, _ := integrationStore(t)
	var webhookSchemaReady bool
	if err := db.QueryRow(`SELECT to_regclass('webhook_inbox') IS NOT NULL`).Scan(&webhookSchemaReady); err != nil {
		t.Fatalf("check webhook inbox schema: %v", err)
	}
	if !webhookSchemaReady {
		t.Fatal("webhook_inbox is missing; apply migrations/webhooks.sql to the test database first")
	}

	rows := seedPaymentRows(t, db, "PENDING", "RESERVED")
	if _, err := db.Exec(`
		UPDATE payment_initializations
		SET status = 'READY', lease_expires_at = NULL,
			next_attempt_at = clock_timestamp() + interval '1 hour'
		WHERE id = $1
	`, rows.initializationID); err != nil {
		t.Fatalf("prepare ready initialization: %v", err)
	}

	t.Cleanup(func() {
		if _, err := db.Exec(`DELETE FROM webhook_inbox WHERE reference = $1`, rows.reference); err != nil {
			t.Errorf("clean up webhook inbox row: %v", err)
		}
	})

	store, err := NewWebhookStore(db)
	if err != nil {
		t.Fatalf("create webhook store: %v", err)
	}
	providerPaidAt := time.Date(2026, time.October, 20, 9, 30, 0, 0, time.UTC)
	event := payment.ProviderWebhookEvent{
		EventType:      "charge.success",
		TransactionID:  int64(rows.paymentAttemptID),
		Reference:      rows.reference,
		ProviderStatus: "success",
		AmountMinor:    2500,
		Currency:       "KES",
		ProviderPaidAt: providerPaidAt,
	}
	requestStartedAt := time.Now().UTC()
	if err := store.RecordAndSchedule(context.Background(), event); err != nil {
		t.Fatalf("RecordAndSchedule() error = %v", err)
	}
	requestFinishedAt := time.Now().UTC()

	var receiptTime, storedPaidAt time.Time
	var providerStatus string
	err = db.QueryRow(`
		SELECT receipt_time, provider_paid_at, provider_status
		FROM webhook_inbox
		WHERE reference = $1
	`, rows.reference).Scan(&receiptTime, &storedPaidAt, &providerStatus)
	if err != nil {
		t.Fatalf("read webhook inbox row: %v", err)
	}
	if receiptTime.Before(requestStartedAt.Add(-time.Second)) || receiptTime.After(requestFinishedAt.Add(time.Second)) {
		t.Errorf("receipt_time = %v, want database receipt time near %v", receiptTime, requestStartedAt)
	}
	if !storedPaidAt.Equal(providerPaidAt) {
		t.Errorf("provider_paid_at = %v, want %v", storedPaidAt, providerPaidAt)
	}
	if providerStatus != "success" {
		t.Errorf("provider_status = %q, want %q", providerStatus, "success")
	}

	var status string
	var nextAttemptAt time.Time
	if err := db.QueryRow(`
		SELECT status::text, next_attempt_at
		FROM payment_initializations
		WHERE id = $1
	`, rows.initializationID).Scan(&status, &nextAttemptAt); err != nil {
		t.Fatalf("read scheduled initialization: %v", err)
	}
	if status != "READY" || nextAttemptAt.After(time.Now().UTC().Add(time.Second)) {
		t.Errorf("initialization status/next attempt = %q/%v, want READY and due now", status, nextAttemptAt)
	}
}

func TestWebhookAfterCancellationMovesInitializationToReviewWithoutRestoringSeats(t *testing.T) {
	db, _ := integrationStore(t)
	rows := seedPaymentRows(t, db, "CANCELLED", "CANCELLED")
	t.Cleanup(func() {
		if _, err := db.Exec(`DELETE FROM webhook_inbox WHERE reference = $1`, rows.reference); err != nil {
			t.Errorf("clean up webhook inbox row: %v", err)
		}
	})
	if _, err := db.Exec(`UPDATE payment_initializations SET status = 'CANCELLED', lease_expires_at = NULL, next_attempt_at = NULL WHERE id = $1`, rows.initializationID); err != nil {
		t.Fatalf("cancel initialization: %v", err)
	}
	if _, err := db.Exec(`DELETE FROM seat_claims WHERE booking_id = $1`, rows.bookingID); err != nil {
		t.Fatalf("release seat claims: %v", err)
	}

	store, err := NewWebhookStore(db)
	if err != nil {
		t.Fatalf("create webhook store: %v", err)
	}
	event := payment.ProviderWebhookEvent{
		EventType:      "charge.success",
		TransactionID:  int64(rows.paymentAttemptID),
		Reference:      rows.reference,
		ProviderStatus: "success",
		AmountMinor:    2500,
		Currency:       "KES",
		ProviderPaidAt: time.Now().UTC(),
	}
	if err := store.RecordAndSchedule(context.Background(), event); err != nil {
		t.Fatalf("RecordAndSchedule() error = %v", err)
	}

	state := readPaymentState(t, db, rows)
	if state.initializationStatus != "REVIEW_REQUIRED" || state.paymentStatus != "CANCELLED" || state.bookingStatus != "CANCELLED" {
		t.Errorf("unexpected late-webhook state: initialization=%q payment=%q booking=%q", state.initializationStatus, state.paymentStatus, state.bookingStatus)
	}
	assertSeatClaims(t, db, rows, 0)
}

func TestMarkUnknownRejectsStaleGenerationWithoutChangingRow(t *testing.T) {
	db, store := integrationStore(t)
	rows := seedPaymentRows(t, db, "PENDING", "RESERVED")
	claim := rows.claim()
	claim.Generation--

	err := store.MarkUnknown(context.Background(), claim, time.Now().UTC().Add(time.Minute))
	if !errors.Is(err, payment.ErrClaimLost) {
		t.Fatalf("MarkUnknown() error = %v, want ErrClaimLost", err)
	}

	state := readPaymentState(t, db, rows)
	if state.initializationStatus != "SUBMITTING" || !state.leaseExpiresAt.Valid || state.nextAttemptAt.Valid {
		t.Errorf("stale claim changed state: init=%q lease=%v next=%v", state.initializationStatus, state.leaseExpiresAt, state.nextAttemptAt)
	}
}

func TestMarkRejectedPersistsReasonAndClearsScheduling(t *testing.T) {
	db, store := integrationStore(t)
	rows := seedPaymentRows(t, db, "PENDING", "RESERVED")
	reason := "Paystack rejected the checkout request"

	if err := store.MarkRejected(context.Background(), rows.claim(), reason); err != nil {
		t.Fatalf("MarkRejected() error = %v", err)
	}

	state := readPaymentState(t, db, rows)
	if state.initializationStatus != "REJECTED" || state.leaseExpiresAt.Valid || state.nextAttemptAt.Valid {
		t.Errorf("unexpected rejected initialization state: status=%q lease=%v next=%v", state.initializationStatus, state.leaseExpiresAt, state.nextAttemptAt)
	}
	var persistedReason string
	if err := db.QueryRow(`SELECT rejection_reason FROM payment_initializations WHERE id = $1`, rows.initializationID).Scan(&persistedReason); err != nil {
		t.Fatalf("read rejection reason: %v", err)
	}
	if persistedReason != reason {
		t.Errorf("rejection_reason = %q, want %q", persistedReason, reason)
	}
}

func TestMarkPaidUpdatesInitializationAttemptAndBooking(t *testing.T) {
	db, store := integrationStore(t)
	rows := seedPaymentRows(t, db, "PENDING", "RESERVED")
	claim := rows.claim()

	if err := store.MarkPaid(context.Background(), claim, paidVerification(claim)); err != nil {
		t.Fatalf("MarkPaid() error = %v", err)
	}

	state := readPaymentState(t, db, rows)
	if state.initializationStatus != "PAID" || state.paymentStatus != "PAID" || state.bookingStatus != "BOOKED" {
		t.Errorf("unexpected paid state: initialization=%q payment=%q booking=%q", state.initializationStatus, state.paymentStatus, state.bookingStatus)
	}
	if state.leaseExpiresAt.Valid || state.nextAttemptAt.Valid {
		t.Errorf("terminal initialization retained scheduling fields: lease=%v next=%v", state.leaseExpiresAt, state.nextAttemptAt)
	}
}

func TestMarkPaidIsIdempotentWhenAttemptAndBookingAlreadyPaid(t *testing.T) {
	db, store := integrationStore(t)
	rows := seedPaymentRows(t, db, "PAID", "BOOKED")
	claim := rows.claim()

	if err := store.MarkPaid(context.Background(), claim, paidVerification(claim)); err != nil {
		t.Fatalf("MarkPaid() error = %v, want idempotent success", err)
	}

	state := readPaymentState(t, db, rows)
	if state.initializationStatus != "PAID" || state.paymentStatus != "PAID" || state.bookingStatus != "BOOKED" {
		t.Errorf("unexpected idempotent paid state: initialization=%q payment=%q booking=%q", state.initializationStatus, state.paymentStatus, state.bookingStatus)
	}
}

func TestMarkPaidConflictRollsBackAllChanges(t *testing.T) {
	db, store := integrationStore(t)
	rows := seedPaymentRows(t, db, "PENDING", "CANCELLED")
	claim := rows.claim()

	err := store.MarkPaid(context.Background(), claim, paidVerification(claim))
	if !errors.Is(err, payment.ErrPaymentStateConflict) {
		t.Fatalf("MarkPaid() error = %v, want ErrPaymentStateConflict", err)
	}

	state := readPaymentState(t, db, rows)
	if state.initializationStatus != "SUBMITTING" || state.paymentStatus != "PENDING" || state.bookingStatus != "CANCELLED" {
		t.Errorf("conflicting paid transition was not rolled back: initialization=%q payment=%q booking=%q", state.initializationStatus, state.paymentStatus, state.bookingStatus)
	}
	if !state.leaseExpiresAt.Valid {
		t.Error("rollback cleared the submitting lease")
	}
}

func TestMarkFailedTransitionsPendingPayment(t *testing.T) {
	db, store := integrationStore(t)
	rows := seedPaymentRows(t, db, "PENDING", "RESERVED")

	if err := store.MarkFailed(context.Background(), rows.claim()); err != nil {
		t.Fatalf("MarkFailed() error = %v", err)
	}

	state := readPaymentState(t, db, rows)
	if state.initializationStatus != "FAILED" || state.paymentStatus != "FAILED" || state.bookingStatus != "CANCELLED" {
		t.Errorf("unexpected failed state: initialization=%q payment=%q booking=%q", state.initializationStatus, state.paymentStatus, state.bookingStatus)
	}
	if state.leaseExpiresAt.Valid || state.nextAttemptAt.Valid {
		t.Errorf("terminal initialization retained scheduling fields: lease=%v next=%v", state.leaseExpiresAt, state.nextAttemptAt)
	}
	assertSeatClaims(t, db, rows, 0)
	assertBookingSeatHistory(t, db, rows, 1)
}

func TestMarkFailedMovesAlreadyPaidAttemptToReview(t *testing.T) {
	db, store := integrationStore(t)
	rows := seedPaymentRows(t, db, "PAID", "BOOKED")

	if err := store.MarkFailed(context.Background(), rows.claim()); err != nil {
		t.Fatalf("MarkFailed() error = %v", err)
	}

	state := readPaymentState(t, db, rows)
	if state.initializationStatus != "REVIEW_REQUIRED" || state.paymentStatus != "PAID" || state.bookingStatus != "PAYMENT_REVIEW" {
		t.Errorf("unexpected review state: initialization=%q payment=%q booking=%q", state.initializationStatus, state.paymentStatus, state.bookingStatus)
	}
	assertSeatClaims(t, db, rows, 1)
}

func TestMarkFailedDoesNotOverwriteBookedBooking(t *testing.T) {
	db, store := integrationStore(t)
	rows := seedPaymentRows(t, db, "PENDING", "BOOKED")

	if err := store.MarkFailed(context.Background(), rows.claim()); err != nil {
		t.Fatalf("MarkFailed() error = %v", err)
	}

	state := readPaymentState(t, db, rows)
	if state.initializationStatus != "REVIEW_REQUIRED" || state.paymentStatus != "PENDING" || state.bookingStatus != "PAYMENT_REVIEW" {
		t.Errorf("unexpected conflict state: initialization=%q payment=%q booking=%q", state.initializationStatus, state.paymentStatus, state.bookingStatus)
	}
	assertSeatClaims(t, db, rows, 1)
}

func TestMarkFailedRejectsStaleClaimWithoutReleasingSeats(t *testing.T) {
	db, store := integrationStore(t)
	rows := seedPaymentRows(t, db, "PENDING", "RESERVED")
	claim := rows.claim()
	claim.Generation--

	err := store.MarkFailed(context.Background(), claim)
	if !errors.Is(err, payment.ErrClaimLost) {
		t.Fatalf("MarkFailed() error = %v, want ErrClaimLost", err)
	}

	state := readPaymentState(t, db, rows)
	if state.initializationStatus != "SUBMITTING" || state.paymentStatus != "PENDING" || state.bookingStatus != "RESERVED" {
		t.Errorf("stale claim changed state: initialization=%q payment=%q booking=%q", state.initializationStatus, state.paymentStatus, state.bookingStatus)
	}
	assertSeatClaims(t, db, rows, 1)
}

func TestMarkFailedRollsBackAllChangesWhenSeatReleaseFails(t *testing.T) {
	db, store := integrationStore(t)
	rows := seedPaymentRows(t, db, "PENDING", "RESERVED")
	triggerName := fmt.Sprintf("fail_seat_claim_delete_%d", rows.bookingID)
	functionName := fmt.Sprintf("fail_seat_claim_delete_fn_%d", rows.bookingID)
	t.Cleanup(func() {
		if _, err := db.Exec(fmt.Sprintf(`DROP TRIGGER IF EXISTS %s ON seat_claims`, triggerName)); err != nil {
			t.Errorf("drop rollback test trigger: %v", err)
		}
		if _, err := db.Exec(fmt.Sprintf(`DROP FUNCTION IF EXISTS %s()`, functionName)); err != nil {
			t.Errorf("drop rollback test function: %v", err)
		}
	})
	_, err := db.Exec(fmt.Sprintf(`
		CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN
			IF OLD.booking_id = %d THEN
				RAISE EXCEPTION 'forced seat claim delete failure';
			END IF;
			RETURN OLD;
		END;
		$$;
	`, functionName, rows.bookingID))
	if err != nil {
		t.Fatalf("create rollback test function: %v", err)
	}
	if _, err := db.Exec(fmt.Sprintf(`CREATE TRIGGER %s BEFORE DELETE ON seat_claims FOR EACH ROW EXECUTE FUNCTION %s()`, triggerName, functionName)); err != nil {
		t.Fatalf("create rollback test trigger: %v", err)
	}

	if err := store.MarkFailed(context.Background(), rows.claim()); err == nil {
		t.Fatal("MarkFailed() error = nil, want forced seat claim delete failure")
	}

	state := readPaymentState(t, db, rows)
	if state.initializationStatus != "SUBMITTING" || state.paymentStatus != "PENDING" || state.bookingStatus != "RESERVED" {
		t.Errorf("failed transition was not rolled back: initialization=%q payment=%q booking=%q", state.initializationStatus, state.paymentStatus, state.bookingStatus)
	}
	assertSeatClaims(t, db, rows, 1)
}

type paymentRows struct {
	auditoriumID     int
	movieID          int
	seatID           int
	screeningID      int
	bookingID        int
	paymentAttemptID int
	initializationID int
	reference        string
}

func (rows paymentRows) claim() payment.Claim {
	return payment.Claim{
		InitializationID:  rows.initializationID,
		PreviousStatus:    payment.InitializationStatusSubmitting,
		Generation:        7,
		PaystackReference: rows.reference,
		AmountMinor:       2500,
		Currency:          "KES",
	}
}

type paymentState struct {
	initializationStatus string
	leaseExpiresAt       sql.NullTime
	nextAttemptAt        sql.NullTime
	generation           int
	paymentStatus        string
	bookingStatus        string
}

func integrationStore(t *testing.T) (*sql.DB, *PostgresStore) {
	t.Helper()

	dsn := os.Getenv("PAYMENT_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set PAYMENT_TEST_DATABASE_URL to a dedicated PostgreSQL database with migrations/migration.sql applied")
	}

	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open PostgreSQL test database: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("close PostgreSQL test database: %v", err)
		}
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		t.Fatalf("connect to PostgreSQL test database: %v", err)
	}

	var schemaReady bool
	if err := db.QueryRowContext(ctx, `SELECT to_regclass('payment_initializations') IS NOT NULL`).Scan(&schemaReady); err != nil {
		t.Fatalf("check PostgreSQL test schema: %v", err)
	}
	if !schemaReady {
		t.Fatalf("payment_initializations is missing; apply migrations/migration.sql to the test database first")
	}

	store, err := NewPostgresStore(db, 30*time.Second)
	if err != nil {
		t.Fatalf("create PostgreSQL store: %v", err)
	}
	return db, store
}

func seedPaymentRows(t *testing.T, db *sql.DB, paymentStatus, bookingStatus string) paymentRows {
	t.Helper()

	base := int(integrationID.Add(10))
	rows := paymentRows{
		auditoriumID:     base,
		movieID:          base + 1,
		seatID:           base + 2,
		screeningID:      base + 3,
		bookingID:        base + 4,
		paymentAttemptID: base + 5,
		initializationID: base + 6,
		reference:        fmt.Sprintf("integration-ref-%d", base),
	}
	now := time.Now().UTC().Truncate(time.Microsecond)

	insert := func(query string, args ...any) {
		t.Helper()
		if _, err := db.Exec(query, args...); err != nil {
			t.Fatalf("seed PostgreSQL test row: %v", err)
		}
	}

	insert(`INSERT INTO auditoriums (id, name, created_at) VALUES ($1, $2, $3)`, rows.auditoriumID, fmt.Sprintf("integration-auditorium-%d", base), now)
	insert(`INSERT INTO movies (id, name, description, runtime_minutes, created_at) VALUES ($1, $2, $3, $4, $5)`, rows.movieID, fmt.Sprintf("integration-movie-%d", base), "integration test movie", 90, now)
	insert(`INSERT INTO seats (id, label, auditorium_id, created_at) VALUES ($1, $2, $3, $4)`, rows.seatID, fmt.Sprintf("integration-seat-%d", base), rows.auditoriumID, now)
	insert(`INSERT INTO screenings (id, movie_id, auditorium_id, currency, start_time, end_time, created_at) VALUES ($1, $2, $3, $4, $5, $6, $7)`, rows.screeningID, rows.movieID, rows.auditoriumID, "KES", now.Add(time.Hour), now.Add(2*time.Hour), now)
	insert(`INSERT INTO screening_seats (screening_id, seat_id, auditorium_id, price_amount) VALUES ($1, $2, $3, $4)`, rows.screeningID, rows.seatID, rows.auditoriumID, 2500)
	insert(`INSERT INTO bookings (id, booking_reference, idempotency_key, request_fingerprint, customer_email, customer_phone, status, screening_id, hold_expires_at, created_at) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`, rows.bookingID, fmt.Sprintf("integration-booking-%d", base), fmt.Sprintf("integration-booking-key-%d", base), "booking-fingerprint", "test@example.test", "+254700000000", bookingStatus, rows.screeningID, now.Add(time.Hour), now)
	insert(`INSERT INTO bookings_seats (booking_id, seat_id, screening_id, unit_price_amount, currency) VALUES ($1, $2, $3, $4, $5)`, rows.bookingID, rows.seatID, rows.screeningID, 2500, "KES")
	insert(`INSERT INTO seat_claims (screening_id, seat_id, booking_id) VALUES ($1, $2, $3)`, rows.screeningID, rows.seatID, rows.bookingID)
	insert(`INSERT INTO payment_attempts (id, booking_id, status, amount, currency, created_at) VALUES ($1, $2, $3, $4, $5, $6)`, rows.paymentAttemptID, rows.bookingID, paymentStatus, 2500, "KES", now)
	insert(`INSERT INTO payment_initializations (id, payment_attempt_id, paystack_reference, payment_method, status, worker_attempt_count, next_attempt_at, lease_expires_at, created_at, updated_at) VALUES ($1, $2, $3, 'CARD', 'SUBMITTING', 7, NULL, $4, $5, $5)`, rows.initializationID, rows.paymentAttemptID, rows.reference, now.Add(30*time.Second), now)
	insert(`INSERT INTO payment_initialization_receipts (idempotency_key, request_fingerprint, requested_payment_method, payment_initialization_id) VALUES ($1, $2, 'CARD', $3)`, fmt.Sprintf("integration-init-key-%d", base), "initialization-fingerprint", rows.initializationID)

	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		for _, statement := range []struct {
			query string
			id    int
		}{
			{`DELETE FROM seat_claims WHERE booking_id = $1`, rows.bookingID},
			{`DELETE FROM bookings_seats WHERE booking_id = $1`, rows.bookingID},
			{`DELETE FROM payment_initializations WHERE id = $1`, rows.initializationID},
			{`DELETE FROM payment_attempts WHERE id = $1`, rows.paymentAttemptID},
			{`DELETE FROM bookings WHERE id = $1`, rows.bookingID},
			{`DELETE FROM screening_seats WHERE screening_id = $1`, rows.screeningID},
			{`DELETE FROM screenings WHERE id = $1`, rows.screeningID},
			{`DELETE FROM seats WHERE id = $1`, rows.seatID},
			{`DELETE FROM movies WHERE id = $1`, rows.movieID},
			{`DELETE FROM auditoriums WHERE id = $1`, rows.auditoriumID},
		} {
			if _, err := db.ExecContext(ctx, statement.query, statement.id); err != nil {
				t.Errorf("clean up PostgreSQL test row: %v", err)
			}
		}
	})

	return rows
}

func assertSeatClaims(t *testing.T, db *sql.DB, rows paymentRows, want int) {
	t.Helper()
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM seat_claims WHERE booking_id = $1`, rows.bookingID).Scan(&count); err != nil {
		t.Fatalf("count active seat claims: %v", err)
	}
	if count != want {
		t.Errorf("active seat claim count = %d, want %d", count, want)
	}
}

func assertBookingSeatHistory(t *testing.T, db *sql.DB, rows paymentRows, want int) {
	t.Helper()
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM bookings_seats WHERE booking_id = $1`, rows.bookingID).Scan(&count); err != nil {
		t.Fatalf("count historical booking seats: %v", err)
	}
	if count != want {
		t.Errorf("historical booking seat count = %d, want %d", count, want)
	}
}

func readPaymentState(t *testing.T, db *sql.DB, rows paymentRows) paymentState {
	t.Helper()
	var state paymentState
	err := db.QueryRow(`
		SELECT pi.status::text, pi.lease_expires_at, pi.next_attempt_at,
			pi.worker_attempt_count, pa.status::text, b.status::text
		FROM payment_initializations AS pi
		JOIN payment_attempts AS pa ON pa.id = pi.payment_attempt_id
		JOIN bookings AS b ON b.id = pa.booking_id
		WHERE pi.id = $1
	`, rows.initializationID).Scan(
		&state.initializationStatus,
		&state.leaseExpiresAt,
		&state.nextAttemptAt,
		&state.generation,
		&state.paymentStatus,
		&state.bookingStatus,
	)
	if err != nil {
		t.Fatalf("read PostgreSQL payment state: %v", err)
	}
	return state
}

func paidVerification(claim payment.Claim) payment.Verification {
	return payment.Verification{
		VerificationStatus: payment.VerificationStatusPaid,
		Reference:          claim.PaystackReference,
		AmountMinor:        claim.AmountMinor,
		Currency:           claim.Currency,
	}
}
