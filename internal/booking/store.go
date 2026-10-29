package booking

import "context"

type CreateBookingParams struct {
	ScreeningID        int
	RequestedSeatIDs   []int
	Customer           CustomerDetails
	IdempotencyKey     string
	RequestFingerprint string
}

type Store interface {
	// Create atomically creates a booking and claims every requested seat, or
	// leaves no booking or seat claims from this operation. If the idempotency
	// key and request fingerprint match an existing booking, it returns that
	// original booking without extending its hold. If the key exists with a
	// different fingerprint, it returns ErrIdempotencyKeyConflict. If any
	// requested seat cannot be claimed, it returns ErrSeatsUnavailable. Other
	// errors indicate a persistence or infrastructure failure.
	Create(ctx context.Context, params CreateBookingParams) (Booking, error)
	GetDetails(ctx context.Context, bookingReference string) (BookingDetails, error)
}
