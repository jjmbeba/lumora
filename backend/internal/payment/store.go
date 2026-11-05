package payment

import (
	"context"
	"time"
)

type PaymentMethod int
type InitializationStatus int
type PaymentStatus int

const (
	PaymentMethodInvalid PaymentMethod = iota
	PaymentMethodCard
	PaymentMethodMpesa
)

const (
	InitializationStatusInvalid InitializationStatus = iota
	InitializationStatusQueued
	InitializationStatusSubmitting
	InitializationStatusReady
	InitializationStatusPaid
	InitializationStatusFailed
	InitializationStatusRejected
	InitializationStatusUnknown
	InitializationStatusCancelled
	InitializationStatusReviewRequired
)

const (
	PaymentStatusInvalid PaymentStatus = iota
	PaymentStatusPending
	PaymentStatusFailed
	PaymentStatusPaid
	PaymentStatusCancelled
)

type Claim struct {
	InitializationID int
	PreviousStatus   InitializationStatus
	Generation       int

	PaystackReference string
	PaymentMethod     PaymentMethod
	AmountMinor       int64
	Currency          string

	PayerEmail       string
	PayerPhoneNumber string
}

type Initialization struct {
	ID                int
	Status            InitializationStatus
	PaymentMethod     PaymentMethod
	PaystackReference string
	AuthorizationURL  string
}

type CreateInitializationRequest struct {
	BookingReference   string
	IdempotencyKey     string
	RequestFingerprint string
	PaymentMethod      PaymentMethod
}

type InitializationCreator interface {
	CreateOrReplayInitialization(ctx context.Context, request CreateInitializationRequest) (Initialization, error)
}

type Store interface {
	ClaimNext(ctx context.Context) (Claim, bool, error)
	MarkReady(ctx context.Context, claim Claim, checkoutURL string, nextCheckAt time.Time) error
	MarkVerificationPending(ctx context.Context, claim Claim, nextCheckAt time.Time) error
	MarkRejected(ctx context.Context, claim Claim, reason string) error
	MarkUnknown(ctx context.Context, claim Claim, retryAt time.Time) error
	MarkPaid(ctx context.Context, claim Claim, verification Verification) error
	MarkFailed(ctx context.Context, claim Claim) error
	MarkCancelled(ctx context.Context, claim Claim) error
	MarkPaymentCancelled(ctx context.Context, claim Claim) error
	MarkReversed(ctx context.Context, claim Claim) error
}
