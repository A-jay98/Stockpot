package identity

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/a-jay98/stockpot/internal/audit"
	"github.com/a-jay98/stockpot/internal/db/dbq"
)

// Membership is one of a user's households.
type Membership struct {
	HouseholdID uuid.UUID
	Name        string
	Role        string
}

// UserDetail is a user with their households.
type UserDetail struct {
	dbq.AppUser
	Households []Membership
}

// Me is the caller's account.
type Me struct {
	UserDetail
	ActiveHouseholdID uuid.UUID
}

// Me returns the caller's account and households.
func (s *Service) Me(ctx context.Context) (Me, error) {
	p := PrincipalFrom(ctx)
	if p == nil {
		return Me{}, ErrUnauthenticated
	}
	d, err := s.userDetail(ctx, s.q, p.UserID)
	if err != nil {
		return Me{}, err
	}
	return Me{UserDetail: d, ActiveHouseholdID: p.HouseholdID}, nil
}

// SwitchHousehold makes household the caller's active one for this session.
func (s *Service) SwitchHousehold(ctx context.Context, household uuid.UUID) (Me, error) {
	p, err := requireActive(ctx)
	if err != nil {
		return Me{}, err
	}
	m, err := s.q.GetMembership(ctx, dbq.GetMembershipParams{UserID: p.UserID, HouseholdID: household})
	if errors.Is(err, pgx.ErrNoRows) {
		return Me{}, ErrNotFound
	}
	if err != nil {
		return Me{}, err
	}
	if err := s.q.SetSessionHousehold(ctx, dbq.SetSessionHouseholdParams{ID: p.SessionID, HouseholdID: &household}); err != nil {
		return Me{}, err
	}
	p.HouseholdID, p.HouseholdRole = m.HouseholdID, m.Role
	return s.Me(ctx)
}

// UpdateMe changes the caller's display name.
func (s *Service) UpdateMe(ctx context.Context, displayName string) (Me, error) {
	p := PrincipalFrom(ctx)
	if p == nil {
		return Me{}, ErrUnauthenticated
	}
	name, err := cleanName("displayName", displayName)
	if err != nil {
		return Me{}, err
	}
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := s.q.WithTx(tx)
		before, err := q.GetUserForUpdate(ctx, p.UserID)
		if err != nil {
			return err
		}
		after, err := q.SetUserDisplayName(ctx, dbq.SetUserDisplayNameParams{ID: p.UserID, DisplayName: name})
		if err != nil {
			return err
		}
		return audit.Record(ctx, tx, "user.update", audit.UserTarget(p.UserID), userAudit(before), userAudit(after))
	})
	if err != nil {
		return Me{}, err
	}
	p.DisplayName = name
	return s.Me(ctx)
}

func (s *Service) userDetail(ctx context.Context, q *dbq.Queries, id uuid.UUID) (UserDetail, error) {
	u, err := q.GetUser(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return UserDetail{}, ErrNotFound
	}
	if err != nil {
		return UserDetail{}, err
	}
	ms, err := q.ListMembershipsForUser(ctx, id)
	if err != nil {
		return UserDetail{}, err
	}
	d := UserDetail{AppUser: u, Households: make([]Membership, len(ms))}
	for i, m := range ms {
		d.Households[i] = Membership{HouseholdID: m.HouseholdID, Name: m.HouseholdName, Role: m.Role}
	}
	return d, nil
}

// --- passkeys / credentials ------------------------------------------------------

// ListMyPasskeys lists the caller's credentials across providers that store
// them.
func (s *Service) ListMyPasskeys(ctx context.Context) ([]Credential, error) {
	p, err := requireActive(ctx)
	if err != nil {
		return nil, err
	}
	return s.credentials(ctx, s.q, p.UserID)
}

func (s *Service) credentials(ctx context.Context, q *dbq.Queries, user uuid.UUID) ([]Credential, error) {
	links, err := q.ListIdentityLinks(ctx, user)
	if err != nil {
		return nil, err
	}
	var out []Credential
	for _, l := range links {
		cm, ok := s.providers[l.Provider].(CredentialManager)
		if !ok {
			continue
		}
		cs, err := cm.ListCredentials(ctx, l.Subject)
		if err != nil {
			return nil, err
		}
		out = append(out, cs...)
	}
	return out, nil
}

type addPasskeyContext struct {
	UserID  uuid.UUID `json:"userId"`
	NewLink bool      `json:"newLink"`
}

