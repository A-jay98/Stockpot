package catalog

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/a-jay98/stockpot/internal/catalog/resolve"
	"github.com/a-jay98/stockpot/internal/db/dbq"
)

// MatchProduct is an in-stock product that matches an ingredient.
type MatchProduct struct {
	ID    uuid.UUID
	Name  string
	Brand *string
}

// MatchResult is the answer of GET /v1/catalog/match.
type MatchResult struct {
	// Ingredient is what the text resolved to; nil when nothing matched.
	Ingredient *resolve.Match
	Products   []MatchProduct
}

// ProductsMatching lists the household's products in stock that satisfy a
// recipe asking for ingredientID: the product's ingredient (or one of its
// extra ingredients) is that ingredient or a kind of it. Hierarchy is "is a
// kind of": clementine → mandarin, so a recipe needing mandarin matches
// clementines, never the other way round.
func ProductsMatching(ctx context.Context, q dbq.DBTX, household, ingredientID uuid.UUID) ([]MatchProduct, error) {
	rows, err := q.Query(ctx, `
		WITH RECURSIVE kinds AS (
			SELECT id FROM ingredient WHERE id = $2 AND deleted_at IS NULL
			UNION
			SELECT i.id FROM ingredient i JOIN kinds k ON i.parent_id = k.id
			WHERE i.deleted_at IS NULL AND (i.household_id = $1 OR i.household_id IS NULL)
		)
		SELECT p.id, p.name, p.brand FROM product p
		WHERE p.household_id = $1 AND p.deleted_at IS NULL
		  AND (p.ingredient_id IN (SELECT id FROM kinds) OR p.extra_ingredient_ids && ARRAY(SELECT id FROM kinds))
		  AND EXISTS (SELECT 1 FROM stock_item s WHERE s.product_id = p.id AND s.deleted_at IS NULL AND (s.quantity IS NULL OR s.quantity > 0) AND (s.amount_kind <> 'portions' OR s.portions > 0))
		ORDER BY lower(p.name), p.id`, household, ingredientID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []MatchProduct{}
	for rows.Next() {
		var m MatchProduct
		if err := rows.Scan(&m.ID, &m.Name, &m.Brand); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// MatchText resolves text to an ingredient and lists the in-stock products that
// match it.
func MatchText(ctx context.Context, q dbq.DBTX, res Resolver, household uuid.UUID, text string) (MatchResult, error) {
	m, err := res.Resolve(ctx, q, household, text, nil)
	if err != nil || m == nil {
		return MatchResult{Products: []MatchProduct{}}, err
	}
	ps, err := ProductsMatching(ctx, q, household, m.IngredientID)
	return MatchResult{Ingredient: m, Products: ps}, err
}

// Matcher serves GET /v1/catalog/match.
type Matcher struct {
	Pool *pgxpool.Pool
	Res  Resolver
}

// Match resolves text for the household and finds matching stock.
func (m Matcher) Match(ctx context.Context, household uuid.UUID, text string) (MatchResult, error) {
	return MatchText(ctx, m.Pool, m.Res, household, text)
}
