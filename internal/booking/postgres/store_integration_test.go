package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"example.com/cinema-tickets-backend/internal/booking"
	"example.com/cinema-tickets-backend/internal/checkout"
	"example.com/cinema-tickets-backend/internal/payment"
	paymentpostgres "example.com/cinema-tickets-backend/internal/payment/postgres"
	_ "github.com/jackc/pgx/v5/stdlib"
)

var bookingIntegrationID atomic.Int64

func init() {
	bookingIntegrationID.Store(time.Now().UnixNano())
}

func TestCreatePersistsBookingSnapshotsAndSeatClaims(t *testing.T) {
	db, store, fixture := newBookingIntegrationFixture(t)
	params := fixture.params("success")

	got, err := store.Create(context.Background(), params)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if got.Status != booking.BookingStatusReserved {
		t.Errorf("status = %q, want RESERVED", got.Status)
	}
	if got.Currency != "KES" || got.TotalAmountMinor != 2500 {
		t.Errorf("price = %d %s, want 2500 KES", got.TotalAmountMinor, got.Currency)
	}
	if got.Reference == "" {
		t.Fatal("booking reference is empty")
	}
	if !got.HoldExpiresAt.After(time.Now()) {
		t.Errorf("hold expiry %v is not in the future", got.HoldExpiresAt)
	}

	assertPersistedBooking(t, db, got.Reference, fixture.screeningID, 2, 2500, 2)
}

func TestCreateReplaysMatchingIdempotencyKeyWithoutExtendingHold(t *testing.T) {
	_, store, fixture := newBookingIntegrationFixture(t)
	params := fixture.params("replay")

	first, err := store.Create(context.Background(), params)
	if err != nil {
		t.Fatalf("first Create() error = %v", err)
	}

	second, err := store.Create(context.Background(), params)
	if err != nil {
		t.Fatalf("replay Create() error = %v", err)
	}
	if second.Reference != first.Reference {
		t.Errorf("replay reference = %q, want original %q", second.Reference, first.Reference)
	}
	if !second.HoldExpiresAt.Equal(first.HoldExpiresAt) {
		t.Errorf("replay hold expiry = %v, want original %v", second.HoldExpiresAt, first.HoldExpiresAt)
	}
	if second.TotalAmountMinor != first.TotalAmountMinor || second.Currency != first.Currency {
		t.Errorf("replay price = %d %s, want original %d %s", second.TotalAmountMinor, second.Currency, first.TotalAmountMinor, first.Currency)
	}
	assertPersistedBooking(t, fixture.db, first.Reference, fixture.screeningID, 2, 2500, 2)
}

func TestCreateRejectsIdempotencyKeyWithDifferentFingerprint(t *testing.T) {
	_, store, fixture := newBookingIntegrationFixture(t)
	params := fixture.params("fingerprint-conflict")
	if _, err := store.Create(context.Background(), params); err != nil {
		t.Fatalf("first Create() error = %v", err)
	}

	params.RequestFingerprint = "different-fingerprint"
	if _, err := store.Create(context.Background(), params); !errors.Is(err, booking.ErrIdempotencyKeyConflict) {
		t.Fatalf("Create() error = %v, want ErrIdempotencyKeyConflict", err)
	}
	assertIdempotencyKeyRows(t, fixture.db, params.IdempotencyKey, 1)
}

func TestCreateRollsBackBookingAndSnapshotsWhenSeatIsUnavailable(t *testing.T) {
	_, store, fixture := newBookingIntegrationFixture(t)

	firstParams := fixture.params("claim-seat-first")
	firstParams.RequestedSeatIDs = []int{fixture.seatIDs[0]}
	first, err := store.Create(context.Background(), firstParams)
	if err != nil {
		t.Fatalf("first Create() error = %v", err)
	}

	conflictingParams := fixture.params("claim-seat-conflict")
	conflictingParams.RequestedSeatIDs = []int{fixture.seatIDs[0], fixture.seatIDs[1]}
	if _, err := store.Create(context.Background(), conflictingParams); !errors.Is(err, booking.ErrSeatsUnavailable) {
		t.Fatalf("conflicting Create() error = %v, want ErrSeatsUnavailable", err)
	}

	assertIdempotencyKeyRows(t, fixture.db, conflictingParams.IdempotencyKey, 0)
	assertPersistedBooking(t, fixture.db, first.Reference, fixture.screeningID, 1, 1000, 1)
	assertScreeningSeatClaimCount(t, fixture.db, fixture.screeningID, 1)
}

