package reports

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/a-jay98/stockpot/internal/audit"
	"github.com/a-jay98/stockpot/internal/llm"
	"github.com/a-jay98/stockpot/internal/reqctx"
	"github.com/a-jay98/stockpot/internal/syncdb"
	"github.com/a-jay98/stockpot/internal/units"
)

// Report is one row of Admin → Reports.
type Report struct {
	ID            uuid.UUID
	CreatedAt     time.Time
	Household     string
	Reporter      *string
	Kind, Source  string
	Status        string
	Suggested     *string
	Note          *string
	Context       Context
	Attempts      int
	LLMVerdict    *string
	LLMValue      *string
	LLMReason     *string
	LLMConfidence *float32
	LLMAt         *time.Time
	Applied       *Change
	ResolvedBy    *string
	ResolvedAt    *time.Time
	AdminNote     *string
}

// Overview is the page.
type Overview struct {
	Enabled, AutoApply bool
	IntervalMinutes    int
	Counts             map[string]int
	Headroom           bool
	RetryAt            *time.Time
	Reports            []Report
}

// List returns the latest reports, all or of one status, with the counts.
func (s *Service) List(ctx context.Context, status string, limit int) (Overview, error) {
	o := Overview{
		Enabled: FlagReview.Get(s.d.Settings), AutoApply: FlagAutoApply.Get(s.d.Settings),
		IntervalMinutes: SettingReviewIntervalMinutes.Get(s.d.Settings),
		Counts:          map[string]int{StatusOpen: 0, StatusNeedsAdmin: 0, StatusApplied: 0, StatusRejected: 0, StatusDismissed: 0},
		Reports:         []Report{},
	}
	rows, err := s.d.Pool.Query(ctx, `SELECT status, count(*) FROM problem_report GROUP BY 1`)
	if err != nil {
		return o, err
	}
	for rows.Next() {
		var st string
		var n int
		if err := rows.Scan(&st, &n); err != nil {
			rows.Close()
			return o, err
		}
		o.Counts[st] = n
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return o, err
	}
	ok, retry := s.d.LLM.Headroom(ctx, llm.Needs{})
	o.Headroom = ok
	if !retry.IsZero() {
		o.RetryAt = &retry
	}
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	q := `SELECT r.id, r.created_at, h.name, u.display_name, r.kind, r.source, r.status, r.suggested, r.note, r.context, r.attempts,
		r.llm_verdict, r.llm_value, r.llm_reason, r.llm_confidence, r.llm_at, r.applied, r.resolved_by, r.resolved_at, r.admin_note
		FROM problem_report r JOIN household h ON h.id = r.household_id LEFT JOIN app_user u ON u.id = r.reporter_id
		WHERE ($1 = '' OR r.status = $1)
		ORDER BY (r.status = 'needs_admin') DESC, r.created_at DESC LIMIT $2`
	rows, err = s.d.Pool.Query(ctx, q, status, limit)
	if err != nil {
		return o, err
	}
	defer rows.Close()
	for rows.Next() {
		var r Report
		var rawCtx, rawApplied []byte
		if err := rows.Scan(&r.ID, &r.CreatedAt, &r.Household, &r.Reporter, &r.Kind, &r.Source, &r.Status, &r.Suggested, &r.Note, &rawCtx, &r.Attempts,
			&r.LLMVerdict, &r.LLMValue, &r.LLMReason, &r.LLMConfidence, &r.LLMAt, &rawApplied, &r.ResolvedBy, &r.ResolvedAt, &r.AdminNote); err != nil {
			return o, err
		}
		_ = json.Unmarshal(rawCtx, &r.Context)
		if rawApplied != nil {
			var ch Change
			if json.Unmarshal(rawApplied, &ch) == nil {
				r.Applied = &ch
			}
		}
		o.Reports = append(o.Reports, r)
	}
	return o, rows.Err()
}

// Admin actions on a report.
const (
	ActionApply   = "apply"   // wrong_unit: set the default to Value
	ActionReject  = "reject"  // the current value is right
	ActionDismiss = "dismiss" // close without a change (fixed elsewhere, spam, …)
	ActionReopen  = "reopen"  // back to the LLM queue
)

// Resolve applies an admin decision and returns the report's new status.
func (s *Service) Resolve(ctx context.Context, id uuid.UUID, action, value, note string) error {
	note, ok := clean(note, maxNote)
	if !ok {
		return ErrInvalid
	}
	value = strings.TrimSpace(value)
	by := "admin"
	if info := reqctx.From(ctx); info != nil && info.UserID != uuid.Nil {
		by = info.UserID.String()
	}
	return s.tx(ctx, func(tx pgxTx) error {
		var kind, status string
		var hh uuid.UUID
		err := tx.QueryRow(ctx, `SELECT kind, status, household_id FROM problem_report WHERE id = $1 FOR UPDATE`, id).Scan(&kind, &status, &hh)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		var applied *Change
		next := ""
		switch action {
		case ActionApply:
			if kind != KindWrongUnit {
				return ErrBadAction
			}
			if !units.Valid(value) {
				return ErrBadUnit
			}
			ch, err := s.applyUnit(ctx, tx, id, value, by)
			if err != nil {
				return err
			}
			applied, next = &ch, StatusApplied
		case ActionReject:
			next = StatusRejected
		case ActionDismiss:
			next = StatusDismissed
		case ActionReopen:
			if _, err := tx.Exec(ctx, `UPDATE problem_report SET status = 'open', attempts = 0, resolved_by = NULL, resolved_at = NULL,
				admin_note = coalesce($2, admin_note), updated_at = now() WHERE id = $1`, id, nullIfEmpty(note)); err != nil {
				return err
			}
			return audit.Record(ctx, tx, "report.reopen", audit.Target{Type: "problem_report", ID: id.String()}, map[string]any{"status": status}, nil)
		default:
			return ErrBadAction
		}
		if _, err := tx.Exec(ctx, `UPDATE problem_report SET status = $2, applied = coalesce($3, applied), resolved_by = $4, resolved_at = now(),
			admin_note = coalesce($5, admin_note), updated_at = now() WHERE id = $1`, id, next, applied, by, nullIfEmpty(note)); err != nil {
			return err
		}
		return audit.Record(ctx, tx, "report."+action, audit.Target{Type: "problem_report", ID: id.String()},
			map[string]any{"status": status}, map[string]any{"status": next, "value": value, "note": note})
	})
}

