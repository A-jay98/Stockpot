// Package catalog holds the ingredient catalog: the global seed, the
// resolver that links products to canonical ingredients, and the catalog
// sync commands (products, stores, locations, household ingredients).
package catalog

import (
	"context"

	"github.com/google/uuid"

	"github.com/a-jay98/stockpot/internal/catalog/resolve"
	"github.com/a-jay98/stockpot/internal/db/dbq"
)

// Match is a resolver result.
type Match = resolve.Match

// Resolver maps a product's text to a canonical ingredient; resolve.Resolver
// is the production implementation.
type Resolver interface {
	// Resolve returns nil, nil when nothing matches.
	Resolve(ctx context.Context, q dbq.DBTX, household uuid.UUID, name string, brand *string) (*Match, error)
}

var _ Resolver = resolve.Resolver{}

// Normalize lowercases, strips accents, turns punctuation into spaces and
// collapses whitespace. Seeds and the resolver compare names in this form.
func Normalize(s string) string { return resolve.Normalize(s) }
