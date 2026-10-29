package httpapi

import (
	"encoding/json/v2"
	"errors"
	"fmt"
	"net/http"

	"example.com/cinema-tickets-backend/internal/booking"
	"example.com/cinema-tickets-backend/internal/checkout"
	"example.com/cinema-tickets-backend/internal/payment"
)

type PaymentInitializationStatusHandler struct {
	checkout     *checkout.Service
	verification *booking.EmailVerificationService
}

type paymentInitializationStatusResponse struct {
	Status           string `json:"status"`
	PaymentMethod    string `json:"payment_method"`
	AuthorizationURL string `json:"authorization_url,omitempty"`
}

func NewPaymentInitializationStatusHandler(checkout *checkout.Service, verification *booking.EmailVerificationService) (*PaymentInitializationStatusHandler, error) {
	if checkout == nil {
		return nil, fmt.Errorf("checkout service must not be nil")
	}
	if verification == nil {
		return nil, fmt.Errorf("email verification service must not be nil")
	}

	return &PaymentInitializationStatusHandler{checkout: checkout, verification: verification}, nil
}

func (h *PaymentInitializationStatusHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
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

	initialization, err := h.checkout.GetLatestInitialization(r.Context(), bookingReference)
	
	switch {
	case errors.Is(err, payment.ErrInvalidInitializationRequest):
		http.Error(w, "invalid booking reference", http.StatusBadRequest)
		return
	case errors.Is(err, payment.ErrInitializationNotFound):
		http.Error(w, "payment initialization not found", http.StatusNotFound)
		return
	case err != nil:
		http.Error(w, "something went wrong", http.StatusInternalServerError)
		return
	}

	status, ok := initializationStatusName(initialization.Status)
	if !ok {
		http.Error(w, "something went wrong", http.StatusInternalServerError)
		return
	}
	
	method, ok := paymentMethodName(initialization.PaymentMethod)
	if !ok {
		http.Error(w, "something went wrong", http.StatusInternalServerError)
		return
	}

	body, err := json.Marshal(paymentInitializationStatusResponse{
		Status:           status,
		PaymentMethod:    method,
		AuthorizationURL: initialization.AuthorizationURL,
	})
	if err != nil {
		http.Error(w, "something went wrong", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}
