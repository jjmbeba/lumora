package httpapi

import (
	"encoding/json/v2"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"example.com/cinema-tickets-backend/internal/booking"
)

type BookingDetailsHandler struct {
	bookings     *booking.Service
	verification *booking.EmailVerificationService
}

func NewBookingDetailsHandler(bookings *booking.Service, verification *booking.EmailVerificationService) (*BookingDetailsHandler, error) {
	if bookings == nil {
		return nil, fmt.Errorf("booking service must not be nil")
	}
	if verification == nil {
		return nil, fmt.Errorf("email verification service must not be nil")
	}

	return &BookingDetailsHandler{bookings: bookings, verification: verification}, nil
}

func (h *BookingDetailsHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	bookingReference := r.PathValue("reference")
	token, ok := bearerToken(r.Header.Get("Authorization"))
	if !ok {
		http.Error(w, "valid booking access token required", http.StatusUnauthorized)
		return
	}

	if err := h.verification.ValidateBookingToken(r.Context(), bookingReference, token); err != nil {
		if errors.Is(err, booking.ErrBookingTokenInvalid) {
			http.Error(w, "valid booking access token required", http.StatusUnauthorized)
		} else {
			http.Error(w, "something went wrong", http.StatusInternalServerError)
		}
		return
	}

	details, err := h.bookings.GetDetails(r.Context(), bookingReference)
	if errors.Is(err, booking.ErrInvalidRequest) {
		http.Error(w, "invalid booking reference", http.StatusBadRequest)
		return
	}
	if errors.Is(err, booking.ErrBookingNotFound) {
		http.Error(w, "booking not found", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "something went wrong", http.StatusInternalServerError)
		return
	}

	body, err := json.Marshal(details)
	if err != nil {
		http.Error(w, "something went wrong", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

func bearerToken(header string) (string, bool) {
	parts := strings.Fields(header)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return "", false
	}

	return parts[1], true
}
