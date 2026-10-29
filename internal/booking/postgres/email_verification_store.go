package postgres

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"example.com/cinema-tickets-backend/internal/booking"
)

const maxEmailVerificationAttempts = 5

var _ booking.EmailVerificationStore = (*PostgresStore)(nil)

func (p *PostgresStore) CreateEmailVerificationChallenge(ctx context.Context, params booking.CreateEmailVerificationChallengeParams) (booking.EmailVerificationDelivery, error) {
	if params.BookingReference == "" || len(params.CodeDigest) != sha256.Size || params.ExpiresAt.IsZero() {
		return booking.EmailVerificationDelivery{}, booking.ErrInvalidRequest
	}

	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return booking.EmailVerificationDelivery{}, fmt.Errorf("begin email verification challenge transaction: %w", err)
	}

	defer tx.Rollback()

	var (
		bookingID      int
		recipientEmail string
	)

	err = tx.QueryRowContext(ctx, `
		SELECT id, customer_email
		FROM bookings
		WHERE booking_reference = $1
		FOR UPDATE
	`, params.BookingReference).Scan(&bookingID, &recipientEmail)

	if errors.Is(err, sql.ErrNoRows) {
		return booking.EmailVerificationDelivery{}, nil
	}

	if err != nil {
		return booking.EmailVerificationDelivery{}, fmt.Errorf("lock booking for email verification challenge: %w", err)
	}

	var (
		databaseNow time.Time
		sendCount   int
		lastSentAt  sql.NullTime
	)

	err = tx.QueryRowContext(ctx, `
		WITH database_time AS MATERIALIZED (
			SELECT clock_timestamp() AS now
		)
		SELECT database_time.now,
		       COUNT(attempt.id)::int,
		       MAX(attempt.created_at)
		FROM database_time
		LEFT JOIN booking_verification_send_attempts AS attempt
		       ON attempt.booking_id = $1
		      AND attempt.created_at > database_time.now - INTERVAL '1 hour'
		GROUP BY database_time.now
	`, bookingID).Scan(&databaseNow, &sendCount, &lastSentAt)
	if err != nil {
		return booking.EmailVerificationDelivery{}, fmt.Errorf("check booking email verification send limits: %w", err)
	}

	if sendCount >= 5 || (lastSentAt.Valid && lastSentAt.Time.After(databaseNow.Add(-time.Minute))) {
		return booking.EmailVerificationDelivery{}, nil
	}

	_, err = tx.ExecContext(ctx, `
		DELETE FROM booking_verification_send_attempts
		WHERE booking_id = $1
		  AND created_at <= $2::timestamptz - INTERVAL '1 hour'
	`, bookingID, databaseNow)
	if err != nil {
		return booking.EmailVerificationDelivery{}, fmt.Errorf("prune old booking email verification send attempts: %w", err)
	}

	_, err = tx.ExecContext(ctx, `
		INSERT INTO booking_verification_challenges (
			booking_id,
			code_digest,
			expires_at,
			failed_attempts,
			consumed_at,
			updated_at
		)
		VALUES ($1, $2, $3, 0, NULL, clock_timestamp())
		ON CONFLICT (booking_id) DO UPDATE
		SET code_digest = EXCLUDED.code_digest,
		    expires_at = EXCLUDED.expires_at,
		    failed_attempts = 0,
		    consumed_at = NULL,
		    updated_at = clock_timestamp()
	`, bookingID, params.CodeDigest, params.ExpiresAt)
	if err != nil {
		return booking.EmailVerificationDelivery{}, fmt.Errorf("save booking email verification challenge: %w", err)
	}

	_, err = tx.ExecContext(ctx, `
		INSERT INTO booking_verification_send_attempts (booking_id, created_at)
		VALUES ($1, clock_timestamp())
	`, bookingID)
	if err != nil {
		return booking.EmailVerificationDelivery{}, fmt.Errorf("record booking email verification send attempt: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return booking.EmailVerificationDelivery{}, fmt.Errorf("commit booking email verification challenge: %w", err)
	}

	return booking.EmailVerificationDelivery{
		ShouldSend:     true,
		RecipientEmail: recipientEmail,
	}, nil
}