func TestCreateConcurrentMatchingRequestsReturnSameBooking(t *testing.T) {
	_, store, fixture := newBookingIntegrationFixture(t)
	params := fixture.params("concurrent")

	start := make(chan struct{})
	results := make(chan booking.Booking, 2)
	errCh := make(chan error, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			result, err := store.Create(context.Background(), params)
			if err != nil {
				errCh <- err
				return
			}
			results <- result
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	close(errCh)

	for err := range errCh {
		t.Errorf("concurrent Create() error = %v", err)
	}

	var got []booking.Booking
	for result := range results {
		got = append(got, result)
	}
	if len(got) != 2 {
		t.Fatalf("successful results = %d, want 2", len(got))
	}
	if got[0].Reference != got[1].Reference {
		t.Errorf("concurrent references = %q and %q, want the same booking", got[0].Reference, got[1].Reference)
	}
	if !got[0].HoldExpiresAt.Equal(got[1].HoldExpiresAt) {
		t.Errorf("concurrent hold expiries = %v and %v, want the same expiry", got[0].HoldExpiresAt, got[1].HoldExpiresAt)
	}
	assertPersistedBooking(t, fixture.db, got[0].Reference, fixture.screeningID, 2, 2500, 2)
}

func TestGetDetailsReturnsSavedBookingAndScreeningSnapshot(t *testing.T) {
	_, store, fixture := newBookingIntegrationFixture(t)
	createdBooking, err := store.Create(context.Background(), fixture.params("details"))
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	got, err := store.GetDetails(context.Background(), createdBooking.Reference)
	if err != nil {
		t.Fatalf("GetDetails() error = %v", err)
	}
	if got.Reference != createdBooking.Reference || got.Status != booking.BookingStatusReserved {
		t.Errorf("booking identity/status = %q/%q, want %q/RESERVED", got.Reference, got.Status, createdBooking.Reference)
	}
	if got.TotalAmountMinor != 2500 || got.Currency != "KES" {
		t.Errorf("total = %d %s, want 2500 KES", got.TotalAmountMinor, got.Currency)
	}
	if got.Screening.MovieTitle == "" || got.Screening.AuditoriumName == "" || !got.Screening.EndTime.After(got.Screening.StartTime) {
		t.Errorf("screening details = %+v, want seeded movie, auditorium, and ordered times", got.Screening)
	}
	if len(got.Seats) != 2 {
		t.Fatalf("seats = %d, want 2: %+v", len(got.Seats), got.Seats)
	}
	if got.Seats[0].UnitPriceAmountMinor+got.Seats[1].UnitPriceAmountMinor != got.TotalAmountMinor {
		t.Errorf("seat prices = %+v, do not sum to total %d", got.Seats, got.TotalAmountMinor)
	}
}

func TestGetDetailsReturnsNotFoundForUnknownReference(t *testing.T) {
	_, store, _ := newBookingIntegrationFixture(t)
	if _, err := store.GetDetails(context.Background(), "bk-not-present"); !errors.Is(err, booking.ErrBookingNotFound) {
		t.Fatalf("GetDetails() error = %v, want ErrBookingNotFound", err)
	}
}

func TestCheckoutRetryAfterInitializationFailureReplaysBooking(t *testing.T) {
	db, bookingStore, fixture := newBookingIntegrationFixture(t)
	bookingService, err := booking.NewService(bookingStore)
	if err != nil {
		t.Fatalf("create booking service: %v", err)
	}
	initializationStore, err := paymentpostgres.NewInitializationStore(db)
	if err != nil {
		t.Fatalf("create payment initialization store: %v", err)
	}
	initializationFailure := errors.New("temporary payment store failure")
	flakyInitializations := &failFirstInitializationCreator{
		delegate: initializationStore,
		err:      initializationFailure,
	}
	checkoutService, err := checkout.NewService(bookingService, flakyInitializations)
	if err != nil {
		t.Fatalf("create checkout service: %v", err)
	}
	request := checkout.StartRequest{
		Booking: booking.CreateBookingRequest{
			ScreeningID:      fixture.screeningID,
			RequestedSeatIDs: []int{fixture.seatIDs[0], fixture.seatIDs[1]},
			Customer:         booking.CustomerDetails{Email: "checkout@example.test", PhoneNumber: "+254700000000"},
			IdempotencyKey:   fmt.Sprintf("checkout-integration-%d", bookingIntegrationID.Load()),
		},
		PaymentMethod: payment.PaymentMethodCard,
	}

	if _, err := checkoutService.Start(context.Background(), request); !errors.Is(err, initializationFailure) {
		t.Fatalf("first Start() error = %v, want temporary initialization failure", err)
	}
	var originalReference string
	var originalHoldExpiresAt time.Time
	if err := db.QueryRow(`
		SELECT booking_reference, hold_expires_at
		FROM bookings
		WHERE idempotency_key = $1
	`, request.Booking.IdempotencyKey).Scan(&originalReference, &originalHoldExpiresAt); err != nil {
		t.Fatalf("load booking committed before initialization failure: %v", err)
	}
	var attemptsAfterFailure int
	if err := db.QueryRow(`
		SELECT COUNT(*)::int
		FROM payment_attempts AS attempt
		JOIN bookings AS booking ON booking.id = attempt.booking_id
		WHERE booking.booking_reference = $1
	`, originalReference).Scan(&attemptsAfterFailure); err != nil {
		t.Fatalf("count payment attempts after simulated failure: %v", err)
	}
	if attemptsAfterFailure != 0 {
		t.Fatalf("payment attempts after simulated pre-store failure = %d, want 0", attemptsAfterFailure)
	}

	got, err := checkoutService.Start(context.Background(), request)
	if err != nil {
		t.Fatalf("retry Start() error = %v", err)
	}
	if got.Booking.Reference != originalReference || !got.Booking.HoldExpiresAt.Equal(originalHoldExpiresAt) {
		t.Errorf("retry booking = %+v, want same reference %q and hold expiry %v", got.Booking, originalReference, originalHoldExpiresAt)
	}
	if got.Initialization.Status != payment.InitializationStatusQueued || got.Initialization.PaystackReference == "" {
		t.Errorf("retry initialization = %+v, want queued initialization with provider reference", got.Initialization)
	}
	var bookingCount, initializationCount int
	if err := db.QueryRow(`
		SELECT COUNT(*)::int
		FROM bookings
		WHERE idempotency_key = $1
	`, request.Booking.IdempotencyKey).Scan(&bookingCount); err != nil {
		t.Fatalf("count idempotent bookings: %v", err)
	}
	if err := db.QueryRow(`
		SELECT COUNT(*)::int
		FROM payment_initializations AS initialization
		JOIN payment_attempts AS attempt ON attempt.id = initialization.payment_attempt_id
		JOIN bookings AS booking ON booking.id = attempt.booking_id
		WHERE booking.booking_reference = $1
	`, originalReference).Scan(&initializationCount); err != nil {
		t.Fatalf("count payment initializations: %v", err)
	}
	if bookingCount != 1 || initializationCount != 1 {
		t.Errorf("persisted booking/initialization counts = %d/%d, want 1/1", bookingCount, initializationCount)
	}
}

type failFirstInitializationCreator struct {
	delegate checkout.InitializationStore
	err      error
	failed   bool
}

func (f *failFirstInitializationCreator) CreateOrReplayInitialization(ctx context.Context, request payment.CreateInitializationRequest) (payment.Initialization, error) {
	if !f.failed {
		f.failed = true
		return payment.Initialization{}, f.err
	}
	return f.delegate.CreateOrReplayInitialization(ctx, request)
}

func (f *failFirstInitializationCreator) GetLatestForBooking(ctx context.Context, bookingReference string) (payment.Initialization, error) {
	return f.delegate.GetLatestForBooking(ctx, bookingReference)
}

type bookingIntegrationFixture struct {
	db           *sql.DB
	store        *PostgresStore
	auditoriumID int
	screeningID  int
	seatIDs      []int
}

func newBookingIntegrationFixture(t *testing.T) (*sql.DB, *PostgresStore, bookingIntegrationFixture) {
	t.Helper()

	dsn := os.Getenv("BOOKING_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set BOOKING_TEST_DATABASE_URL to a dedicated PostgreSQL database with migrations/migration.sql applied")
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
	if err := db.QueryRowContext(ctx, `SELECT to_regclass('bookings_seats') IS NOT NULL AND to_regclass('seat_claims') IS NOT NULL`).Scan(&schemaReady); err != nil {
		t.Fatalf("check booking schema: %v", err)
	}
	if !schemaReady {
		t.Fatal("booking tables are missing; apply migrations/migration.sql to the test database first")
	}

	store, err := NewPostgresStore(db)
	if err != nil {
		t.Fatalf("create booking store: %v", err)
	}
	fixture := bookingIntegrationFixture{db: db, store: store}
	fixture.seed(t)
	return db, store, fixture
}

func (f *bookingIntegrationFixture) seed(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	createdAt := time.Now().UTC()
	base := bookingIntegrationID.Add(1)

	err := f.db.QueryRowContext(ctx, `
		INSERT INTO auditoriums (name, created_at)
		VALUES ($1, $2)
		RETURNING id
	`, fmt.Sprintf("booking-test-auditorium-%d", base), createdAt).Scan(&f.auditoriumID)
	if err != nil {
		t.Fatalf("insert test auditorium: %v", err)
	}

	var movieID int
	err = f.db.QueryRowContext(ctx, `
		INSERT INTO movies (name, description, runtime_minutes, created_at)
		VALUES ($1, $2, $3, $4)
		RETURNING id
	`, fmt.Sprintf("booking-test-movie-%d", base), "integration test fixture", 90, createdAt).Scan(&movieID)
	if err != nil {
		t.Fatalf("insert test movie: %v", err)
	}

	err = f.db.QueryRowContext(ctx, `
		INSERT INTO screenings (movie_id, auditorium_id, currency, start_time, end_time, created_at)
		VALUES ($1, $2, 'KES', $3, $4, $5)
		RETURNING id
	`, movieID, f.auditoriumID, createdAt.Add(time.Hour), createdAt.Add(3*time.Hour), createdAt).Scan(&f.screeningID)
	if err != nil {
		t.Fatalf("insert test screening: %v", err)
	}

	f.seatIDs = make([]int, 3)
	for i := range f.seatIDs {
		label := fmt.Sprintf("T%d-%d", base, i+1)
		if err := f.db.QueryRowContext(ctx, `
			INSERT INTO seats (label, auditorium_id, created_at)
			VALUES ($1, $2, $3)
			RETURNING id
		`, label, f.auditoriumID, createdAt).Scan(&f.seatIDs[i]); err != nil {
			t.Fatalf("insert test seat: %v", err)
		}
		price := []int{1000, 1500, 2000}[i]
		if _, err := f.db.ExecContext(ctx, `
			INSERT INTO screening_seats (screening_id, seat_id, auditorium_id, price_amount)
			VALUES ($1, $2, $3, $4)
		`, f.screeningID, f.seatIDs[i], f.auditoriumID, price); err != nil {
			t.Fatalf("insert test screening seat: %v", err)
		}
	}

	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		cleanup := []struct {
			query string
			arg   int
		}{
			{`DELETE FROM seat_claims WHERE screening_id = $1`, f.screeningID},
			{`DELETE FROM bookings_seats WHERE screening_id = $1`, f.screeningID},
			{`DELETE FROM payment_initializations AS initialization
			 USING payment_attempts AS attempt, bookings AS booking
			 WHERE initialization.payment_attempt_id = attempt.id
			   AND attempt.booking_id = booking.id
			   AND booking.screening_id = $1`, f.screeningID},
			{`DELETE FROM payment_attempts AS attempt
			 USING bookings AS booking
			 WHERE attempt.booking_id = booking.id
			   AND booking.screening_id = $1`, f.screeningID},
			{`DELETE FROM bookings WHERE screening_id = $1`, f.screeningID},
			{`DELETE FROM screening_seats WHERE screening_id = $1`, f.screeningID},
			{`DELETE FROM screenings WHERE id = $1`, f.screeningID},
			{`DELETE FROM seats WHERE auditorium_id = $1`, f.auditoriumID},
			{`DELETE FROM movies WHERE id = $1`, movieID},
			{`DELETE FROM auditoriums WHERE id = $1`, f.auditoriumID},
		}
		for _, statement := range cleanup {
			if _, err := f.db.ExecContext(cleanupCtx, statement.query, statement.arg); err != nil {
				t.Errorf("clean up booking fixture: %v", err)
			}
		}
	})
}

