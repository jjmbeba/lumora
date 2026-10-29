package httpapi

import (
	"context"
	"encoding/json/v2"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"example.com/cinema-tickets-backend/internal/booking"
	"example.com/cinema-tickets-backend/internal/checkout"
	"example.com/cinema-tickets-backend/internal/payment"
)

func TestCreateBookingHandlerStartsBookingAndPayment(t *testing.T) {
	holdExpiresAt := time.Date(2026, time.October, 26, 12, 30, 0, 0, time.UTC)
	starter := &checkoutStarterFake{result: checkout.StartResult{
		Booking: booking.Booking{
			Reference:        "bk_test123",
			Status:           booking.BookingStatusReserved,
			TotalAmountMinor: 2500,
			Currency:         "KES",
			HoldExpiresAt:    holdExpiresAt,
		},
		Initialization: payment.Initialization{
			Status:        payment.InitializationStatusQueued,
			PaymentMethod: payment.PaymentMethodCard,
		},
	}}
	handler := newTestCreateBookingHandler(t, starter)
	request := httptest.NewRequest(http.MethodPost, "/bookings", strings.NewReader(validCreateBookingJSON))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "booking-request-123")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d; body = %s", response.Code, http.StatusCreated, response.Body)
	}
	if got := response.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", got)
	}
	var got struct {
		Booking booking.Booking `json:"booking"`
		Payment struct {
			Status           string `json:"status"`
			PaymentMethod    string `json:"payment_method"`
			AuthorizationURL string `json:"authorization_url"`
		} `json:"payment_initialization"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response JSON: %v", err)
	}
	if got.Booking.Reference != "bk_test123" || got.Booking.TotalAmountMinor != 2500 || got.Booking.Currency != "KES" {
		t.Errorf("response booking = %+v", got.Booking)
	}
	if got.Payment.Status != "QUEUED" || got.Payment.PaymentMethod != "CARD" || got.Payment.AuthorizationURL != "" {
		t.Errorf("response payment initialization = %+v, want QUEUED CARD with no URL yet", got.Payment)
	}
	if starter.calls != 1 {
		t.Fatalf("checkout service calls = %d, want 1", starter.calls)
	}
	if starter.request.Booking.IdempotencyKey != "booking-request-123" || starter.request.Booking.ScreeningID != 42 || starter.request.PaymentMethod != payment.PaymentMethodCard {
		t.Errorf("checkout request = %+v", starter.request)
	}
	if !reflect.DeepEqual(starter.request.Booking.RequestedSeatIDs, []int{202, 101}) {
		t.Errorf("handler seat IDs = %v, want request order preserved for service canonicalization", starter.request.Booking.RequestedSeatIDs)
	}
	if starter.request.Booking.Customer != (booking.CustomerDetails{Email: " person@example.com ", PhoneNumber: " +254700000000 "}) {
		t.Errorf("handler customer = %+v, want raw input for service normalization", starter.request.Booking.Customer)
	}
}

func TestCreateBookingHandlerRejectsMalformedJSONBeforeCheckout(t *testing.T) {
	starter := &checkoutStarterFake{}
	handler := newTestCreateBookingHandler(t, starter)
	request := httptest.NewRequest(http.MethodPost, "/bookings", strings.NewReader(`{"screening_id":`))
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", response.Code, http.StatusBadRequest)
	}
	if starter.calls != 0 {
		t.Errorf("checkout service calls = %d, want 0 for malformed JSON", starter.calls)
	}
}

func TestCreateBookingHandlerRejectsInvalidPaymentMethodBeforeCheckout(t *testing.T) {
	starter := &checkoutStarterFake{}
	handler := newTestCreateBookingHandler(t, starter)
	request := httptest.NewRequest(http.MethodPost, "/bookings", strings.NewReader(strings.Replace(validCreateBookingJSON, `"CARD"`, `"BITCOIN"`, 1)))
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", response.Code, http.StatusBadRequest)
	}
	if starter.calls != 0 {
		t.Errorf("checkout service calls = %d, want 0 for invalid payment method", starter.calls)
	}
}

func TestCreateBookingHandlerMapsCheckoutErrors(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantStatus int
	}{
		{name: "invalid booking", err: booking.ErrInvalidRequest, wantStatus: http.StatusBadRequest},
		{name: "seat conflict", err: booking.ErrSeatsUnavailable, wantStatus: http.StatusConflict},
		{name: "booking idempotency conflict", err: booking.ErrIdempotencyKeyConflict, wantStatus: http.StatusConflict},
		{name: "initialization idempotency conflict", err: payment.ErrInitializationIdempotencyConflict, wantStatus: http.StatusConflict},
		{name: "booking not eligible", err: payment.ErrBookingNotEligibleForPayment, wantStatus: http.StatusConflict},
		{name: "booking not found", err: payment.ErrBookingNotFound, wantStatus: http.StatusNotFound},
		{name: "unexpected failure", err: errors.New("database unavailable"), wantStatus: http.StatusInternalServerError},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			starter := &checkoutStarterFake{err: test.err}
			handler := newTestCreateBookingHandler(t, starter)
			request := httptest.NewRequest(http.MethodPost, "/bookings", strings.NewReader(validCreateBookingJSON))
			request.Header.Set("Idempotency-Key", "booking-request-123")
			response := httptest.NewRecorder()

			handler.ServeHTTP(response, request)

			if response.Code != test.wantStatus {
				t.Errorf("status = %d, want %d; body = %s", response.Code, test.wantStatus, response.Body)
			}
			if starter.calls != 1 {
				t.Errorf("checkout service calls = %d, want 1", starter.calls)
			}
		})
	}
}

func TestCreateBookingHandlerRejectsUnsupportedMethod(t *testing.T) {
	starter := &checkoutStarterFake{}
	handler := newTestCreateBookingHandler(t, starter)
	request := httptest.NewRequest(http.MethodGet, "/bookings", nil)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want %d", response.Code, http.StatusMethodNotAllowed)
	}
	if got := response.Header().Get("Allow"); got != http.MethodPost {
		t.Errorf("Allow = %q, want POST", got)
	}
	if starter.calls != 0 {
		t.Errorf("checkout service calls = %d, want 0 for unsupported method", starter.calls)
	}
}

const validCreateBookingJSON = `{
	"screening_id": 42,
	"requested_seat_ids": [202, 101],
	"customer": {
		"email": " person@example.com ",
		"phone_number": " +254700000000 "
	},
	"payment_method": "CARD"
}`

type checkoutStarterFake struct {
	result  checkout.StartResult
	err     error
	calls   int
	request checkout.StartRequest
}

func (f *checkoutStarterFake) Start(_ context.Context, request checkout.StartRequest) (checkout.StartResult, error) {
	f.calls++
	f.request = request
	return f.result, f.err
}

func newTestCreateBookingHandler(t *testing.T, starter checkout.Starter) *CreateBookingHandler {
	t.Helper()
	handler, err := NewCreateBookingHandler(starter)
	if err != nil {
		t.Fatalf("create booking HTTP handler: %v", err)
	}
	return handler
}
