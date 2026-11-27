package backup

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Run is a backup_run row.
type Run struct {
	ID            uuid.UUID
	Trigger       string
	Status        string // running | ok | failed
	StartedAt     time.Time
	FinishedAt    *time.Time
	SizeBytes     int64
	Parts         int
	AppVersion    string
	SchemaVersion *int64
	Error         *string
	MessageIDs    []int64
	PrunedAt      *time.Time
	DeleteNote    *string
}

// ErrAlreadyRunning means another backup is in progress.
var ErrAlreadyRunning = errors.New("a backup is already running")

const runCols = `id, trigger, status, started_at, finished_at, size_bytes, parts, app_version,
	schema_version, error, message_ids, pruned_at, delete_note`

func scanRun(row pgx.Row) (Run, error) {
	var r Run
	err := row.Scan(&r.ID, &r.Trigger, &r.Status, &r.StartedAt, &r.FinishedAt, &r.SizeBytes, &r.Parts,
		&r.AppVersion, &r.SchemaVersion, &r.Error, &r.MessageIDs, &r.PrunedAt, &r.DeleteNote)
	return r, err
}

// failStale marks runs whose job is no longer running as failed (a crash or
// restart mid-backup), so they don't block the next one.
func failStale(ctx context.Context, pool *pgxpool.Pool) error {
	_, err := pool.Exec(ctx, `
		UPDATE backup_run SET status = 'failed', finished_at = now(), error = 'interrupted (the server stopped during the backup)'
		WHERE status = 'running'
		  AND (job_id IS NULL OR NOT EXISTS (SELECT 1 FROM river_job j WHERE j.id = backup_run.job_id AND j.state = 'running'))`)
	return err
}

func startRun(ctx context.Context, pool *pgxpool.Pool, trigger string, jobID *int64, now time.Time) (uuid.UUID, error) {
	var id uuid.UUID
	err := pool.QueryRow(ctx,
		`INSERT INTO backup_run (trigger, status, job_id, started_at) VALUES ($1, 'running', $2, $3) RETURNING id`,
		trigger, jobID, now).Scan(&id)
	var pe *pgconn.PgError
	if errors.As(err, &pe) && pe.Code == "23505" {
		return id, ErrAlreadyRunning
	}
	return id, err
}

func addMessage(ctx context.Context, pool *pgxpool.Pool, id uuid.UUID, msg int64) error {
	_, err := pool.Exec(ctx, `UPDATE backup_run SET message_ids = message_ids || $2::bigint WHERE id = $1`, id, msg)
	return err
}

func finishOK(ctx context.Context, pool *pgxpool.Pool, id uuid.UUID, m Manifest, now time.Time) error {
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	_, err = pool.Exec(ctx, `UPDATE backup_run SET status = 'ok', finished_at = $2, size_bytes = $3, parts = $4,
		app_version = $5, schema_version = $6, manifest = $7 WHERE id = $1`,
		id, now, m.Size(), m.PartCount(), m.AppVersion, m.SchemaVersion, b)
	return err
}

func finishFailed(ctx context.Context, pool *pgxpool.Pool, id uuid.UUID, msg string, now time.Time) error {
	_, err := pool.Exec(ctx, `UPDATE backup_run SET status = 'failed', finished_at = $2, error = $3 WHERE id = $1`, id, now, msg)
	return err
}

func recentRuns(ctx context.Context, pool *pgxpool.Pool, limit int) ([]Run, error) {
	rows, err := pool.Query(ctx, `SELECT `+runCols+` FROM backup_run ORDER BY started_at DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Run
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// unprunedOlder returns ok runs beyond the newest keep whose messages haven't
// been through retention yet, and failed runs with leftover messages.
func unprunedOlder(ctx context.Context, pool *pgxpool.Pool, keep int) ([]Run, error) {
	rows, err := pool.Query(ctx, `
		SELECT `+runCols+` FROM backup_run
		WHERE pruned_at IS NULL AND cardinality(message_ids) > 0
		  AND (status = 'failed' OR id NOT IN (SELECT id FROM backup_run WHERE status = 'ok' ORDER BY started_at DESC LIMIT $1))
		  AND status <> 'running'
		ORDER BY started_at`, keep)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Run
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func markPruned(ctx context.Context, pool *pgxpool.Pool, id uuid.UUID, note string, now time.Time) error {
	var n *string
	if note != "" {
		n = &note
	}
	_, err := pool.Exec(ctx, `UPDATE backup_run SET pruned_at = $2, delete_note = $3 WHERE id = $1`, id, now, n)
	return err
}

// LastOK is the newest successful run, or nil.
func lastRuns(ctx context.Context, pool *pgxpool.Pool) (last *Run, lastOK *Run, err error) {
	rs, err := recentRuns(ctx, pool, 50)
	if err != nil {
		return nil, nil, err
	}
	for i := range rs {
		if last == nil {
			last = &rs[i]
		}
		if rs[i].Status == "ok" {
			lastOK = &rs[i]
			break
		}
	}
	return last, lastOK, nil
}
