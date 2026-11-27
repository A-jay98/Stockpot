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
// case-insensitive name, preferring the brand-less product, so a free-text
// "mandarins" lands on the product the household already buys instead of a
// duplicate. The caller holds the household write lock (command handlers do).
func EnsureProduct(ctx context.Context, tx pgx.Tx, res Resolver, hh uuid.UUID, name string) (uuid.UUID, error) {
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
	id = uuid.New()
	if _, err := tx.Exec(ctx, `INSERT INTO product (id, household_id, name) VALUES ($1, $2, $3)`, id, hh, name); err != nil {
		return uuid.Nil, err
	}
	if err := (&handlers{res: res}).resolve(ctx, tx, hh, id, name, nil); err != nil {
		return uuid.Nil, err
	}
	// A new product inherits the ingredient's category.
	_, err = tx.Exec(ctx, `UPDATE product p SET category_id = i.category_id
		FROM ingredient i WHERE p.id = $1 AND p.ingredient_id = i.id AND p.category_id IS NULL`, id)
	return id, err
}
