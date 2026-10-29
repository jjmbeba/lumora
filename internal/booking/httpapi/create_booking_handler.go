package httpapi

import (
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"

	"example.com/cinema-tickets-backend/internal/booking"
)

type createBookingRequest struct {
	ScreeningID      int      `json:"screening_id"`
	RequestedSeatIDs []int    `json:"requested_seat_ids"`
	Customer         customer `json:"customer"`
}

type customer struct {
	Email       string `json:"email"`
	PhoneNumber string `json:"phone_number"`
}

type CreateBookingHandler struct {
	service *booking.Service
}

const maxBodyBytes = 1 << 20

func NewCreateBookingHandler(service *booking.Service) (*CreateBookingHandler, error) {
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

	request := booking.CreateBookingRequest{
		ScreeningID:      body.ScreeningID,
		RequestedSeatIDs: body.RequestedSeatIDs,
		Customer: booking.CustomerDetails{
			Email:       body.Customer.Email,
			PhoneNumber: body.Customer.PhoneNumber,
		},
		IdempotencyKey: r.Header.Get("Idempotency-Key"),
	}

	newBooking, err := h.service.Create(r.Context(), request)
	if err != nil {
		if errors.Is(err, booking.ErrInvalidRequest) {
			http.Error(w, "invalid payload", http.StatusBadRequest)
		} else if errors.Is(err, booking.ErrSeatsUnavailable) {
			http.Error(w, "some of the requested seats are unavailable", http.StatusConflict)
		} else if errors.Is(err, booking.ErrIdempotencyKeyConflict) {
			http.Error(w, "idempotency key already in use by another booking", http.StatusConflict)
		} else {
			http.Error(w, "something went wrong", http.StatusInternalServerError)
		}
		return
	}

	resp, err := json.Marshal(newBooking)
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
