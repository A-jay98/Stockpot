package identity

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/a-jay98/stockpot/internal/audit"
	"github.com/a-jay98/stockpot/internal/db/dbq"
)

// Invite statuses, derived from the row.
const (
	InvitePending  = "pending"
	InviteAccepted = "accepted"
	InviteRevoked  = "revoked"
	InviteExpired  = "expired"
)

// Invite is an invite as its household's owners and admins see it.
type Invite struct {
	ID            uuid.UUID
	HouseholdID   uuid.UUID
	HouseholdName string
	Role          string
	Note          string
	CreatedBy     uuid.UUID
	CreatedByName string
	CreatedAt     time.Time
	ExpiresAt     time.Time
	AcceptedAt    *time.Time
	AcceptedBy    *uuid.UUID
	Status        string
}

// NewInvite is a created invite with its one-time code. The code is shown
// once; only its hash is stored.
type NewInvite struct {
	Invite
	Code string
	URL  string
}

// InviteInfo is what an invitee may see before accepting.
type InviteInfo struct {
	HouseholdName string
	Role          string
	ExpiresAt     time.Time
}

func inviteStatus(accepted, revoked *time.Time, expires, now time.Time) string {
	switch {
	case accepted != nil:
		return InviteAccepted
	case revoked != nil:
		return InviteRevoked
	case !expires.After(now):
		return InviteExpired
	}
	return InvitePending
}

// manageable checks the caller may manage household's invites and returns
// them. household uuid.Nil means the caller's active household.
func (s *Service) manageable(ctx context.Context, household uuid.UUID) (*Principal, uuid.UUID, error) {
	p, err := requireActive(ctx)
	if err != nil {
		return nil, uuid.Nil, err
	}
	if household == uuid.Nil {
		if p.HouseholdID == uuid.Nil {
			return nil, uuid.Nil, ErrNoHousehold
		}
		household = p.HouseholdID
	}
	if p.IsAdmin() {
		if _, err := s.q.GetHousehold(ctx, household); errors.Is(err, pgx.ErrNoRows) {
			return nil, uuid.Nil, ErrNotFound
		} else if err != nil {
			return nil, uuid.Nil, err
		}
		return p, household, nil
	}
	m, err := s.q.GetMembership(ctx, dbq.GetMembershipParams{UserID: p.UserID, HouseholdID: household})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, uuid.Nil, ErrForbidden
	}
	if err != nil {
		return nil, uuid.Nil, err
	}
	if !CanManageHousehold(p, m.Role) {
		return nil, uuid.Nil, ErrForbidden
	}
	return p, household, nil
}

// CreateInvite makes a single-use invite to household (uuid.Nil: the
// caller's active one) with the given household role.
func (s *Service) CreateInvite(ctx context.Context, household uuid.UUID, role, note string) (NewInvite, error) {
	if role != HouseholdOwner && role != HouseholdMember {
		return NewInvite{}, &ValidationError{Field: "role", Msg: "must be owner or member"}
	}
	note = strings.TrimSpace(note)
	if utf8.RuneCountInString(note) > 200 {
		return NewInvite{}, &ValidationError{Field: "note", Msg: "must be at most 200 characters"}
	}
	p, household, err := s.manageable(ctx, household)
	if err != nil {
		return NewInvite{}, err
	}
	code, hash := randomToken(16)
	expires := s.now().Add(time.Duration(InviteDays.Get(s.settings)) * 24 * time.Hour)
	var out NewInvite
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := s.q.WithTx(tx)
		row, err := q.CreateInvite(ctx, dbq.CreateInviteParams{
			ID: uuid.Must(uuid.NewV7()), CodeHash: hash, HouseholdID: household,
			Role: role, Note: note, CreatedBy: p.UserID, ExpiresAt: expires,
		})
		if err != nil {
			return err
		}
		hh, err := q.GetHousehold(ctx, household)
		if err != nil {
			return err
		}
		out = NewInvite{
			Invite: Invite{
				ID: row.ID, HouseholdID: household, HouseholdName: hh.Name, Role: role, Note: note,
				CreatedBy: p.UserID, CreatedByName: p.DisplayName, CreatedAt: row.CreatedAt,
				ExpiresAt: row.ExpiresAt, Status: InvitePending,
			},
			Code: code,
			// The code travels in the fragment, so it never reaches a server
			// or proxy log.
			URL: s.publicURL.JoinPath("invite").String() + "#" + code,
		}
		return audit.Record(ctx, tx, "invite.create", audit.InviteTarget(row.ID), nil,
			map[string]any{"householdId": household, "role": role, "note": note, "expiresAt": row.ExpiresAt})
	})
	return out, err
}

