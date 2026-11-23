// Package admin is the logic behind the admin panel: system overview, logs,
// background jobs and runtime settings. The HTTP layer (internal/httpapi)
// maps it onto /api/v1/admin/*; authorization (admin only) happens there.
//
// Every mutation records an audit event in the same transaction as the
// change, through the Auditor the app wires in.
package admin

import (
	"context"
	"encoding/base64"
	"errors"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/a-jay98/stockpot/internal/db"
	"github.com/a-jay98/stockpot/internal/db/dbq"
	"github.com/a-jay98/stockpot/internal/jobs"
	"github.com/a-jay98/stockpot/internal/settings"
)

// Errors the HTTP layer maps to problem responses.
var (
	ErrNotFound  = errors.New("not found")
	ErrBadCursor = errors.New("invalid cursor")
)

// Schema reports the database schema state (db.SchemaCheck).
type Schema interface {
	Ping(ctx context.Context) error
	Versions(ctx context.Context) (db.Versions, error)
}

// SinkStats are the app_log sink's counters (logging.Sink).
type SinkStats interface {
	Written() uint64
	Dropped() uint64
}

// LLMToday is today's LLM usage for the overview.
type LLMToday struct {
	Requests, Tokens int64
	// RequestCap is the global daily request cap; nil when uncapped.
	RequestCap *int64
	// Available reports whether any provider can take a request now.
	Available bool
}

// Deps are the parts of the app the admin panel reads and controls.
type Deps struct {
	Pool     *pgxpool.Pool
	Settings *settings.Store
	Jobs     *jobs.Client
	Schema   Schema
	LogSink  SinkStats // optional
	Audit    AuditFunc
	// LLMToday reports LLM usage; nil until the LLM gateway is wired.
	LLMToday func(ctx context.Context) (LLMToday, error)
	// StartedAt is when this process started serving.
	StartedAt time.Time
}

// Service implements the admin panel's operations.
type Service struct {
	d   Deps
	q   *dbq.Queries
	now func() time.Time
}

// New returns the admin service. d.Audit defaults to LogAudit.
func New(d Deps) *Service {
	if d.Audit == nil {
		d.Audit = LogAudit
	}
	if d.StartedAt.IsZero() {
		d.StartedAt = time.Now()
	}
	return &Service{d: d, q: dbq.New(d.Pool), now: time.Now}
}

// inTx runs fn in a transaction.
func (s *Service) inTx(ctx context.Context, fn func(pgx.Tx) error) error {
	return pgx.BeginFunc(ctx, s.d.Pool, fn)
}

// Page limits shared by the list endpoints.
const (
	DefaultLimit = 100
	MaxLimit     = 500
)

func clampLimit(n int) int {
	switch {
	case n <= 0:
		return DefaultLimit
	case n > MaxLimit:
		return MaxLimit
	}
	return n
}

// encodeID and decodeID make an opaque keyset cursor from a row id.
func encodeID(id int64) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.FormatInt(id, 10)))
}

func decodeID(cursor string) (int64, error) {
	b, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return 0, ErrBadCursor
	}
	id, err := strconv.ParseInt(string(b), 10, 64)
	if err != nil || id <= 0 {
		return 0, ErrBadCursor
	}
	return id, nil
}
