package postgres

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"testing"
	"time"

	"example.com/cinema-tickets-backend/internal/booking"
)

func TestCreateEmailVerificationChallengeStoresDigestAndEnforcesCooldown(t *testing.T) {
	db, store, fixture := newBookingIntegrationFixture(t)
	createdBooking, err := store.Create(context.Background(), fixture.params("email-verification-cooldown"))
	if err != nil {
		t.Fatalf("create booking: %v", err)
	}

	digest := bytes.Repeat([]byte{1}, sha256.Size)
	delivery, err := store.CreateEmailVerificationChallenge(context.Background(), booking.CreateEmailVerificationChallengeParams{
		BookingReference: createdBooking.Reference,
		CodeDigest:       digest,
		ExpiresAt:        time.Now().Add(10 * time.Minute),
	})
	if err != nil {
		t.Fatalf("CreateEmailVerificationChallenge() error = %v", err)
	}
	if !delivery.ShouldSend || delivery.RecipientEmail != "customer@example.test" {
		t.Fatalf("delivery = %+v, want accepted send to fixture email", delivery)
	}

	blocked, err := store.CreateEmailVerificationChallenge(context.Background(), booking.CreateEmailVerificationChallengeParams{
		BookingReference: createdBooking.Reference,
		CodeDigest:       bytes.Repeat([]byte{2}, sha256.Size),
		ExpiresAt:        time.Now().Add(10 * time.Minute),
	})
	if err != nil {
		t.Fatalf("cooldown CreateEmailVerificationChallenge() error = %v", err)
	}
	if blocked.ShouldSend {
		t.Fatal("cooldown request ShouldSend = true, want false")
	}

	var storedDigest []byte
	var attempts int
	if err := db.QueryRow(`
		SELECT challenge.code_digest, COUNT(send_attempt.id)::int
		FROM bookings AS booking
		JOIN booking_verification_challenges AS challenge ON challenge.booking_id = booking.id
		LEFT JOIN booking_verification_send_attempts AS send_attempt ON send_attempt.booking_id = booking.id
		WHERE booking.booking_reference = $1
		GROUP BY challenge.code_digest
	`, createdBooking.Reference).Scan(&storedDigest, &attempts); err != nil {
		t.Fatalf("read saved challenge: %v", err)
	}
	if !bytes.Equal(storedDigest, digest) {
		t.Errorf("stored digest = %x, want first digest %x", storedDigest, digest)
	}
	if attempts != 1 {
		t.Errorf("send attempts = %d, want 1", attempts)
	}
}

func TestVerifyEmailCodeConsumesChallengeAndPersistsBookingToken(t *testing.T) {
	_, store, fixture := newBookingIntegrationFixture(t)
	createdBooking, err := store.Create(context.Background(), fixture.params("email-verification-success"))
	if err != nil {
		t.Fatalf("create booking: %v", err)
	}

	codeDigest := bytes.Repeat([]byte{3}, sha256.Size)
	if _, err := store.CreateEmailVerificationChallenge(context.Background(), booking.CreateEmailVerificationChallengeParams{
		BookingReference: createdBooking.Reference,
		CodeDigest:       codeDigest,
		ExpiresAt:        time.Now().Add(10 * time.Minute),
	}); err != nil {
		t.Fatalf("create challenge: %v", err)
	}

	tokenDigest := bytes.Repeat([]byte{4}, sha256.Size)
	verifyParams := booking.VerifyEmailCodeParams{
		BookingReference: createdBooking.Reference,
		CodeDigest:       codeDigest,
		TokenDigest:      tokenDigest,
		TokenExpiresAt:   time.Now().Add(15 * time.Minute),
	}
	if err := store.VerifyEmailCode(context.Background(), verifyParams); err != nil {
		t.Fatalf("VerifyEmailCode() error = %v", err)
	}

	if err := store.ValidateBookingToken(context.Background(), booking.ValidateBookingTokenParams{
		BookingReference: createdBooking.Reference,
		TokenDigest:      tokenDigest,
	}); err != nil {
		t.Fatalf("ValidateBookingToken() error = %v", err)
	}

	if err := store.VerifyEmailCode(context.Background(), verifyParams); !errors.Is(err, booking.ErrEmailCodeRejected) {
		t.Fatalf("second VerifyEmailCode() error = %v, want ErrEmailCodeRejected", err)
	}
	if err := store.ValidateBookingToken(context.Background(), booking.ValidateBookingTokenParams{
		BookingReference: createdBooking.Reference,
		TokenDigest:      bytes.Repeat([]byte{5}, sha256.Size),
	}); !errors.Is(err, booking.ErrBookingTokenInvalid) {
		t.Fatalf("ValidateBookingToken() for unknown digest error = %v, want ErrBookingTokenInvalid", err)
	}
}