// ListInvites lists invites of household (uuid.Nil: the caller's active one).
// Admins may pass all=true for every household.
func (s *Service) ListInvites(ctx context.Context, household uuid.UUID, all bool) ([]Invite, error) {
	var filter *uuid.UUID
	if all {
		if _, err := requireAdmin(ctx); err != nil {
			return nil, err
		}
	} else {
		_, hh, err := s.manageable(ctx, household)
		if err != nil {
			return nil, err
		}
		filter = &hh
	}
	rows, err := s.q.ListInvites(ctx, filter)
	if err != nil {
		return nil, err
	}
	now := s.now()
	out := make([]Invite, len(rows))
	for i, r := range rows {
		out[i] = Invite{
			ID: r.ID, HouseholdID: r.HouseholdID, HouseholdName: r.HouseholdName, Role: r.Role, Note: r.Note,
			CreatedBy: r.CreatedBy, CreatedByName: r.CreatedByName, CreatedAt: r.CreatedAt, ExpiresAt: r.ExpiresAt,
			AcceptedAt: r.AcceptedAt, AcceptedBy: r.AcceptedBy,
			Status: inviteStatus(r.AcceptedAt, r.RevokedAt, r.ExpiresAt, now),
		}
	}
	return out, nil
}

// RevokeInvite cancels a pending invite.
func (s *Service) RevokeInvite(ctx context.Context, id uuid.UUID) error {
	inv, err := s.q.GetInvite(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if _, _, err := s.manageable(ctx, inv.HouseholdID); err != nil {
		if errors.Is(err, ErrForbidden) {
			return ErrNotFound // don't confirm other households' invites exist
		}
		return err
	}
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := s.q.WithTx(tx).RevokeInvite(ctx, id); errors.Is(err, pgx.ErrNoRows) {
			return ErrInviteInvalid
		} else if err != nil {
			return err
		}
		return audit.Record(ctx, tx, "invite.revoke", audit.InviteTarget(id), map[string]any{"status": InvitePending}, map[string]any{"status": InviteRevoked})
	})
}

// validInvite finds a usable invite by code.
func (s *Service) validInvite(ctx context.Context, q *dbq.Queries, code string) (dbq.GetInviteByCodeHashRow, error) {
	code = strings.TrimSpace(code)
	if code == "" || len(code) > 64 {
		return dbq.GetInviteByCodeHashRow{}, ErrInviteInvalid
	}
	inv, err := q.GetInviteByCodeHash(ctx, hashToken(code))
	if errors.Is(err, pgx.ErrNoRows) {
		return inv, ErrInviteInvalid
	}
	if err != nil {
		return inv, err
	}
	if inviteStatus(inv.AcceptedAt, inv.RevokedAt, inv.ExpiresAt, s.now()) != InvitePending {
		return inv, ErrInviteInvalid
	}
	return inv, nil
}

// InspectInvite tells an invitee what they are joining.
func (s *Service) InspectInvite(ctx context.Context, code string) (InviteInfo, error) {
	inv, err := s.validInvite(ctx, s.q, code)
	if err != nil {
		return InviteInfo{}, err
	}
	return InviteInfo{HouseholdName: inv.HouseholdName, Role: inv.Role, ExpiresAt: inv.ExpiresAt}, nil
}

type inviteContext struct {
	InviteID    uuid.UUID `json:"inviteId"`
	DisplayName string    `json:"displayName"`
}

// BeginAcceptInvite starts enrolling a new user's passkey for an invite.
func (s *Service) BeginAcceptInvite(ctx context.Context, code, displayName string) (CeremonyStart, error) {
	inv, err := s.validInvite(ctx, s.q, code)
	if err != nil {
		return CeremonyStart{}, err
	}
	displayName, err = cleanName("displayName", displayName)
	if err != nil {
		return CeremonyStart{}, err
	}
	ch, err := s.enroller.BeginEnroll(ctx, EnrollStart{Name: displayName, DisplayName: displayName})
	if err != nil {
		return CeremonyStart{}, err
	}
	return s.startCeremony(ctx, purposeInvite, s.enroller, ch, inviteContext{InviteID: inv.ID, DisplayName: displayName})
}