// DefaultRow is one ingredient of Admin → Defaults.
type DefaultRow struct {
	ID            uuid.UUID
	Slug, Name    string
	Household     *string // nil: global
	Parent        *string
	Unit          *string
	InheritedUnit *string
	Source        string
	Products      int
	OpenReports   int
}

// DefaultChange is one entry of the defaults history.
type DefaultChange struct {
	Time       time.Time
	TargetType string
	TargetID   string
	Name       string
	Before     *string
	After      *string
	Actor      string
	ReportID   *string
}

// Defaults lists ingredients (global and household) and their default unit,
// matching q by name or slug, and the latest default changes.
func (s *Service) Defaults(ctx context.Context, q string, limit int) ([]DefaultRow, []DefaultChange, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	q = strings.TrimSpace(q)
	rows, err := s.d.Pool.Query(ctx, `
		SELECT i.id, i.slug, i.name, h.name, par.name, i.default_unit,
		       (WITH RECURSIVE up AS (SELECT parent_id, 1 AS depth FROM ingredient WHERE id = i.id
		          UNION ALL SELECT x.parent_id, up.depth + 1 FROM ingredient x JOIN up ON x.id = up.parent_id WHERE up.depth < 10)
		        SELECT a.default_unit FROM up JOIN ingredient a ON a.id = up.parent_id WHERE a.default_unit IS NOT NULL ORDER BY up.depth LIMIT 1),
		       i.source,
		       (SELECT count(*) FROM product p WHERE p.ingredient_id = i.id AND p.deleted_at IS NULL),
		       (SELECT count(*) FROM problem_report r WHERE r.ingredient_id = i.id AND r.status IN ('open', 'needs_admin'))
		FROM ingredient i LEFT JOIN household h ON h.id = i.household_id LEFT JOIN ingredient par ON par.id = i.parent_id
		WHERE i.deleted_at IS NULL AND ($1 = '' OR i.name ILIKE '%' || $1 || '%' OR i.slug ILIKE '%' || $1 || '%')
		ORDER BY (SELECT count(*) FROM problem_report r WHERE r.ingredient_id = i.id AND r.status IN ('open', 'needs_admin')) DESC,
		         i.household_id IS NOT NULL, i.name LIMIT $2`, q, limit)
	if err != nil {
		return nil, nil, err
	}
	var out []DefaultRow
	for rows.Next() {
		var r DefaultRow
		if err := rows.Scan(&r.ID, &r.Slug, &r.Name, &r.Household, &r.Parent, &r.Unit, &r.InheritedUnit, &r.Source, &r.Products, &r.OpenReports); err != nil {
			rows.Close()
			return nil, nil, err
		}
		out = append(out, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	hist, err := s.history(ctx, 50)
	return out, hist, err
}

func (s *Service) history(ctx context.Context, n int) ([]DefaultChange, error) {
	rows, err := s.d.Pool.Query(ctx, `
		SELECT a.time, a.target_type, a.target_id, coalesce(i.name, p.name, ''), a.before->>'default_unit', a.after->>'default_unit',
		       coalesce(a.after->>'actor', u.display_name, 'admin'), a.after->>'report_id'
		FROM audit_event a
		LEFT JOIN ingredient i ON a.target_type = 'ingredient' AND i.id::text = a.target_id
		LEFT JOIN product p ON a.target_type = 'product' AND p.id::text = a.target_id
		LEFT JOIN app_user u ON u.id = a.actor_id
		WHERE a.action = $1 ORDER BY a.time DESC, a.id DESC LIMIT $2`, ActionDefaultUpdate, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []DefaultChange{}
	for rows.Next() {
		var c DefaultChange
		if err := rows.Scan(&c.Time, &c.TargetType, &c.TargetID, &c.Name, &c.Before, &c.After, &c.Actor, &c.ReportID); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// SetDefaultUnit is the admin's edit of an ingredient's default unit (nil or
// "" clears it, so the parent's applies).
func (s *Service) SetDefaultUnit(ctx context.Context, id uuid.UUID, unit *string) error {
	if unit != nil && *unit == "" {
		unit = nil
	}
	if unit != nil && !units.Valid(*unit) {
		return ErrBadUnit
	}
	var owner *uuid.UUID
	err := s.d.Pool.QueryRow(ctx, `SELECT household_id FROM ingredient WHERE id = $1 AND deleted_at IS NULL`, id).Scan(&owner)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	return s.tx(ctx, func(tx pgxTx) error {
		if owner == nil {
			if err := syncdb.LockGlobal(ctx, tx); err != nil {
				return err
			}
		} else if err := syncdb.LockHousehold(ctx, tx, *owner); err != nil {
			return err
		}
		_, err := setIngredientUnit(ctx, tx, id, unit, nil)
		return err
	})
}
