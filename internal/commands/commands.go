// Package commands is the write path of the sync engine: every offline-capable
// write is a command with a client-generated id, applied exactly once in its
// own transaction under the household's write lock.
package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/a-jay98/stockpot/internal/audit"
	"github.com/a-jay98/stockpot/internal/identity"
	"github.com/a-jay98/stockpot/internal/syncdb"
)

// Problem codes a handler can reject a command with. The client shows them
// in the "Couldn't sync" sheet.
const (
	CodeGone      = "gone"      // the row it targets was deleted
	CodeInvalid   = "invalid"   // the payload is not acceptable
	CodeForbidden = "forbidden" // the caller may not do this
)

// Error is a rejection a handler returns: the command is recorded as
// rejected, its changes roll back, and a retry of the same id answers the
// same way. Any other error aborts the request (HTTP 500) and nothing is
// recorded, so the client retries.
type Error struct{ Code, Detail string }

func (e *Error) Error() string { return e.Code + ": " + e.Detail }

// Gone rejects a command whose target was deleted.
func Gone(format string, a ...any) error { return &Error{CodeGone, fmt.Sprintf(format, a...)} }

// Invalid rejects a command with an unacceptable payload.
func Invalid(format string, a ...any) error { return &Error{CodeInvalid, fmt.Sprintf(format, a...)} }

// Forbidden rejects a command the caller may not issue.
func Forbidden(format string, a ...any) error {
	return &Error{CodeForbidden, fmt.Sprintf(format, a...)}
}

type idKey struct{}

// ID is the id of the command a handler is applying, for ledgers that link
// their rows back to it.
func ID(ctx context.Context) (uuid.UUID, bool) {
	id, ok := ctx.Value(idKey{}).(uuid.UUID)
	return id, ok
}

// Handler applies one decoded command inside tx (a savepoint of the
// command's transaction). The household comes from identity.HouseholdID(ctx).
type Handler[P any] func(ctx context.Context, tx pgx.Tx, p P) error

type entry func(ctx context.Context, tx pgx.Tx, raw json.RawMessage) (after any, err error)

// Registry maps command types to handlers.
type Registry struct {
	mu       sync.RWMutex
	handlers map[string]entry
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry { return &Registry{handlers: map[string]entry{}} }

// Register adds the handler for typ (e.g. "inventory.stock.consume", which
// is also its audit action). Payloads are decoded strictly: unknown fields
// are rejected. It panics on a duplicate type, a wiring bug.
func Register[P any](r *Registry, typ string, h Handler[P]) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.handlers[typ]; dup {
		panic("commands: duplicate command type " + typ)
	}
	r.handlers[typ] = func(ctx context.Context, tx pgx.Tx, raw json.RawMessage) (any, error) {
		var p P
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&p); err != nil {
			return nil, Invalid("payload: %v", err)
		}
		if _, err := dec.Token(); !errors.Is(err, io.EOF) {
			return nil, Invalid("payload: trailing data")
		}
		return p, h(ctx, tx, p)
	}
}

// Types lists the registered command types.
func (r *Registry) Types() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.handlers))
	for t := range r.handlers {
		out = append(out, t)
	}
	return out
}

// Command is one entry of a POST /v1/commands batch.
type Command struct {
	ID         uuid.UUID       `json:"id"`
	Type       string          `json:"type"`
	Payload    json.RawMessage `json:"payload"`
	ClientTime time.Time       `json:"clientTime"`
}

// Status of one command in the response.
const (
	StatusApplied   = "applied"
	StatusDuplicate = "duplicate"
	StatusRejected  = "rejected"
)

// Result is the outcome of one command.
type Result struct {
	ID      uuid.UUID
	Status  string
	Problem *Error
}

// Service applies command batches.
type Service struct {
	pool *pgxpool.Pool
	reg  *Registry
}

// NewService returns a service over reg.
func NewService(pool *pgxpool.Pool, reg *Registry) *Service { return &Service{pool: pool, reg: reg} }

// ErrHouseholdMismatch means the batch was written for a household other than
// the session's active one (the user switched households in between).
var ErrHouseholdMismatch = errors.New("commands: batch is for another household")

// Apply runs cmds in order, each in its own transaction, and returns one
// result per command. household is the one the client queued them under; it
// must be the session's active household or nothing is applied. On an
// unexpected error it returns the results so far plus the error; commands
// after it were not attempted.
func (s *Service) Apply(ctx context.Context, household uuid.UUID, cmds []Command) ([]Result, error) {
	hh, err := identity.HouseholdID(ctx)
	if err != nil {
		return nil, err
	}
	if hh != household {
		return nil, ErrHouseholdMismatch
	}
	p := identity.PrincipalFrom(ctx)
	out := make([]Result, 0, len(cmds))
	for _, c := range cmds {
		r, err := s.applyOne(ctx, hh, p.UserID, c)
		if err != nil {
			return out, fmt.Errorf("command %s (%s): %w", c.ID, c.Type, err)
		}
		out = append(out, r)
	}
	return out, nil
}

