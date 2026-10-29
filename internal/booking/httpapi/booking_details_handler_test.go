package httpapi

import (
	"encoding/json/v2"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"example.com/cinema-tickets-backend/internal/booking"
)

func TestBookingDetailsHandlerRequiresValidBookingBearerToken(t *testing.T) {
	for _, test := range []struct {
		name          string
		authorization string
		validationErr error
		wantValidates int
	}{
		{name: "missing token", wantValidates: 0},
		{name: "malformed scheme", authorization: "Basic abc", wantValidates: 0},
		{name: "rejected token", authorization: "Bearer invalid-token", validationErr: booking.ErrBookingTokenInvalid, wantValidates: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := &stubBookingStore{}
			verificationStore := &emailVerificationHTTPStoreFake{validateErr: test.validationErr}
			handler := newBookingDetailsTestHandler(t, store, verificationStore)
			request := httptest.NewRequest(http.MethodGet, "/bookings/bk-test", nil)
			request.SetPathValue("reference", "bk-test")
			if test.authorization != "" {
				request.Header.Set("Authorization", test.authorization)
			}
			response := httptest.NewRecorder()

			handler.ServeHTTP(response, request)

			if response.Code != http.StatusUnauthorized {
				t.Errorf("status = %d, want %d", response.Code, http.StatusUnauthorized)
			}
			if verificationStore.validateCalls != test.wantValidates {
				t.Errorf("token validation calls = %d, want %d", verificationStore.validateCalls, test.wantValidates)
			}
			if store.detailsCalls != 0 {
				t.Errorf("booking detail store calls = %d, want 0 for unauthorized request", store.detailsCalls)
			}
		})
	}
}

func TestBookingDetailsHandlerReturnsDetailsAfterTokenValidation(t *testing.T) {
	startTime := time.Date(2026, time.November, 2, 18, 0, 0, 0, time.UTC)
	want := booking.BookingDetails{
		Reference:        "bk-test",
		Status:           booking.BookingStatusReserved,
		TotalAmountMinor: 2500,
		Currency:         "KES",
		HoldExpiresAt:    startTime.Add(-time.Hour),
		Screening: booking.ScreeningDetails{
			MovieTitle:     "Example film",
			AuditoriumName: "Screen 1",
			StartTime:      startTime,
			EndTime:        startTime.Add(2 * time.Hour),
		},
		Seats: []booking.BookingSeatDetail{
			{Label: "A1", UnitPriceAmountMinor: 1000},
			{Label: "A2", UnitPriceAmountMinor: 1500},
		},
	}
	store := &stubBookingStore{details: want}
	verificationStore := &emailVerificationHTTPStoreFake{}
	handler := newBookingDetailsTestHandler(t, store, verificationStore)
	request := httptest.NewRequest(http.MethodGet, "/bookings/bk-test", nil)
	request.SetPathValue("reference", "bk-test")
	request.Header.Set("Authorization", "Bearer opaque-token-value")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body = %s", response.Code, http.StatusOK, response.Body)
	}
	if got := response.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", got)
	}
	var got booking.BookingDetails
	if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response JSON: %v", err)
	}
	if got.Reference != want.Reference || got.Status != want.Status || got.TotalAmountMinor != want.TotalAmountMinor || got.Currency != want.Currency ||
		!got.HoldExpiresAt.Equal(want.HoldExpiresAt) || got.Screening != want.Screening || len(got.Seats) != len(want.Seats) ||
		got.Seats[0] != want.Seats[0] || got.Seats[1] != want.Seats[1] {
		t.Errorf("details = %+v, want %+v", got, want)
	}
	if verificationStore.validateCalls != 1 {
		t.Errorf("token validation calls = %d, want 1", verificationStore.validateCalls)
	}
	if verificationStore.validateParams.BookingReference != "bk-test" {
		t.Errorf("validated booking reference = %q, want route reference bk-test", verificationStore.validateParams.BookingReference)
	}
	if store.detailsCalls != 1 || store.detailsRef != "bk-test" {
		t.Errorf("details store calls/reference = %d/%q, want 1/bk-test", store.detailsCalls, store.detailsRef)
	}
	if strings.Contains(response.Body.String(), "customer@example") || strings.Contains(response.Body.String(), "phone") {
		t.Errorf("booking response unexpectedly contains customer contact data: %s", response.Body)
	}
}

func TestBookingDetailsHandlerMapsMissingBooking(t *testing.T) {
	store := &stubBookingStore{detailsErr: booking.ErrBookingNotFound}
	handler := newBookingDetailsTestHandler(t, store, &emailVerificationHTTPStoreFake{})
	request := httptest.NewRequest(http.MethodGet, "/bookings/bk-missing", nil)
	request.SetPathValue("reference", "bk-missing")
	request.Header.Set("Authorization", "Bearer valid-token")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d; body = %s", response.Code, http.StatusNotFound, response.Body)
	}
}

func TestBookingDetailsHandlerMapsUnexpectedTokenStoreFailure(t *testing.T) {
	storeFailure := errors.New("database unavailable")
	verificationStore := &emailVerificationHTTPStoreFake{validateErr: storeFailure}
	handler := newBookingDetailsTestHandler(t, &stubBookingStore{}, verificationStore)
	request := httptest.NewRequest(http.MethodGet, "/bookings/bk-test", nil)
	request.SetPathValue("reference", "bk-test")
	request.Header.Set("Authorization", "Bearer token")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want %d", response.Code, http.StatusInternalServerError)
	}
}

func newBookingDetailsTestHandler(t *testing.T, store booking.Store, verificationStore booking.EmailVerificationStore) *BookingDetailsHandler {
	t.Helper()
	bookingService, err := booking.NewService(store)
	if err != nil {
		t.Fatalf("create booking service: %v", err)
	}
	verificationService, err := booking.NewEmailVerificationService(verificationStore, &emailVerificationHTTPSenderFake{}, []byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatalf("create email verification service: %v", err)
	}
	handler, err := NewBookingDetailsHandler(bookingService, verificationService)
	if err != nil {
		t.Fatalf("create booking details handler: %v", err)
	}
	return handler
}
