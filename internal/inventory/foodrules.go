package inventory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/a-jay98/stockpot/internal/audit"
	"github.com/a-jay98/stockpot/internal/identity"
)

// Errors of the food-rule service; the API maps them to problems.
var (
	ErrInvalidRule = errors.New("invalid food rule")
	ErrNoSubject   = errors.New("no such ingredient or category")
)

// Subject kinds a rule applies to.
const (
	SubjectIngredient = "ingredient"
	SubjectCategory   = "category"
)

// Rule is a shelf-life rule: a Stockpot default (HouseholdID nil) or a
// household's override.
type Rule struct {
	ID          uuid.UUID
	HouseholdID *uuid.UUID
	Subject     string
	SubjectID   uuid.UUID
	ShelfLife   ShelfLife
	UpdatedAt   time.Time
}

// AdminRule is a household override with the household's name.
type AdminRule struct {
	Rule
	HouseholdName string
}

// RuleService reads and writes food rules.
type RuleService struct{ pool *pgxpool.Pool }

// NewRuleService returns a service over pool.
func NewRuleService(pool *pgxpool.Pool) *RuleService { return &RuleService{pool: pool} }

const (
	minDays = 1
	maxDays = 3650
)

// Validate checks a shelf life: known keys, sane days, at least one.
func (s ShelfLife) Validate() error {
	if len(s) == 0 {
		return fmt.Errorf("%w: give at least one shelf life", ErrInvalidRule)
	}
	for k, d := range s {
		known := false
		for _, kk := range ShelfKeys {
			known = known || k == kk
		}
		if !known {
			return fmt.Errorf("%w: unknown key %q", ErrInvalidRule, k)
		}
		if d < minDays || d > maxDays {
			return fmt.Errorf("%w: %s must be %d-%d days", ErrInvalidRule, k, minDays, maxDays)
		}
	}
	return nil
}

const ruleCols = `id, household_id, coalesce(ingredient_id, category_id), ingredient_id IS NOT NULL, shelf_life, updated_at`

func scanRule(row pgx.Row) (Rule, error) {
	var (
		r     Rule
		isIng bool
		b     []byte
	)
	if err := row.Scan(&r.ID, &r.HouseholdID, &r.SubjectID, &isIng, &b, &r.UpdatedAt); err != nil {
		return r, err
	}
	r.Subject = SubjectCategory
	if isIng {
		r.Subject = SubjectIngredient
	}
	r.ShelfLife = decodeShelf(b)
	return r, nil
}

func collect(rows pgx.Rows) ([]Rule, error) {
	defer rows.Close()
	out := []Rule{}
	for rows.Next() {
		r, err := scanRule(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// List returns the Stockpot defaults and the caller's household overrides.
func (s *RuleService) List(ctx context.Context) ([]Rule, error) {
	hh, err := identity.HouseholdID(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `SELECT `+ruleCols+` FROM food_rule WHERE household_id = $1 OR household_id IS NULL
		ORDER BY household_id NULLS FIRST, id`, hh)
	if err != nil {
		return nil, err
	}
	return collect(rows)
}

func subjectColumn(kind string) (string, error) {
	switch kind {
	case SubjectIngredient:
		return "ingredient_id", nil
	case SubjectCategory:
		return "category_id", nil
	}
	return "", fmt.Errorf("%w: subject must be ingredient or category", ErrInvalidRule)
}

// Put sets the household's override for a subject.
func (s *RuleService) Put(ctx context.Context, kind string, subject uuid.UUID, sl ShelfLife) error {
	hh, err := identity.HouseholdID(ctx)
	if err != nil {
		return err
	}
	col, err := subjectColumn(kind)
	if err != nil {
		return err
	}
	if err := sl.Validate(); err != nil {
		return err
	}
	b, _ := json.Marshal(sl)
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var ok bool
		if err := tx.QueryRow(ctx, fmt.Sprintf(`SELECT EXISTS (SELECT 1 FROM %s WHERE id = $1 AND deleted_at IS NULL AND (household_id = $2 OR household_id IS NULL))`, kind),
			subject, hh).Scan(&ok); err != nil {
			return err
		}
		if !ok {
			return ErrNoSubject
		}
		var before []byte
		_ = tx.QueryRow(ctx, fmt.Sprintf(`SELECT shelf_life FROM food_rule WHERE household_id = $1 AND %s = $2`, col), hh, subject).Scan(&before)
		// Atomic upsert: Postgres infers the expression index, so concurrent first Puts cannot collide.
		if _, err := tx.Exec(ctx, fmt.Sprintf(`
			INSERT INTO food_rule (household_id, %[1]s, shelf_life) VALUES ($1, $2, $3)
			ON CONFLICT ((coalesce(household_id, '00000000-0000-0000-0000-000000000000'::uuid)), %[1]s) WHERE %[1]s IS NOT NULL
			DO UPDATE SET shelf_life = EXCLUDED.shelf_life, updated_at = now()`, col), hh, subject, b); err != nil {
			return err
		}
		return audit.Record(ctx, tx, "inventory.food_rule.set", audit.Target{Type: kind, ID: subject.String()}, json.RawMessage(nilIfEmpty(before)), sl)
	})
}

// Delete removes the household's override, returning to the default.
func (s *RuleService) Delete(ctx context.Context, kind string, subject uuid.UUID) error {
	hh, err := identity.HouseholdID(ctx)
	if err != nil {
		return err
	}
	col, err := subjectColumn(kind)
	if err != nil {
		return err
	}
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var before []byte
		err := tx.QueryRow(ctx, fmt.Sprintf(`DELETE FROM food_rule WHERE household_id = $1 AND %s = $2 RETURNING shelf_life`, col), hh, subject).Scan(&before)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil // already gone
		}
		if err != nil {
			return err
		}
		return audit.Record(ctx, tx, "inventory.food_rule.reset", audit.Target{Type: kind, ID: subject.String()}, json.RawMessage(before), nil)
	})
}

// Admin returns every default and every household override, for the read-only admin view.
func (s *RuleService) Admin(ctx context.Context) (defaults []Rule, overrides []AdminRule, err error) {
	rows, err := s.pool.Query(ctx, `SELECT `+ruleCols+` FROM food_rule WHERE household_id IS NULL ORDER BY id`)
	if err != nil {
		return nil, nil, err
	}
	if defaults, err = collect(rows); err != nil {
		return nil, nil, err
	}
	orows, err := s.pool.Query(ctx, `SELECT r.id, r.household_id, coalesce(r.ingredient_id, r.category_id), r.ingredient_id IS NOT NULL, r.shelf_life, r.updated_at, h.name
		FROM food_rule r JOIN household h ON h.id = r.household_id ORDER BY h.name, r.updated_at DESC`)
	if err != nil {
		return nil, nil, err
	}
	defer orows.Close()
	overrides = []AdminRule{}
	for orows.Next() {
		var (
			r     AdminRule
			isIng bool
			b     []byte
		)
		if err := orows.Scan(&r.ID, &r.HouseholdID, &r.SubjectID, &isIng, &b, &r.UpdatedAt, &r.HouseholdName); err != nil {
			return nil, nil, err
		}
		r.Subject = SubjectCategory
		if isIng {
			r.Subject = SubjectIngredient
		}
		r.ShelfLife = decodeShelf(b)
		overrides = append(overrides, r)
	}
	return defaults, overrides, orows.Err()
}

func nilIfEmpty(b []byte) []byte {
	if len(b) == 0 {
		return []byte("null")
	}
	return b
}
