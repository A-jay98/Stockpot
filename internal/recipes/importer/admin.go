package importer

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/a-jay98/stockpot/internal/audit"
	"github.com/a-jay98/stockpot/internal/jobs"
)

// AdminStatus is Admin → Recipes.
type AdminStatus struct {
	WebImport, Instagram bool
	// Counts of imports by status created in the last 24 h and 14 days.
	Last24h, Last14d map[string]int
	Failures         []AdminImport
	Fetches          []AdminFetch
	Tools            ToolStatus
	Transcription    bool
}

// AdminImport is an import that needs attention.
type AdminImport struct {
	ID        uuid.UUID
	Household string
	Kind      string
	Input     *string
	Status    string
	Code      string
	Detail    string
	Attempts  int
	UpdatedAt time.Time
}

// AdminFetch is one recipe_fetch row.
type AdminFetch struct {
	At         time.Time
	Purpose    string
	URL        string
	FinalURL   *string
	Status     *int
	Bytes      int64
	DurationMs int
	Error      *string
}

// ErrImportNotFound means no live import has that id.
var ErrImportNotFound = errors.New("recipe import not found")

// AdminStatus gathers the page.
func (s *Service) AdminStatus(ctx context.Context) (AdminStatus, error) {
	st := AdminStatus{
		WebImport: FlagWebImport.Get(s.d.Settings), Instagram: FlagInstagram.Get(s.d.Settings),
		Last24h: map[string]int{}, Last14d: map[string]int{}, Failures: []AdminImport{}, Fetches: []AdminFetch{},
	}
	now := s.d.Now()
	rows, err := s.d.Pool.Query(ctx, `SELECT status, count(*) FILTER (WHERE created_at >= $1), count(*)
		FROM recipe_import WHERE created_at >= $2 AND deleted_at IS NULL GROUP BY status`, now.Add(-24*time.Hour), now.Add(-14*24*time.Hour))
	if err != nil {
		return st, err
	}
	for rows.Next() {
		var status string
		var day, fortnight int
		if err := rows.Scan(&status, &day, &fortnight); err != nil {
			rows.Close()
			return st, err
		}
		if day > 0 {
			st.Last24h[status] = day
		}
		st.Last14d[status] = fortnight
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return st, err
	}

	rows, err = s.d.Pool.Query(ctx, `SELECT i.id, h.name, i.kind, i.input, i.status, i.error, i.attempts, i.updated_at
		FROM recipe_import i JOIN household h ON h.id = i.household_id
		WHERE i.status IN ('failed', 'needs_input', 'waiting_quota') AND i.deleted_at IS NULL
		ORDER BY i.updated_at DESC LIMIT 30`)
	if err != nil {
		return st, err
	}
	for rows.Next() {
		var a AdminImport
		var raw []byte
		if err := rows.Scan(&a.ID, &a.Household, &a.Kind, &a.Input, &a.Status, &raw, &a.Attempts, &a.UpdatedAt); err != nil {
			rows.Close()
			return st, err
		}
		var p problem
		if json.Unmarshal(raw, &p) == nil {
			a.Code, a.Detail = p.Code, p.Detail
		}
		if a.Kind == KindText && a.Input != nil { // pasted text is household content: show its start only
			short := truncate(*a.Input, 60)
			a.Input = &short
		}
		st.Failures = append(st.Failures, a)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return st, err
	}

	rows, err = s.d.Pool.Query(ctx, `SELECT created_at, purpose, url, final_url, status, bytes, duration_ms, error
		FROM recipe_fetch ORDER BY id DESC LIMIT 50`)
	if err != nil {
		return st, err
	}
	for rows.Next() {
		var f AdminFetch
		if err := rows.Scan(&f.At, &f.Purpose, &f.URL, &f.FinalURL, &f.Status, &f.Bytes, &f.DurationMs, &f.Error); err != nil {
			rows.Close()
			return st, err
		}
		st.Fetches = append(st.Fetches, f)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return st, err
	}
	if s.d.Reels != nil {
		st.Tools = s.d.Reels.Status(ctx)
	}
	st.Transcription = s.d.TranscriptionAvailable == nil || s.d.TranscriptionAvailable(ctx)
	return st, nil
}

// AdminRetry requeues a failed, blocked or waiting import (audited).
func (s *Service) AdminRetry(ctx context.Context, id uuid.UUID) error {
	return pgx.BeginFunc(ctx, s.d.Pool, func(tx pgx.Tx) error {
		var status string
		err := tx.QueryRow(ctx, `SELECT status FROM recipe_import WHERE id = $1 AND deleted_at IS NULL FOR UPDATE`, id).Scan(&status)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrImportNotFound
		}
		if err != nil {
			return err
		}
		if !retriable(status) {
			return nil
		}
		if _, err := tx.Exec(ctx, `UPDATE recipe_import SET status = 'queued', error = NULL, attempts = 0 WHERE id = $1`, id); err != nil {
			return err
		}
		if err := s.enqueue(ctx, tx, id, false); err != nil {
			return err
		}
		return audit.Record(ctx, tx, "recipe.import.admin_retry", audit.Target{Type: "recipe_import", ID: id.String()},
			map[string]any{"status": status}, map[string]any{"status": StatusQueued})
	})
}

// Sweepers prune the fetch log.
func (s *Service) Sweepers() []jobs.Sweeper {
	const batch = 5000
	return []jobs.Sweeper{{
		Name: "recipe_fetch",
		Days: SettingFetchRetentionDays,
		Sweep: func(ctx context.Context, before time.Time) (int64, error) {
			return jobs.SweepInBatches(ctx, batch, func(ctx context.Context) (int64, error) {
				tag, err := s.d.Pool.Exec(ctx, `DELETE FROM recipe_fetch WHERE id IN (SELECT id FROM recipe_fetch WHERE created_at < $1 LIMIT $2)`, before, batch)
				return tag.RowsAffected(), err
			})
		},
	}}
}
