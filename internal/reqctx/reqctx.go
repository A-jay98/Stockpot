// Package reqctx carries per-request facts (request id, client IP, and from
// identity the user, session and household) through a context.
//
// The HTTP middleware stores one *Info per request near the top of the chain;
// inner layers fill in fields on the same struct, so outer layers (the access
// log) see what inner ones (session load) learned. A request is served by one
// goroutine, so the fields need no locking.
package reqctx

import (
	"context"

	"github.com/google/uuid"
)

// Info describes the request being served.
type Info struct {
	RequestID   string
	ClientIP    string
	UserAgent   string
	UserID      uuid.UUID // uuid.Nil until a session is loaded
	SessionID   uuid.UUID
	HouseholdID uuid.UUID
}

type key struct{}

// With returns ctx carrying info.
func With(ctx context.Context, info *Info) context.Context {
	return context.WithValue(ctx, key{}, info)
}

// From returns the request's Info, or nil outside a request.
func From(ctx context.Context) *Info {
	info, _ := ctx.Value(key{}).(*Info)
	return info
}