// BeginAddPasskey starts enrolling another passkey for the caller.
func (s *Service) BeginAddPasskey(ctx context.Context) (CeremonyStart, error) {
	p, err := requireActive(ctx)
	if err != nil {
		return CeremonyStart{}, err
	}
	subject, err := s.subjectFor(ctx, p.UserID, s.enroller.ID())
	if err != nil {
		return CeremonyStart{}, err
	}
	ch, err := s.enroller.BeginEnroll(ctx, EnrollStart{Subject: subject, Name: p.DisplayName, DisplayName: p.DisplayName})
	if err != nil {
		return CeremonyStart{}, err
	}
	return s.startCeremony(ctx, purposeAddPasskey, s.enroller, ch, addPasskeyContext{UserID: p.UserID, NewLink: subject == ""})
}

// FinishAddPasskey stores the new passkey.
func (s *Service) FinishAddPasskey(ctx context.Context, ceremony uuid.UUID, response json.RawMessage, label string) error {
	p, err := requireActive(ctx)
	if err != nil {
		return err
	}
	var ac addPasskeyContext
	row, prov, err := s.takeCeremony(ctx, ceremony, purposeAddPasskey, &ac)
	if err != nil {
		return err
	}
	if ac.UserID != p.UserID {
		return ErrCeremonyInvalid
	}
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		ident, err := prov.FinishEnroll(ctx, EnrollFinish{State: row.State, Response: response, Label: label, Tx: tx})
		if err != nil {
			return err
		}
		if ac.NewLink {
			if err := s.link(ctx, s.q.WithTx(tx), p.UserID, ident); err != nil {
				return err
			}
		}
		return audit.Record(ctx, tx, "auth.passkey.add", audit.UserTarget(p.UserID), nil, map[string]any{"provider": ident.Provider, "name": label})
	})
}

// RemoveMyPasskey deletes one of the caller's passkeys, unless it is their
// last way to sign in.
func (s *Service) RemoveMyPasskey(ctx context.Context, id string) error {
	p, err := requireActive(ctx)
	if err != nil {
		return err
	}
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := s.q.WithTx(tx)
		// Lock the user so two concurrent removals can't each see "one left".
		if _, err := q.GetUserForUpdate(ctx, p.UserID); err != nil {
			return err
		}
		creds, err := s.credentials(ctx, q, p.UserID)
		if err != nil {
			return err
		}
		var found *Credential
		for i := range creds {
			if creds[i].ID == id {
				found = &creds[i]
			}
		}
		if found == nil {
			return ErrNotFound
		}
		links, err := q.ListIdentityLinks(ctx, p.UserID)
		if err != nil {
			return err
		}
		// Providers without stored credentials (OIDC) still sign the user in.
		otherLogins := 0
		for _, l := range links {
			if _, ok := s.providers[l.Provider].(CredentialManager); !ok {
				otherLogins++
			}
		}
		if len(creds) <= 1 && otherLogins == 0 {
			return ErrLastCredential
		}
		removed := false
		for _, l := range links {
			cm, ok := s.providers[l.Provider].(CredentialManager)
			if !ok {
				continue
			}
			switch err := cm.RemoveCredential(ctx, tx, l.Subject, id); {
			case err == nil:
				removed = true
			case !errors.Is(err, ErrNotFound):
				return err
			}
			if removed {
				break
			}
		}
		if !removed {
			return ErrNotFound
		}
		return audit.Record(ctx, tx, "auth.passkey.remove", audit.UserTarget(p.UserID), map[string]any{"name": found.Name, "id": id}, nil)
	})
}

// subjectFor is the user's subject at provider, or "" if not linked yet.
func (s *Service) subjectFor(ctx context.Context, user uuid.UUID, provider string) (string, error) {
	links, err := s.q.ListIdentityLinks(ctx, user)
	if err != nil {
		return "", err
	}
	for _, l := range links {
		if l.Provider == provider {
			return l.Subject, nil
		}
	}
	return "", nil
}

// --- admin: users ------------------------------------------------------------------

// AdminListUsers lists users, newest first, optionally by status.
func (s *Service) AdminListUsers(ctx context.Context, status string) ([]UserDetail, error) {
	if _, err := requireAdmin(ctx); err != nil {
		return nil, err
	}
	var st *string
	if status != "" {
		st = &status
	}
	users, err := s.q.ListUsers(ctx, st)
	if err != nil {
		return nil, err
	}
	ids := make([]uuid.UUID, len(users))
	for i, u := range users {
		ids[i] = u.ID
	}
	ms, err := s.q.ListMembershipsForUsers(ctx, ids)
	if err != nil {
		return nil, err
	}
	byUser := map[uuid.UUID][]Membership{}
	for _, m := range ms {
		byUser[m.UserID] = append(byUser[m.UserID], Membership{HouseholdID: m.HouseholdID, Name: m.HouseholdName, Role: m.Role})
	}
	out := make([]UserDetail, len(users))
	for i, u := range users {
		out[i] = UserDetail{AppUser: u, Households: byUser[u.ID]}
		if out[i].Households == nil {
			out[i].Households = []Membership{}
		}
	}
	return out, nil
}

