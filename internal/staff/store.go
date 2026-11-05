package staff

import (
	"context"
	"time"
)

// LoginCredentials contains private authentication material loaded only for
// password and MFA verification. Callers must not serialize or log it.
type LoginCredentials struct {
	Account             Account
	PasswordHash        string
	MFASecretCiphertext []byte
	MFAKeyVersion       int16
}

// BootstrapOwner contains already-hashed and encrypted credentials. The
// concrete store inserts the account and recovery-code digests atomically.
type BootstrapOwner struct {
	Email               string
	DisplayName         string
	PasswordHash        string
	MFASecretCiphertext []byte
	MFAKeyVersion       int16
	RecoveryCodeDigests [][]byte
}

type BootstrapOwnerRequest struct {
	Email       string
	DisplayName string
	Password    string
	TOTPSecret  string
	TOTPCode    string
}

type BootstrapOwnerResult struct {
	Account       Account
	RecoveryCodes []string
}

type ChallengeCredentials struct {
	Account             Account
	Purpose             ChallengePurpose
	MFASecretCiphertext []byte
	MFAKeyVersion       int16
}

type Store interface {
	BootstrapOwnerExists(context.Context) (bool, error)
	CreateBootstrapOwner(context.Context, BootstrapOwner) (Account, error)
	FindLoginCredentials(context.Context, string) (LoginCredentials, error)
	CreateLoginChallenge(context.Context, int, ChallengePurpose, []byte, time.Time) error
	LoadLoginChallenge(context.Context, []byte) (ChallengeCredentials, error)
	RecordLoginChallengeFailure(context.Context, []byte) error
	CompleteLoginChallenge(context.Context, []byte, []byte, []byte, time.Time) (Identity, error)
	AuthenticateSession(context.Context, []byte) (Identity, time.Time, error)
	RevokeSession(context.Context, []byte) error
	LoadInvitation(context.Context, []byte) (Account, error)
	StartInvitationEnrollment(context.Context, []byte, []byte, int16, []byte, time.Time) error
	CompleteInvitationEnrollment(context.Context, []byte, []byte, string, [][]byte) (Account, error)
}
