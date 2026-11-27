// Package inventory is the stock side of the catalog: commands that change
// what the household has, shelf-life rules that estimate expiry, and the
// purchase history behind autocomplete.
package inventory

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Shelf-life keys, in days. They match ingredient.shelf_life and food_rule.
const (
	KeyFridge       = "fridge"
	KeyFreezer      = "freezer"
	KeyPantry       = "pantry"
	KeyOpenedFridge = "opened_fridge"
	KeyThawedFridge = "thawed_fridge"
)

// ShelfKeys lists every shelf-life key.
var ShelfKeys = []string{KeyFridge, KeyFreezer, KeyPantry, KeyOpenedFridge, KeyThawedFridge}

// ShelfLife maps a key to days. A missing key is unknown.
type ShelfLife map[string]int

// IngredientLayer is what is known about one ingredient on the way up the
// "is a kind of" chain: the household's own rule first, then the seed's.
type IngredientLayer struct {
	Household ShelfLife `json:"household"`
	Seed      ShelfLife `json:"seed"`
}

// Chain is everything an estimate needs about one product, most specific
// first. Days resolves a key through it:
// product override → household ingredient rules (self, then ancestors) →
// household category rule → seed ingredient values → Stockpot category default.
type Chain struct {
	Product           ShelfLife         `json:"product"`
	Ingredients       []IngredientLayer `json:"ingredients"`
	CategoryHousehold ShelfLife         `json:"categoryHousehold"`
	CategoryDefault   ShelfLife         `json:"categoryDefault"`
}

// Days is the shelf life for key, if any layer knows it.
func (c Chain) Days(key string) (int, bool) {
	// A household's own rule always beats a Stockpot seed value, so a
	// Settings edit takes effect whatever level it was made at.
	layers := []ShelfLife{c.Product}
	for _, i := range c.Ingredients {
		layers = append(layers, i.Household)
	}
	layers = append(layers, c.CategoryHousehold)
	for _, i := range c.Ingredients {
		layers = append(layers, i.Seed)
	}
	layers = append(layers, c.CategoryDefault)
	for _, l := range layers {
		if d, ok := l[key]; ok && d > 0 {
			return d, true
		}
	}
	return 0, false
}

// Op is the event an expiry is estimated for.
type Op string

// Estimate operations.
const (
	OpAdd    Op = "add"    // into a location
	OpFreeze Op = "freeze" // into a freezer
	OpThaw   Op = "thaw"   // out of the freezer
	OpOpen   Op = "open"   // package opened
)

// LocationKind is location.kind.
type LocationKind string

// Location kinds.
const (
	Fridge  LocationKind = "fridge"
	Freezer LocationKind = "freezer"
	Pantry  LocationKind = "pantry"
	Other   LocationKind = "other"
)

// DateLayout is the wire format of every date.
const DateLayout = "2006-01-02"

// Estimate returns the estimated expiry date (on + days) for op, if the chain
// knows a shelf life. loc is where it is stored (OpAdd, and OpOpen, which does nothing in a freezer).
func Estimate(c Chain, op Op, loc LocationKind, on time.Time) (time.Time, bool) {
	var days int
	var ok bool
	switch op {
	case OpAdd:
		switch loc {
		case Fridge:
			days, ok = c.Days(KeyFridge)
		case Freezer:
			days, ok = c.Days(KeyFreezer)
		default:
			days, ok = c.Days(KeyPantry)
		}
	case OpFreeze:
		days, ok = c.Days(KeyFreezer)
	case OpThaw:
		if days, ok = c.Days(KeyThawedFridge); !ok {
			days, ok = c.Days(KeyFridge)
		}
	case OpOpen:
		if loc == Freezer {
			return time.Time{}, false // opened in the freezer: the frozen date still stands
		}
		days, ok = c.Days(KeyOpenedFridge)
	}
	if !ok {
		return time.Time{}, false
	}
	return on.AddDate(0, 0, days), true
}

// LoadChain reads the chain for a product: its override, its ingredient and
// ancestors with the household's rules, and its category's rules. The
// category is the product's own, else the nearest ingredient's.
func LoadChain(ctx context.Context, tx pgx.Tx, hh, productID uuid.UUID) (Chain, error) {
	var (
		c        Chain
		ing, cat *uuid.UUID
		override []byte
	)
	if err := tx.QueryRow(ctx, `SELECT ingredient_id, category_id, shelf_life FROM product WHERE id = $1 AND household_id = $2`,
		productID, hh).Scan(&ing, &cat, &override); err != nil {
		return c, err
	}
	c.Product = decodeShelf(override)
	if ing != nil {
		rows, err := tx.Query(ctx, `
			WITH RECURSIVE up AS (
				SELECT id, parent_id, category_id, shelf_life, 0 AS depth FROM ingredient WHERE id = $1
				UNION ALL
				SELECT i.id, i.parent_id, i.category_id, i.shelf_life, up.depth + 1 FROM ingredient i JOIN up ON i.id = up.parent_id WHERE up.depth < 12
			)
			SELECT up.category_id, up.shelf_life, r.shelf_life
			FROM up LEFT JOIN food_rule r ON r.ingredient_id = up.id AND r.household_id = $2
			ORDER BY up.depth`, *ing, hh)
		if err != nil {
			return c, err
		}
		defer rows.Close()
		for rows.Next() {
			var icat *uuid.UUID
			var seed, own []byte
			if err := rows.Scan(&icat, &seed, &own); err != nil {
				return c, err
			}
			c.Ingredients = append(c.Ingredients, IngredientLayer{Household: decodeShelf(own), Seed: decodeShelf(seed)})
			if cat == nil {
				cat = icat
			}
		}
		if err := rows.Err(); err != nil {
			return c, err
		}
	}
	if cat != nil {
		rows, err := tx.Query(ctx, `SELECT household_id IS NULL, shelf_life FROM food_rule
			WHERE category_id = $1 AND (household_id = $2 OR household_id IS NULL)`, *cat, hh)
		if err != nil {
			return c, err
		}
		defer rows.Close()
		for rows.Next() {
			var def bool
			var b []byte
			if err := rows.Scan(&def, &b); err != nil {
				return c, err
			}
			if def {
				c.CategoryDefault = decodeShelf(b)
			} else {
				c.CategoryHousehold = decodeShelf(b)
			}
		}
		return c, rows.Err()
	}
	return c, nil
}

func decodeShelf(b []byte) ShelfLife {
	if len(b) == 0 {
		return nil
	}
	var s ShelfLife
	_ = json.Unmarshal(b, &s) // our own jsonb; unknown shapes read as unknown
	return s
}
