package reports

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/a-jay98/stockpot/internal/audit"
	"github.com/a-jay98/stockpot/internal/reqctx"
	"github.com/a-jay98/stockpot/internal/syncdb"
	"github.com/a-jay98/stockpot/internal/units"
)

type pgxTx = pgx.Tx

// FieldChange is one value a report changed.
type FieldChange struct {
	Target string    `json:"target"` // ingredient | product
	ID     uuid.UUID `json:"id"`
	Name   string    `json:"name"`
	Global bool      `json:"global,omitempty"`
	Field  string    `json:"field"`
	Before *string   `json:"before"`
	After  *string   `json:"after"`
}

// Change is what applying a report did.
type Change struct {
	Changes []FieldChange `json:"changes"`
	// Reverted: the admin overruled the report and the old values were put back.
	Reverted bool `json:"reverted,omitempty"`
}

// ActionDefaultUpdate is the audit action of every default-unit change, from
// a report or the admin's Defaults page; the Defaults history lists these.
const ActionDefaultUpdate = "catalog.default.update"

func (s *Service) tx(ctx context.Context, fn func(tx pgxTx) error) error {
	tx, err := s.d.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// withHousehold attributes audit events to hh when the caller has no request
// context (the job); a request's own context wins.
func withHousehold(ctx context.Context, hh uuid.UUID) context.Context {
	if info := reqctx.From(ctx); info != nil {
		return ctx
	}
	return reqctx.With(ctx, &reqctx.Info{HouseholdID: hh})
}

// applyOpts says how far an apply may reach.
type applyOpts struct {
	// actor goes into the audit record; empty for a person (the audit row's
	// actor_id names them).
	actor string
	// global lets the change reach a global ingredient (every household). The
	// job only gets it when households agree; the admin always.
	global bool
}

// applyUnit makes unit the default of the report's ingredient (a global one
// only with opts.global) and of the reporting household's product when that
// product's own default is empty or is the unit reported as wrong. A
// product's other default was picked by hand and stays. skippedGlobal says a
// global ingredient was left alone. It returns ErrNoTarget when nothing could
// change.
func (s *Service) applyUnit(ctx context.Context, tx pgxTx, reportID uuid.UUID, unit string, opts applyOpts) (ch Change, skippedGlobal bool, err error) {
	if !units.Valid(unit) {
		return ch, false, ErrBadUnit
	}
	var (
		hh                  uuid.UUID
		product, ingredient *uuid.UUID
		raw                 []byte
	)
	if err := tx.QueryRow(ctx, `SELECT household_id, product_id, ingredient_id, context FROM problem_report WHERE id = $1`, reportID).
		Scan(&hh, &product, &ingredient, &raw); err != nil {
		return ch, false, err
	}
	var c Context
	_ = json.Unmarshal(raw, &c)
	// The ingredient may have changed since the report: follow the product.
	if product != nil {
		var cur *uuid.UUID
		err := tx.QueryRow(ctx, `SELECT ingredient_id FROM product WHERE id = $1 AND deleted_at IS NULL`, *product).Scan(&cur)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			product = nil
		case err != nil:
			return ch, false, err
		default:
			ingredient = cur
		}
	}
	var global bool
	if ingredient != nil {
		var owner *uuid.UUID
		err := tx.QueryRow(ctx, `SELECT household_id FROM ingredient WHERE id = $1 AND deleted_at IS NULL`, *ingredient).Scan(&owner)
		if errors.Is(err, pgx.ErrNoRows) {
			ingredient = nil
		} else if err != nil {
			return ch, false, err
		}
		global = ingredient != nil && owner == nil
		if global && !opts.global {
			ingredient, global, skippedGlobal = nil, false, true
		}
	}
	if product == nil && ingredient == nil {
		return ch, skippedGlobal, ErrNoTarget
	}
	// Lock order everywhere: global, then household.
	if global {
		if err := syncdb.LockGlobal(ctx, tx); err != nil {
			return ch, false, err
		}
	}
	if err := syncdb.LockHousehold(ctx, tx, hh); err != nil {
		return ch, false, err
	}
	actx := withHousehold(ctx, hh)
	if ingredient != nil {
		fc, err := setIngredientUnit(actx, tx, *ingredient, &unit, opts.meta(reportID))
		if err != nil {
			return ch, false, err
		}
		if fc != nil {
			ch.Changes = append(ch.Changes, *fc)
		}
	}
	if product != nil {
		var name string
		var before *string
		if err := tx.QueryRow(ctx, `SELECT name, default_unit FROM product WHERE id = $1 FOR UPDATE`, *product).Scan(&name, &before); err != nil {
			return ch, false, err
		}
		wrong := shownUnit(c)
		if (before == nil || *before == wrong) && deref(before) != unit {
			if _, err := tx.Exec(ctx, `UPDATE product SET default_unit = $2 WHERE id = $1`, *product, unit); err != nil {
				return ch, false, err
			}
			if err := audit.Record(actx, tx, ActionDefaultUpdate, audit.Target{Type: "product", ID: product.String()},
				map[string]any{"default_unit": before}, opts.meta(reportID, "default_unit", unit)); err != nil {
				return ch, false, err
			}
			ch.Changes = append(ch.Changes, FieldChange{Target: "product", ID: *product, Name: name, Field: "default_unit", Before: before, After: &unit})
		}
	}
	if ch.Changes == nil {
		ch.Changes = []FieldChange{}
	}
	return ch, skippedGlobal, nil
}

