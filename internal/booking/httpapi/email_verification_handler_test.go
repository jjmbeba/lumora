package httpapi

import (
	"context"
	"encoding/json/v2"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"example.com/cinema-tickets-backend/internal/booking"
)

func TestNewEmailVerificationHandlerRequiresService(t *testing.T) {
	if _, err := NewEmailVerificationHandler(nil); err == nil {
		t.Fatal("NewEmailVerificationHandler(nil) error = nil, want error")
	}
}

func TestRequestCodeReturnsSameGenericResponseWhenNoEmailIsSent(t *testing.T) {
	tests := []struct {
		name  string
		store booking.EmailVerificationStore
		send  booking.VerificationEmailSender
	}{
		{
			name:  "unknown booking or rate limited",
			store: &emailVerificationHTTPStoreFake{},
			send:  &emailVerificationHTTPSenderFake{},
		},
		{
			name: "email provider failure",
			store: &emailVerificationHTTPStoreFake{
				createResult: booking.EmailVerificationDelivery{
					ShouldSend:     true,
					RecipientEmail: "customer@example.com",
				},
			},
			send: &emailVerificationHTTPSenderFake{err: errors.New("provider unavailable")},
		},
	}

	wantBody := emailVerificationChallengeResponse + "\n"
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			handler := newEmailVerificationTestHandler(t, test.store, test.send)
			request := httptest.NewRequest(http.MethodPost, "/bookings/bk-123/email-verification-challenges", nil)
			request.SetPathValue("reference", "bk-123")
			response := httptest.NewRecorder()

			handler.RequestCode(response, request)

			if response.Code != http.StatusAccepted {
				t.Fatalf("status = %d, want %d; body = %s", response.Code, http.StatusAccepted, response.Body)
			}
			if got := response.Body.String(); got != wantBody {
				t.Errorf("response = %q, want generic response %q", got, wantBody)
			}
		})
	}
}

func TestVerifyCodeReturnsBookingToken(t *testing.T) {
	store := &emailVerificationHTTPStoreFake{}
	handler := newEmailVerificationTestHandler(t, store, &emailVerificationHTTPSenderFake{})
	request := httptest.NewRequest(http.MethodPost, "/bookings/bk-123/email-verifications", strings.NewReader(`{"code":"004281"}`))
	request.Header.Set("Content-Type", "application/json")
	request.SetPathValue("reference", "bk-123")
	response := httptest.NewRecorder()

	handler.VerifyCode(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body = %s", response.Code, http.StatusOK, response.Body)
	}
	if got := response.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", got)
	}
	var body bookingTokenResponse
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode token response: %v", err)
	}
	if body.Token == "" {
		t.Error("response token is empty")
	}
	if body.TokenType != "Bearer" {
		t.Errorf("token_type = %q, want Bearer", body.TokenType)
	}
	if !body.ExpiresAt.After(time.Now()) {
		t.Errorf("expires_at = %v, want a future time", body.ExpiresAt)
	}
	if store.verifyCalls != 1 {
		t.Errorf("store verification calls = %d, want 1", store.verifyCalls)
	}
	if store.verifyParams.BookingReference != "bk-123" {
		t.Errorf("store booking reference = %q, want bk-123", store.verifyParams.BookingReference)
	}
}

func TestVerifyCodeRejectsMalformedJSONAndInvalidCode(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		wantStatus int
	}{
		{name: "malformed JSON", body: `{"code":`, wantStatus: http.StatusBadRequest},
		{name: "invalid code", body: `{"code":"12x"}`, wantStatus: http.StatusBadRequest},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := &emailVerificationHTTPStoreFake{}
			handler := newEmailVerificationTestHandler(t, store, &emailVerificationHTTPSenderFake{})
			request := httptest.NewRequest(http.MethodPost, "/bookings/bk-123/email-verifications", strings.NewReader(test.body))
			request.SetPathValue("reference", "bk-123")
			response := httptest.NewRecorder()

			handler.VerifyCode(response, request)

			if response.Code != test.wantStatus {
				t.Errorf("status = %d, want %d", response.Code, test.wantStatus)
			}
			if store.verifyCalls != 0 {
				t.Errorf("store verification calls = %d, want 0", store.verifyCalls)
			}
		})
	}
}

func TestVerifyCodeMapsRejectedCodeToUnauthorized(t *testing.T) {
	store := &emailVerificationHTTPStoreFake{verifyErr: booking.ErrEmailCodeRejected}
	handler := newEmailVerificationTestHandler(t, store, &emailVerificationHTTPSenderFake{})
	request := httptest.NewRequest(http.MethodPost, "/bookings/bk-123/email-verifications", strings.NewReader(`{"code":"004281"}`))
	request.SetPathValue("reference", "bk-123")
	response := httptest.NewRecorder()

	handler.VerifyCode(response, request)

	if response.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d; body = %s", response.Code, http.StatusUnauthorized, response.Body)
	}
}

type emailVerificationHTTPStoreFake struct {
	createResult   booking.EmailVerificationDelivery
	createErr      error
	verifyErr      error
	validateErr    error
	verifyCalls    int
	verifyParams   booking.VerifyEmailCodeParams
	validateCalls  int
	validateParams booking.ValidateBookingTokenParams
}

func (s *emailVerificationHTTPStoreFake) CreateEmailVerificationChallenge(_ context.Context, _ booking.CreateEmailVerificationChallengeParams) (booking.EmailVerificationDelivery, error) {
	return s.createResult, s.createErr
}

func (s *emailVerificationHTTPStoreFake) VerifyEmailCode(_ context.Context, params booking.VerifyEmailCodeParams) error {
	s.verifyCalls++
	s.verifyParams = params
	return s.verifyErr
}

func (s *emailVerificationHTTPStoreFake) ValidateBookingToken(_ context.Context, params booking.ValidateBookingTokenParams) error {
	s.validateCalls++
	s.validateParams = params
	return s.validateErr
}

type emailVerificationHTTPSenderFake struct {
	err error
}

func (s *emailVerificationHTTPSenderFake) SendVerificationCode(context.Context, booking.VerificationCodeEmail) error {
	return s.err
}

func newEmailVerificationTestHandler(t *testing.T, store booking.EmailVerificationStore, sender booking.VerificationEmailSender) *EmailVerificationHandler {
	t.Helper()
	service, err := booking.NewEmailVerificationService(store, sender, []byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatalf("create email verification service: %v", err)
	}
	handler, err := NewEmailVerificationHandler(service)
	if err != nil {
		t.Fatalf("create email verification HTTP handler: %v", err)
	}
	return handler
}
