package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"example.com/cinema-tickets-backend/internal/ticket"
	"example.com/cinema-tickets-backend/internal/ticket/postgres/dbgen"
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

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ticket.TicketEmailJob{}, false, fmt.Errorf("begin ticket email claim: %w", err)
	}
	defer tx.Rollback()

	row, err := dbgen.New(tx).ClaimNextTicketEmail(ctx, leaseDuration.Microseconds())

	if errors.Is(err, sql.ErrNoRows) {
		return ticket.TicketEmailJob{}, false, nil
	}
	if err != nil {
		return ticket.TicketEmailJob{}, false, fmt.Errorf("claim next ticket email: %w", err)
	}
	if !row.BookingID.Valid {
		return ticket.TicketEmailJob{}, false, errors.New("claimed ticket email has no booking ID")
	}
	job := ticket.TicketEmailJob{ID: int(row.ID), BookingID: int(row.BookingID.Int32), Recipient: row.RecipientEmail, EventKey: row.EventKey, AttemptCount: int(row.AttemptCount)}

	if err := tx.Commit(); err != nil {
		return ticket.TicketEmailJob{}, false, fmt.Errorf("commit ticket email claim: %w", err)
	}

	return job, true, nil
}

func (s *EmailOutboxStore) LoadTicketEmail(ctx context.Context, job ticket.TicketEmailJob) (ticket.TicketEmail, error) {
	if job.ID <= 0 || job.BookingID <= 0 || job.AttemptCount <= 0 {
		return ticket.TicketEmail{}, errors.New("ticket email claim is invalid")
	}

	rows, err := dbgen.New(s.db).LoadTicketEmailItems(ctx, int32(job.BookingID))
	if err != nil {
		return ticket.TicketEmail{}, fmt.Errorf("load tickets for booking %d: %w", job.BookingID, err)
	}
	email := ticket.TicketEmail{
		To:             job.Recipient,
		IdempotencyKey: job.EventKey,
		Tickets:        make([]ticket.TicketEmailItem, 0),
	}

	for _, row := range rows {
		credential := ticket.EncryptedCredential{Digest: row.CredentialDigest, Ciphertext: row.CredentialCiphertext, KeyVersion: row.CredentialKeyVersion}
		bookingReference, seatLabel, seatID := row.BookingReference, row.SeatLabel, int(row.SeatID)

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

	if len(email.Tickets) == 0 {
		return ticket.TicketEmail{}, fmt.Errorf("booking %d has no ticket rows", job.BookingID)
	}

	return email, nil
}

func (s *EmailOutboxStore) MarkTicketEmailSent(ctx context.Context, job ticket.TicketEmailJob) error {
	_, err := dbgen.New(s.db).MarkTicketEmailSent(ctx, dbgen.MarkTicketEmailSentParams{ID: int32(job.ID), AttemptCount: int32(job.AttemptCount)})
	if errors.Is(err, sql.ErrNoRows) {
		return ticket.ErrEmailClaimLost
	}

	if err != nil {
		return fmt.Errorf("mark ticket email %d sent: %w", job.ID, err)
	}

	return nil
}

func (s *EmailOutboxStore) RetryTicketEmail(ctx context.Context, job ticket.TicketEmailJob, retryAt time.Time, terminal bool) error {
	_, err := dbgen.New(s.db).RetryTicketEmail(ctx, dbgen.RetryTicketEmailParams{
		Terminal: terminal, NextAttemptAt: retryAt, ID: int32(job.ID), AttemptCount: int32(job.AttemptCount),
	})

	if errors.Is(err, sql.ErrNoRows) {
		return ticket.ErrEmailClaimLost
	}
	if err != nil {
		return fmt.Errorf("schedule ticket email %d retry: %w", job.ID, err)
	}

	return nil
}
