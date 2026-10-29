package booking

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"errors"
	"testing"
	"time"
)

type emailVerificationStoreFake struct {
	createResult   EmailVerificationDelivery
	createErr      error
	createParams   CreateEmailVerificationChallengeParams
	createCalls    int
	verifyErr      error
	verifyParams   VerifyEmailCodeParams
	verifyCalls    int
	validateErr    error
	validateParams ValidateBookingTokenParams
	validateCalls  int
}

func (s *emailVerificationStoreFake) CreateEmailVerificationChallenge(_ context.Context, params CreateEmailVerificationChallengeParams) (EmailVerificationDelivery, error) {
	s.createCalls++
	s.createParams = params
	return s.createResult, s.createErr
}

func (s *emailVerificationStoreFake) VerifyEmailCode(_ context.Context, params VerifyEmailCodeParams) error {
	s.verifyCalls++
	s.verifyParams = params
	return s.verifyErr
}

func (s *emailVerificationStoreFake) ValidateBookingToken(_ context.Context, params ValidateBookingTokenParams) error {
	s.validateCalls++
	s.validateParams = params
	return s.validateErr
}

type verificationEmailSenderFake struct {
	email VerificationCodeEmail
	err   error
	calls int
}

func (s *verificationEmailSenderFake) SendVerificationCode(_ context.Context, email VerificationCodeEmail) error {
	s.calls++
	s.email = email
	return s.err
}

func TestNewEmailVerificationServiceRequiresDependenciesAndStrongKey(t *testing.T) {
	sender := &verificationEmailSenderFake{}
	store := &emailVerificationStoreFake{}
	key := make([]byte, minimumHMACKeyBytes)

	tests := []struct {
		name   string
		store  EmailVerificationStore
		sender VerificationEmailSender
		key    []byte
	}{
		{name: "nil store", sender: sender, key: key},
		{name: "nil sender", store: store, key: key},
		{name: "short HMAC key", store: store, sender: sender, key: key[:minimumHMACKeyBytes-1]},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := NewEmailVerificationService(tt.store, tt.sender, tt.key); err == nil {
				t.Fatal("NewEmailVerificationService() error = nil, want error")
			}
		})
	}
}

func TestRequestEmailCodePersistsDigestAndSendsCode(t *testing.T) {
	now := time.Date(2026, time.October, 27, 12, 0, 0, 0, time.UTC)
	store := &emailVerificationStoreFake{
		createResult: EmailVerificationDelivery{
			ShouldSend:     true,
			RecipientEmail: "customer@example.com",
		},
	}
	sender := &verificationEmailSenderFake{}
	service := newTestEmailVerificationService(t, store, sender, now)

	if err := service.RequestEmailCode(context.Background(), " booking-123 "); err != nil {
		t.Fatalf("RequestEmailCode() error = %v", err)
	}

	if store.createCalls != 1 {
		t.Fatalf("CreateEmailVerificationChallenge() calls = %d, want 1", store.createCalls)
	}
	if store.createParams.BookingReference != "booking-123" {
		t.Errorf("BookingReference = %q, want booking-123", store.createParams.BookingReference)
	}
	if len(store.createParams.CodeDigest) != sha256.Size {
		t.Errorf("CodeDigest length = %d, want %d", len(store.createParams.CodeDigest), sha256.Size)
	}
	if !store.createParams.ExpiresAt.Equal(now.Add(emailVerificationCodeTTL)) {
		t.Errorf("ExpiresAt = %v, want %v", store.createParams.ExpiresAt, now.Add(emailVerificationCodeTTL))
	}
	if sender.calls != 1 {
		t.Fatalf("SendVerificationCode() calls = %d, want 1", sender.calls)
	}
	if sender.email.To != "customer@example.com" {
		t.Errorf("email recipient = %q, want customer@example.com", sender.email.To)
	}
	if sender.email.ExpiresIn != emailVerificationCodeTTL {
		t.Errorf("email expiry = %v, want %v", sender.email.ExpiresIn, emailVerificationCodeTTL)
	}
	if !isVerificationCode(sender.email.Code) {
		t.Errorf("sent code %q is not six ASCII digits", sender.email.Code)
	}
	wantDigest := verificationDigestForTest(service.hmacKey, "booking-123", sender.email.Code)
	if !hmac.Equal(store.createParams.CodeDigest, wantDigest) {
		t.Error("stored challenge digest does not match HMAC of the emailed code")
	}
}

func TestRequestEmailCodeDoesNotSendWhenStoreDeclines(t *testing.T) {
	store := &emailVerificationStoreFake{}
	sender := &verificationEmailSenderFake{}
	service := newTestEmailVerificationService(t, store, sender, time.Now())

	if err := service.RequestEmailCode(context.Background(), "unknown-booking"); err != nil {
		t.Fatalf("RequestEmailCode() error = %v, want nil", err)
	}
	if sender.calls != 0 {
		t.Errorf("SendVerificationCode() calls = %d, want 0", sender.calls)
	}
}

