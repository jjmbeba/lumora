package booking

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"
)

const (
	emailVerificationCodeTTL = 10 * time.Minute
	bookingTokenTTL          = 15 * time.Minute
	verificationCodeDigits   = 6
	minimumHMACKeyBytes      = 32
)

type VerificationCodeEmail struct {
	To        string
	Code      string
	ExpiresIn time.Duration
}

type VerificationEmailSender interface {
	SendVerificationCode(ctx context.Context, email VerificationCodeEmail) error
}

type EmailVerificationService struct {
	store   EmailVerificationStore
	sender  VerificationEmailSender
	hmacKey []byte
	now     func() time.Time
}

func NewEmailVerificationService(store EmailVerificationStore, sender VerificationEmailSender, hmacKey []byte) (*EmailVerificationService, error) {
	if store == nil {
		return nil, errors.New("email verification store is required")
	}

	if sender == nil {
		return nil, errors.New("verification email sender is required")
	}

	if len(hmacKey) < minimumHMACKeyBytes {
		return nil, fmt.Errorf("email verification HMAC key must be at least %d bytes", minimumHMACKeyBytes)
	}

	return &EmailVerificationService{
		store:   store,
		sender:  sender,
		hmacKey: append([]byte(nil), hmacKey...),
		now:     time.Now,
	}, nil
}

func (s *EmailVerificationService) RequestEmailCode(ctx context.Context, bookingReference string) error {
	bookingReference = strings.TrimSpace(bookingReference)
	if bookingReference == "" {
		return ErrInvalidRequest
	}

	code, err := newVerificationCode()
	if err != nil {
		return fmt.Errorf("generate email verification code: %w", err)
	}

	params := CreateEmailVerificationChallengeParams{
		BookingReference: bookingReference,
		CodeDigest:       s.codeDigest(bookingReference, code),
		ExpiresAt:        s.now().UTC().Add(emailVerificationCodeTTL),
	}

	delivery, err := s.store.CreateEmailVerificationChallenge(ctx, params)
	if err != nil {
		return fmt.Errorf("create email verification challenge: %w", err)
	}

	if !delivery.ShouldSend {
		return nil
	}

	if strings.TrimSpace(delivery.RecipientEmail) == "" {
		return errors.New("email verification store returned an empty recipient for an eligible challenge")
	}

	if err := s.sender.SendVerificationCode(ctx, VerificationCodeEmail{
		To:        delivery.RecipientEmail,
		Code:      code,
		ExpiresIn: emailVerificationCodeTTL,
	}); err != nil {
		return fmt.Errorf("%w: %v", ErrVerificationEmailDelivery, err)
	}

	return nil
}

func (s *EmailVerificationService) VerifyEmailCode(ctx context.Context, request VerifyEmailCodeRequest) (BookingToken, error) {
	request.BookingReference = strings.TrimSpace(request.BookingReference)
	if request.BookingReference == "" || !isVerificationCode(request.Code) {
		return BookingToken{}, ErrInvalidRequest
	}

	rawToken, tokenDigest, err := newBookingToken()
	if err != nil {
		return BookingToken{}, fmt.Errorf("generate booking token: %w", err)
	}

	tokenExpiresAt := s.now().UTC().Add(bookingTokenTTL)

	params := VerifyEmailCodeParams{
		BookingReference: request.BookingReference,
		CodeDigest:       s.codeDigest(request.BookingReference, request.Code),
		TokenDigest:      tokenDigest,
		TokenExpiresAt:   tokenExpiresAt,
	}
	if err := s.store.VerifyEmailCode(ctx, params); err != nil {
		return BookingToken{}, fmt.Errorf("verify booking email code: %w", err)
	}

	return BookingToken{Token: rawToken, ExpiresAt: tokenExpiresAt}, nil
}

func (s *EmailVerificationService) ValidateBookingToken(ctx context.Context, bookingReference, rawToken string) error {
	bookingReference = strings.TrimSpace(bookingReference)
	if bookingReference == "" || rawToken == "" {
		return ErrBookingTokenInvalid
	}

	digest := sha256.Sum256([]byte(rawToken))
	params := ValidateBookingTokenParams{
		BookingReference: bookingReference,
		TokenDigest:      digest[:],
	}

	if err := s.store.ValidateBookingToken(ctx, params); err != nil {
		return fmt.Errorf("validate booking token: %w", err)
	}

	return nil
}

func (s *EmailVerificationService) codeDigest(bookingReference, code string) []byte {
	mac := hmac.New(sha256.New, s.hmacKey)

	_, _ = mac.Write([]byte("booking-email-verification\x00"))
	_, _ = mac.Write([]byte(bookingReference))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write([]byte(code))

	return mac.Sum(nil)
}

func newVerificationCode() (string, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(1_000_000))
	if err != nil {
		return "", err
	}

	return fmt.Sprintf("%0*d", verificationCodeDigits, n.Int64()), nil
}

func isVerificationCode(code string) bool {
	if len(code) != verificationCodeDigits {
		return false
	}

	for _, digit := range code {
		if digit < '0' || digit > '9' {
			return false
		}
	}

	return true
}

func newBookingToken() (raw string, digest []byte, err error) {
	randomBytes := make([]byte, 32)
	if _, err := rand.Read(randomBytes); err != nil {
		return "", nil, err
	}

	raw = base64.RawURLEncoding.EncodeToString(randomBytes)
	hash := sha256.Sum256([]byte(raw))

	return raw, hash[:], nil
}
