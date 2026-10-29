package httpapi

import (
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"

	"example.com/cinema-tickets-backend/internal/booking"
	"example.com/cinema-tickets-backend/internal/checkout"
	"example.com/cinema-tickets-backend/internal/payment"
)

type createBookingRequest struct {
	ScreeningID      int      `json:"screening_id"`
	RequestedSeatIDs []int    `json:"requested_seat_ids"`
	Customer         customer `json:"customer"`
	PaymentMethod    string   `json:"payment_method"`
}

type customer struct {
	Email       string `json:"email"`
	PhoneNumber string `json:"phone_number"`
}

type CreateBookingHandler struct {
	service checkout.Starter
}

type createBookingResponse struct {
	Booking               booking.Booking               `json:"booking"`
	PaymentInitialization paymentInitializationResponse `json:"payment_initialization"`
}

type paymentInitializationResponse struct {
	Status           string `json:"status"`
	PaymentMethod    string `json:"payment_method"`
	AuthorizationURL string `json:"authorization_url,omitempty"`
}

const maxBodyBytes = 1 << 20

func NewCreateBookingHandler(service checkout.Starter) (*CreateBookingHandler, error) {
	if service == nil {
		return &CreateBookingHandler{}, fmt.Errorf("service must not be nil")
	}

	return &CreateBookingHandler{
		service: service,
	}, nil
}

func (h *CreateBookingHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Add("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)

		return
	}

	rawBody, ok := h.readBody(w, r)
	if !ok {
		return
	}

	var body createBookingRequest

	if err := json.Unmarshal(rawBody, &body); err != nil {
		http.Error(w, "invalid payload", http.StatusBadRequest)

		return
	}

	var method payment.PaymentMethod
	switch strings.ToUpper(strings.TrimSpace(body.PaymentMethod)) {
	case "CARD":
		method = payment.PaymentMethodCard
	case "MPESA":
		method = payment.PaymentMethodMpesa
	default:
		http.Error(w, "payment_method must be CARD or MPESA", http.StatusBadRequest)
		return
	}

	result, err := h.service.Start(r.Context(), checkout.StartRequest{
		Booking: booking.CreateBookingRequest{
			ScreeningID:      body.ScreeningID,
			RequestedSeatIDs: body.RequestedSeatIDs,
			Customer: booking.CustomerDetails{
				Email:       body.Customer.Email,
				PhoneNumber: body.Customer.PhoneNumber,
			},
			IdempotencyKey: r.Header.Get("Idempotency-Key"),
		},
		PaymentMethod: method,
	})
	if err != nil {
		writeCreateCheckoutError(w, err)
		return
	}

	status, ok := initializationStatusName(result.Initialization.Status)
	if !ok {
		http.Error(w, "something went wrong", http.StatusInternalServerError)

		return
	}
	methodName, ok := paymentMethodName(result.Initialization.PaymentMethod)
	if !ok {
		http.Error(w, "something went wrong", http.StatusInternalServerError)

		return
	}

	resp, err := json.Marshal(createBookingResponse{
		Booking: result.Booking,
		PaymentInitialization: paymentInitializationResponse{
			Status:           status,
			PaymentMethod:    methodName,
			AuthorizationURL: result.Initialization.AuthorizationURL,
		},
	})
	if err != nil {
		http.Error(w, "something went wrong", http.StatusInternalServerError)

		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)

	if _, err := w.Write(resp); err != nil {
		log.Printf("failed to write booking response: %v", err)
	}
}

func writeCreateCheckoutError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, booking.ErrInvalidRequest), errors.Is(err, checkout.ErrInvalidPaymentMethod), errors.Is(err, payment.ErrInvalidInitializationRequest):
		http.Error(w, "invalid booking or payment request", http.StatusBadRequest)
	case errors.Is(err, booking.ErrSeatsUnavailable), errors.Is(err, booking.ErrIdempotencyKeyConflict), errors.Is(err, payment.ErrInitializationIdempotencyConflict), errors.Is(err, payment.ErrBookingNotEligibleForPayment), errors.Is(err, payment.ErrPaymentAttemptNotRetryable):
		http.Error(w, "booking or payment request conflicts with current state", http.StatusConflict)
	case errors.Is(err, payment.ErrBookingNotFound):
		http.Error(w, "booking not found", http.StatusNotFound)
	default:
		http.Error(w, "something went wrong", http.StatusInternalServerError)
	}
}

func initializationStatusName(status payment.InitializationStatus) (string, bool) {
	switch status {
	case payment.InitializationStatusQueued:
		return "QUEUED", true
	case payment.InitializationStatusSubmitting:
		return "SUBMITTING", true
	case payment.InitializationStatusReady:
		return "READY", true
	case payment.InitializationStatusPaid:
		return "PAID", true
	case payment.InitializationStatusFailed:
		return "FAILED", true
	case payment.InitializationStatusRejected:
		return "REJECTED", true
	case payment.InitializationStatusUnknown:
		return "UNKNOWN", true
	case payment.InitializationStatusCancelled:
		return "CANCELLED", true
	case payment.InitializationStatusReviewRequired:
		return "REVIEW_REQUIRED", true
	default:
		return "", false
	}
}

func paymentMethodName(method payment.PaymentMethod) (string, bool) {
	switch method {
	case payment.PaymentMethodCard:
		return "CARD", true
	case payment.PaymentMethodMpesa:
		return "MPESA", true
	default:
		return "", false
	}
}

func (h *CreateBookingHandler) readBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)

	rawBody, err := io.ReadAll(r.Body)
	if err != nil {
		if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
			http.Error(w, "payload too large", http.StatusRequestEntityTooLarge)
		} else {
			http.Error(w, "unable to read payload", http.StatusBadRequest)
		}

		return nil, false
	}

	if len(rawBody) == 0 {
		http.Error(w, "empty payload", http.StatusBadRequest)
		return nil, false
	}

	return rawBody, true
}