func TestRequestEmailCodeClassifiesDeliveryFailure(t *testing.T) {
	store := &emailVerificationStoreFake{
		createResult: EmailVerificationDelivery{ShouldSend: true, RecipientEmail: "customer@example.com"},
	}
	sender := &verificationEmailSenderFake{err: errors.New("provider unavailable")}
	service := newTestEmailVerificationService(t, store, sender, time.Now())

	err := service.RequestEmailCode(context.Background(), "booking-123")
	if !errors.Is(err, ErrVerificationEmailDelivery) {
		t.Fatalf("RequestEmailCode() error = %v, want ErrVerificationEmailDelivery", err)
	}
}

func TestVerifyEmailCodePersistsDigestsAndReturnsToken(t *testing.T) {
	now := time.Date(2026, time.October, 27, 12, 0, 0, 0, time.UTC)
	store := &emailVerificationStoreFake{}
	service := newTestEmailVerificationService(t, store, &verificationEmailSenderFake{}, now)
	request := VerifyEmailCodeRequest{BookingReference: "booking-123", Code: "004281"}

	grant, err := service.VerifyEmailCode(context.Background(), request)
	if err != nil {
		t.Fatalf("VerifyEmailCode() error = %v", err)
	}
	if grant.Token == "" {
		t.Fatal("VerifyEmailCode() returned an empty token")
	}
	if !grant.ExpiresAt.Equal(now.Add(bookingTokenTTL)) {
		t.Errorf("ExpiresAt = %v, want %v", grant.ExpiresAt, now.Add(bookingTokenTTL))
	}
	if store.verifyCalls != 1 {
		t.Fatalf("store VerifyEmailCode() calls = %d, want 1", store.verifyCalls)
	}
	if got := len(store.verifyParams.CodeDigest); got != sha256.Size {
		t.Errorf("CodeDigest length = %d, want %d", got, sha256.Size)
	}
	wantCodeDigest := verificationDigestForTest(service.hmacKey, request.BookingReference, request.Code)
	if !hmac.Equal(store.verifyParams.CodeDigest, wantCodeDigest) {
		t.Error("store did not receive the expected HMAC code digest")
	}
	wantTokenDigest := sha256.Sum256([]byte(grant.Token))
	if !hmac.Equal(store.verifyParams.TokenDigest, wantTokenDigest[:]) {
		t.Error("store did not receive the SHA-256 digest of the returned token")
	}
	if !store.verifyParams.TokenExpiresAt.Equal(grant.ExpiresAt) {
		t.Errorf("stored token expiry = %v, returned expiry = %v", store.verifyParams.TokenExpiresAt, grant.ExpiresAt)
	}
}

func TestVerifyEmailCodeRejectsMalformedCodeBeforeStoreCall(t *testing.T) {
	store := &emailVerificationStoreFake{}
	service := newTestEmailVerificationService(t, store, &verificationEmailSenderFake{}, time.Now())

	_, err := service.VerifyEmailCode(context.Background(), VerifyEmailCodeRequest{
		BookingReference: "booking-123",
		Code:             "12x",
	})
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("VerifyEmailCode() error = %v, want ErrInvalidRequest", err)
	}
	if store.verifyCalls != 0 {
		t.Errorf("store VerifyEmailCode() calls = %d, want 0", store.verifyCalls)
	}
}

func TestVerifyEmailCodeDoesNotReturnTokenWhenStoreRejectsCode(t *testing.T) {
	store := &emailVerificationStoreFake{verifyErr: ErrEmailCodeRejected}
	service := newTestEmailVerificationService(t, store, &verificationEmailSenderFake{}, time.Now())

	grant, err := service.VerifyEmailCode(context.Background(), VerifyEmailCodeRequest{
		BookingReference: "booking-123",
		Code:             "004281",
	})
	if !errors.Is(err, ErrEmailCodeRejected) {
		t.Fatalf("VerifyEmailCode() error = %v, want ErrEmailCodeRejected", err)
	}
	if grant.Token != "" {
		t.Errorf("VerifyEmailCode() returned token %q after rejection", grant.Token)
	}
}

func TestValidateBookingTokenHashesRawToken(t *testing.T) {
	store := &emailVerificationStoreFake{}
	service := newTestEmailVerificationService(t, store, &verificationEmailSenderFake{}, time.Now())
	rawToken := "a-random-booking-token"

	if err := service.ValidateBookingToken(context.Background(), "booking-123", rawToken); err != nil {
		t.Fatalf("ValidateBookingToken() error = %v", err)
	}
	wantDigest := sha256.Sum256([]byte(rawToken))
	if !hmac.Equal(store.validateParams.TokenDigest, wantDigest[:]) {
		t.Error("store did not receive the SHA-256 digest of the raw token")
	}
	if store.validateParams.BookingReference != "booking-123" {
		t.Errorf("BookingReference = %q, want booking-123", store.validateParams.BookingReference)
	}
}

func newTestEmailVerificationService(t *testing.T, store EmailVerificationStore, sender VerificationEmailSender, now time.Time) *EmailVerificationService {
	t.Helper()
	key := []byte("0123456789abcdef0123456789abcdef")
	service, err := NewEmailVerificationService(store, sender, key)
	if err != nil {
		t.Fatalf("NewEmailVerificationService() error = %v", err)
	}
	service.now = func() time.Time { return now }
	return service
}

func verificationDigestForTest(key []byte, bookingReference, code string) []byte {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte("booking-email-verification\x00"))
	_, _ = mac.Write([]byte(bookingReference))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write([]byte(code))
	return mac.Sum(nil)
}
