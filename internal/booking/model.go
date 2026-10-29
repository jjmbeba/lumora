package booking

import "time"

type BookingStatus string

const (
	BookingStatusInvalid       BookingStatus = ""
	BookingStatusReserved      BookingStatus = "RESERVED"
	BookingStatusPaymentReview BookingStatus = "PAYMENT_REVIEW"
	BookingStatusBooked        BookingStatus = "BOOKED"
	BookingStatusCancelled     BookingStatus = "CANCELLED"
)

type CustomerDetails struct {
	Email       string
	PhoneNumber string
}

type Booking struct {
	Reference        string        `json:"reference"`
	Status           BookingStatus `json:"status"`
	TotalAmountMinor int64         `json:"total_amount_minor"`
	Currency         string        `json:"currency"`
	HoldExpiresAt    time.Time     `json:"hold_expires_at"`
}

type BookingDetails struct {
	Reference        string              `json:"reference"`
	Status           BookingStatus       `json:"status"`
	TotalAmountMinor int64               `json:"total_amount_minor"`
	Currency         string              `json:"currency"`
	HoldExpiresAt    time.Time           `json:"hold_expires_at"`
	Screening        ScreeningDetails    `json:"screening"`
	Seats            []BookingSeatDetail `json:"seats"`
}

type ScreeningDetails struct {
	MovieTitle     string    `json:"movie_title"`
	AuditoriumName string    `json:"auditorium_name"`
	StartTime      time.Time `json:"start_time"`
	EndTime        time.Time `json:"end_time"`
}

type BookingSeatDetail struct {
	Label                string `json:"label"`
	UnitPriceAmountMinor int64  `json:"unit_price_amount_minor"`
}

type CreateBookingRequest struct {
	ScreeningID      int
	RequestedSeatIDs []int
	Customer         CustomerDetails
	IdempotencyKey   string
}