func (f bookingIntegrationFixture) params(suffix string) booking.CreateBookingParams {
	return booking.CreateBookingParams{
		ScreeningID:        f.screeningID,
		RequestedSeatIDs:   []int{f.seatIDs[0], f.seatIDs[1]},
		Customer:           booking.CustomerDetails{Email: "customer@example.test", PhoneNumber: "+254700000000"},
		IdempotencyKey:     fmt.Sprintf("booking-integration-%d-%s", bookingIntegrationID.Load(), suffix),
		RequestFingerprint: "fingerprint-" + suffix,
	}
}

func assertPersistedBooking(t *testing.T, db *sql.DB, reference string, screeningID, wantSnapshotCount int, wantTotal int64, wantClaimCount int) {
	t.Helper()
	var snapshotCount, claimCount int
	var total int64
	var currency string
	err := db.QueryRow(`
		SELECT COUNT(bs.seat_id), COALESCE(SUM(bs.unit_price_amount), 0)::bigint, MIN(bs.currency)
		FROM bookings AS b
		LEFT JOIN bookings_seats AS bs ON bs.booking_id = b.id
		WHERE b.booking_reference = $1 AND b.screening_id = $2
		GROUP BY b.id
	`, reference, screeningID).Scan(&snapshotCount, &total, &currency)
	if err != nil {
		t.Fatalf("read saved booking snapshots: %v", err)
	}
	if snapshotCount != wantSnapshotCount || total != wantTotal || currency != "KES" {
		t.Errorf("saved snapshots = count %d, total %d, currency %q; want %d, %d, KES", snapshotCount, total, currency, wantSnapshotCount, wantTotal)
	}
	if err := db.QueryRow(`
		SELECT COUNT(*) FROM seat_claims sc
		JOIN bookings AS b ON b.id = sc.booking_id
		WHERE b.booking_reference = $1 AND b.screening_id = $2
	`, reference, screeningID).Scan(&claimCount); err != nil {
		t.Fatalf("read saved seat claims: %v", err)
	}
	if claimCount != wantClaimCount {
		t.Errorf("saved seat claims = %d, want %d", claimCount, wantClaimCount)
	}
}

func assertIdempotencyKeyRows(t *testing.T, db *sql.DB, key string, want int) {
	t.Helper()
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM bookings WHERE idempotency_key = $1`, key).Scan(&count); err != nil {
		t.Fatalf("count bookings for idempotency key: %v", err)
	}
	if count != want {
		t.Errorf("bookings for idempotency key = %d, want %d", count, want)
	}
}

func assertScreeningSeatClaimCount(t *testing.T, db *sql.DB, screeningID, want int) {
	t.Helper()
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM seat_claims WHERE screening_id = $1`, screeningID).Scan(&count); err != nil {
		t.Fatalf("count screening seat claims: %v", err)
	}
	if count != want {
		t.Errorf("seat claims for screening = %d, want %d", count, want)
	}
}
