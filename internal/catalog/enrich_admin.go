package catalog

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/a-jay98/stockpot/internal/audit"
	"github.com/a-jay98/stockpot/internal/llm"
	"github.com/a-jay98/stockpot/internal/syncdb"
)

// Errors of the admin ingredient operations.
var (
	ErrIngredientNotFound = errors.New("ingredient not found")
	// ErrNotReviewable: only ingredients the LLM created can be promoted, merged or deleted here.
	ErrNotReviewable   = errors.New("only ingredients created by the enricher can be changed here")
	ErrSlugTaken       = errors.New("a global ingredient with this slug already exists")
	ErrParentNotGlobal = errors.New("the parent ingredient is household-only: promote or merge it first")
	ErrBadMergeTarget  = errors.New("merge target must be another ingredient visible to the same household, not a kind of this one")
)

// EnrichStatus is what Admin → Catalog shows.
type EnrichStatus struct {
	Enabled         bool
	IntervalMinutes int
	// Counts of live products by enrich_state.
	Counts map[string]int
	// Headroom: background LLM work could run now; RetryAt says when quota returns otherwise.
	Headroom       bool
	RetryAt        *time.Time
	Runs           []EnrichRun
	LLMIngredients []ReviewIngredient
	Review         []ReviewProduct
}

// EnrichRun is one enrich_run row.
type EnrichRun struct {
	ID                                    uuid.UUID
	Trigger, Outcome                      string
	StartedAt                             time.Time
	FinishedAt                            *time.Time
	Items, Applied, Failed, Cached, Calls int
	Provider, Reason                      *string
}

// ReviewIngredient is an ingredient the enricher created.
type ReviewIngredient struct {
	ID         uuid.UUID
	Slug, Name string
	Parent     *string
	Household  string
	Products   int
	CreatedAt  time.Time
}

// ReviewProduct is a product whose match is unsure or unresolved.
type ReviewProduct struct {
	ID          uuid.UUID
	Name        string
	Brand       *string
	Household   string
	Ingredient  *string
	MatchSource string
	Confidence  *float32
	State       string
	Attempts    int
}

// Status gathers the page.
func (e *Enricher) Status(ctx context.Context) (EnrichStatus, error) {
	st := EnrichStatus{
		Enabled:         FlagEnrich.Get(e.d.Settings),
		IntervalMinutes: SettingEnrichIntervalMinutes.Get(e.d.Settings),
		Counts:          map[string]int{"pending": 0, "done": 0, "failed": 0, "skipped": 0},
	}
	rows, err := e.d.Pool.Query(ctx, `SELECT enrich_state, count(*) FROM product WHERE deleted_at IS NULL GROUP BY 1`)
	if err != nil {
		return st, err
	}
	for rows.Next() {
		var s string
		var n int
		if err := rows.Scan(&s, &n); err != nil {
			rows.Close()
			return st, err
		}
		st.Counts[s] = n
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return st, err
	}
	ok, retry := e.d.LLM.Headroom(ctx, llm.Needs{})
	st.Headroom = ok
	if !retry.IsZero() {
		st.RetryAt = &retry
	}
	runs, err := e.d.Pool.Query(ctx, `SELECT id, trigger, outcome, started_at, finished_at, items, applied, failed, cached, calls, provider, reason
		FROM enrich_run ORDER BY started_at DESC LIMIT 20`)
	if err != nil {
		return st, err
	}
	for runs.Next() {
		var r EnrichRun
		if err := runs.Scan(&r.ID, &r.Trigger, &r.Outcome, &r.StartedAt, &r.FinishedAt, &r.Items, &r.Applied, &r.Failed, &r.Cached, &r.Calls, &r.Provider, &r.Reason); err != nil {
			runs.Close()
			return st, err
		}
		st.Runs = append(st.Runs, r)
	}
	runs.Close()
	if err := runs.Err(); err != nil {
		return st, err
	}
	ings, err := e.d.Pool.Query(ctx, `
		SELECT i.id, i.slug, i.name, par.name, coalesce(h.name, ''), i.created_at,
		       (SELECT count(*) FROM product p WHERE p.deleted_at IS NULL AND (p.ingredient_id = i.id OR i.id = ANY(p.extra_ingredient_ids)))
		FROM ingredient i LEFT JOIN ingredient par ON par.id = i.parent_id LEFT JOIN household h ON h.id = i.household_id
		WHERE i.source = 'llm' AND i.deleted_at IS NULL ORDER BY i.created_at DESC LIMIT 100`)
	if err != nil {
		return st, err
	}
	for ings.Next() {
		var r ReviewIngredient
		if err := ings.Scan(&r.ID, &r.Slug, &r.Name, &r.Parent, &r.Household, &r.CreatedAt, &r.Products); err != nil {
			ings.Close()
			return st, err
		}
		st.LLMIngredients = append(st.LLMIngredients, r)
	}
	ings.Close()
	if err := ings.Err(); err != nil {
		return st, err
	}
	rev, err := e.d.Pool.Query(ctx, `
		SELECT p.id, p.name, p.brand, coalesce(h.name, ''), i.name, p.match_source, p.match_confidence, p.enrich_state, p.enrich_attempts
		FROM product p JOIN household h ON h.id = p.household_id LEFT JOIN ingredient i ON i.id = p.ingredient_id
		WHERE p.deleted_at IS NULL AND p.match_source <> 'user' AND p.match_source <> 'barcode'
		  AND (p.enrich_state IN ('pending', 'failed') OR p.match_confidence < 0.85)
		ORDER BY (p.enrich_state = 'failed') DESC, p.match_confidence NULLS FIRST, p.created_at DESC LIMIT 50`)
	if err != nil {
		return st, err
	}
	defer rev.Close()
	for rev.Next() {
		var r ReviewProduct
		if err := rev.Scan(&r.ID, &r.Name, &r.Brand, &r.Household, &r.Ingredient, &r.MatchSource, &r.Confidence, &r.State, &r.Attempts); err != nil {
			return st, err
		}
		st.Review = append(st.Review, r)
	}
	return st, rev.Err()
}

