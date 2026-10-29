package payment

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestWorkerProcessInitializationErrorSchedulesUnknownAndStops(t *testing.T) {
	providerErr := errors.New("provider timed out")
	store := &fakeWorkerStore{}
	provider := &fakeWorkerProvider{
		initResult: InitResult{
			Status:           ResultStatusCheckoutCreated,
			AuthorizationURL: "https://checkout.example/abc",
			Reference:        "payment-ref-1",
		},
		initErr: providerErr,
	}
	worker := newTestWorker(t, store, provider)
	claim := queuedTestClaim()

	if err := worker.process(context.Background(), claim); err != nil {
		t.Fatalf("process() error = %v, want nil after persisting UNKNOWN", err)
	}

	if store.markUnknownCalls != 1 {
		t.Errorf("MarkUnknown calls = %d, want 1", store.markUnknownCalls)
	}
	if store.markReadyCalls != 0 {
		t.Errorf("MarkReady calls = %d, want 0 after provider error", store.markReadyCalls)
	}
	if provider.initializeCalls != 1 {
		t.Errorf("Initialize calls = %d, want 1", provider.initializeCalls)
	}
}

func TestWorkerProcessVerificationErrorSchedulesUnknownAndStops(t *testing.T) {
	providerErr := errors.New("verification timed out")
	store := &fakeWorkerStore{}
	provider := &fakeWorkerProvider{
		verifyResult: Verification{VerificationStatus: VerificationStatusPaid},
		verifyErr:    providerErr,
	}
	worker := newTestWorker(t, store, provider)
	claim := queuedTestClaim()
	claim.PreviousStatus = InitializationStatusUnknown

	if err := worker.process(context.Background(), claim); err != nil {
		t.Fatalf("process() error = %v, want nil after persisting UNKNOWN", err)
	}

	if store.markUnknownCalls != 1 {
		t.Errorf("MarkUnknown calls = %d, want 1", store.markUnknownCalls)
	}
	if store.markPaidCalls != 0 {
		t.Errorf("MarkPaid calls = %d, want 0 after verification error", store.markPaidCalls)
	}
	if provider.verifyCalls != 1 {
		t.Errorf("Verify calls = %d, want 1", provider.verifyCalls)
	}
}

func TestWorkerProcessInvalidCheckoutSchedulesUnknownAndDoesNotMarkReady(t *testing.T) {
	tests := []struct {
		name   string
		result InitResult
	}{
		{
			name: "missing authorization URL",
			result: InitResult{
				Status:    ResultStatusCheckoutCreated,
				Reference: "payment-ref-1",
			},
		},
		{
			name: "reference mismatch",
			result: InitResult{
				Status:           ResultStatusCheckoutCreated,
				AuthorizationURL: "https://checkout.example/abc",
				Reference:        "different-ref",
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := &fakeWorkerStore{}
			provider := &fakeWorkerProvider{initResult: test.result}
			worker := newTestWorker(t, store, provider)

			if err := worker.process(context.Background(), queuedTestClaim()); err != nil {
				t.Fatalf("process() error = %v, want nil after persisting UNKNOWN", err)
			}

			if store.markUnknownCalls != 1 {
				t.Errorf("MarkUnknown calls = %d, want 1", store.markUnknownCalls)
			}
			if store.markReadyCalls != 0 {
				t.Errorf("MarkReady calls = %d, want 0 for invalid checkout result", store.markReadyCalls)
			}
		})
	}
}

func TestWorkerRunReturnsMarkUnknownError(t *testing.T) {
	storeErr := errors.New("database unavailable")
	store := &fakeWorkerStore{
		claim:           queuedTestClaim(),
		claimsAvailable: true,
		markUnknownErr:  storeErr,
	}
	provider := &fakeWorkerProvider{initErr: errors.New("provider timed out")}
	worker := newTestWorker(t, store, provider)

	err := worker.Run(context.Background())
	if !errors.Is(err, storeErr) {
		t.Fatalf("Run() error = %v, want %v", err, storeErr)
	}
}

func TestWorkerProcessSuccessfulCheckoutMarksReady(t *testing.T) {
	claim := queuedTestClaim()
	checkoutURL := "https://checkout.example/abc"
	store := &fakeWorkerStore{}
	provider := &fakeWorkerProvider{
		initResult: InitResult{
			Status:           ResultStatusCheckoutCreated,
			AuthorizationURL: checkoutURL,
			Reference:        claim.PaystackReference,
		},
	}
	worker := newTestWorker(t, store, provider)

	if err := worker.process(context.Background(), claim); err != nil {
		t.Fatalf("process() error = %v, want nil", err)
	}

	if store.markReadyCalls != 1 {
		t.Errorf("MarkReady calls = %d, want 1", store.markReadyCalls)
	}
	if store.checkoutURL != checkoutURL {
		t.Errorf("checkout URL = %q, want %q", store.checkoutURL, checkoutURL)
	}
	if store.markUnknownCalls != 0 {
		t.Errorf("MarkUnknown calls = %d, want 0", store.markUnknownCalls)
	}
}

