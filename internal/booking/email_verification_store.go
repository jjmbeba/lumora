package booking

import (
	"context"
	"time"
)

// CreateEmailVerificationChallengeParams contains only values produced by the
// booking service. CodeDigest is an HMAC-SHA-256 digest, never the raw code.
type CreateEmailVerificationChallengeParams struct {
	BookingReference string
	CodeDigest       []byte
	ExpiresAt        time.Time
}

// EmailVerificationDelivery is populated only when the store accepted a new
// challenge and the service should send the code. A false ShouldSend also
// represents an unknown booking or a booking-level send limit, so the caller
// can return the same public response in each case.
type EmailVerificationDelivery struct {
	ShouldSend     bool
	RecipientEmail string
}

// VerifyEmailCodeParams carries the candidate code digest and the digest of a
// newly generated bearer token. The store persists the token digest only.
type VerifyEmailCodeParams struct {
	BookingReference string
	CodeDigest       []byte
	TokenDigest      []byte
	TokenExpiresAt   time.Time
}

// ValidateBookingTokenParams identifies a booking-scoped token by its digest.
type ValidateBookingTokenParams struct {
	BookingReference string
	TokenDigest      []byte
}

// EmailVerificationStore exposes the atomic persistence operations needed by
// the email-verification service. Implementations should use the database
// clock for cooldown, rate-window, and token-expiration comparisons.
type EmailVerificationStore interface {
	// CreateEmailVerificationChallenge enforces the per-booking cooldown and
	// hourly send limit, invalidates any active challenge, and stores the new
	// digest and expiry in one transaction. Unknown bookings and bookings that
	// are cooling down or over the send limit return ShouldSend=false without
	// an error.
	CreateEmailVerificationChallenge(ctx context.Context, params CreateEmailVerificationChallengeParams) (EmailVerificationDelivery, error)

	// VerifyEmailCode atomically checks and consumes the active code and, on
	// success, stores the booking-scoped token digest and expiry. A rejected
	// attempt must have its attempt count committed before ErrEmailCodeRejected
	// is returned; otherwise callers could retry forever without consuming
	// attempts.
	VerifyEmailCode(ctx context.Context, params VerifyEmailCodeParams) error

	// ValidateBookingToken succeeds only for an unexpired token belonging to
	// the referenced booking. Invalid, expired, and wrong-booking tokens return
	// ErrBookingTokenInvalid.
	ValidateBookingToken(ctx context.Context, params ValidateBookingTokenParams) error
}
