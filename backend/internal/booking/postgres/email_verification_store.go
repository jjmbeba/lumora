package postgres

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/jjmbeba/lumora/backend/internal/booking"
	"github.com/jjmbeba/lumora/backend/internal/booking/postgres/dbgen"
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
	queries := dbgen.New(tx)

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

	err = queries.PruneBookingEmailVerificationSendAttempts(ctx, dbgen.PruneBookingEmailVerificationSendAttemptsParams{BookingID: int32(bookingID), PruneBefore: limits.DatabaseNow})
	if err != nil {
		return booking.EmailVerificationDelivery{}, fmt.Errorf("prune old booking email verification send attempts: %w", err)
	}

	err = queries.UpsertBookingEmailVerificationChallenge(ctx, dbgen.UpsertBookingEmailVerificationChallengeParams{
		BookingID: int32(bookingID), CodeDigest: params.CodeDigest, ExpiresAt: params.ExpiresAt,
	})
	if err != nil {
		return booking.EmailVerificationDelivery{}, fmt.Errorf("save booking email verification challenge: %w", err)
	}

	err = queries.RecordBookingEmailVerificationSendAttempt(ctx, int32(bookingID))
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
	queries := dbgen.New(tx)

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
		err := queries.RecordRejectedBookingEmailVerificationAttempt(ctx, dbgen.RecordRejectedBookingEmailVerificationAttemptParams{BookingID: int32(bookingID), FailedAttempts: maxEmailVerificationAttempts})

		if err != nil {
			return fmt.Errorf("record rejected booking email verification attempt: %w", err)
		}

		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit rejected booking email verification attempt: %w", err)
		}

		return booking.ErrEmailCodeRejected
	}

	updated, err := queries.ConsumeBookingEmailVerificationChallenge(ctx, int32(bookingID))

	if err != nil {
		return fmt.Errorf("consume booking email verification code: %w", err)
	}

	if updated != 1 {
		return booking.ErrEmailCodeRejected
	}

	err = queries.CreateBookingToken(ctx, dbgen.CreateBookingTokenParams{BookingID: int32(bookingID), TokenDigest: params.TokenDigest, ExpiresAt: params.TokenExpiresAt})
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