func (s *Service) applyOne(ctx context.Context, hh, user uuid.UUID, c Command) (Result, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := syncdb.LockHousehold(ctx, tx, hh); err != nil {
		return Result{}, err
	}

	tag, err := tx.Exec(ctx, `INSERT INTO applied_command (id, household_id, user_id, type, client_time)
		VALUES ($1, $2, $3, $4, $5) ON CONFLICT (id) DO NOTHING`, c.ID, hh, user, c.Type, nullTime(c.ClientTime))
	if err != nil {
		return Result{}, err
	}
	if tag.RowsAffected() == 0 {
		return s.replay(ctx, tx, hh, c)
	}

	ctx = context.WithValue(ctx, idKey{}, c.ID)
	h := s.lookup(c.Type)
	if h == nil {
		return s.reject(ctx, tx, c, &Error{CodeInvalid, "unknown command type " + c.Type})
	}
	sp, err := tx.Begin(ctx) // savepoint: a rejection discards the handler's partial writes
	if err != nil {
		return Result{}, err
	}
	after, err := h(withClientTime(ctx, c.ClientTime), sp, c.Payload)
	var rej *Error
	var pgErr *pgconn.PgError
	switch {
	case errors.As(err, &pgErr) && (strings.HasPrefix(pgErr.Code, "22") || strings.HasPrefix(pgErr.Code, "23")):
		// Data exceptions and integrity violations are the payload's fault, and retrying
		// can't help: reject, so one bad command never wedges the client's queue.
		_ = sp.Rollback(ctx)
		return s.reject(ctx, tx, c, &Error{CodeInvalid, "the change conflicts with existing data"})
	case errors.As(err, &rej):
		_ = sp.Rollback(ctx)
		return s.reject(ctx, tx, c, rej)
	case err != nil:
		_ = sp.Rollback(ctx)
		return Result{}, err
	}
	if err := sp.Commit(ctx); err != nil {
		return Result{}, err
	}
	if err := audit.Record(ctx, tx, c.Type, audit.Target{Type: "command", ID: c.ID.String()}, nil, after); err != nil {
		return Result{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Result{}, err
	}
	return Result{ID: c.ID, Status: StatusApplied}, nil
}

func (s *Service) lookup(typ string) entry {
	s.reg.mu.RLock()
	defer s.reg.mu.RUnlock()
	return s.reg.handlers[typ]
}

// reject records the rejection (the handler's writes are already undone) and commits.
func (s *Service) reject(ctx context.Context, tx pgx.Tx, c Command, e *Error) (Result, error) {
	prob, _ := json.Marshal(e)
	if _, err := tx.Exec(ctx, `UPDATE applied_command SET status = 'rejected', problem = $2 WHERE id = $1`, c.ID, prob); err != nil {
		return Result{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Result{}, err
	}
	return Result{ID: c.ID, Status: StatusRejected, Problem: e}, nil
}

// replay answers a command id seen before.
func (s *Service) replay(ctx context.Context, tx pgx.Tx, hh uuid.UUID, c Command) (Result, error) {
	var (
		owner  uuid.UUID
		status string
		prob   []byte
	)
	if err := tx.QueryRow(ctx, `SELECT household_id, status, problem FROM applied_command WHERE id = $1`, c.ID).Scan(&owner, &status, &prob); err != nil {
		return Result{}, err
	}
	if owner != hh {
		return Result{ID: c.ID, Status: StatusRejected, Problem: &Error{CodeInvalid, "command id already used"}}, nil
	}
	if status == StatusRejected {
		var e Error
		_ = json.Unmarshal(prob, &e)
		return Result{ID: c.ID, Status: StatusRejected, Problem: &e}, nil
	}
	return Result{ID: c.ID, Status: StatusDuplicate}, nil
}

func nullTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

// MarshalJSON renders the problem as {code, detail}.
func (e *Error) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Code   string `json:"code"`
		Detail string `json:"detail"`
	}{e.Code, e.Detail})
}

// UnmarshalJSON reads what MarshalJSON wrote.
func (e *Error) UnmarshalJSON(b []byte) error {
	var v struct{ Code, Detail string }
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	e.Code, e.Detail = v.Code, v.Detail
	return nil
}

// CheckOwned is for create handlers that insert with ON CONFLICT (id) DO NOTHING:
// when nothing was inserted, the id must already belong to this household
// (a replay, fine) and not to another one (rejected, never silently "applied").
// table is a trusted constant.
func CheckOwned(ctx context.Context, tx pgx.Tx, table string, id, household uuid.UUID) error {
	var owner *uuid.UUID // NULL for global rows
	err := tx.QueryRow(ctx, "SELECT household_id FROM "+table+" WHERE id = $1", id).Scan(&owner)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if owner == nil || *owner != household {
		return Invalid("id already in use")
	}
	return nil
}

type clientTimeKey struct{}

func withClientTime(ctx context.Context, t time.Time) context.Context {
	return context.WithValue(ctx, clientTimeKey{}, t)
}

// maxClientSkew bounds how far back a client's clock is trusted: an offline
// shopping trip syncs within days, anything older is treated as unreliable.
const maxClientSkew = 7 * 24 * time.Hour

// ClientTime is when the user performed the command being applied, clamped to
// [now - 7 days, now]; it is the time of application when the client sent
// none. Handlers use it for events that happened offline (a purchase).
func ClientTime(ctx context.Context) time.Time {
	now := time.Now()
	t, _ := ctx.Value(clientTimeKey{}).(time.Time)
	if t.IsZero() || t.After(now) {
		return now
	}
	if t.Before(now.Add(-maxClientSkew)) {
		return now.Add(-maxClientSkew)
	}
	return t
}