// FinishAcceptInvite creates the invited user (pending approval unless
// auth.invite_requires_approval is off), joins them to the household and
// signs them in.
func (s *Service) FinishAcceptInvite(ctx context.Context, ceremony uuid.UUID, response json.RawMessage, label string) (SessionToken, error) {
	var ic inviteContext
	row, p, err := s.takeCeremony(ctx, ceremony, purposeInvite, &ic)
	if err != nil {
		return SessionToken{}, err
	}
	var tok SessionToken
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := s.q.WithTx(tx)
		inv, err := q.LockInvite(ctx, ic.InviteID)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrInviteInvalid
		}
		if err != nil {
			return err
		}
		if inviteStatus(inv.AcceptedAt, inv.RevokedAt, inv.ExpiresAt, s.now()) != InvitePending {
			return ErrInviteInvalid
		}
		ident, err := p.FinishEnroll(ctx, EnrollFinish{State: row.State, Response: response, Label: label, Tx: tx})
		if err != nil {
			return err
		}
		params := dbq.CreateUserParams{
			ID: uuid.Must(uuid.NewV7()), DisplayName: ic.DisplayName, Status: StatusPending, SystemRole: SystemUser,
		}
		if !InviteRequiresApproval.Get(s.settings) {
			// The inviter vouched for them.
			now := s.now()
			params.Status, params.ApprovedAt, params.ApprovedBy = StatusActive, &now, &inv.CreatedBy
		}
		user, err := q.CreateUser(ctx, params)
		if err != nil {
			return err
		}
		if err := s.link(ctx, q, user.ID, ident); err != nil {
			return err
		}
		if err := q.AddMembership(ctx, dbq.AddMembershipParams{UserID: user.ID, HouseholdID: inv.HouseholdID, Role: inv.Role}); err != nil {
			return err
		}
		if err := q.AcceptInvite(ctx, dbq.AcceptInviteParams{ID: inv.ID, AcceptedBy: &user.ID}); err != nil {
			return err
		}
		actx := audit.WithActor(ctx, user.ID, inv.HouseholdID)
		if err := audit.Record(actx, tx, "invite.accept", audit.InviteTarget(inv.ID), nil,
			map[string]any{"user": userAudit(user), "householdId": inv.HouseholdID, "role": inv.Role}); err != nil {
			return err
		}
		if err := audit.Record(actx, tx, "auth.passkey.add", audit.UserTarget(user.ID), nil, map[string]any{"provider": ident.Provider}); err != nil {
			return err
		}
		tok, err = s.createSession(actx, q, user, inv.HouseholdID, ident.Provider)
		return err
	})
	return tok, err
}

// JoinWithInvite adds the signed-in caller to an invite's household and
// makes it their active one.
func (s *Service) JoinWithInvite(ctx context.Context, code string) (Me, error) {
	p, err := requireActive(ctx)
	if err != nil {
		return Me{}, err
	}
	found, err := s.validInvite(ctx, s.q, code)
	if err != nil {
		return Me{}, err
	}
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := s.q.WithTx(tx)
		inv, err := q.LockInvite(ctx, found.ID)
		if err != nil {
			return err
		}
		if inviteStatus(inv.AcceptedAt, inv.RevokedAt, inv.ExpiresAt, s.now()) != InvitePending {
			return ErrInviteInvalid
		}
		if _, err := q.GetMembership(ctx, dbq.GetMembershipParams{UserID: p.UserID, HouseholdID: inv.HouseholdID}); err == nil {
			return ErrAlreadyMember
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if err := q.AddMembership(ctx, dbq.AddMembershipParams{UserID: p.UserID, HouseholdID: inv.HouseholdID, Role: inv.Role}); err != nil {
			return err
		}
		if err := q.AcceptInvite(ctx, dbq.AcceptInviteParams{ID: inv.ID, AcceptedBy: &p.UserID}); err != nil {
			return err
		}
		if err := q.SetSessionHousehold(ctx, dbq.SetSessionHouseholdParams{ID: p.SessionID, HouseholdID: &inv.HouseholdID}); err != nil {
			return err
		}
		return audit.Record(ctx, tx, "invite.accept", audit.InviteTarget(inv.ID), nil,
			map[string]any{"userId": p.UserID, "householdId": inv.HouseholdID, "role": inv.Role})
	})
	if err != nil {
		return Me{}, err
	}
	p.HouseholdID, p.HouseholdRole = found.HouseholdID, found.Role
	return s.Me(ctx)
}