// ApproveUser activates a pending user.
func (s *Service) ApproveUser(ctx context.Context, id uuid.UUID) (UserDetail, error) {
	return s.decide(ctx, id, StatusActive, "user.approve")
}

// RejectUser disables a pending user and signs them out.
func (s *Service) RejectUser(ctx context.Context, id uuid.UUID) (UserDetail, error) {
	return s.decide(ctx, id, StatusDisabled, "user.reject")
}

func (s *Service) decide(ctx context.Context, id uuid.UUID, status, action string) (UserDetail, error) {
	p, err := requireAdmin(ctx)
	if err != nil {
		return UserDetail{}, err
	}
	var out UserDetail
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := s.q.WithTx(tx)
		before, err := q.GetUserForUpdate(ctx, id)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if before.Status != StatusPending {
			return ErrNotPending
		}
		now := s.now()
		params := dbq.SetUserStatusParams{ID: id, Status: status}
		if status == StatusActive {
			params.ApprovedAt, params.ApprovedBy = &now, &p.UserID
		}
		after, err := q.SetUserStatus(ctx, params)
		if err != nil {
			return err
		}
		if status == StatusDisabled {
			if _, err := q.RevokeSessionsForUser(ctx, id); err != nil {
				return err
			}
		}
		if err := audit.Record(ctx, tx, action, audit.UserTarget(id), userAudit(before), userAudit(after)); err != nil {
			return err
		}
		out, err = s.userDetail(ctx, q, id)
		return err
	})
	return out, err
}

// UserUpdate changes an approved user. Nil fields stay as they are.
type UserUpdate struct {
	Status     *string // active | disabled
	SystemRole *string // admin | user
}

// adminLockID serializes changes that could remove the last admin.
const adminLockID int64 = 0x73746f636b61646d // "stockadm"

// AdminUpdateUser enables, disables or changes the system role of a user.
// Disabling signs them out everywhere. At least one active admin remains.
func (s *Service) AdminUpdateUser(ctx context.Context, id uuid.UUID, up UserUpdate) (UserDetail, error) {
	if _, err := requireAdmin(ctx); err != nil {
		return UserDetail{}, err
	}
	if up.Status != nil && *up.Status != StatusActive && *up.Status != StatusDisabled {
		return UserDetail{}, &ValidationError{Field: "status", Msg: "must be active or disabled"}
	}
	if up.SystemRole != nil && *up.SystemRole != SystemAdmin && *up.SystemRole != SystemUser {
		return UserDetail{}, &ValidationError{Field: "systemRole", Msg: "must be admin or user"}
	}
	var out UserDetail
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", adminLockID); err != nil {
			return err
		}
		q := s.q.WithTx(tx)
		before, err := q.GetUserForUpdate(ctx, id)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if before.Status == StatusPending {
			return ErrUserPending
		}
		after := before
		wasActiveAdmin := before.Status == StatusActive && before.SystemRole == SystemAdmin
		if up.SystemRole != nil && *up.SystemRole != after.SystemRole {
			if after, err = q.SetUserSystemRole(ctx, dbq.SetUserSystemRoleParams{ID: id, SystemRole: *up.SystemRole}); err != nil {
				return err
			}
		}
		if up.Status != nil && *up.Status != after.Status {
			if after, err = q.SetUserStatus(ctx, dbq.SetUserStatusParams{ID: id, Status: *up.Status}); err != nil {
				return err
			}
			if *up.Status == StatusDisabled {
				if _, err := q.RevokeSessionsForUser(ctx, id); err != nil {
					return err
				}
			}
		}
		if wasActiveAdmin && (after.Status != StatusActive || after.SystemRole != SystemAdmin) {
			n, err := q.CountActiveAdmins(ctx)
			if err != nil {
				return err
			}
			if n == 0 {
				return ErrLastAdmin
			}
		}
		for _, change := range []struct {
			changed bool
			action  string
		}{
			{before.SystemRole != after.SystemRole, "user.role.change"},
			{before.Status != after.Status && after.Status == StatusDisabled, "user.disable"},
			{before.Status != after.Status && after.Status == StatusActive, "user.enable"},
		} {
			if change.changed {
				if err := audit.Record(ctx, tx, change.action, audit.UserTarget(id), userAudit(before), userAudit(after)); err != nil {
					return err
				}
			}
		}
		out, err = s.userDetail(ctx, q, id)
		return err
	})
	return out, err
}

// AdminListHouseholds lists every household.
func (s *Service) AdminListHouseholds(ctx context.Context) ([]dbq.ListHouseholdsRow, error) {
	if _, err := requireAdmin(ctx); err != nil {
		return nil, err
	}
	return s.q.ListHouseholds(ctx)
}
