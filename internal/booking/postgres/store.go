package postgres

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"example.com/cinema-tickets-backend/internal/booking"
)

type PostgresStore struct {
	db *sql.DB
}

func NewPostgresStore(db *sql.DB) (*PostgresStore, error) {
	if db == nil {
		return nil, errors.New("database pool is required")
	}

	return &PostgresStore{db: db}, nil
}

func (p *PostgresStore) Create(ctx context.Context, params booking.CreateBookingParams) (booking.Booking, error) {
	const loadIdempotentBooking = `
		SELECT b.request_fingerprint,
		       b.booking_reference,
		       b.status::text,
		       b.hold_expires_at,
		       COALESCE(SUM(bs.unit_price_amount), 0)::bigint,
		       MIN(bs.currency),
		       COUNT(bs.seat_id)
		FROM bookings AS b
		LEFT JOIN bookings_seats AS bs ON bs.booking_id = b.id
		WHERE b.idempotency_key = $1
		GROUP BY b.id, b.request_fingerprint, b.booking_reference, b.status, b.hold_expires_at
	`

	const createBooking = `
		WITH booking_time AS MATERIALIZED (
			SELECT clock_timestamp() AS created_at
		)
		INSERT INTO bookings (
			booking_reference,
			idempotency_key,
			request_fingerprint,
			customer_email,
			customer_phone,
			status,
			screening_id,
			created_at,
			hold_expires_at
		)
		SELECT $1, $2, $3, $4, $5, 'RESERVED', $6,
		       booking_time.created_at,
		       booking_time.created_at + INTERVAL '5 minutes'
		FROM booking_time
		ON CONFLICT (idempotency_key) DO NOTHING
		RETURNING id, hold_expires_at
	`

	const createSeatSnapshots = `
		WITH inserted AS (
			INSERT INTO bookings_seats (
				booking_id,
				seat_id,
				screening_id,
				unit_price_amount,
				currency
			)
			SELECT $1, ss.seat_id, ss.screening_id, ss.price_amount, s.currency
			FROM screening_seats AS ss
			JOIN screenings AS s ON s.id = ss.screening_id
			WHERE ss.screening_id = $2
			  AND ss.seat_id = ANY($3::int[])
			RETURNING unit_price_amount, currency, seat_id
		)
		SELECT COALESCE(SUM(unit_price_amount), 0)::bigint,
		       MIN(currency),
		       COUNT(seat_id)
		FROM inserted
	`

	const claimSeats = `
		INSERT INTO seat_claims (screening_id, seat_id, booking_id)
		SELECT screening_id, seat_id, booking_id
		FROM bookings_seats
		WHERE booking_id = $1
		ON CONFLICT (screening_id, seat_id) DO NOTHING
	`

	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return booking.Booking{}, fmt.Errorf("begin booking transaction: %w", err)
	}
	defer tx.Rollback()

	existing, fingerprint, err := loadBooking(ctx, tx, loadIdempotentBooking, params.IdempotencyKey)
	if err == nil {
		if fingerprint != params.RequestFingerprint {
			return booking.Booking{}, booking.ErrIdempotencyKeyConflict
		}

		if err := tx.Commit(); err != nil {
			return booking.Booking{}, fmt.Errorf("commit idempotent booking replay: %w", err)
		}

		return existing, nil
	}

	if !errors.Is(err, sql.ErrNoRows) {
		return booking.Booking{}, fmt.Errorf("load booking by idempotency key: %w", err)
	}

	reference, err := newBookingReference()
	if err != nil {
		return booking.Booking{}, err
	}

	var bookingID int
	var holdExpiresAt time.Time

	err = tx.QueryRowContext(
		ctx,
		createBooking,
		reference,
		params.IdempotencyKey,
		params.RequestFingerprint,
		params.Customer.Email,
		params.Customer.PhoneNumber,
		params.ScreeningID,
	).Scan(&bookingID, &holdExpiresAt)

	if errors.Is(err, sql.ErrNoRows) {
		existing, fingerprint, err = loadBooking(ctx, tx, loadIdempotentBooking, params.IdempotencyKey)
		if err != nil {
			return booking.Booking{}, fmt.Errorf("load concurrently created booking: %w", err)
		}

		if fingerprint != params.RequestFingerprint {
			return booking.Booking{}, booking.ErrIdempotencyKeyConflict
		}

		if err := tx.Commit(); err != nil {
			return booking.Booking{}, fmt.Errorf("commit concurrent booking replay: %w", err)
		}

		return existing, nil
	}

	if err != nil {
		return booking.Booking{}, fmt.Errorf("insert booking: %w", err)
	}

	var (
		totalPrice    int64
		currency      sql.NullString
		snapshotCount int64
	)

	err = tx.QueryRowContext(
		ctx,
		createSeatSnapshots,
		bookingID,
		params.ScreeningID,
		params.RequestedSeatIDs,
	).Scan(&totalPrice, &currency, &snapshotCount)
	if err != nil {
		return booking.Booking{}, fmt.Errorf("create booking seat-price snapshots: %w", err)
	}

	if snapshotCount != int64(len(params.RequestedSeatIDs)) || !currency.Valid || totalPrice <= 0 {
		return booking.Booking{}, booking.ErrSeatsUnavailable
	}

	result, err := tx.ExecContext(ctx, claimSeats, bookingID)
	if err != nil {
		return booking.Booking{}, fmt.Errorf("claim booking seats: %w", err)
	}

	claimedSeatCount, err := result.RowsAffected()
	if err != nil {
		return booking.Booking{}, fmt.Errorf("count claimed booking seats: %w", err)
	}

	if claimedSeatCount != int64(len(params.RequestedSeatIDs)) {
		return booking.Booking{}, booking.ErrSeatsUnavailable
	}

	if err := tx.Commit(); err != nil {
		return booking.Booking{}, fmt.Errorf("commit booking transaction: %w", err)
	}

	return booking.Booking{
		Reference:        reference,
		Status:           booking.BookingStatusReserved,
		TotalAmountMinor: totalPrice,
		Currency:         currency.String,
		HoldExpiresAt:    holdExpiresAt,
	}, nil
}

