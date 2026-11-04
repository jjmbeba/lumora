package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"example.com/cinema-tickets-backend/internal/payment/postgres/dbgen"
)

// insertTickets creates one ticket for each seat saved for the booking. The
// caller owns tx so ticket rows can commit atomically with payment/booking state.
func (p *PostgresStore) insertTickets(ctx context.Context, tx *sql.Tx, bookingID int) error {
	if bookingID <= 0 {
		return errors.New("booking ID must be positive")
	}
	if tx == nil {
		return errors.New("database transaction is required")
	}
	if p == nil || p.ticketCredentials == nil {
		return ErrTicketCredentialCipherRequired
	}

	seatRows, err := dbgen.New(tx).GetBookingSeatIDs(ctx, int32(bookingID))
	if err != nil {
		return fmt.Errorf("query seats for booking %d: %w", bookingID, err)
	}
	seatIDs := make([]int, len(seatRows))
	for i, seatID := range seatRows {
		seatIDs[i] = int(seatID)
	}

	if len(seatIDs) == 0 {
		return fmt.Errorf("booking %d has no saved seats", bookingID)
	}

	const insertTicket = `
		INSERT INTO tickets (
			booking_id,
			seat_id,
			credential_digest,
			credential_ciphertext,
			credential_key_version
		)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (booking_id, seat_id) DO NOTHING
	`

	stmt, err := tx.PrepareContext(ctx, insertTicket)
	if err != nil {
		return fmt.Errorf("prepare ticket insert: %w", err)
	}
	defer stmt.Close()

	for _, seatID := range seatIDs {
		credential, err := p.ticketCredentials.Issue(bookingID, seatID)
		if err != nil {
			return fmt.Errorf("issue credential for booking %d seat %d: %w", bookingID, seatID, err)
		}

		if _, err := stmt.ExecContext(ctx, bookingID, seatID, credential.Digest, credential.Ciphertext, credential.KeyVersion); err != nil {
			return fmt.Errorf("insert ticket for booking %d seat %d: %w", bookingID, seatID, err)
		}
	}

	return nil
}

// insertEmailTask enqueues one durable ticket email for the booking. The
// partial unique index makes this safe to call again on a MarkPaid replay.
func (p *PostgresStore) insertEmailTask(ctx context.Context, tx *sql.Tx, bookingID int, recipientEmail string) error {
	if bookingID <= 0 {
		return errors.New("booking ID must be positive")
	}
	if tx == nil {
		return errors.New("database transaction is required")
	}
	if strings.TrimSpace(recipientEmail) == "" {
		return errors.New("recipient email is required")
	}

	const insertEmailTask = `
		INSERT INTO email_outbox (
			event_key,
			kind,
			recipient_email,
			booking_id
		)
		VALUES ($1, 'BOOKING_TICKETS', $2, $3)
		ON CONFLICT (booking_id) WHERE kind = 'BOOKING_TICKETS' DO NOTHING
	`

	eventKey := fmt.Sprintf("booking-ticket-email:%d", bookingID)
	if _, err := tx.ExecContext(ctx, insertEmailTask, eventKey, recipientEmail, bookingID); err != nil {
		return fmt.Errorf("enqueue ticket email for booking %d: %w", bookingID, err)
	}

	return nil
}
