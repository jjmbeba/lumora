package payment

import (
	"context"
	"time"
)

type ResultStatus int
type VerificationStatus int

const (
	ResultStatusInvalid ResultStatus = iota
	ResultStatusCheckoutCreated
	ResultStatusRejected
	ResultStatusCancelled
	ResultStatusUnknown
)

const (
	VerificationStatusInvalid VerificationStatus = iota
	VerificationStatusPaid
	VerificationStatusCancelled
	VerificationStatusReversed
	VerificationStatusPending
	VerificationStatusFailed
	VerificationStatusUnknown
)

type InitRequest struct {
	AmountMinor   int64
	Currency      string
	Email         string
	PhoneNumber   string
	Reference     string
	PaymentMethod PaymentMethod
}

type InitResult struct {
	Status           ResultStatus
	Message          string
	AuthorizationURL string
	Reference        string
	AccessCode       string
}

type Verification struct {
	VerificationStatus VerificationStatus
	Message            string
	TransactionID      int64
	Reference          string
	AmountMinor        int64
	PaidAt             time.Time
	Currency           string
}

// Provider is the payment-provider boundary used by the payment worker.
//
// Initialize must return a definitive ResultStatus only when the provider's
// response establishes that outcome. If it returns an error, the worker must
// treat the initialization outcome as unknown: the provider may have accepted
// the request even if the response was lost or could not be parsed. The worker
// must reconcile by the persisted reference before attempting initialization
// again. A provider-confirmed rejection is returned as ResultStatusRejected,
// with no error, only when the provider confirms that no checkout was created.
type Provider interface {
	Initialize(ctx context.Context, request InitRequest) (InitResult, error)
	Verify(ctx context.Context, reference string) (Verification, error)
}
