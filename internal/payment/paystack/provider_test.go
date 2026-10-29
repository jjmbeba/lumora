package paystack

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"example.com/cinema-tickets-backend/internal/payment"
)

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestVerifyAbandonedIsPending(t *testing.T) {
	provider, err := NewPaystackProvider("test-secret")
	if err != nil {
		t.Fatalf("NewPaystackProvider() error = %v", err)
	}
	provider.client.Transport = roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodGet {
			t.Errorf("request method = %q, want GET", req.Method)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(`{"status":true,"message":"Verification successful","data":{"status":"abandoned","reference":"ref-abandoned"}}`)),
			Header:     make(http.Header),
			Request:    req,
		}, nil
	})

	got, err := provider.Verify(context.Background(), "ref-abandoned")
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if got.VerificationStatus != payment.VerificationStatusPending {
		t.Errorf("VerificationStatus = %v, want pending", got.VerificationStatus)
	}
}
