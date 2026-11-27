package settings

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/a-jay98/stockpot/internal/db/dbq"
)

// ForHousehold reads a household-scoped structured setting inside q (often
// the command's transaction): the household's own value, else the system
// value, else the default. It bypasses the cache because household values
// aren't cached (there could be many households).
func (s *JSON[T]) ForHousehold(ctx context.Context, q dbq.DBTX, household uuid.UUID) (T, error) {
	var raw []byte
	err := q.QueryRow(ctx, `SELECT value FROM setting WHERE key = $1 AND value IS NOT NULL
		AND (household_id = $2 OR household_id IS NULL) ORDER BY household_id NULLS LAST LIMIT 1`, s.def.Key, household).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return *s.def.Default.(*T), nil //nolint:forcetypeassert // set by DefineJSON
	}
	if err != nil {
		return *new(T), fmt.Errorf("setting %s: %w", s.def.Key, err)
	}
	v, err := s.def.decode(raw)
	if err != nil {
		return *new(T), err
	}
	return *v.(*T), nil //nolint:forcetypeassert // decode returns *T
}
