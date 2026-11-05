package staff

import "errors"

var (
	ErrAccountUnavailable  = errors.New("staff account is unavailable")
	ErrBootstrapUnavailable = errors.New("staff bootstrap is unavailable")
	ErrInvalidChallenge    = errors.New("invalid or expired MFA challenge")
	ErrInvalidCredentials  = errors.New("invalid staff credentials")
	ErrInvalidMFA          = errors.New("invalid MFA code")
	ErrInvalidSession      = errors.New("invalid or expired staff session")
)
