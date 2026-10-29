package webhook

import (
	"context"
	"crypto/hmac"
	"crypto/sha512"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"example.com/cinema-tickets-backend/internal/payment"
)

const testSecret = "test-paystack-secret"

const validChargeSuccess = `{"event":"charge.success","data":{"id":9876543210,"status":"success","reference":"ref-test-123","amount":2500,"paid_at":"2026-10-20T09:30:00Z","currency":"KES"}}`

type recordingEventStore struct {
	calls int
	event payment.ProviderWebhookEvent
	err   error
}

func (s *recordingEventStore) RecordAndSchedule(_ context.Context, event payment.ProviderWebhookEvent) error {
	s.calls++
	s.event = event
	return s.err
}

func TestNewHandlerRequiresSecretAndStore(t *testing.T) {
	store := &recordingEventStore{}

	if _, err := NewHandler("", store); err == nil {
		t.Fatal("NewHandler() error = nil for empty secret")
	}
	if _, err := NewHandler(testSecret, nil); err == nil {
		t.Fatal("NewHandler() error = nil for nil store")
	}
}

func TestHandlerStoresValidatedChargeSuccess(t *testing.T) {
	store := &recordingEventStore{}
	handler, err := NewHandler(testSecret, store)
	if err != nil {
		t.Fatalf("NewHandler() error = %v", err)
	}

	response := sendSignedWebhook(t, handler, validChargeSuccess)
	if response.Code != http.StatusOK {
		t.Fatalf("HTTP status = %d, want %d; body=%q", response.Code, http.StatusOK, response.Body.String())
	}
	if store.calls != 1 {
		t.Fatalf("store calls = %d, want 1", store.calls)
	}

	wantPaidAt := time.Date(2026, time.October, 20, 9, 30, 0, 0, time.UTC)
	want := payment.ProviderWebhookEvent{
		EventType:      "charge.success",
		TransactionID:  9876543210,
		Reference:      "ref-test-123",
		ProviderStatus: "success",
		AmountMinor:    2500,
		Currency:       "KES",
		ProviderPaidAt: wantPaidAt,
	}
	if store.event.EventType != want.EventType ||
		store.event.TransactionID != want.TransactionID ||
		store.event.Reference != want.Reference ||
		store.event.ProviderStatus != want.ProviderStatus ||
		store.event.AmountMinor != want.AmountMinor ||
		store.event.Currency != want.Currency ||
		!store.event.ProviderPaidAt.Equal(want.ProviderPaidAt) {
		t.Errorf("stored event = %+v, want %+v", store.event, want)
	}
}

func TestHandlerRejectsInvalidChargeSuccessFields(t *testing.T) {
	tests := []struct {
		name    string
		payload string
	}{
		{
			name:    "missing reference",
			payload: strings.Replace(validChargeSuccess, `"reference":"ref-test-123",`, "", 1),
		},
		{
			name:    "zero transaction ID",
			payload: strings.Replace(validChargeSuccess, `"id":9876543210`, `"id":0`, 1),
		},
		{
			name:    "non-positive amount",
			payload: strings.Replace(validChargeSuccess, `"amount":2500`, `"amount":0`, 1),
		},
		{
			name:    "non-success status",
			payload: strings.Replace(validChargeSuccess, `"status":"success"`, `"status":"failed"`, 1),
		},
		{
			name:    "missing paid_at",
			payload: strings.Replace(validChargeSuccess, `"paid_at":"2026-10-20T09:30:00Z",`, "", 1),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := &recordingEventStore{}
			handler, err := NewHandler(testSecret, store)
			if err != nil {
				t.Fatalf("NewHandler() error = %v", err)
			}

			response := sendSignedWebhook(t, handler, test.payload)
			if response.Code != http.StatusBadRequest {
				t.Errorf("HTTP status = %d, want %d; body=%q", response.Code, http.StatusBadRequest, response.Body.String())
			}
			if store.calls != 0 {
				t.Errorf("store calls = %d, want 0", store.calls)
			}
		})
	}
}

func TestHandlerAcknowledgesUnsupportedEventWithoutCallingStore(t *testing.T) {
	store := &recordingEventStore{}
	handler, err := NewHandler(testSecret, store)
	if err != nil {
		t.Fatalf("NewHandler() error = %v", err)
	}

	response := sendSignedWebhook(t, handler, `{"event":"refund.processed","data":{"id":42}}`)
	if response.Code != http.StatusOK {
		t.Errorf("HTTP status = %d, want %d", response.Code, http.StatusOK)
	}
	if store.calls != 0 {
		t.Errorf("store calls = %d, want 0", store.calls)
	}
}

func sendSignedWebhook(t *testing.T, handler http.Handler, payload string) *httptest.ResponseRecorder {
	t.Helper()

	request := httptest.NewRequest(http.MethodPost, "/webhooks/paystack", strings.NewReader(payload))
	request.Header.Set("x-paystack-signature", webhookSignature(payload))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func webhookSignature(payload string) string {
	mac := hmac.New(sha512.New, []byte(testSecret))
	_, _ = mac.Write([]byte(payload))
	return hex.EncodeToString(mac.Sum(nil))
}
