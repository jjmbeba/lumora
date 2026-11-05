package booking

import "time"

type VerifyEmailCodeRequest struct {
	BookingReference string
	Code             string
}

type BookingToken struct {
	Token     string
	ExpiresAt time.Time
}
