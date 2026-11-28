package catalog

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/a-jay98/stockpot/internal/syncdb"
)

//go:embed seed/ingredients.json
var seedJSON []byte

// SeedIngredient is one entry of seed/ingredients.json.
type SeedIngredient struct {
	Slug        string         `json:"slug"`
	Name        string         `json:"name"`
	Plural      string         `json:"plural"`
	Parent      *string        `json:"parent"`
	Category    string         `json:"category"`
	Icon        *string        `json:"icon"`
	Aliases     []string       `json:"aliases"`
	ShelfLife   map[string]int `json:"shelf_life"`
	DefaultUnit *string        `json:"default_unit"`
	// Conversions bridge dimensions for recipes ("ea": "150 g"); see units.Convert.
	Conversions map[string]string `json:"conversions"`
}

// SeedIngredients returns the embedded global ingredient seed.
func SeedIngredients() ([]SeedIngredient, error) {
	var out []SeedIngredient
	if err := json.Unmarshal(seedJSON, &out); err != nil {
		return nil, fmt.Errorf("catalog seed: %w", err)
	}
	return out, nil
}

// normalizedAliases stores aliases in the form the resolver compares (see Normalize).
func normalizedAliases(in []string) []string {
	out := make([]string, 0, len(in))
	seen := map[string]bool{}
	for _, a := range in {
		if n := Normalize(a); n != "" && !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	return out
}

// Seed upserts the global ingredient seed by slug. Entries removed from the
// file stay in the database (households may reference them). It is idempotent: rows
// that already match are not touched (so rev doesn't churn on every
// migrate), rows an admin edited (source != 'seed') are left alone, and no
// household row is ever changed or deleted.
func Seed(ctx context.Context, pool *pgxpool.Pool) error {
	items, err := SeedIngredients()
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
	if err := seedTx(ctx, tx, items); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

const zeroUUID = "'00000000-0000-0000-0000-000000000000'::uuid"

// unitEdited is true for a seed ingredient whose default_unit an admin or an
// applied problem report changed (audited as reports.ActionDefaultUpdate):
// startup seeding keeps that value instead of resetting it to the seed file's.
const unitEdited = `EXISTS (SELECT 1 FROM audit_event a WHERE a.action = 'catalog.default.update'
	AND a.target_type = 'ingredient' AND a.target_id = ingredient.id::text)`

func seedTx(ctx context.Context, tx pgx.Tx, items []SeedIngredient) error {
	for _, it := range items {
		shelf, _ := json.Marshal(it.ShelfLife)
		aliases := normalizedAliases(it.Aliases)
		conv := it.Conversions
		if conv == nil {
			conv = map[string]string{}
		}
		convJSON, _ := json.Marshal(conv)
		_, err := tx.Exec(ctx, `
			INSERT INTO ingredient (household_id, slug, name, plural, category_id, icon, aliases, shelf_life, default_unit, conversions, source)
			VALUES (NULL, $1, $2, $3, (SELECT id FROM category WHERE household_id IS NULL AND slug = $4), $5, $6, $7, $8, $9, 'seed')
			ON CONFLICT ((coalesce(household_id, `+zeroUUID+`)), slug) DO UPDATE
			SET name = EXCLUDED.name, plural = EXCLUDED.plural, category_id = EXCLUDED.category_id, icon = EXCLUDED.icon,
			    aliases = EXCLUDED.aliases, shelf_life = EXCLUDED.shelf_life,
			    default_unit = CASE WHEN `+unitEdited+` THEN ingredient.default_unit ELSE EXCLUDED.default_unit END,
			    conversions = EXCLUDED.conversions
			WHERE ingredient.source = 'seed' AND ingredient.deleted_at IS NULL
			  AND (ingredient.name, ingredient.plural, ingredient.category_id, ingredient.icon, ingredient.aliases, ingredient.shelf_life,
			       CASE WHEN `+unitEdited+` THEN ingredient.default_unit ELSE EXCLUDED.default_unit END, ingredient.conversions)
			      IS DISTINCT FROM (EXCLUDED.name, EXCLUDED.plural, EXCLUDED.category_id, EXCLUDED.icon, EXCLUDED.aliases, EXCLUDED.shelf_life,
			       ingredient.default_unit, EXCLUDED.conversions)`,
			it.Slug, it.Name, it.Plural, it.Category, it.Icon, aliases, shelf, it.DefaultUnit, convJSON)
		if err != nil {
			return fmt.Errorf("seed ingredient %s: %w", it.Slug, err)
		}
	}
	// Parents second, so order in the file doesn't matter.
	for _, it := range items {
		if it.Parent == nil {
			if _, err := tx.Exec(ctx, `UPDATE ingredient SET parent_id = NULL
				WHERE household_id IS NULL AND slug = $1 AND source = 'seed' AND parent_id IS NOT NULL`, it.Slug); err != nil {
				return fmt.Errorf("seed parent %s: %w", it.Slug, err)
			}
			continue
		}
		if _, err := tx.Exec(ctx, `
			UPDATE ingredient c SET parent_id = p.id
			FROM ingredient p
			WHERE c.household_id IS NULL AND c.slug = $1 AND c.source = 'seed'
			  AND p.household_id IS NULL AND p.slug = $2
			  AND c.parent_id IS DISTINCT FROM p.id`, it.Slug, *it.Parent); err != nil {
			return fmt.Errorf("seed parent %s: %w", it.Slug, err)
		}
	}
	return nil
}
