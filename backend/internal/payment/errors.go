package payment

import "errors"

var (
	ErrClaimLost                         = errors.New("The worker’s claim is no longer current, so its result was not saved.")
	ErrPaymentAttemptNotPending          = errors.New("payment attempt is not pending")
	ErrBookingNotFound                   = errors.New("booking not found")
	ErrPaymentStateConflict              = errors.New("The verified payment result conflicts with the current payment or booking state. No state changes were saved; reconciliation is required.")
	ErrInitializationIdempotencyConflict = errors.New("payment initialization idempotency key was used with a different request")
	ErrInvalidInitializationRequest      = errors.New("invalid payment initialization request")
	ErrBookingNotEligibleForPayment      = errors.New("booking is not eligible for payment initialization")
	ErrPaymentAttemptNotRetryable        = errors.New("payment attempt does not permit a new initialization")
	ErrInitializationNotFound            = errors.New("payment initialization not found")
)
