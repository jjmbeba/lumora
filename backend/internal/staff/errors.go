package staff

import "errors"

var (
	ErrAccountUnavailable       = errors.New("staff account is unavailable")
	ErrBootstrapUnavailable     = errors.New("staff bootstrap is unavailable")
	ErrInvalidAccountID         = errors.New("invalid staff account ID")
	ErrInvalidAccountList       = errors.New("invalid staff account list options")
	ErrInvalidChallenge         = errors.New("invalid or expired MFA challenge")
	ErrInvalidCredentials       = errors.New("invalid staff credentials")
	ErrInvalidMFA               = errors.New("invalid MFA code")
	ErrInvalidPassword          = errors.New("invalid staff password")
	ErrInvalidSession           = errors.New("invalid or expired staff session")
	ErrInvalidStaffInvitation   = errors.New("invalid staff invitation")
	ErrStaffEmailAlreadyExists  = errors.New("staff account email already exists")
	ErrInvitationEmailClaimLost = errors.New("staff invitation email claim was lost")
	ErrInvitationExpired        = errors.New("staff invitation has expired")
	ErrInvalidStaffEnrollment   = errors.New("invalid staff invitation enrollment")
	ErrCannotDeactivateSelf     = errors.New("cannot deactivate your own staff account")
	ErrLastActiveManager        = errors.New("cannot deactivate the last active manager")
	ErrManagerUnavailable       = errors.New("staff manager is unavailable")
	ErrStaffAccountNotFound     = errors.New("staff account not found")
)
