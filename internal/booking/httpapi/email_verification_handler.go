package httpapi

import (
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"example.com/cinema-tickets-backend/internal/booking"
)

const emailVerificationChallengeResponse = "If the booking is eligible, a verification code will be sent."

type EmailVerificationHandler struct {
	service *booking.EmailVerificationService
}

type verifyEmailCodeRequest struct {
	Code string `json:"code"`
}

type bookingTokenResponse struct {
	Token     string    `json:"token"`
	TokenType string    `json:"token_type"`
	ExpiresAt time.Time `json:"expires_at"`
}

func NewEmailVerificationHandler(service *booking.EmailVerificationService) (*EmailVerificationHandler, error) {
	if service == nil {
		return nil, fmt.Errorf("email verification service must not be nil")
	}

	return &EmailVerificationHandler{service: service}, nil
}

func (h *EmailVerificationHandler) RequestCode(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	err := h.service.RequestEmailCode(r.Context(), r.PathValue("reference"))
	if errors.Is(err, booking.ErrVerificationEmailDelivery) {
		// Keep this response identical to the unknown-booking and send-limit cases.
		writeEmailVerificationChallengeResponse(w)
		return
	}

	if errors.Is(err, booking.ErrInvalidRequest) {
		http.Error(w, "invalid booking reference", http.StatusBadRequest)
		return
	}

	if err != nil {
		http.Error(w, "something went wrong", http.StatusInternalServerError)
		return
	}

	writeEmailVerificationChallengeResponse(w)
}

func (h *EmailVerificationHandler) VerifyCode(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)

		return
	}

	body, ok := readEmailVerificationBody(w, r)
	if !ok {
		return
	}

	var request verifyEmailCodeRequest
	if err := json.Unmarshal(body, &request); err != nil {
		http.Error(w, "invalid payload", http.StatusBadRequest)
		return
	}

	bookingToken, err := h.service.VerifyEmailCode(r.Context(), booking.VerifyEmailCodeRequest{
		BookingReference: r.PathValue("reference"),
		Code:             strings.TrimSpace(request.Code),
	})

	if errors.Is(err, booking.ErrInvalidRequest) {
		http.Error(w, "invalid payload", http.StatusBadRequest)
		return
	}

	if errors.Is(err, booking.ErrEmailCodeRejected) {
		http.Error(w, "verification code is invalid or expired", http.StatusUnauthorized)
		return
	}

	if err != nil {
		http.Error(w, "something went wrong", http.StatusInternalServerError)
		return
	}

	writeEmailVerificationJSON(w, http.StatusOK, bookingTokenResponse{
		Token:     bookingToken.Token,
		TokenType: "Bearer",
		ExpiresAt: bookingToken.ExpiresAt,
	})
}

func writeEmailVerificationChallengeResponse(w http.ResponseWriter) {
	http.Error(w, emailVerificationChallengeResponse, http.StatusAccepted)
}

func writeEmailVerificationJSON(w http.ResponseWriter, status int, response any) {
	body, err := json.Marshal(response)
	if err != nil {
		http.Error(w, "something went wrong", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

func readEmailVerificationBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)

	body, err := io.ReadAll(r.Body)

	if err != nil {
		if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
			http.Error(w, "payload too large", http.StatusRequestEntityTooLarge)
		} else {
			http.Error(w, "unable to read payload", http.StatusBadRequest)
		}
		return nil, false
	}

	if len(body) == 0 {
		http.Error(w, "empty payload", http.StatusBadRequest)
		return nil, false
	}

	return body, true
}