func TestWorkerProcessReadyPendingSchedulesAnotherVerification(t *testing.T) {
	claim := queuedTestClaim()
	claim.PreviousStatus = InitializationStatusReady
	store := &fakeWorkerStore{}
	provider := &fakeWorkerProvider{
		verifyResult: Verification{VerificationStatus: VerificationStatusPending},
	}
	worker := newTestWorker(t, store, provider)

	if err := worker.process(context.Background(), claim); err != nil {
		t.Fatalf("process() error = %v, want nil", err)
	}

	if provider.verifyCalls != 1 {
		t.Errorf("Verify calls = %d, want 1", provider.verifyCalls)
	}
	if store.markVerificationPendingCalls != 1 {
		t.Errorf("MarkVerificationPending calls = %d, want 1", store.markVerificationPendingCalls)
	}
	if store.verificationNextCheckAt.IsZero() {
		t.Error("MarkVerificationPending next check time is zero")
	}
	if store.markUnknownCalls != 0 {
		t.Errorf("MarkUnknown calls = %d, want 0 for a known pending checkout", store.markUnknownCalls)
	}
}

func newTestWorker(t *testing.T, store Store, provider Provider) *Worker {
	t.Helper()

	worker, err := NewWorker(store, provider, time.Second)
	if err != nil {
		t.Fatalf("NewWorker() error = %v", err)
	}
	return worker
}

func queuedTestClaim() Claim {
	return Claim{
		InitializationID:  1,
		PreviousStatus:    InitializationStatusQueued,
		Generation:        1,
		PaystackReference: "payment-ref-1",
		PaymentMethod:     PaymentMethodCard,
		AmountMinor:       2500,
		Currency:          "KES",
		PayerEmail:        "customer@example.test",
	}
}

type fakeWorkerProvider struct {
	initResult      InitResult
	initErr         error
	verifyResult    Verification
	verifyErr       error
	initializeCalls int
	verifyCalls     int
}

func (p *fakeWorkerProvider) Initialize(context.Context, InitRequest) (InitResult, error) {
	p.initializeCalls++
	return p.initResult, p.initErr
}

func (p *fakeWorkerProvider) Verify(context.Context, string) (Verification, error) {
	p.verifyCalls++
	return p.verifyResult, p.verifyErr
}

type fakeWorkerStore struct {
	claim                        Claim
	claimsAvailable              bool
	markUnknownErr               error
	markUnknownCalls             int
	markReadyCalls               int
	markVerificationPendingCalls int
	verificationNextCheckAt      time.Time
	markRejectedCalls            int
	markPaidCalls                int
	markFailedCalls              int
	markCancelledCalls           int
	markPaymentCancelledCalls    int
	markReversedCalls            int
	checkoutURL                  string
}

func (s *fakeWorkerStore) ClaimNext(context.Context) (Claim, bool, error) {
	if !s.claimsAvailable {
		return Claim{}, false, nil
	}
	s.claimsAvailable = false
	return s.claim, true, nil
}

func (s *fakeWorkerStore) MarkReady(_ context.Context, _ Claim, checkoutURL string, _ time.Time) error {
	s.markReadyCalls++
	s.checkoutURL = checkoutURL
	return nil
}

func (s *fakeWorkerStore) MarkVerificationPending(_ context.Context, _ Claim, nextCheckAt time.Time) error {
	s.markVerificationPendingCalls++
	s.verificationNextCheckAt = nextCheckAt
	return nil
}

func (s *fakeWorkerStore) MarkRejected(_ context.Context, _ Claim, _ string) error {
	s.markRejectedCalls++
	return nil
}

func (s *fakeWorkerStore) MarkUnknown(context.Context, Claim, time.Time) error {
	s.markUnknownCalls++
	return s.markUnknownErr
}

func (s *fakeWorkerStore) MarkPaid(context.Context, Claim, Verification) error {
	s.markPaidCalls++
	return nil
}

func (s *fakeWorkerStore) MarkFailed(context.Context, Claim) error {
	s.markFailedCalls++
	return nil
}

func (s *fakeWorkerStore) MarkCancelled(context.Context, Claim) error {
	s.markCancelledCalls++
	return nil
}

func (s *fakeWorkerStore) MarkPaymentCancelled(context.Context, Claim) error {
	s.markPaymentCancelledCalls++
	return nil
}

func (s *fakeWorkerStore) MarkReversed(context.Context, Claim) error {
	s.markReversedCalls++
	return nil
}
