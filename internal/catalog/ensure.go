package catalog

import (
	"context"
	"errors"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/a-jay98/stockpot/internal/commands"
)

// EnsureProduct returns the household product called name, creating it (and
// letting res link it to an ingredient) when none exists. Matching is by
// case-insensitive name, then by resolved ingredient (brand-less product), so a free-text
// "mandarins" lands on the product the household already buys instead of a
// duplicate. With want set (an offline client already shows a product under
// that id), only the exact name match applies, so the client's product exists
// once it syncs; it is created under want unless that id is taken. The caller
// holds the household write lock (command handlers do).
func EnsureProduct(ctx context.Context, tx pgx.Tx, res Resolver, hh uuid.UUID, name string, want uuid.UUID) (uuid.UUID, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return uuid.Nil, commands.Invalid("name is required")
	}
	if len([]rune(name)) > 200 {
		name = string([]rune(name)[:200])
	}
	var id uuid.UUID
	err := tx.QueryRow(ctx, `SELECT id FROM product
		WHERE household_id = $1 AND deleted_at IS NULL AND lower(name) = lower($2)
		ORDER BY (brand IS NULL) DESC, created_at LIMIT 1`, hh, name).Scan(&id)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, err
	}
	// "mandarin" for the household's "Mandarins": the same ingredient, no brand, is the same product.
	// Skipped when the client matched names itself and chose a new product.
	if m, err := resolveFor(ctx, tx, res, hh, name, want); err != nil {
		return uuid.Nil, err
	} else if m != nil && m.Confidence >= 0.85 {
		err := tx.QueryRow(ctx, `SELECT id FROM product
			WHERE household_id = $1 AND deleted_at IS NULL AND ingredient_id = $2 AND brand IS NULL
			ORDER BY created_at LIMIT 1`, hh, m.IngredientID).Scan(&id)
		if err == nil {
			return id, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return uuid.Nil, err
		}
	}
	id = want
	if id == uuid.Nil {
		id = uuid.New()
	}
	tag, err := tx.Exec(ctx, `INSERT INTO product (id, household_id, name) VALUES ($1, $2, $3) ON CONFLICT (id) DO NOTHING`, id, hh, name)
	if err != nil {
		return uuid.Nil, err
	}
	if tag.RowsAffected() == 0 { // the proposed id is taken
		id = uuid.New()
		if _, err := tx.Exec(ctx, `INSERT INTO product (id, household_id, name) VALUES ($1, $2, $3)`, id, hh, name); err != nil {
			return uuid.Nil, err
		}
	}
	if err := (&handlers{res: res}).resolve(ctx, tx, hh, id, name, nil); err != nil {
		return uuid.Nil, err
	}
	// A new product inherits the ingredient's category.
	_, err = tx.Exec(ctx, `UPDATE product p SET category_id = i.category_id
		FROM ingredient i WHERE p.id = $1 AND p.ingredient_id = i.id AND p.category_id IS NULL`, id)
	return id, err
}

func resolveFor(ctx context.Context, tx pgx.Tx, res Resolver, hh uuid.UUID, name string, want uuid.UUID) (*Match, error) {
	if want != uuid.Nil {
		return nil, nil
	}
	return res.Resolve(ctx, tx, hh, name, nil)
}
