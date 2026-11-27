package inventory

import (
	"context"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// statWindow is how many recent purchases the typical amount and the repeat
// interval are computed over.
const statWindow = 10

type purchase struct {
	Qty  *string
	Unit *string
	At   time.Time
}

// Stat is product_stat's derived columns for one (product, store) scope.
type Stat struct {
	TimesBought  int
	LastBoughtAt time.Time
	LastQty      *string
	TypicalQty   *string
	TypicalUnit  *string
	// MedianIntervalDays is the median gap between distinct purchase days; nil with fewer than two.
	MedianIntervalDays *float64
}

func str(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// computeStat derives a scope's stat from its recent purchases, newest
// first, and the all-time count. The typical amount is the most common
// (qty, unit) among them; a tie goes to the more recent.
func computeStat(recent []purchase, total int) Stat {
	s := Stat{TimesBought: total}
	if len(recent) == 0 {
		return s
	}
	s.LastBoughtAt = recent[0].At
	s.LastQty = recent[0].Qty

	type key struct{ q, u string }
	counts := map[key]int{}
	for _, p := range recent {
		counts[key{str(p.Qty), str(p.Unit)}]++
	}
	var best key
	bestN := 0
	for _, p := range recent { // newest first: the first key to reach the top count wins a tie
		k := key{str(p.Qty), str(p.Unit)}
		if counts[k] > bestN {
			best, bestN = k, counts[k]
		}
	}
	if best.q != "" {
		q, u := best.q, best.u
		s.TypicalQty = &q
		if u != "" {
			s.TypicalUnit = &u
		}
	}

	days := make([]time.Time, 0, len(recent))
	for _, p := range recent {
		d := p.At.UTC().Truncate(24 * time.Hour)
		if len(days) == 0 || !days[len(days)-1].Equal(d) {
			days = append(days, d)
		}
	}
	if len(days) >= 2 {
		gaps := make([]float64, 0, len(days)-1)
		for i := 0; i+1 < len(days); i++ { // newest first: days[i] is later than days[i+1]
			gaps = append(gaps, days[i].Sub(days[i+1]).Hours()/24)
		}
		slices.Sort(gaps)
		m := gaps[len(gaps)/2]
		if len(gaps)%2 == 0 {
			m = (gaps[len(gaps)/2-1] + gaps[len(gaps)/2]) / 2
		}
		s.MedianIntervalDays = &m
	}
	return s
}

// RecordPurchase logs one purchase of a product and refreshes product_stat
// for the store it was bought at and for "any store" (store_id NULL), in
// tx's transaction. Callers are command handlers (stock add from manual or
// shopping, and m1-4's shopping check-off) and so already hold the
// household write lock.
func RecordPurchase(ctx context.Context, tx pgx.Tx, hh, product uuid.UUID, store *uuid.UUID, qty, unit *string, at time.Time) error {
	if _, err := tx.Exec(ctx, `INSERT INTO purchase_log (household_id, product_id, store_id, qty, unit, bought_at)
		VALUES ($1, $2, $3, $4::text::numeric, $5, $6)`, hh, product, store, qty, unit, at); err != nil {
		return err
	}
	scopes := []*uuid.UUID{nil}
	if store != nil {
		scopes = append(scopes, store)
	}
	for _, sc := range scopes {
		if err := refreshStat(ctx, tx, hh, product, sc); err != nil {
			return err
		}
	}
	return nil
}

func refreshStat(ctx context.Context, tx pgx.Tx, hh, product uuid.UUID, store *uuid.UUID) error {
	var total int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM purchase_log WHERE household_id = $1 AND product_id = $2 AND ($3::uuid IS NULL OR store_id = $3)`,
		hh, product, store).Scan(&total); err != nil {
		return err
	}
	rows, err := tx.Query(ctx, `SELECT qty::text, unit, bought_at FROM purchase_log
		WHERE household_id = $1 AND product_id = $2 AND ($3::uuid IS NULL OR store_id = $3)
		ORDER BY bought_at DESC, id DESC LIMIT $4`, hh, product, store, statWindow)
	if err != nil {
		return err
	}
	defer rows.Close()
	var recent []purchase
	for rows.Next() {
		var p purchase
		if err := rows.Scan(&p.Qty, &p.Unit, &p.At); err != nil {
			return err
		}
		recent = append(recent, p)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	s := computeStat(recent, total)
	_, err = tx.Exec(ctx, `
		INSERT INTO product_stat (household_id, product_id, store_id, times_bought, last_bought_at, typical_qty, typical_unit, median_interval_days, last_qty)
		VALUES ($1, $2, $3, $4, $5, $6::text::numeric, $7, $8, $9::text::numeric)
		ON CONFLICT (product_id, (coalesce(store_id, '00000000-0000-0000-0000-000000000000'::uuid))) DO UPDATE SET
			deleted_at = NULL, times_bought = EXCLUDED.times_bought, last_bought_at = EXCLUDED.last_bought_at,
			typical_qty = EXCLUDED.typical_qty, typical_unit = EXCLUDED.typical_unit,
			median_interval_days = EXCLUDED.median_interval_days, last_qty = EXCLUDED.last_qty`,
		hh, product, store, s.TimesBought, s.LastBoughtAt, s.TypicalQty, s.TypicalUnit, s.MedianIntervalDays, s.LastQty)
	return err
}
