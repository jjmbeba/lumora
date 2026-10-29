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
)

func TestCreateBookingHandlerCreatesBookingAndReturnsJSON(t *testing.T) {
	holdExpiresAt := time.Date(2026, time.October, 26, 12, 30, 0, 0, time.UTC)
	store := &stubBookingStore{
		result: booking.Booking{
			Reference:        "bk_test123",
			Status:           booking.BookingStatusReserved,
			TotalAmountMinor: 2500,
			Currency:         "KES",
			HoldExpiresAt:    holdExpiresAt,
		},
	}
	handler := newTestCreateBookingHandler(t, store)
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
	var got booking.Booking
	if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response JSON: %v", err)
	}
	if !reflect.DeepEqual(got, store.result) {
		t.Errorf("response booking = %+v, want %+v", got, store.result)
	}
	if store.calls != 1 {
		t.Fatalf("store calls = %d, want 1", store.calls)
	}
	if store.params.IdempotencyKey != "booking-request-123" {
		t.Errorf("store idempotency key = %q, want booking-request-123", store.params.IdempotencyKey)
	}
	if store.params.ScreeningID != 42 {
		t.Errorf("store screening ID = %d, want 42", store.params.ScreeningID)
	}
	if !reflect.DeepEqual(store.params.RequestedSeatIDs, []int{101, 202}) {
		t.Errorf("store seat IDs = %v, want sorted [101 202]", store.params.RequestedSeatIDs)
	}
	if store.params.Customer != (booking.CustomerDetails{Email: "person@example.com", PhoneNumber: "+254700000000"}) {
		t.Errorf("store customer = %+v, want normalized request customer", store.params.Customer)
	}
	if store.params.RequestFingerprint == "" {
		t.Error("store request fingerprint is empty")
	}
}

func TestCreateBookingHandlerRejectsMalformedJSONBeforeStore(t *testing.T) {
	store := &stubBookingStore{}
	handler := newTestCreateBookingHandler(t, store)
	request := httptest.NewRequest(http.MethodPost, "/bookings", strings.NewReader(`{"screening_id":`))
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", response.Code, http.StatusBadRequest)
	}
	if store.calls != 0 {
		t.Errorf("store calls = %d, want 0 for malformed JSON", store.calls)
	}
}

func TestCreateBookingHandlerRejectsInvalidBookingBeforeStore(t *testing.T) {
	store := &stubBookingStore{}
	handler := newTestCreateBookingHandler(t, store)
	request := httptest.NewRequest(http.MethodPost, "/bookings", strings.NewReader(`{
		"screening_id": 0,
		"requested_seat_ids": [101],
		"customer": {"email": "person@example.com", "phone_number": "+254700000000"}
	}`))
	request.Header.Set("Idempotency-Key", "booking-request-123")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", response.Code, http.StatusBadRequest)
	}
	if store.calls != 0 {
		t.Errorf("store calls = %d, want 0 for invalid booking", store.calls)
	}
}

func TestCreateBookingHandlerMapsBookingErrors(t *testing.T) {
	tests := []struct {
		name       string
		storeErr   error
		wantStatus int
	}{
		{
			name:       "seats unavailable",
			storeErr:   booking.ErrSeatsUnavailable,
			wantStatus: http.StatusConflict,
		},
		{
			name:       "idempotency key conflict",
			storeErr:   booking.ErrIdempotencyKeyConflict,
			wantStatus: http.StatusConflict,
		},
		{
			name:       "unexpected store failure",
			storeErr:   errors.New("database unavailable"),
			wantStatus: http.StatusInternalServerError,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := &stubBookingStore{err: test.storeErr}
			handler := newTestCreateBookingHandler(t, store)
			request := httptest.NewRequest(http.MethodPost, "/bookings", strings.NewReader(validCreateBookingJSON))
			request.Header.Set("Idempotency-Key", "booking-request-123")
			response := httptest.NewRecorder()

			handler.ServeHTTP(response, request)

			if response.Code != test.wantStatus {
				t.Errorf("status = %d, want %d; body = %s", response.Code, test.wantStatus, response.Body)
			}
			if store.calls != 1 {
				t.Errorf("store calls = %d, want 1", store.calls)
			}
		})
	}
}

func TestCreateBookingHandlerRejectsUnsupportedMethod(t *testing.T) {
	store := &stubBookingStore{}
	handler := newTestCreateBookingHandler(t, store)
	request := httptest.NewRequest(http.MethodGet, "/bookings", nil)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want %d", response.Code, http.StatusMethodNotAllowed)
	}
	if got := response.Header().Get("Allow"); got != http.MethodPost {
		t.Errorf("Allow = %q, want POST", got)
	}
	if store.calls != 0 {
		t.Errorf("store calls = %d, want 0 for unsupported method", store.calls)
	}
}

const validCreateBookingJSON = `{
	"screening_id": 42,
	"requested_seat_ids": [202, 101],
	"customer": {
		"email": " person@example.com ",
		"phone_number": " +254700000000 "
	}
}`

type stubBookingStore struct {
	result       booking.Booking
	details      booking.BookingDetails
	err          error
	detailsErr   error
	calls        int
	detailsCalls int
	detailsRef   string
	params       booking.CreateBookingParams
}

func (s *stubBookingStore) Create(_ context.Context, params booking.CreateBookingParams) (booking.Booking, error) {
	s.calls++
	s.params = params
	return s.result, s.err
}

func (s *stubBookingStore) GetDetails(_ context.Context, bookingReference string) (booking.BookingDetails, error) {
	s.detailsCalls++
	s.detailsRef = bookingReference
	return s.details, s.detailsErr
}

func newTestCreateBookingHandler(t *testing.T, store booking.Store) *CreateBookingHandler {
	t.Helper()
	service, err := booking.NewService(store)
	if err != nil {
		t.Fatalf("create booking service: %v", err)
	}
	handler, err := NewCreateBookingHandler(service)
	if err != nil {
		t.Fatalf("create HTTP handler: %v", err)
	}
	return handler
}