func (p *PostgresStore) GetDetails(ctx context.Context, bookingReference string) (booking.BookingDetails, error) {
	rows, err := p.db.QueryContext(ctx, `
		SELECT b.booking_reference,
		       b.status::text,
		       b.hold_expires_at,
		       s.currency,
		       COALESCE(SUM(bs.unit_price_amount) OVER (PARTITION BY b.id), 0)::bigint,
		       m.name,
		       a.name,
		       s.start_time,
		       s.end_time,
		       seat.label,
		       bs.unit_price_amount
		FROM bookings AS b
		JOIN screenings AS s ON s.id = b.screening_id
		JOIN movies AS m ON m.id = s.movie_id
		JOIN auditoriums AS a ON a.id = s.auditorium_id
		JOIN bookings_seats AS bs ON bs.booking_id = b.id
		JOIN seats AS seat ON seat.id = bs.seat_id
		WHERE b.booking_reference = $1
		ORDER BY seat.label
	`, bookingReference)
	if err != nil {
		return booking.BookingDetails{}, fmt.Errorf("query booking details: %w", err)
	}
	defer rows.Close()

	var details booking.BookingDetails
	var status string
	found := false
	for rows.Next() {
		var seat booking.BookingSeatDetail
		if err := rows.Scan(
			&details.Reference,
			&status,
			&details.HoldExpiresAt,
			&details.Currency,
			&details.TotalAmountMinor,
			&details.Screening.MovieTitle,
			&details.Screening.AuditoriumName,
			&details.Screening.StartTime,
			&details.Screening.EndTime,
			&seat.Label,
			&seat.UnitPriceAmountMinor,
		); err != nil {
			return booking.BookingDetails{}, fmt.Errorf("scan booking details: %w", err)
		}
		details.Seats = append(details.Seats, seat)
		found = true
	}
	if err := rows.Err(); err != nil {
		return booking.BookingDetails{}, fmt.Errorf("iterate booking details: %w", err)
	}
	if !found {
		return booking.BookingDetails{}, booking.ErrBookingNotFound
	}

	details.Status = booking.BookingStatus(status)
	return details, nil
}

func loadBooking(ctx context.Context, tx *sql.Tx, query, idempotencyKey string) (booking.Booking, string, error) {
	var (
		result        booking.Booking
		fingerprint   string
		status        string
		currency      sql.NullString
		snapshotCount int64
	)

	err := tx.QueryRowContext(ctx, query, idempotencyKey).Scan(
		&fingerprint,
		&result.Reference,
		&status,
		&result.HoldExpiresAt,
		&result.TotalAmountMinor,
		&currency,
		&snapshotCount,
	)
	if err != nil {
		return booking.Booking{}, "", err
	}

	if snapshotCount == 0 || !currency.Valid {
		return booking.Booking{}, "", fmt.Errorf("booking %q has no saved seat-price snapshots", result.Reference)
	}

	result.Status = booking.BookingStatus(status)
	result.Currency = currency.String

	return result, fingerprint, nil
}

func newBookingReference() (string, error) {
	var randomPart [16]byte
	if _, err := rand.Read(randomPart[:]); err != nil {
		return "", fmt.Errorf("generate booking reference: %w", err)
	}
	return "bk_" + hex.EncodeToString(randomPart[:]), nil
}
