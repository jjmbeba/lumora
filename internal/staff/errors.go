package staff

import "errors"

var (
	ErrAccountUnavailable   = errors.New("staff account is unavailable")
	ErrBootstrapUnavailable = errors.New("staff bootstrap is unavailable")
	ErrInvalidAccountID     = errors.New("invalid staff account ID")
	ErrInvalidChallenge     = errors.New("invalid or expired MFA challenge")
	ErrInvalidCredentials   = errors.New("invalid staff credentials")
	ErrInvalidMFA           = errors.New("invalid MFA code")
	ErrInvalidSession       = errors.New("invalid or expired staff session")
	ErrLastActiveManager    = errors.New("cannot deactivate the last active manager")
	ErrManagerUnavailable   = errors.New("staff manager is unavailable")
	ErrStaffAccountNotFound = errors.New("staff account not found")
)
