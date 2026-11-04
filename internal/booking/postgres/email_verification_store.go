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
	"example.com/cinema-tickets-backend/internal/booking/postgres/dbgen"
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

	lockedBooking, err := dbgen.New(tx).LockBookingForEmailVerificationChallenge(ctx, params.BookingReference)
	bookingID = int(lockedBooking.ID)
	recipientEmail = lockedBooking.CustomerEmail

	if errors.Is(err, sql.ErrNoRows) {
		return booking.EmailVerificationDelivery{}, nil
	}

	if err != nil {
		return booking.EmailVerificationDelivery{}, fmt.Errorf("lock booking for email verification challenge: %w", err)
	}

	limits, err := dbgen.New(tx).GetEmailVerificationSendLimits(ctx, int32(bookingID))
	if err != nil {
		return booking.EmailVerificationDelivery{}, fmt.Errorf("check booking email verification send limits: %w", err)
	}

	if limits.SendCount >= 5 || limits.LastSentAt.After(limits.DatabaseNow.Add(-time.Minute)) {
		return booking.EmailVerificationDelivery{}, nil
	}

	_, err = tx.ExecContext(ctx, `
		DELETE FROM booking_verification_send_attempts
		WHERE booking_id = $1
		  AND created_at <= $2::timestamptz - INTERVAL '1 hour'
	`, bookingID, limits.DatabaseNow)
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

	bookingIDValue, err := dbgen.New(tx).LockBookingForEmailVerification(ctx, params.BookingReference)
	bookingID := int(bookingIDValue)
	if errors.Is(err, sql.ErrNoRows) {
		return booking.ErrEmailCodeRejected
	}

	if err != nil {
		return fmt.Errorf("lock booking for email verification: %w", err)
	}

	challenge, err := dbgen.New(tx).LockEmailVerificationChallenge(ctx, int32(bookingID))

	if errors.Is(err, sql.ErrNoRows) {
		return booking.ErrEmailCodeRejected
	}

	if err != nil {
		return fmt.Errorf("load booking email verification challenge: %w", err)
	}

	if !challenge.IsUnexpired || !challenge.IsUnconsumed || int(challenge.FailedAttempts) >= maxEmailVerificationAttempts {
		return booking.ErrEmailCodeRejected
	}

	if !hmac.Equal(challenge.CodeDigest, params.CodeDigest) {
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

	valid, err := dbgen.New(p.db).ValidateBookingToken(ctx, dbgen.ValidateBookingTokenParams{
		BookingReference: params.BookingReference,
		TokenDigest:      params.TokenDigest,
	})
	if err != nil {
		return fmt.Errorf("validate booking token: %w", err)
	}

	if !valid {
		return booking.ErrBookingTokenInvalid
	}

	return nil
}