func (o applyOpts) meta(report uuid.UUID, kv ...any) map[string]any {
	m := map[string]any{"report_id": report}
	if o.actor != "" {
		m["actor"] = o.actor
	}
	for i := 0; i+1 < len(kv); i += 2 {
		m[kv[i].(string)] = kv[i+1]
	}
	return m
}

// revert undoes what applying a report changed, for each value still as the
// report left it (a later edit wins). It returns what it put back.
func (s *Service) revert(ctx context.Context, tx pgxTx, reportID, hh uuid.UUID, ch Change) ([]FieldChange, error) {
	var global bool
	for _, c := range ch.Changes {
		global = global || c.Global
	}
	if global {
		if err := syncdb.LockGlobal(ctx, tx); err != nil {
			return nil, err
		}
	}
	if err := syncdb.LockHousehold(ctx, tx, hh); err != nil {
		return nil, err
	}
	actx := withHousehold(ctx, hh)
	meta := map[string]any{"report_id": reportID, "revert": true}
	var out []FieldChange
	for _, c := range ch.Changes {
		if c.Field != "default_unit" {
			continue
		}
		switch c.Target {
		case "ingredient":
			var cur *string
			if err := tx.QueryRow(ctx, `SELECT default_unit FROM ingredient WHERE id = $1 AND deleted_at IS NULL`, c.ID).Scan(&cur); err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					continue
				}
				return nil, err
			}
			if deref(cur) != deref(c.After) {
				continue
			}
			fc, err := setIngredientUnit(actx, tx, c.ID, c.Before, meta)
			if err != nil {
				return nil, err
			}
			if fc != nil {
				out = append(out, *fc)
			}
		case "product":
			tag, err := tx.Exec(ctx, `UPDATE product SET default_unit = $3 WHERE id = $1 AND household_id = $2 AND deleted_at IS NULL
				AND default_unit IS NOT DISTINCT FROM $4`, c.ID, hh, c.Before, c.After)
			if err != nil {
				return nil, err
			}
			if tag.RowsAffected() == 0 {
				continue
			}
			if err := audit.Record(actx, tx, ActionDefaultUpdate, audit.Target{Type: "product", ID: c.ID.String()},
				map[string]any{"default_unit": c.After}, map[string]any{"default_unit": c.Before, "report_id": reportID, "revert": true}); err != nil {
				return nil, err
			}
			out = append(out, FieldChange{Target: "product", ID: c.ID, Name: c.Name, Field: c.Field, Before: c.After, After: c.Before})
		}
	}
	return out, nil
}

// setIngredientUnit changes an ingredient's default_unit (nil clears it) and
// audits it; nil, nil when it already had that value. The caller holds the
// sync lock of the ingredient's owner.
func setIngredientUnit(ctx context.Context, tx pgxTx, id uuid.UUID, unit *string, meta map[string]any) (*FieldChange, error) {
	var name string
	var before *string
	var owner *uuid.UUID
	err := tx.QueryRow(ctx, `SELECT name, default_unit, household_id FROM ingredient WHERE id = $1 AND deleted_at IS NULL FOR UPDATE`, id).
		Scan(&name, &before, &owner)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if deref(before) == deref(unit) {
		return nil, nil
	}
	if _, err := tx.Exec(ctx, `UPDATE ingredient SET default_unit = $2 WHERE id = $1`, id, unit); err != nil {
		return nil, err
	}
	after := map[string]any{"default_unit": unit}
	for k, v := range meta {
		after[k] = v
	}
	if err := audit.Record(ctx, tx, ActionDefaultUpdate, audit.Target{Type: "ingredient", ID: id.String()},
		map[string]any{"default_unit": before}, after); err != nil {
		return nil, err
	}
	return &FieldChange{Target: "ingredient", ID: id, Name: name, Global: owner == nil, Field: "default_unit", Before: before, After: unit}, nil
}
