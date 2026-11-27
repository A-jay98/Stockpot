// Package catalog holds the ingredient catalog: the global seed, the
// resolver that links products to canonical ingredients, and the catalog
// sync commands (products, stores, locations, household ingredients).
package catalog

import (
	"context"
	"errors"
	"strings"
	"unicode"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/a-jay98/stockpot/internal/db/dbq"
)

// Match is a resolver result.
type Match struct {
	IngredientID uuid.UUID
	Confidence   float32 // 1.0 = whole name matched exactly
}

// Resolver maps a product's text to a canonical ingredient. The M1-3 fuzzy
// resolver replaces ExactResolver behind this interface.
type Resolver interface {
	// Resolve returns nil, nil when nothing matches.
	Resolve(ctx context.Context, q dbq.DBTX, household uuid.UUID, name string, brand *string) (*Match, error)
}

// ExactResolver matches the whole normalized name against ingredient name,
// plural, slug and aliases (household rows win over global ones).
type ExactResolver struct{}

// Resolve implements Resolver.
func (ExactResolver) Resolve(ctx context.Context, q dbq.DBTX, household uuid.UUID, name string, _ *string) (*Match, error) {
	n := Normalize(name)
	if n == "" {
		return nil, nil
	}
	var id uuid.UUID
	err := q.QueryRow(ctx, `
		SELECT id FROM ingredient
		WHERE deleted_at IS NULL AND (household_id = $1 OR household_id IS NULL)
		  AND (`+sqlNorm("name")+` = $2 OR `+sqlNorm("coalesce(plural, '')")+` = $2
		       OR replace(slug, '-', ' ') = $2 OR $2 = ANY(aliases))
		ORDER BY household_id IS NULL, id LIMIT 1`, household, n).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &Match{IngredientID: id, Confidence: 1}, nil
}

// sqlNorm is Normalize in SQL, for columns stored as typed (aliases are stored normalized).
func sqlNorm(expr string) string {
	return "btrim(regexp_replace(immutable_unaccent(lower(" + expr + ")), '[^[:alnum:]]+', ' ', 'g'))"
}

// Normalize lowercases, strips accents (common Latin ones; SQL does the
// rest), turns punctuation into spaces and collapses whitespace.
func Normalize(s string) string {
	var b strings.Builder
	space := true
	for _, r := range strings.ToLower(s) {
		switch {
		case r == '\u200c' || r == '\u200d': // zero-width (non-)joiners inside Persian words
			continue
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(foldAccent(r))
			space = false
		case !space:
			b.WriteByte(' ')
			space = true
		}
	}
	return strings.TrimSpace(b.String())
}

var accentFold = func() map[rune]rune {
	from, to := []rune("àáâãäåçèéêëìíîïñòóôõöùúûüýÿ"), []rune("aaaaaaceeeeiiiinooooouuuuyy")
	m := make(map[rune]rune, len(from))
	for i, r := range from {
		m[r] = to[i]
	}
	return m
}()

func foldAccent(r rune) rune {
	if f, ok := accentFold[r]; ok {
		return f
	}
	return r
}
