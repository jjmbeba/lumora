package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"example.com/cinema-tickets-backend/internal/ticket"
)

type EmailOutboxStore struct {
	db     *sql.DB
	cipher *ticket.CredentialCipher
}

func NewEmailOutboxStore(db *sql.DB, cipher *ticket.CredentialCipher) (*EmailOutboxStore, error) {
	if db == nil {
		return nil, errors.New("database pool is required")
	}

	if cipher == nil {
		return nil, errors.New("ticket credential cipher is required")
	}

	return &EmailOutboxStore{db: db, cipher: cipher}, nil
}

var _ ticket.TicketEmailStore = (*EmailOutboxStore)(nil)

func (s *EmailOutboxStore) ClaimNextTicketEmail(ctx context.Context, leaseDuration time.Duration) (ticket.TicketEmailJob, bool, error) {
	if leaseDuration < time.Microsecond {
		return ticket.TicketEmailJob{}, false, errors.New("ticket email lease duration must be at least one microsecond")
	}

	const claimNextEmail = `
		WITH candidate AS (
			SELECT id
			FROM email_outbox
			WHERE kind = 'BOOKING_TICKETS'
				AND (
					(status = 'QUEUED' AND next_attempt_at <= clock_timestamp())
					OR (status = 'SENDING' AND lease_expires_at <= clock_timestamp())
				)
			ORDER BY next_attempt_at, id
			LIMIT 1
			FOR UPDATE SKIP LOCKED
		)
		UPDATE email_outbox AS eo
		SET status = 'SENDING',
			attempt_count = eo.attempt_count + 1,
			lease_expires_at = clock_timestamp() + ($1::bigint * interval '1 microsecond'),
			updated_at = clock_timestamp()
		FROM candidate
		WHERE eo.id = candidate.id
		RETURNING eo.id, eo.booking_id, eo.recipient_email, eo.event_key, eo.attempt_count
	`

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ticket.TicketEmailJob{}, false, fmt.Errorf("begin ticket email claim: %w", err)
	}
	defer tx.Rollback()

	var job ticket.TicketEmailJob
	err = tx.QueryRowContext(ctx, claimNextEmail, leaseDuration.Microseconds()).Scan(
		&job.ID,
		&job.BookingID,
		&job.Recipient,
		&job.EventKey,
		&job.AttemptCount,
	)

	if errors.Is(err, sql.ErrNoRows) {
		return ticket.TicketEmailJob{}, false, nil
	}
	if err != nil {
		return ticket.TicketEmailJob{}, false, fmt.Errorf("claim next ticket email: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return ticket.TicketEmailJob{}, false, fmt.Errorf("commit ticket email claim: %w", err)
	}

	return job, true, nil
}

func (s *EmailOutboxStore) LoadTicketEmail(ctx context.Context, job ticket.TicketEmailJob) (ticket.TicketEmail, error) {
	if job.ID <= 0 || job.BookingID <= 0 || job.AttemptCount <= 0 {
		return ticket.TicketEmail{}, errors.New("ticket email claim is invalid")
	}

	const loadEmail = `
		SELECT b.booking_reference, s.label, t.seat_id,
			t.credential_digest, t.credential_ciphertext, t.credential_key_version
		FROM bookings AS b
		JOIN tickets AS t ON t.booking_id = b.id
		JOIN seats AS s ON s.id = t.seat_id
		WHERE b.id = $1
		ORDER BY t.seat_id
	`

	rows, err := s.db.QueryContext(ctx, loadEmail, job.BookingID)
	if err != nil {
		return ticket.TicketEmail{}, fmt.Errorf("load tickets for booking %d: %w", job.BookingID, err)
	}
	defer rows.Close()

	email := ticket.TicketEmail{
		To:             job.Recipient,
		IdempotencyKey: job.EventKey,
		Tickets:        make([]ticket.TicketEmailItem, 0),
	}

	for rows.Next() {
		var bookingReference, seatLabel string
		var seatID int
		var credential ticket.EncryptedCredential

		if err := rows.Scan(
			&bookingReference,
			&seatLabel,
			&seatID,
			&credential.Digest,
			&credential.Ciphertext,
			&credential.KeyVersion,
		); err != nil {
			return ticket.TicketEmail{}, fmt.Errorf("read ticket for booking %d: %w", job.BookingID, err)
		}

		if email.BookingReference != "" && email.BookingReference != bookingReference {
			return ticket.TicketEmail{}, fmt.Errorf("booking reference changed while loading booking %d", job.BookingID)
		}
		email.BookingReference = bookingReference

		value, err := s.cipher.Reveal(job.BookingID, seatID, credential)
		if err != nil {
			return ticket.TicketEmail{}, fmt.Errorf("reveal ticket credential for booking %d seat %d: %w", job.BookingID, seatID, err)
		}

		email.Tickets = append(email.Tickets, ticket.TicketEmailItem{
			SeatLabel:  seatLabel,
			Credential: value,
		})
	}

	if err := rows.Err(); err != nil {
		return ticket.TicketEmail{}, fmt.Errorf("iterate tickets for booking %d: %w", job.BookingID, err)
	}
	if len(email.Tickets) == 0 {
		return ticket.TicketEmail{}, fmt.Errorf("booking %d has no ticket rows", job.BookingID)
	}

	return email, nil
}

func (s *EmailOutboxStore) MarkTicketEmailSent(ctx context.Context, job ticket.TicketEmailJob) error {
	const markSent = `
		UPDATE email_outbox
		SET status = 'SENT',
			lease_expires_at = NULL,
			last_error_code = NULL,
			sent_at = clock_timestamp(),
			updated_at = clock_timestamp()
		WHERE id = $1 AND status = 'SENDING' AND attempt_count = $2
		RETURNING id
	`

	var id int
	err := s.db.QueryRowContext(ctx, markSent, job.ID, job.AttemptCount).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return ticket.ErrEmailClaimLost
	}

	if err != nil {
		return fmt.Errorf("mark ticket email %d sent: %w", job.ID, err)
	}

	return nil
}

func (s *EmailOutboxStore) RetryTicketEmail(ctx context.Context, job ticket.TicketEmailJob, retryAt time.Time, terminal bool) error {
	const retry = `
		UPDATE email_outbox
		SET status = CASE WHEN $3 THEN 'FAILED'::"EmailOutboxStatus" ELSE 'QUEUED'::"EmailOutboxStatus" END,
			next_attempt_at = CASE WHEN $3 THEN clock_timestamp() ELSE $4 END,
			lease_expires_at = NULL,
			last_error_code = 'DELIVERY_FAILED',
			updated_at = clock_timestamp()
		WHERE id = $1 AND status = 'SENDING' AND attempt_count = $2
		RETURNING id
	`

	var id int
	err := s.db.QueryRowContext(ctx, retry, job.ID, job.AttemptCount, terminal, retryAt).Scan(&id)

	if errors.Is(err, sql.ErrNoRows) {
		return ticket.ErrEmailClaimLost
	}
	if err != nil {
		return fmt.Errorf("schedule ticket email %d retry: %w", job.ID, err)
	}

	return nil
}
