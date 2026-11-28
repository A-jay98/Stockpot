package catalog

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/a-jay98/stockpot/internal/units"
)

// FillBuyUnits gives the product's open shopping items that have no unit the
// unit it is usually bought in: the product's own default (hand-picked), else
// its usual purchase unit, else its ingredient's (or the nearest ancestor's)
// default_unit, which is how the ingredient is bought ("salt" -> bag).
// A count-like unit with no quantity gets 1, so the list shows "bag", "2× bag".
// Items the user already gave a unit keep it.
func FillBuyUnits(ctx context.Context, tx pgx.Tx, product uuid.UUID) error {
	_, err := tx.Exec(ctx, `
		WITH RECURSIVE chain (id, parent_id, unit, depth) AS (
			SELECT i.id, i.parent_id, i.default_unit, 0
			FROM product p JOIN ingredient i ON i.id = p.ingredient_id
			WHERE p.id = $1
			UNION ALL
			SELECT i.id, i.parent_id, i.default_unit, c.depth + 1
			FROM ingredient i JOIN chain c ON i.id = c.parent_id
			WHERE c.unit IS NULL AND c.depth < 8
		), pick AS (
			SELECT u.unit, u.qty FROM (
				SELECT p.default_unit AS unit, p.default_qty AS qty, 0 AS rank FROM product p WHERE p.id = $1
				UNION ALL
				SELECT s.typical_unit, s.typical_qty, 1 FROM product_stat s
				WHERE s.product_id = $1 AND s.store_id IS NULL AND s.deleted_at IS NULL
				UNION ALL
				SELECT c.unit, NULL, 2 + c.depth FROM chain c
			) u
			WHERE u.unit IS NOT NULL
			ORDER BY u.rank LIMIT 1
		)
		UPDATE shopping_item s
		SET unit = pick.unit,
		    qty = coalesce(s.qty, pick.qty, CASE WHEN pick.unit = ANY ($2) THEN 1 END)
		FROM pick
		WHERE s.product_id = $1 AND s.unit IS NULL AND s.checked_at IS NULL AND s.deleted_at IS NULL`,
		product, units.OfDimension(units.Count))
	return err
}