func TestValidateBookingTokenEnforcesBookingScopeAndExpiry(t *testing.T) {
	db, store, fixture := newBookingIntegrationFixture(t)
	firstParams := fixture.params("email-token-scope-first")
	firstParams.RequestedSeatIDs = []int{fixture.seatIDs[0]}
	firstBooking, err := store.Create(context.Background(), firstParams)
	if err != nil {
		t.Fatalf("create first booking: %v", err)
	}
	secondParams := fixture.params("email-token-scope-second")
	secondParams.RequestedSeatIDs = []int{fixture.seatIDs[2]}
	secondBooking, err := store.Create(context.Background(), secondParams)
	if err != nil {
		t.Fatalf("create second booking: %v", err)
	}

	codeDigest := bytes.Repeat([]byte{9}, sha256.Size)
	if _, err := store.CreateEmailVerificationChallenge(context.Background(), booking.CreateEmailVerificationChallengeParams{
		BookingReference: firstBooking.Reference,
		CodeDigest:       codeDigest,
		ExpiresAt:        time.Now().Add(10 * time.Minute),
	}); err != nil {
		t.Fatalf("create challenge: %v", err)
	}
	tokenDigest := bytes.Repeat([]byte{10}, sha256.Size)
	if err := store.VerifyEmailCode(context.Background(), booking.VerifyEmailCodeParams{
		BookingReference: firstBooking.Reference,
		CodeDigest:       codeDigest,
		TokenDigest:      tokenDigest,
		TokenExpiresAt:   time.Now().Add(15 * time.Minute),
	}); err != nil {
		t.Fatalf("VerifyEmailCode() error = %v", err)
	}

	if err := store.ValidateBookingToken(context.Background(), booking.ValidateBookingTokenParams{
		BookingReference: secondBooking.Reference,
		TokenDigest:      tokenDigest,
	}); !errors.Is(err, booking.ErrBookingTokenInvalid) {
		t.Fatalf("ValidateBookingToken() for another booking error = %v, want ErrBookingTokenInvalid", err)
	}

	if _, err := db.Exec(`
		UPDATE booking_tokens
		SET created_at = clock_timestamp() - INTERVAL '2 hours',
		    expires_at = clock_timestamp() - INTERVAL '1 hour'
		WHERE token_digest = $1
	`, tokenDigest); err != nil {
		t.Fatalf("expire booking token: %v", err)
	}
	if err := store.ValidateBookingToken(context.Background(), booking.ValidateBookingTokenParams{
		BookingReference: firstBooking.Reference,
		TokenDigest:      tokenDigest,
	}); !errors.Is(err, booking.ErrBookingTokenInvalid) {
		t.Fatalf("ValidateBookingToken() for expired token error = %v, want ErrBookingTokenInvalid", err)
	}
}

func TestVerifyEmailCodeCommitsFailedAttemptsAndLocksAfterLimit(t *testing.T) {
	db, store, fixture := newBookingIntegrationFixture(t)
	createdBooking, err := store.Create(context.Background(), fixture.params("email-verification-attempt-limit"))
	if err != nil {
		t.Fatalf("create booking: %v", err)
	}

	codeDigest := bytes.Repeat([]byte{6}, sha256.Size)
	if _, err := store.CreateEmailVerificationChallenge(context.Background(), booking.CreateEmailVerificationChallengeParams{
		BookingReference: createdBooking.Reference,
		CodeDigest:       codeDigest,
		ExpiresAt:        time.Now().Add(10 * time.Minute),
	}); err != nil {
		t.Fatalf("create challenge: %v", err)
	}

	for attempt := range 5 {
		err := store.VerifyEmailCode(context.Background(), booking.VerifyEmailCodeParams{
			BookingReference: createdBooking.Reference,
			CodeDigest:       bytes.Repeat([]byte{byte(attempt + 10)}, sha256.Size),
			TokenDigest:      bytes.Repeat([]byte{7}, sha256.Size),
			TokenExpiresAt:   time.Now().Add(15 * time.Minute),
		})
		if !errors.Is(err, booking.ErrEmailCodeRejected) {
			t.Fatalf("wrong-code attempt %d error = %v, want ErrEmailCodeRejected", attempt+1, err)
		}
	}

	var failedAttempts int
	var consumedAt *time.Time
	if err := db.QueryRow(`
		SELECT challenge.failed_attempts, challenge.consumed_at
		FROM booking_verification_challenges AS challenge
		JOIN bookings AS booking ON booking.id = challenge.booking_id
		WHERE booking.booking_reference = $1
	`, createdBooking.Reference).Scan(&failedAttempts, &consumedAt); err != nil {
		t.Fatalf("read failed challenge state: %v", err)
	}
	if failedAttempts != 5 || consumedAt == nil {
		t.Fatalf("challenge after five failures = attempts %d, consumed_at %v; want 5 and non-null", failedAttempts, consumedAt)
	}

	if err := store.VerifyEmailCode(context.Background(), booking.VerifyEmailCodeParams{
		BookingReference: createdBooking.Reference,
		CodeDigest:       codeDigest,
		TokenDigest:      bytes.Repeat([]byte{8}, sha256.Size),
		TokenExpiresAt:   time.Now().Add(15 * time.Minute),
	}); !errors.Is(err, booking.ErrEmailCodeRejected) {
		t.Fatalf("correct code after attempt limit error = %v, want ErrEmailCodeRejected", err)
	}

	var tokenCount int
	if err := db.QueryRow(`
		SELECT COUNT(*)::int
		FROM booking_tokens AS token
		JOIN bookings AS booking ON booking.id = token.booking_id
		WHERE booking.booking_reference = $1
	`, createdBooking.Reference).Scan(&tokenCount); err != nil {
		t.Fatalf("count booking tokens: %v", err)
	}
	if tokenCount != 0 {
		t.Errorf("booking tokens after failed verification = %d, want 0", tokenCount)
	}
}
