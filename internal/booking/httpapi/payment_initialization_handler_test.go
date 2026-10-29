package httpapi

import (
	"context"
	"encoding/json/v2"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"example.com/cinema-tickets-backend/internal/booking"
	"example.com/cinema-tickets-backend/internal/checkout"
	"example.com/cinema-tickets-backend/internal/payment"
)

func TestPaymentInitializationStatusHandlerReturnsCurrentState(t *testing.T) {
	for _, test := range []struct {
		name            string
		initialization  payment.Initialization
		wantStatus      string
		wantMethod      string
		wantCheckoutURL string
		wantURLField    bool
	}{
		{
			name: "queued initialization has no checkout URL",
			initialization: payment.Initialization{
				Status:        payment.InitializationStatusQueued,
				PaymentMethod: payment.PaymentMethodCard,
			},
			wantStatus: "QUEUED",
			wantMethod: "CARD",
		},
		{
			name: "ready initialization returns checkout URL",
			initialization: payment.Initialization{
				Status:           payment.InitializationStatusReady,
				PaymentMethod:    payment.PaymentMethodMpesa,
				AuthorizationURL: "https://checkout.example.test/pay",
			},
			wantStatus:      "READY",
			wantMethod:      "MPESA",
			wantCheckoutURL: "https://checkout.example.test/pay",
			wantURLField:    true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			reader := &paymentInitializationHTTPStoreFake{initialization: test.initialization}
			verificationStore := &emailVerificationHTTPStoreFake{}
			handler := newPaymentInitializationStatusTestHandler(t, reader, verificationStore)
			request := httptest.NewRequest(http.MethodGet, "/bookings/bk-test/payment-initialization", nil)
			request.SetPathValue("reference", "bk-test")
			request.Header.Set("Authorization", "Bearer valid-token")
			response := httptest.NewRecorder()

			handler.ServeHTTP(response, request)

			if response.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d; body = %s", response.Code, http.StatusOK, response.Body)
			}
			var got struct {
				Status           string `json:"status"`
				PaymentMethod    string `json:"payment_method"`
				AuthorizationURL string `json:"authorization_url"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
				t.Fatalf("decode response JSON: %v", err)
			}
			if got.Status != test.wantStatus || got.PaymentMethod != test.wantMethod || got.AuthorizationURL != test.wantCheckoutURL {
				t.Errorf("response = %+v, want status=%q method=%q URL=%q", got, test.wantStatus, test.wantMethod, test.wantCheckoutURL)
			}
			if hasJSONField(response.Body.Bytes(), "authorization_url") != test.wantURLField {
				t.Errorf("authorization_url field presence = %t, want %t; body=%s", hasJSONField(response.Body.Bytes(), "authorization_url"), test.wantURLField, response.Body)
			}
			if reader.readCalls != 1 || reader.bookingReference != "bk-test" {
				t.Errorf("read calls/reference = %d/%q, want 1/bk-test", reader.readCalls, reader.bookingReference)
			}
			if verificationStore.validateCalls != 1 || verificationStore.validateParams.BookingReference != "bk-test" {
				t.Errorf("token validation calls/booking = %d/%q, want 1/bk-test", verificationStore.validateCalls, verificationStore.validateParams.BookingReference)
			}
		})
	}
}

func TestPaymentInitializationStatusHandlerRequiresBookingToken(t *testing.T) {
	for _, test := range []struct {
		name          string
		authorization string
		validationErr error
	}{
		{name: "missing token"},
		{name: "invalid token", authorization: "Bearer wrong-token", validationErr: booking.ErrBookingTokenInvalid},
	} {
		t.Run(test.name, func(t *testing.T) {
			reader := &paymentInitializationHTTPStoreFake{}
			verificationStore := &emailVerificationHTTPStoreFake{validateErr: test.validationErr}
			handler := newPaymentInitializationStatusTestHandler(t, reader, verificationStore)
			request := httptest.NewRequest(http.MethodGet, "/bookings/bk-other/payment-initialization", nil)
			request.SetPathValue("reference", "bk-other")
			if test.authorization != "" {
				request.Header.Set("Authorization", test.authorization)
			}
			response := httptest.NewRecorder()

			handler.ServeHTTP(response, request)

			if response.Code != http.StatusUnauthorized {
				t.Errorf("status = %d, want %d", response.Code, http.StatusUnauthorized)
			}
			if reader.readCalls != 0 {
				t.Errorf("initialization reads = %d, want 0 for unauthorized request", reader.readCalls)
			}
		})
	}
}

func TestPaymentInitializationStatusHandlerMapsReadErrors(t *testing.T) {
	for _, test := range []struct {
		name       string
		readErr    error
		wantStatus int
	}{
		{name: "missing initialization", readErr: payment.ErrInitializationNotFound, wantStatus: http.StatusNotFound},
		{name: "store failure", readErr: errors.New("database unavailable"), wantStatus: http.StatusInternalServerError},
	} {
		t.Run(test.name, func(t *testing.T) {
			reader := &paymentInitializationHTTPStoreFake{readErr: test.readErr}
			handler := newPaymentInitializationStatusTestHandler(t, reader, &emailVerificationHTTPStoreFake{})
			request := httptest.NewRequest(http.MethodGet, "/bookings/bk-test/payment-initialization", nil)
			request.SetPathValue("reference", "bk-test")
			request.Header.Set("Authorization", "Bearer valid-token")
			response := httptest.NewRecorder()

			handler.ServeHTTP(response, request)

			if response.Code != test.wantStatus {
				t.Errorf("status = %d, want %d; body=%s", response.Code, test.wantStatus, response.Body)
			}
		})
	}
}

func newPaymentInitializationStatusTestHandler(t *testing.T, reader *paymentInitializationHTTPStoreFake, verificationStore *emailVerificationHTTPStoreFake) *PaymentInitializationStatusHandler {
	t.Helper()
	checkoutService, err := checkout.NewService(&paymentInitializationBookingCreatorFake{}, reader)
	if err != nil {
		t.Fatalf("create checkout service: %v", err)
	}
	verificationService, err := booking.NewEmailVerificationService(verificationStore, &emailVerificationHTTPSenderFake{}, []byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatalf("create email verification service: %v", err)
	}
	handler, err := NewPaymentInitializationStatusHandler(checkoutService, verificationService)
	if err != nil {
		t.Fatalf("create payment initialization handler: %v", err)
	}
	return handler
}

func hasJSONField(body []byte, key string) bool {
	var fields map[string]any
	if err := json.Unmarshal(body, &fields); err != nil {
		return false
	}
	_, ok := fields[key]
	return ok
}

type paymentInitializationBookingCreatorFake struct{}

func (paymentInitializationBookingCreatorFake) CreateWithFingerprint(context.Context, booking.CreateBookingRequest, string) (booking.Booking, error) {
	return booking.Booking{}, nil
}

type paymentInitializationHTTPStoreFake struct {
	initialization   payment.Initialization
	readErr          error
	readCalls        int
	bookingReference string
}

func (*paymentInitializationHTTPStoreFake) CreateOrReplayInitialization(context.Context, payment.CreateInitializationRequest) (payment.Initialization, error) {
	return payment.Initialization{}, nil
}

func (f *paymentInitializationHTTPStoreFake) GetLatestForBooking(_ context.Context, bookingReference string) (payment.Initialization, error) {
	f.readCalls++
	f.bookingReference = bookingReference
	return f.initialization, f.readErr
}
