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
	"example.com/cinema-tickets-backend/internal/booking/postgres/dbgen"
	"example.com/cinema-tickets-backend/internal/ticket"
)

type PostgresStore struct {
	db                *sql.DB
	ticketCredentials *ticket.CredentialCipher
}

func NewPostgresStore(db *sql.DB, ticketCredentials *ticket.CredentialCipher) (*PostgresStore, error) {
	if db == nil {
		return nil, errors.New("database pool is required")
	}
	if ticketCredentials == nil {
		return nil, errors.New("ticket credential cipher is required")
	}

	return &PostgresStore{db: db, ticketCredentials: ticketCredentials}, nil
}

func (p *PostgresStore) Create(ctx context.Context, params booking.CreateBookingParams) (booking.Booking, error) {
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
	queries := dbgen.New(tx)

	existingRow, err := queries.LoadBookingByIdempotencyKey(ctx, params.IdempotencyKey)
	if err == nil {
		if existingRow.RequestFingerprint != params.RequestFingerprint {
			return booking.Booking{}, booking.ErrIdempotencyKeyConflict
		}
		existing, err := bookingFromIdempotencyRow(existingRow)
		if err != nil {
			return booking.Booking{}, err
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
		existingRow, err = queries.LoadBookingByIdempotencyKey(ctx, params.IdempotencyKey)
		if err != nil {
			return booking.Booking{}, fmt.Errorf("load concurrently created booking: %w", err)
		}

		if existingRow.RequestFingerprint != params.RequestFingerprint {
			return booking.Booking{}, booking.ErrIdempotencyKeyConflict
		}
		existing, err := bookingFromIdempotencyRow(existingRow)
		if err != nil {
			return booking.Booking{}, err
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
	rows, err := dbgen.New(p.db).GetBookingDetails(ctx, bookingReference)
	if err != nil {
		return booking.BookingDetails{}, fmt.Errorf("query booking details: %w", err)
	}
	if len(rows) == 0 {
		return booking.BookingDetails{}, booking.ErrBookingNotFound
	}

	var details booking.BookingDetails
	for _, row := range rows {
		var seat booking.BookingSeatDetail
		details.Reference = row.BookingReference
		details.Status = booking.BookingStatus(row.Status)
		details.HoldExpiresAt = row.HoldExpiresAt
		details.Currency = row.Currency
		details.TotalAmountMinor = row.TotalAmountMinor
		details.Screening.MovieTitle = row.MovieTitle
		details.Screening.AuditoriumName = row.AuditoriumName
		details.Screening.StartTime = row.StartTime
		details.Screening.EndTime = row.EndTime
		seat.Label = row.SeatLabel
		seat.UnitPriceAmountMinor = int64(row.SeatPriceAmountMinor)

		if row.CredentialKeyVersion.Valid {
			credential, err := p.ticketCredentials.Reveal(int(row.BookingID), int(row.SeatID), ticket.EncryptedCredential{
				Digest:     row.CredentialDigest,
				Ciphertext: row.CredentialCiphertext,
				KeyVersion: row.CredentialKeyVersion.Int16,
			})
			if err != nil {
				return booking.BookingDetails{}, fmt.Errorf("reveal ticket credential for booking %d seat %q: %w", row.BookingID, seat.Label, err)
			}

			seat.Ticket = &booking.BookingTicketDetails{QRCodeValue: credential}
		}
		details.Seats = append(details.Seats, seat)
	}

	return details, nil
}

func bookingFromIdempotencyRow(row dbgen.LoadBookingByIdempotencyKeyRow) (booking.Booking, error) {
	if row.SnapshotCount == 0 || row.Currency == "" {
		return booking.Booking{}, fmt.Errorf("booking %q has no saved seat-price snapshots", row.BookingReference)
	}

	return booking.Booking{
		Reference:        row.BookingReference,
		Status:           booking.BookingStatus(row.Status),
		TotalAmountMinor: row.TotalAmountMinor,
		Currency:         row.Currency,
		HoldExpiresAt:    row.HoldExpiresAt,
	}, nil
}

func newBookingReference() (string, error) {
	var randomPart [16]byte
	if _, err := rand.Read(randomPart[:]); err != nil {
		return "", fmt.Errorf("generate booking reference: %w", err)
	}
	return "bk_" + hex.EncodeToString(randomPart[:]), nil
}
