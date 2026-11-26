package admin

import (
	"context"
	"log/slog"

	"github.com/jackc/pgx/v5"
)

// Target is what an audited action changed.
type Target struct {
	Type string // "setting", "job", …
	ID   string
}

// AuditFunc records an audited action inside tx, the transaction that makes
// the change, so the change and its record commit or fail together. The
// actor, request id and client IP come from ctx (reqctx). before and after
// are JSON-encodable snapshots; secrets must already be redacted.
type AuditFunc func(ctx context.Context, tx pgx.Tx, action string, target Target, before, after any) error

// LogAudit writes audit events to the application log. It stands in until
// the audit_event table exists (identity milestone), which replaces it in
// app wiring.
func LogAudit(ctx context.Context, _ pgx.Tx, action string, target Target, before, after any) error {
	slog.InfoContext(ctx, "audit",
		"action", action,
		"target_type", target.Type,
		"target_id", target.ID,
		"before", before,
		"after", after,
	)
	return nil
}
