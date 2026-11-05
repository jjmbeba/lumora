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

// BootstrapFirstOwner creates the single active manager account. The caller
// must enforce the external bootstrap gate before calling this method; this
// method independently checks the database's one-owner invariant.
func (s *AuthService) BootstrapFirstOwner(ctx context.Context, request BootstrapOwnerRequest) (BootstrapOwnerResult, error) {
	exists, err := s.store.BootstrapOwnerExists(ctx)
	if err != nil {
		return BootstrapOwnerResult{}, fmt.Errorf("check first-owner bootstrap state: %w", err)
	}
	if exists {
		return BootstrapOwnerResult{}, ErrBootstrapUnavailable
	}

	email := strings.ToLower(strings.TrimSpace(request.Email))
	displayName := strings.TrimSpace(request.DisplayName)
	if email == "" || displayName == "" {
		return BootstrapOwnerResult{}, errors.New("owner email and display name are required")
	}

	if !VerifyTOTP(request.TOTPSecret, request.TOTPCode, time.Now()) {
		return BootstrapOwnerResult{}, ErrInvalidMFA
	}

	passwordHash, err := HashPassword(request.Password)
	if err != nil {
		return BootstrapOwnerResult{}, fmt.Errorf("hash bootstrap owner password: %w", err)
	}

	secretCiphertext, keyVersion, err := s.cipher.Encrypt(email, request.TOTPSecret)
	if err != nil {
		return BootstrapOwnerResult{}, fmt.Errorf("encrypt bootstrap owner MFA secret: %w", err)
	}

	recoveryCodes, recoveryDigests, err := NewRecoveryCodes()
	if err != nil {
		return BootstrapOwnerResult{}, err
	}

	account, err := s.store.CreateBootstrapOwner(ctx, BootstrapOwner{
		Email: email, DisplayName: displayName, PasswordHash: passwordHash,
		MFASecretCiphertext: secretCiphertext, MFAKeyVersion: keyVersion,
		RecoveryCodeDigests: recoveryDigests,
	})
	if err != nil {
		return BootstrapOwnerResult{}, fmt.Errorf("persist bootstrap owner: %w", err)
	}

	return BootstrapOwnerResult{Account: account, RecoveryCodes: recoveryCodes}, nil
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

func (s *AuthService) StartInvitationEnrollment(ctx context.Context, invitationToken string) (InvitationEnrollmentChallenge, error) {
	invitationToken = strings.TrimSpace(invitationToken)
	if !isOpaqueToken(invitationToken) {
		return InvitationEnrollmentChallenge{}, ErrInvalidStaffInvitation
	}

	secret, err := NewTOTPSecret()
	if err != nil {
		return InvitationEnrollmentChallenge{}, err
	}

	invitation, err := s.store.LoadInvitation(ctx, digestToken(invitationToken))
	if err != nil {
		if errors.Is(err, ErrAccountUnavailable) {
			return InvitationEnrollmentChallenge{}, ErrInvalidStaffInvitation
		}
		return InvitationEnrollmentChallenge{}, err
	}

	secretCiphertext, keyVersion, err := s.cipher.Encrypt(invitation.Email, secret)
	if err != nil {
		return InvitationEnrollmentChallenge{}, fmt.Errorf("encrypt staff enrollment TOTP secret: %w", err)
	}

	challengeToken, challengeDigest, err := newOpaqueToken()
	if err != nil {
		return InvitationEnrollmentChallenge{}, err
	}

	expiresAt := time.Now().UTC().Add(loginChallengeLifetime)
	if err := s.store.StartInvitationEnrollment(
		ctx,
		digestToken(invitationToken),
		secretCiphertext,
		keyVersion,
		challengeDigest,
		expiresAt,
	); err != nil {
		return InvitationEnrollmentChallenge{}, err
	}

	return InvitationEnrollmentChallenge{Token: challengeToken, TOTPSecret: secret, ExpiresAt: expiresAt}, nil
}

func (s *AuthService) CompleteInvitationEnrollment(ctx context.Context, invitationToken, challengeToken, password, code string) (InvitationEnrollmentResult, error) {
	invitationToken = strings.TrimSpace(invitationToken)
	challengeToken = strings.TrimSpace(challengeToken)
	if !isOpaqueToken(invitationToken) || !isOpaqueToken(challengeToken) {
		return InvitationEnrollmentResult{}, ErrInvalidStaffEnrollment
	}

	challengeDigest := digestToken(challengeToken)
	challenge, err := s.store.LoadLoginChallenge(ctx, challengeDigest)
	if err != nil {
		if errors.Is(err, ErrInvalidChallenge) {
			return InvitationEnrollmentResult{}, ErrInvalidStaffEnrollment
		}
		return InvitationEnrollmentResult{}, err
	}

	if challenge.Purpose != ChallengePurposeEnrollment || challenge.Account.Status != AccountStatusInvited {
		return InvitationEnrollmentResult{}, ErrInvalidStaffEnrollment
	}

	secret, err := s.cipher.Decrypt(challenge.Account.Email, challenge.MFAKeyVersion, challenge.MFASecretCiphertext)
	if err != nil {
		return InvitationEnrollmentResult{}, fmt.Errorf("decrypt staff enrollment TOTP secret: %w", err)
	}

	if !VerifyTOTP(secret, code, time.Now()) {
		if err := s.store.RecordLoginChallengeFailure(ctx, challengeDigest); err != nil {
			if errors.Is(err, ErrInvalidChallenge) {
				return InvitationEnrollmentResult{}, ErrInvalidStaffEnrollment
			}
			return InvitationEnrollmentResult{}, fmt.Errorf("record invalid staff enrollment MFA attempt: %w", err)
		}
		return InvitationEnrollmentResult{}, ErrInvalidMFA
	}

	passwordHash, err := HashPassword(password)
	if err != nil {
		return InvitationEnrollmentResult{}, err
	}

	recoveryCodes, recoveryDigests, err := NewRecoveryCodes()
	if err != nil {
		return InvitationEnrollmentResult{}, err
	}

	account, err := s.store.CompleteInvitationEnrollment(ctx, digestToken(invitationToken), challengeDigest, passwordHash, recoveryDigests)
	if err != nil {
		return InvitationEnrollmentResult{}, err
	}

	account.MFAEnabled = true
	return InvitationEnrollmentResult{Account: account, RecoveryCodes: recoveryCodes}, nil
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