// RetryFailed puts failed products back in the queue and returns how many.
func (e *Enricher) RetryFailed(ctx context.Context) (int, error) {
	rows, err := e.d.Pool.Query(ctx, `SELECT DISTINCT household_id FROM product WHERE enrich_state = 'failed' AND deleted_at IS NULL`)
	if err != nil {
		return 0, err
	}
	var hhs []uuid.UUID
	for rows.Next() {
		var h uuid.UUID
		if err := rows.Scan(&h); err != nil {
			rows.Close()
			return 0, err
		}
		hhs = append(hhs, h)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	total := 0
	for _, hh := range hhs {
		if err := e.inHousehold(ctx, hh, func(tx pgx.Tx) error {
			tag, err := tx.Exec(ctx, `UPDATE product SET enrich_state = 'pending', enrich_attempts = 0
				WHERE household_id = $1 AND enrich_state = 'failed' AND deleted_at IS NULL`, hh)
			total += int(tag.RowsAffected())
			return err
		}); err != nil {
			return total, err
		}
	}
	if total > 0 {
		tx, err := e.d.Pool.Begin(ctx)
		if err != nil {
			return total, err
		}
		defer func() { _ = tx.Rollback(ctx) }()
		if err := audit.Record(ctx, tx, "catalog.enrich.retry_failed", audit.Target{Type: "catalog", ID: "enrich"}, nil, map[string]any{"products": total}); err != nil {
			return total, err
		}
		return total, tx.Commit(ctx)
	}
	return 0, nil
}

type reviewed struct {
	id, parent, category *uuid.UUID
	household            uuid.UUID
	slug, name, source   string
}

// loadReviewable reads an enricher-created household ingredient.
func loadReviewable(ctx context.Context, tx pgx.Tx, id uuid.UUID) (r reviewed, err error) {
	var hh *uuid.UUID
	var rid uuid.UUID
	err = tx.QueryRow(ctx, `SELECT id, household_id, slug, name, source, parent_id, category_id FROM ingredient WHERE id = $1 AND deleted_at IS NULL FOR UPDATE`, id).
		Scan(&rid, &hh, &r.slug, &r.name, &r.source, &r.parent, &r.category)
	if errors.Is(err, pgx.ErrNoRows) {
		return r, ErrIngredientNotFound
	}
	if err != nil {
		return r, err
	}
	if hh == nil || r.source != "llm" {
		return r, ErrNotReviewable
	}
	r.id, r.household = &rid, *hh
	return r, nil
}

// owner starts a transaction holding the locks an ingredient change needs.
func (e *Enricher) adminTx(ctx context.Context, owner uuid.UUID, global bool, fn func(tx pgx.Tx) error) error {
	tx, err := e.d.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if global {
		if err := syncdb.LockGlobal(ctx, tx); err != nil {
			return err
		}
	}
	if err := syncdb.LockHousehold(ctx, tx, owner); err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (e *Enricher) ownerOf(ctx context.Context, id uuid.UUID) (uuid.UUID, error) {
	var hh *uuid.UUID
	err := e.d.Pool.QueryRow(ctx, `SELECT household_id FROM ingredient WHERE id = $1 AND deleted_at IS NULL`, id).Scan(&hh)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, ErrIngredientNotFound
	}
	if err != nil {
		return uuid.Nil, err
	}
	if hh == nil {
		return uuid.Nil, ErrNotReviewable
	}
	return *hh, nil
}

// Promote makes an enricher-created ingredient global, so every household
// gets it. The parent must already be global.
func (e *Enricher) Promote(ctx context.Context, id uuid.UUID) error {
	owner, err := e.ownerOf(ctx, id)
	if err != nil {
		return err
	}
	return e.adminTx(ctx, owner, true, func(tx pgx.Tx) error {
		r, err := loadReviewable(ctx, tx, id)
		if err != nil {
			return err
		}
		if r.parent != nil {
			var global bool
			if err := tx.QueryRow(ctx, `SELECT household_id IS NULL FROM ingredient WHERE id = $1`, *r.parent).Scan(&global); err != nil {
				return err
			}
			if !global {
				return ErrParentNotGlobal
			}
		}
		if taken, err := exists(ctx, tx, `SELECT 1 FROM ingredient WHERE household_id IS NULL AND slug = $1`, r.slug); err != nil {
			return err
		} else if taken {
			return ErrSlugTaken
		}
		if _, err := tx.Exec(ctx, `UPDATE ingredient SET household_id = NULL, source = 'admin' WHERE id = $1`, id); err != nil {
			return err
		}
		return audit.Record(ctx, tx, "catalog.ingredient.promote", audit.Target{Type: "ingredient", ID: id.String()}, map[string]any{"household_id": r.household}, map[string]any{"slug": r.slug})
	})
}

// Merge points everything that used an enricher-created ingredient at target
// and removes it. The merged name becomes an alias of the target (unless it
// is a seeded global, which the seed owns), so the same text resolves there next time.
func (e *Enricher) Merge(ctx context.Context, id, target uuid.UUID) error {
	if id == target {
		return ErrBadMergeTarget
	}
	owner, err := e.ownerOf(ctx, id)
	if err != nil {
		return err
	}
	return e.adminTx(ctx, owner, true, func(tx pgx.Tx) error {
		r, err := loadReviewable(ctx, tx, id)
		if err != nil {
			return err
		}
		var tSource string
		var tHH *uuid.UUID
		err = tx.QueryRow(ctx, `SELECT source, household_id FROM ingredient WHERE id = $1 AND deleted_at IS NULL FOR UPDATE`, target).Scan(&tSource, &tHH)
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && tHH != nil && *tHH != owner) {
			return ErrBadMergeTarget
		}
		if err != nil {
			return err
		}
		if under, err := exists(ctx, tx, `WITH RECURSIVE up AS (SELECT id, parent_id FROM ingredient WHERE id = $1
			UNION SELECT i.id, i.parent_id FROM ingredient i JOIN up ON i.id = up.parent_id) SELECT 1 FROM up WHERE id = $2`, target, id); err != nil {
			return err
		} else if under {
			return ErrBadMergeTarget
		}
		if _, err := tx.Exec(ctx, `UPDATE product SET ingredient_id = $2 WHERE ingredient_id = $1`, id, target); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE product SET extra_ingredient_ids = ARRAY(SELECT DISTINCT CASE WHEN x = $1 THEN $2 ELSE x END FROM unnest(extra_ingredient_ids) x)
			WHERE $1 = ANY(extra_ingredient_ids)`, id, target); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE ingredient SET parent_id = $2 WHERE parent_id = $1 AND deleted_at IS NULL`, id, target); err != nil {
			return err
		}
		if tSource != "seed" {
			if _, err := tx.Exec(ctx, `UPDATE ingredient SET aliases = ARRAY(SELECT DISTINCT unnest(aliases || $2::text[])) WHERE id = $1`,
				target, normalizedAliases([]string{r.name, r.slug})); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE ingredient SET deleted_at = now() WHERE id = $1`, id); err != nil {
			return err
		}
		return audit.Record(ctx, tx, "catalog.ingredient.merge", audit.Target{Type: "ingredient", ID: id.String()}, map[string]any{"slug": r.slug}, map[string]any{"into": target})
	})
}

// DeleteIngredient removes an enricher-created ingredient; products using it
// go back to the queue, and its kinds move up to its parent.
func (e *Enricher) DeleteIngredient(ctx context.Context, id uuid.UUID) error {
	owner, err := e.ownerOf(ctx, id)
	if err != nil {
		return err
	}
	return e.adminTx(ctx, owner, false, func(tx pgx.Tx) error {
		r, err := loadReviewable(ctx, tx, id)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE product SET ingredient_id = NULL, match_source = 'none', match_confidence = NULL, enrich_state = 'pending', enrich_attempts = 0
			WHERE ingredient_id = $1`, id); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE product SET extra_ingredient_ids = array_remove(extra_ingredient_ids, $1) WHERE $1 = ANY(extra_ingredient_ids)`, id); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE ingredient SET parent_id = $2 WHERE parent_id = $1 AND deleted_at IS NULL`, id, r.parent); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE ingredient SET deleted_at = now() WHERE id = $1`, id); err != nil {
			return err
		}
		return audit.Record(ctx, tx, "catalog.ingredient.delete", audit.Target{Type: "ingredient", ID: id.String()}, map[string]any{"slug": r.slug}, nil)
	})
}
