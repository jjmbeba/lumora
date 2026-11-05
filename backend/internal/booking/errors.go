package booking

import "errors"

var (
	ErrSeatsUnavailable          = errors.New("one or more requested seats are unavailable")
	ErrIdempotencyKeyConflict    = errors.New("idempotency key was used with a different booking request")
	ErrInvalidRequest            = errors.New("invalid request")
	ErrEmailCodeRejected         = errors.New("email verification code is invalid, expired, or already used")
	ErrVerificationEmailDelivery = errors.New("verification email delivery failed")
	ErrBookingTokenInvalid       = errors.New("booking token is invalid or expired")
	ErrBookingNotFound           = errors.New("booking not found")
)
