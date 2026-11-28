package recipes

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/a-jay98/stockpot/internal/syncdb"
)

// Substitution sources.
const (
	SubSeed = "seed"
	SubLLM  = "llm"
	SubUser = "user"
)

//go:embed seed/substitutions.json
var substitutionsJSON []byte

// SeedUse is one ingredient a seeded swap uses, by slug. Ratio is the amount
// per unit of the missing ingredient, in the recipe line's unit unless Unit
// says otherwise.
type SeedUse struct {
	Slug  string  `json:"slug"`
	Ratio float64 `json:"ratio"`
	Unit  string  `json:"unit,omitempty"`
}

// SeedSubstitution is one entry of seed/substitutions.json.
type SeedSubstitution struct {
	Missing string    `json:"missing"`
	Uses    []SeedUse `json:"uses"`
	Note    string    `json:"note"`
}

// Use is one element of substitution.uses as stored (and synced).
type Use struct {
	IngredientID uuid.UUID `json:"ingredient_id"`
	Ratio        float64   `json:"ratio"`
	Unit         string    `json:"unit,omitempty"`
	Note         string    `json:"note,omitempty"`
}

// SeedSubstitutions returns the embedded global swap seed.
func SeedSubstitutions() ([]SeedSubstitution, error) {
	var out []SeedSubstitution
	if err := json.Unmarshal(substitutionsJSON, &out); err != nil {
		return nil, fmt.Errorf("substitution seed: %w", err)
	}
	return out, nil
}

// seedNamespace makes seeded rows' ids stable across deploys, so reseeding
// updates rows in place instead of duplicating them.
var seedNamespace = uuid.MustParse("4f5b0f5e-2a51-4c55-9a0e-6d5f3b8a9c10")

// Key identifies a seeded swap: what is missing and what it uses.
func (s SeedSubstitution) Key() string {
	slugs := make([]string, len(s.Uses))
	for i, u := range s.Uses {
		slugs[i] = u.Slug
	}
	return s.Missing + "<-" + strings.Join(slugs, "+")
}

// SeedSubstitutionRows upserts the global swap seed after the ingredient seed
// (slugs resolve to global ingredients). Rows are keyed by a name-based UUID
// of Key, so it is idempotent; unchanged rows are not touched (rev doesn't
// churn) and swaps removed from the file are tombstoned.
func SeedSubstitutionRows(ctx context.Context, pool *pgxpool.Pool) error {
	items, err := SeedSubstitutions()
	if err != nil {
		return err
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := syncdb.LockGlobal(ctx, tx); err != nil {
		return err
	}
	ids, err := globalSlugs(ctx, tx)
	if err != nil {
		return err
	}
	keep := make([]uuid.UUID, 0, len(items))
	for _, it := range items {
		missing, ok := ids[it.Missing]
		if !ok {
			return fmt.Errorf("substitution seed %s: unknown ingredient %q", it.Key(), it.Missing)
		}
		uses := make([]Use, 0, len(it.Uses))
		for _, u := range it.Uses {
			id, ok := ids[u.Slug]
			if !ok {
				return fmt.Errorf("substitution seed %s: unknown ingredient %q", it.Key(), u.Slug)
			}
			uses = append(uses, Use{IngredientID: id, Ratio: u.Ratio, Unit: u.Unit})
		}
		usesJSON, _ := json.Marshal(uses) // plain data
		id := uuid.NewSHA1(seedNamespace, []byte(it.Key()))
		keep = append(keep, id)
		if _, err := tx.Exec(ctx, `
			INSERT INTO substitution (id, household_id, ingredient_id, uses, note, source)
			VALUES ($1, NULL, $2, $3, $4, 'seed')
			ON CONFLICT (id) DO UPDATE SET ingredient_id = EXCLUDED.ingredient_id, uses = EXCLUDED.uses, note = EXCLUDED.note, deleted_at = NULL
			WHERE (substitution.ingredient_id, substitution.uses, substitution.note, substitution.deleted_at)
			      IS DISTINCT FROM (EXCLUDED.ingredient_id, EXCLUDED.uses, EXCLUDED.note, NULL::timestamptz)`,
			id, missing, usesJSON, it.Note); err != nil {
			return fmt.Errorf("seed substitution %s: %w", it.Key(), err)
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE substitution SET deleted_at = now()
		WHERE household_id IS NULL AND source = 'seed' AND deleted_at IS NULL AND NOT (id = ANY($1))`, keep); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func globalSlugs(ctx context.Context, tx pgx.Tx) (map[string]uuid.UUID, error) {
	rows, err := tx.Query(ctx, `SELECT slug, id FROM ingredient WHERE household_id IS NULL AND deleted_at IS NULL`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]uuid.UUID{}
	for rows.Next() {
		var s string
		var id uuid.UUID
		if err := rows.Scan(&s, &id); err != nil {
			return nil, err
		}
		out[s] = id
	}
	return out, rows.Err()
}
