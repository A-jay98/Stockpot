package catalog

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/a-jay98/stockpot/internal/commands"
	"github.com/a-jay98/stockpot/internal/identity"
	"github.com/a-jay98/stockpot/internal/units"
)

// ProductSetConversion is catalog.product.set_conversion: one of the
// product's cross-dimension conversions ("1 ea = 150 g"), or remove it with
// a null amount. It beats the ingredient's conversion for the same unit.
type ProductSetConversion struct {
	ID     uuid.UUID `json:"id"`
	Unit   string    `json:"unit"`
	Amount *string   `json:"amount"`
}

func (h *handlers) productSetConversion(ctx context.Context, tx pgx.Tx, p ProductSetConversion) error {
	hh, err := identity.HouseholdID(ctx)
	if err != nil {
		return err
	}
	if !units.Valid(p.Unit) {
		return commands.Invalid("unknown unit %q", p.Unit)
	}
	var tag pgconn.CommandTag
	if p.Amount == nil {
		tag, err = tx.Exec(ctx, `UPDATE product SET conversions = conversions - $3::text
			WHERE id = $1 AND household_id = $2 AND deleted_at IS NULL`, p.ID, hh, p.Unit)
	} else {
		q, u, ok := units.ParseAmount(*p.Amount)
		switch {
		case !ok:
			return commands.Invalid(`amount is a quantity and a unit, like "150 g"`)
		case units.DimensionOf(u) == units.Count:
			return commands.Invalid("convert to a mass or volume")
		case units.DimensionOf(u) == units.DimensionOf(p.Unit):
			return commands.Invalid("%s and %s already convert", p.Unit, u)
		}
		val, _ := json.Marshal(map[string]string{p.Unit: units.Format(q, 3) + " " + u})
		tag, err = tx.Exec(ctx, `UPDATE product SET conversions = conversions || $3::jsonb
			WHERE id = $1 AND household_id = $2 AND deleted_at IS NULL`, p.ID, hh, val)
	}
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return commands.Gone("product no longer exists")
	}
	return nil
}
