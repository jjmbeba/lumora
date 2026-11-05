package staff

import "time"

type Role string

const (
	RoleManager Role = "MANAGER"
	RoleScanner Role = "SCANNER"
)

type AccountStatus string

const (
	AccountStatusInvited  AccountStatus = "INVITED"
	AccountStatusActive   AccountStatus = "ACTIVE"
	AccountStatusDisabled AccountStatus = "DISABLED"
)

type ChallengePurpose string

const (
	ChallengePurposeSignIn     ChallengePurpose = "MFA_SIGN_IN"
	ChallengePurposeEnrollment ChallengePurpose = "MFA_ENROLLMENT"
)

// Account is the public staff identity. Credential and TOTP material belongs
// to the authentication store and is never exposed through this value.
type Account struct {
	ID          int
	Email       string
	DisplayName string
	Role        Role
	Status      AccountStatus
	MFAEnabled  bool
}

// Identity is the authenticated staff principal attached to a request.
type Identity struct {
	AccountID   int
	Email       string
	DisplayName string
	Role        Role
}

// LoginChallenge is temporary proof that the password check succeeded. It
// does not authorize staff-only operations until MFA is completed.
type LoginChallenge struct {
	Token     string
	Purpose   ChallengePurpose
	ExpiresAt time.Time
}

// Session contains the raw opaque token once, for delivery in an HttpOnly
// cookie. Persistence stores only its digest.
type Session struct {
	Token     string
	ExpiresAt time.Time
}
