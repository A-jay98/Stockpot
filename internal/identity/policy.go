package identity

import (
	"context"
	"errors"
	"slices"

	"github.com/google/uuid"
)

// User statuses, system roles and household roles.
const (
	StatusPending  = "pending"
	StatusActive   = "active"
	StatusDisabled = "disabled"

	SystemAdmin = "admin"
	SystemUser  = "user"

	HouseholdOwner  = "owner"
	HouseholdMember = "member"
)

// Route roles, as listed in an operation's x-roles. A request may call the
// operation when it satisfies any one of them.
const (
	RolePublic         = "public"          // anyone
	RoleSignedIn       = "signed_in"       // any session, including pending users
	RoleUser           = "user"            // an active (approved) user
	RoleHouseholdOwner = "household_owner" // active user who owns their active household
	RoleAdmin          = "admin"           // active system admin
)

// RouteRoles lists every role x-roles may name.
var RouteRoles = []string{RolePublic, RoleSignedIn, RoleUser, RoleHouseholdOwner, RoleAdmin}

// Principal is the signed-in caller, loaded from their session.
type Principal struct {
	UserID      uuid.UUID
	SessionID   uuid.UUID
	DisplayName string
	Status      string
	SystemRole  string
	// HouseholdID is the active household; uuid.Nil when the user has none.
	HouseholdID   uuid.UUID
	HouseholdRole string
}

// Active reports an approved, enabled user.
func (p *Principal) Active() bool { return p != nil && p.Status == StatusActive }

// IsAdmin reports an active system admin.
func (p *Principal) IsAdmin() bool { return p.Active() && p.SystemRole == SystemAdmin }

// HasRole reports whether p satisfies one route role. p may be nil.
func (p *Principal) HasRole(role string) bool {
	switch role {
	case RolePublic:
		return true
	case RoleSignedIn:
		return p != nil
	case RoleUser:
		return p.Active()
	case RoleHouseholdOwner:
		return p.Active() && p.HouseholdID != uuid.Nil && p.HouseholdRole == HouseholdOwner
	case RoleAdmin:
		return p.IsAdmin()
	}
	return false
}

// Allow decides a route: nil, ErrUnauthenticated (no session) or
// ErrForbidden.
func Allow(p *Principal, roles []string) error {
	if slices.ContainsFunc(roles, p.HasRole) {
		return nil
	}
	if p == nil {
		return ErrUnauthenticated
	}
	return ErrForbidden
}

// CanManageHousehold reports whether p may invite to and manage household:
// admins anywhere, owners in their own households.
func CanManageHousehold(p *Principal, role string) bool {
	return p.IsAdmin() || (p.Active() && role == HouseholdOwner)
}

type principalKey struct{}

// WithPrincipal returns ctx carrying p.
func WithPrincipal(ctx context.Context, p *Principal) context.Context {
	return context.WithValue(ctx, principalKey{}, p)
}

// PrincipalFrom returns the caller, or nil when the request has no session.
func PrincipalFrom(ctx context.Context) *Principal {
	p, _ := ctx.Value(principalKey{}).(*Principal)
	return p
}

// HouseholdID is the caller's active household. Every household-scoped
// query takes its household id from here, never from the request body.
func HouseholdID(ctx context.Context) (uuid.UUID, error) {
	p := PrincipalFrom(ctx)
	switch {
	case p == nil:
		return uuid.Nil, ErrUnauthenticated
	case !p.Active():
		return uuid.Nil, ErrForbidden
	case p.HouseholdID == uuid.Nil:
		return uuid.Nil, ErrNoHousehold
	}
	return p.HouseholdID, nil
}

// requireActive returns the caller when they are an active user.
func requireActive(ctx context.Context) (*Principal, error) {
	p := PrincipalFrom(ctx)
	if p == nil {
		return nil, ErrUnauthenticated
	}
	if !p.Active() {
		return nil, ErrForbidden
	}
	return p, nil
}

func requireAdmin(ctx context.Context) (*Principal, error) {
	p, err := requireActive(ctx)
	if err != nil {
		return nil, err
	}
	if !p.IsAdmin() {
		return nil, ErrForbidden
	}
	return p, nil
}

// IsCode reports whether err is an identity *Error with code.
func IsCode(err error, code string) bool {
	var e *Error
	return errors.As(err, &e) && e.Code == code
}
