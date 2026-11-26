// Package audit records who did what: audit_event rows for security events
// and mutations (written in the same transaction as the change), and the
// per-request access_log (written asynchronously in batches).
package audit

import (
	"context"
	"encoding/json"
	"fmt"
	"net/netip"
	"regexp"

	"github.com/google/uuid"

	"github.com/a-jay98/stockpot/internal/db/dbq"
	"github.com/a-jay98/stockpot/internal/reqctx"
)

// Target is what an event acted on, e.g. {Type: "user", ID: "<uuid>"}.
type Target struct {
	Type string
	ID   string
}

// UserTarget names a user.
func UserTarget(id uuid.UUID) Target { return Target{Type: "user", ID: id.String()} }

// HouseholdTarget names a household.
func HouseholdTarget(id uuid.UUID) Target { return Target{Type: "household", ID: id.String()} }

// SessionTarget names a session.
func SessionTarget(id uuid.UUID) Target { return Target{Type: "session", ID: id.String()} }

// InviteTarget names an invite.
func InviteTarget(id uuid.UUID) Target { return Target{Type: "invite", ID: id.String()} }

var actionPattern = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)+$`)

type actorKey struct{}

type actor struct {
	user      uuid.UUID
	household uuid.UUID
}

// WithActor attributes events recorded under ctx to user (and household, if
// not uuid.Nil) when the request has no session yet, e.g. the login that
// creates it.
func WithActor(ctx context.Context, user, household uuid.UUID) context.Context {
	return context.WithValue(ctx, actorKey{}, actor{user: user, household: household})
}

// Record writes one audit event through tx, so it commits or rolls back with
// the change it describes. before and after are JSON-encoded (nil = NULL);
// pass only what an auditor needs and never secrets. The actor, household,
// request id, session and IP come from ctx.
func Record(ctx context.Context, tx dbq.DBTX, action string, target Target, before, after any) error {
	if !actionPattern.MatchString(action) {
		return fmt.Errorf("audit: invalid action %q", action)
	}
	b, err := encode(before)
	if err != nil {
		return fmt.Errorf("audit %s: before: %w", action, err)
	}
	a, err := encode(after)
	if err != nil {
		return fmt.Errorf("audit %s: after: %w", action, err)
	}
	p := dbq.InsertAuditEventParams{
		Action:     action,
		TargetType: target.Type,
		TargetID:   target.ID,
		Before:     b,
		After:      a,
	}
	if info := reqctx.From(ctx); info != nil {
		p.ActorID = nonNil(info.UserID)
		p.HouseholdID = nonNil(info.HouseholdID)
		p.SessionID = nonNil(info.SessionID)
		if info.RequestID != "" {
			p.RequestID = &info.RequestID
		}
		p.Ip = parseIP(info.ClientIP)
	}
	// An explicit actor wins: a browser still signed in as A that signs in
	// (or accepts an invite) as B acts as B.
	if ac, ok := ctx.Value(actorKey{}).(actor); ok {
		if info(ctx).UserID != ac.user {
			p.SessionID = nil // the request's session belongs to someone else
		}
		p.ActorID = nonNil(ac.user)
		if ac.household != uuid.Nil {
			p.HouseholdID = &ac.household
		}
	}
	if err := dbq.New(tx).InsertAuditEvent(ctx, p); err != nil {
		return fmt.Errorf("audit %s: %w", action, err)
	}
	return nil
}

func info(ctx context.Context) reqctx.Info {
	if i := reqctx.From(ctx); i != nil {
		return *i
	}
	return reqctx.Info{}
}

func encode(v any) ([]byte, error) {
	if v == nil {
		return nil, nil
	}
	if raw, ok := v.(json.RawMessage); ok {
		return raw, nil
	}
	return json.Marshal(v)
}

func nonNil(id uuid.UUID) *uuid.UUID {
	if id == uuid.Nil {
		return nil
	}
	return &id
}

func parseIP(s string) *netip.Addr {
	if s == "" {
		return nil
	}
	ip, err := netip.ParseAddr(s)
	if err != nil {
		return nil
	}
	return &ip
}
