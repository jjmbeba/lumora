package staff

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	loginChallengeLifetime = 5 * time.Minute
	staffSessionLifetime   = 8 * time.Hour
)

type AuthService struct {
	store  Store
	cipher *MFACipher
}

func NewAuthService(store Store, cipher *MFACipher) (*AuthService, error) {
	if store == nil || cipher == nil {
		return nil, errors.New("staff auth requires a store and MFA cipher")
	}

	return &AuthService{store: store, cipher: cipher}, nil
}

// StartLogin validates the password and returns a short-lived MFA challenge.
// The challenge is not a staff session and must not authorize staff actions.
func (s *AuthService) StartLogin(ctx context.Context, email, password string) (LoginChallenge, error) {
	credentials, err := s.store.FindLoginCredentials(ctx, strings.ToLower(strings.TrimSpace(email)))
	if err != nil {
		if errors.Is(err, ErrAccountUnavailable) {
			verifyDummyPassword(password)
			return LoginChallenge{}, ErrInvalidCredentials
		}
		return LoginChallenge{}, fmt.Errorf("load staff login credentials: %w", err)
	}

	passwordErr := VerifyPassword(credentials.PasswordHash, password)
	if passwordErr != nil || credentials.Account.Status != AccountStatusActive || !credentials.Account.MFAEnabled {
		return LoginChallenge{}, ErrInvalidCredentials
	}

	token, digest, err := newOpaqueToken()
	if err != nil {
		return LoginChallenge{}, err
	}

	expiresAt := time.Now().UTC().Add(loginChallengeLifetime)
	if err := s.store.CreateLoginChallenge(ctx, credentials.Account.ID, ChallengePurposeSignIn, digest, expiresAt); err != nil {
		return LoginChallenge{}, fmt.Errorf("create staff sign-in challenge: %w", err)
	}

	return LoginChallenge{Token: token, Purpose: ChallengePurposeSignIn, ExpiresAt: expiresAt}, nil
}

// CompleteLogin consumes a valid TOTP or one recovery code and atomically
// exchanges the challenge for a persisted session.
func (s *AuthService) CompleteLogin(ctx context.Context, challengeToken, code string) (Identity, Session, error) {
	challengeToken = strings.TrimSpace(challengeToken)
	if !isOpaqueToken(challengeToken) {
		return Identity{}, Session{}, ErrInvalidChallenge
	}

	challengeDigest := digestToken(challengeToken)
	challenge, err := s.store.LoadLoginChallenge(ctx, challengeDigest)
	if err != nil {
		if errors.Is(err, ErrAccountUnavailable) {
			return Identity{}, Session{}, ErrInvalidChallenge
		}
		return Identity{}, Session{}, err
	}

	if challenge.Purpose != ChallengePurposeSignIn || challenge.Account.Status != AccountStatusActive || !challenge.Account.MFAEnabled {
		return Identity{}, Session{}, ErrInvalidChallenge
	}

	secret, err := s.cipher.Decrypt(challenge.Account.Email, challenge.MFAKeyVersion, challenge.MFASecretCiphertext)
	if err != nil {
		return Identity{}, Session{}, fmt.Errorf("decrypt staff MFA secret: %w", err)
	}

	code = strings.TrimSpace(code)
	var recoveryDigest []byte
	validCode := VerifyTOTP(secret, code, time.Now())

	if !validCode && isRecoveryCode(code) {
		recoveryDigest = digestToken(strings.ToUpper(code))
	}
	if !validCode && len(recoveryDigest) == 0 {
		return Identity{}, Session{}, s.recordInvalidMFA(ctx, challengeDigest)
	}

	token, sessionDigest, err := newOpaqueToken()
	if err != nil {
		return Identity{}, Session{}, err
	}

	expiresAt := time.Now().UTC().Add(staffSessionLifetime)
	identity, err := s.store.CompleteLoginChallenge(ctx, challengeDigest, recoveryDigest, sessionDigest, expiresAt)

	if errors.Is(err, ErrInvalidMFA) {
		return Identity{}, Session{}, s.recordInvalidMFA(ctx, challengeDigest)
	}
	if err != nil {
		return Identity{}, Session{}, err
	}

	return identity, Session{Token: token, ExpiresAt: expiresAt}, nil
}

func (s *AuthService) AuthenticateSession(ctx context.Context, token string) (Identity, time.Time, error) {
	if !isOpaqueToken(token) {
		return Identity{}, time.Time{}, ErrInvalidSession
	}

	return s.store.AuthenticateSession(ctx, digestToken(token))
}

func (s *AuthService) RevokeSession(ctx context.Context, token string) error {
	if !isOpaqueToken(token) {
		return ErrInvalidSession
	}

	return s.store.RevokeSession(ctx, digestToken(token))
}

func (s *AuthService) recordInvalidMFA(ctx context.Context, challengeDigest []byte) error {
	if err := s.store.RecordLoginChallengeFailure(ctx, challengeDigest); err != nil {
		if errors.Is(err, ErrInvalidChallenge) {
			return ErrInvalidChallenge
		}
		return fmt.Errorf("record failed staff MFA attempt: %w", err)
	}

	return ErrInvalidMFA
}

func newOpaqueToken() (string, []byte, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", nil, fmt.Errorf("generate staff auth token: %w", err)
	}

	token := base64.RawURLEncoding.EncodeToString(raw)
	digest := digestToken(token)
	clear(raw)

	return token, digest, nil
}

func isOpaqueToken(token string) bool {
	encoding := base64.RawURLEncoding.Strict()
	raw, err := encoding.DecodeString(token)

	if err != nil || len(raw) != 32 || encoding.EncodeToString(raw) != token {
		clear(raw)
		return false
	}
	clear(raw)

	return true
}

func isRecoveryCode(code string) bool {
	if len(code) != 16 {
		return false
	}

	for _, char := range strings.ToUpper(code) {
		if (char < 'A' || char > 'Z') && (char < '2' || char > '7') {
			return false
		}
	}

	return true
}
