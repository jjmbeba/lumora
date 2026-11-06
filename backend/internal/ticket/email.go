package ticket

import (
	"context"
	"errors"
	"time"
)

// TicketEmail is the content of a booking confirmation email. Credentials are
// bearer secrets and must not be logged.
type TicketEmail struct {
	To               string
	BookingReference string
	MovieTitle       string
	ScreeningAt      time.Time
	AuditoriumName   string
	IdempotencyKey   string
	Tickets          []TicketEmailItem
}

type TicketEmailItem struct {
	SeatLabel  string
	Credential string
}

// TicketEmailJob identifies a leased outbox row. AttemptCount is also its
// generation, so stale workers cannot overwrite a newer claim.
type TicketEmailJob struct {
	ID           int
	BookingID    int
	Recipient    string
	EventKey     string
	AttemptCount int
}

var ErrEmailClaimLost = errors.New("ticket email claim is no longer current")

type TicketEmailStore interface {
	ClaimNextTicketEmail(ctx context.Context, leaseDuration time.Duration) (TicketEmailJob, bool, error)
	LoadTicketEmail(ctx context.Context, job TicketEmailJob) (TicketEmail, error)
	MarkTicketEmailSent(ctx context.Context, job TicketEmailJob) error
	RetryTicketEmail(ctx context.Context, job TicketEmailJob, retryAt time.Time, terminal bool) error
}

type TicketEmailSender interface {
	SendTicketEmail(ctx context.Context, email TicketEmail) error
}
