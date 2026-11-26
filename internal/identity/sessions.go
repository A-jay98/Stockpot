package identity

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/a-jay98/stockpot/internal/audit"
	"github.com/a-jay98/stockpot/internal/db/dbq"
	"github.com/a-jay98/stockpot/internal/reqctx"
)

// SessionCookie is the cookie holding the session token. The __Host- prefix
// makes browsers insist on Secure, Path=/ and no Domain, so no subdomain or
// plain-HTTP page can set or read it.
const SessionCookie = "__Host-stockpot_session"

// SessionToken is a newly issued session, for the Set-Cookie header.
type SessionToken struct {
	Token     string
	ExpiresAt time.Time
}

// touchEvery throttles sliding-expiry writes to one per session per period.
const touchEvery = time.Minute

func (s *Service) sessionTTL() time.Duration {
	return time.Duration(SessionDays.Get(s.settings)) * 24 * time.Hour
}

// createSession issues a session for user and marks the request as theirs,
// so its access log row names them.
func (s *Service) createSession(ctx context.Context, q *dbq.Queries, user dbq.AppUser, household uuid.UUID, provider string) (SessionToken, error) {
	tok, hash := randomToken(32)
	id := uuid.Must(uuid.NewV7())
	expires := s.now().Add(s.sessionTTL())
	var hh *uuid.UUID
	if household != uuid.Nil {
		hh = &household
	}
	ua := ""
	if info := reqctx.From(ctx); info != nil {
		ua = info.UserAgent
		if len(ua) > 512 {
			ua = ua[:512]
		}
	}
	if err := q.CreateSession(ctx, dbq.CreateSessionParams{
		ID: id, TokenHash: hash, UserID: user.ID, HouseholdID: hh, Provider: provider,
		ExpiresAt: expires, Ip: clientIP(ctx), UserAgent: ua,
	}); err != nil {
		return SessionToken{}, err
	}
	if info := reqctx.From(ctx); info != nil {
		info.UserID, info.SessionID, info.HouseholdID = user.ID, id, household
	}
	return SessionToken{Token: tok, ExpiresAt: expires}, nil
}

// Authenticate loads the session for a cookie token. It slides the expiry
// and resolves the active household (falling back to the first membership
// when the stored one is gone). ErrUnauthenticated for anything invalid.
func (s *Service) Authenticate(ctx context.Context, token string) (*Principal, SessionToken, error) {
	if token == "" || len(token) > 128 {
		return nil, SessionToken{}, ErrUnauthenticated
	}
	row, err := s.q.GetSessionByTokenHash(ctx, hashToken(token))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, SessionToken{}, ErrUnauthenticated
	}
	if err != nil {
		return nil, SessionToken{}, err
	}
	sess, user := row.Session, row.AppUser
	now := s.now()
	if sess.RevokedAt != nil || !sess.ExpiresAt.After(now) || user.Status == StatusDisabled {
		return nil, SessionToken{}, ErrUnauthenticated
	}
	p := &Principal{
		UserID: user.ID, SessionID: sess.ID, DisplayName: user.DisplayName,
		Status: user.Status, SystemRole: user.SystemRole,
	}
	if sess.HouseholdID != nil {
		m, err := s.q.GetMembership(ctx, dbq.GetMembershipParams{UserID: user.ID, HouseholdID: *sess.HouseholdID})
		switch {
		case err == nil:
			p.HouseholdID, p.HouseholdRole = m.HouseholdID, m.Role
		case !errors.Is(err, pgx.ErrNoRows):
			return nil, SessionToken{}, err
		}
	}
	if p.HouseholdID == uuid.Nil {
		hh, role, err := s.defaultHousehold(ctx, s.q, user.ID)
		if err != nil {
			return nil, SessionToken{}, err
		}
		p.HouseholdID, p.HouseholdRole = hh, role
		if hh != uuid.Nil {
			if err := s.q.SetSessionHousehold(ctx, dbq.SetSessionHouseholdParams{ID: sess.ID, HouseholdID: &hh}); err != nil {
				return nil, SessionToken{}, err
			}
		}
	}
	renewed := SessionToken{Token: token, ExpiresAt: sess.ExpiresAt}
	if now.Sub(sess.LastSeenAt) >= touchEvery {
		renewed.ExpiresAt = now.Add(s.sessionTTL())
		if err := s.q.TouchSession(ctx, dbq.TouchSessionParams{
			ID: sess.ID, LastSeenAt: now, ExpiresAt: renewed.ExpiresAt, Ip: clientIP(ctx),
		}); err != nil {
			return nil, SessionToken{}, err
		}
	}
	return p, renewed, nil
}

// Logout revokes the caller's session.
func (s *Service) Logout(ctx context.Context) error {
	p := PrincipalFrom(ctx)
	if p == nil {
		return ErrUnauthenticated
	}
	return s.revokeSession(ctx, p.SessionID, "auth.logout")
}

// SessionInfo is a session as its owner or an admin sees it.
type SessionInfo struct {
	dbq.Session
	Current bool
}

// ListMySessions lists the caller's active sessions.
func (s *Service) ListMySessions(ctx context.Context) ([]SessionInfo, error) {
	p := PrincipalFrom(ctx)
	if p == nil {
		return nil, ErrUnauthenticated
	}
	return s.listSessions(ctx, p, p.UserID)
}

// RevokeMySession signs out one of the caller's sessions.
func (s *Service) RevokeMySession(ctx context.Context, id uuid.UUID) error {
	p := PrincipalFrom(ctx)
	if p == nil {
		return ErrUnauthenticated
	}
	sess, err := s.q.GetSession(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && sess.UserID != p.UserID) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	return s.revokeSession(ctx, id, "auth.session.revoke")
}

// AdminListSessions lists a user's active sessions.
func (s *Service) AdminListSessions(ctx context.Context, user uuid.UUID) ([]SessionInfo, error) {
	p, err := requireAdmin(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := s.q.GetUser(ctx, user); errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	} else if err != nil {
		return nil, err
	}
	return s.listSessions(ctx, p, user)
}

// AdminRevokeSession signs out any session.
func (s *Service) AdminRevokeSession(ctx context.Context, id uuid.UUID) error {
	if _, err := requireAdmin(ctx); err != nil {
		return err
	}
	return s.revokeSession(ctx, id, "auth.session.revoke")
}

func (s *Service) listSessions(ctx context.Context, p *Principal, user uuid.UUID) ([]SessionInfo, error) {
	rows, err := s.q.ListActiveSessionsForUser(ctx, user)
	if err != nil {
		return nil, err
	}
	out := make([]SessionInfo, len(rows))
	for i, r := range rows {
		out[i] = SessionInfo{Session: r, Current: r.ID == p.SessionID}
	}
	return out, nil
}

func (s *Service) revokeSession(ctx context.Context, id uuid.UUID, action string) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		sess, err := s.q.WithTx(tx).RevokeSession(ctx, id)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		return audit.Record(ctx, tx, action, audit.SessionTarget(id), nil, map[string]any{"userId": sess.UserID})
	})
}
