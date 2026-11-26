package identity

// Error is a failure the client may see. Code is stable; the HTTP layer maps
// it to a status.
type Error struct {
	Code string
	Msg  string
}

func (e *Error) Error() string { return e.Msg }

// Errors returned by the service.
var (
	ErrUnauthenticated   = &Error{"unauthenticated", "Sign in to continue."}
	ErrForbidden         = &Error{"forbidden", "You don't have access to this."}
	ErrNotFound          = &Error{"not_found", "Not found."}
	ErrSetupUnavailable  = &Error{"setup_unavailable", "Setup is already complete."}
	ErrSetupNotEnabled   = &Error{"setup_not_configured", "No admin setup token is configured on the server."}
	ErrSetupToken        = &Error{"setup_token_invalid", "The setup token is wrong."}
	ErrCeremonyInvalid   = &Error{"ceremony_expired", "That sign-in attempt expired. Try again."}
	ErrCredentialInvalid = &Error{"passkey_rejected", "That passkey wasn't accepted."}
	ErrAccountDisabled   = &Error{"account_disabled", "This account is disabled."}
	ErrInviteInvalid     = &Error{"invite_invalid", "This invite is invalid, used or expired."}
	ErrLastCredential    = &Error{"last_passkey", "You can't remove your only passkey."}
	ErrLastAdmin         = &Error{"last_admin", "At least one active admin must remain."}
	ErrNotPending        = &Error{"not_pending", "This user isn't waiting for approval."}
	ErrUserPending       = &Error{"user_pending", "Approve or reject this user first."}
	ErrNoHousehold       = &Error{"no_household", "You aren't in a household yet."}
	ErrAlreadyMember     = &Error{"already_member", "You're already in this household."}
)

// ValidationError is a bad input field.
type ValidationError struct {
	Field string
	Msg   string
}

func (e *ValidationError) Error() string { return e.Field + ": " + e.Msg }
