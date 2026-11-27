package shopping

import (
	"context"
	"errors"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// statWindow is how many recent purchases typical quantity and interval use.
const statWindow = 10

// Purchase is one bought product, as the stats see it.
type Purchase struct {
	At   time.Time
	Qty  string // decimal string, "" when unknown
	Unit string
}

// Stat is the derived row of product_stat.
type Stat struct {
	Times        int
	Last         time.Time
	LastQty      string
	TypicalQty   string
	TypicalUnit  string
	MedianInterv *float64 // days; nil with fewer than two purchase days
}

// ComputeStat derives a product's stat from its purchases, newest first.
// typical qty/unit is the most common pair of the last statWindow purchases
// (ties go to the most recent); the median interval is over the distinct
// purchase days of those, so two items ticked on one trip count once.
func ComputeStat(ps []Purchase) Stat {
	if len(ps) == 0 {
		return Stat{}
	}
	s := Stat{Times: len(ps), Last: ps[0].At, LastQty: ps[0].Qty}
	recent := ps
	if len(recent) > statWindow {
		recent = recent[:statWindow]
	}
	type pair struct{ qty, unit string }
	counts := map[pair]int{}
	for _, p := range recent {
		counts[pair{p.Qty, p.Unit}]++
	}
	var best pair
	bestN := 0
	for _, p := range recent { // newest first: the first of equal counts wins
		k := pair{p.Qty, p.Unit}
		if counts[k] > bestN {
			best, bestN = k, counts[k]
		}
	}
	s.TypicalQty, s.TypicalUnit = best.qty, best.unit

	var days []time.Time
	for _, p := range recent {
		d := p.At.UTC().Truncate(24 * time.Hour)
		if len(days) == 0 || !days[len(days)-1].Equal(d) {
			days = append(days, d)
		}
	}
	if len(days) >= 2 {
		gaps := make([]float64, 0, len(days)-1)
		for i := 0; i+1 < len(days); i++ {
			gaps = append(gaps, days[i].Sub(days[i+1]).Hours()/24)
		}
		sort.Float64s(gaps)
		m := gaps[len(gaps)/2]
		if len(gaps)%2 == 0 {
			m = (gaps[len(gaps)/2-1] + gaps[len(gaps)/2]) / 2
		}
		s.MedianInterv = &m
	}
	return s
}

// recompute rebuilds the product_stat rows of a product from its purchases:
// one per store it was bought at, plus the store-less row over all of them.
func recompute(ctx context.Context, tx pgx.Tx, hh, product uuid.UUID) error {
	rows, err := tx.Query(ctx, `SELECT store_id, bought_at, coalesce(qty::text, ''), coalesce(unit, '')
		FROM purchase WHERE household_id = $1 AND product_id = $2 ORDER BY bought_at DESC, id`, hh, product)
	if err != nil {
		return err
	}
	byStore := map[uuid.UUID][]Purchase{}
	var all []Purchase
	for rows.Next() {
		var store *uuid.UUID
		var p Purchase
		if err := rows.Scan(&store, &p.At, &p.Qty, &p.Unit); err != nil {
			rows.Close()
			return err
		}
		all = append(all, p)
		if store != nil {
			byStore[*store] = append(byStore[*store], p)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if err := upsertStat(ctx, tx, hh, product, nil, all); err != nil {
		return err
	}
	for s, ps := range byStore {
		if err := upsertStat(ctx, tx, hh, product, &s, ps); err != nil {
			return err
		}
	}
	// A store whose purchases were all removed keeps a zeroed row (clients ignore times 0).
	_, err = tx.Exec(ctx, `UPDATE product_stat SET times_bought = 0, last_bought_at = NULL, typical_qty = NULL,
		typical_unit = NULL, median_interval_days = NULL, last_qty = NULL
		WHERE household_id = $1 AND product_id = $2 AND store_id IS NOT NULL AND times_bought > 0
		AND NOT EXISTS (SELECT 1 FROM purchase p WHERE p.product_id = product_stat.product_id AND p.store_id = product_stat.store_id)`, hh, product)
	return err
}

func upsertStat(ctx context.Context, tx pgx.Tx, hh, product uuid.UUID, store *uuid.UUID, ps []Purchase) error {
	st := ComputeStat(ps)
	if st.Times == 0 {
		_, err := tx.Exec(ctx, `UPDATE product_stat SET times_bought = 0, last_bought_at = NULL, typical_qty = NULL,
			typical_unit = NULL, median_interval_days = NULL, last_qty = NULL
			WHERE household_id = $1 AND product_id = $2 AND store_id IS NOT DISTINCT FROM $3`, hh, product, store)
		return err
	}
	_, err := tx.Exec(ctx, `INSERT INTO product_stat
			(household_id, product_id, store_id, times_bought, last_bought_at, typical_qty, typical_unit, median_interval_days, last_qty, deleted_at)
		VALUES ($1, $2, $3, $4, $5, nullif($6, '')::numeric, nullif($7, ''), $8, nullif($9, '')::numeric, NULL)
		ON CONFLICT (product_id, coalesce(store_id, '00000000-0000-0000-0000-000000000000'::uuid)) DO UPDATE SET
			times_bought = EXCLUDED.times_bought, last_bought_at = EXCLUDED.last_bought_at,
			typical_qty = EXCLUDED.typical_qty, typical_unit = EXCLUDED.typical_unit,
			median_interval_days = EXCLUDED.median_interval_days, last_qty = EXCLUDED.last_qty, deleted_at = NULL`,
		hh, product, store, st.Times, st.Last, st.TypicalQty, st.TypicalUnit, st.MedianInterv, st.LastQty)
	return err
}

// RecordPurchase logs that product was bought (qty/unit/store optional) and
// refreshes its product_stat rows. item links the purchase to a shopping item
// so ticking twice, or unticking, stays consistent; uuid.Nil for none.
// Inventory adds from outside the shopping list can call it too.
func RecordPurchase(ctx context.Context, tx pgx.Tx, hh, product uuid.UUID, store *uuid.UUID, item uuid.UUID, qty, unit *string) error {
	var itemID *uuid.UUID
	if item != uuid.Nil {
		itemID = &item
	}
	if _, err := tx.Exec(ctx, `INSERT INTO purchase (household_id, product_id, store_id, shopping_item_id, qty, unit)
		VALUES ($1, $2, $3, $4, $5::text::numeric, $6) ON CONFLICT (shopping_item_id) WHERE shopping_item_id IS NOT NULL DO NOTHING`,
		hh, product, store, itemID, qty, unit); err != nil {
		return err
	}
	return recompute(ctx, tx, hh, product)
}

// ForgetPurchase removes the purchase a shopping item logged (untick).
func ForgetPurchase(ctx context.Context, tx pgx.Tx, hh, item uuid.UUID) error {
	var product uuid.UUID
	err := tx.QueryRow(ctx, `DELETE FROM purchase WHERE household_id = $1 AND shopping_item_id = $2 RETURNING product_id`, hh, item).Scan(&product)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	return recompute(ctx, tx, hh, product)
}