func (p *PostgresStore) VerifyEmailCode(ctx context.Context, params booking.VerifyEmailCodeParams) error {
	if params.BookingReference == "" ||
		len(params.CodeDigest) != sha256.Size ||
		len(params.TokenDigest) != sha256.Size ||
		params.TokenExpiresAt.IsZero() {
		return booking.ErrInvalidRequest
	}

	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin booking email verification transaction: %w", err)
	}

	defer tx.Rollback()

	var bookingID int
	err = tx.QueryRowContext(ctx, `
		SELECT id
		FROM bookings
		WHERE booking_reference = $1
		FOR UPDATE
	`, params.BookingReference).Scan(&bookingID)
	if errors.Is(err, sql.ErrNoRows) {
		return booking.ErrEmailCodeRejected
	}

	if err != nil {
		return fmt.Errorf("lock booking for email verification: %w", err)
	}

	var (
		storedDigest   []byte
		failedAttempts int
		isUnexpired    bool
		isUnconsumed   bool
	)
	err = tx.QueryRowContext(ctx, `
		SELECT code_digest,
		       failed_attempts,
		       expires_at > clock_timestamp(),
		       consumed_at IS NULL
		FROM booking_verification_challenges
		WHERE booking_id = $1
		FOR UPDATE
	`, bookingID).Scan(&storedDigest, &failedAttempts, &isUnexpired, &isUnconsumed)

	if errors.Is(err, sql.ErrNoRows) {
		return booking.ErrEmailCodeRejected
	}

	if err != nil {
		return fmt.Errorf("load booking email verification challenge: %w", err)
	}

	if !isUnexpired || !isUnconsumed || failedAttempts >= maxEmailVerificationAttempts {
		return booking.ErrEmailCodeRejected
	}

	if !hmac.Equal(storedDigest, params.CodeDigest) {
		_, err := tx.ExecContext(ctx, `
			UPDATE booking_verification_challenges
			SET failed_attempts = failed_attempts + 1,
			    consumed_at = CASE
			        WHEN failed_attempts + 1 >= $2 THEN clock_timestamp()
			        ELSE consumed_at
			    END,
			    updated_at = clock_timestamp()
			WHERE booking_id = $1
		`, bookingID, maxEmailVerificationAttempts)

		if err != nil {
			return fmt.Errorf("record rejected booking email verification attempt: %w", err)
		}

		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit rejected booking email verification attempt: %w", err)
		}

		return booking.ErrEmailCodeRejected
	}

	result, err := tx.ExecContext(ctx, `
		UPDATE booking_verification_challenges
		SET consumed_at = clock_timestamp(),
		    updated_at = clock_timestamp()
		WHERE booking_id = $1
		  AND consumed_at IS NULL
		  AND expires_at > clock_timestamp()
	`, bookingID)

	if err != nil {
		return fmt.Errorf("consume booking email verification code: %w", err)
	}

	updated, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("check consumed booking email verification code: %w", err)
	}

	if updated != 1 {
		return booking.ErrEmailCodeRejected
	}

	_, err = tx.ExecContext(ctx, `
		INSERT INTO booking_tokens (booking_id, token_digest, expires_at)
		VALUES ($1, $2, $3)
	`, bookingID, params.TokenDigest, params.TokenExpiresAt)
	if err != nil {
		return fmt.Errorf("persist booking token digest: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit booking email verification: %w", err)
	}

	return nil
}

func (p *PostgresStore) ValidateBookingToken(ctx context.Context, params booking.ValidateBookingTokenParams) error {
	if params.BookingReference == "" || len(params.TokenDigest) != sha256.Size {
		return booking.ErrBookingTokenInvalid
	}

	var valid bool
	err := p.db.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM booking_tokens AS token
			JOIN bookings AS booking ON booking.id = token.booking_id
			WHERE booking.booking_reference = $1
			  AND token.token_digest = $2
			  AND token.revoked_at IS NULL
			  AND token.expires_at > clock_timestamp()
		)
	`, params.BookingReference, params.TokenDigest).Scan(&valid)
	if err != nil {
		return fmt.Errorf("validate booking token: %w", err)
	}

	if !valid {
		return booking.ErrBookingTokenInvalid
	}

	return nil
}
