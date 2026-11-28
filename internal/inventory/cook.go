package inventory

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/a-jay98/stockpot/internal/commands"
)

// Stock changes "I cooked this" makes inside its own command (recipe.cook).
// They share the inventory internals rather than re-dispatching commands, and
// every event they write carries the cook session id.

// CookUse is one used item: consume a delta (exact: Quantity in the item's
// unit; portions: Portions), set a rough level (Level), or use it all
// (Finished).
type CookUse struct {
	Item, Event uuid.UUID
	Session     uuid.UUID
	Quantity    *string
	Portions    *int
	Level       *string
	Finished    bool
}

// CookUsed is what a use did. It is stored on the cook session (snake_case
// jsonb, read by the client) and is what undo reverses.
type CookUsed struct {
	Item        uuid.UUID `json:"stock_item_id"`
	Event       uuid.UUID `json:"event_id"`
	Product     uuid.UUID `json:"product_id"`
	Kind        string    `json:"amount_kind"`
	Quantity    *string   `json:"quantity,omitempty"`
	Unit        *string   `json:"unit,omitempty"`
	Portions    *int      `json:"portions,omitempty"`
	LevelBefore *string   `json:"level_before,omitempty"`
	LevelAfter  *string   `json:"level_after,omitempty"`
	Finished    bool      `json:"finished"`
}

const cookReason = "cook"

// ApplyCookUse applies u to one of the household's items. An item that no
// longer exists answers commands.Gone, which the caller reports and skips.
func ApplyCookUse(ctx context.Context, tx pgx.Tx, hh uuid.UUID, u CookUse) (CookUsed, error) {
	it, err := load(ctx, tx, hh, u.Item)
	if err != nil {
		return CookUsed{}, err
	}
	out := CookUsed{Item: it.ID, Event: u.Event, Product: it.ProductID, Kind: it.Kind, Unit: it.Unit, Finished: u.Finished}
	ev := event{ID: u.Event, Item: it.ID, Kind: "consume", Unit: it.Unit, From: it.LocationID, Reason: ptr(cookReason), CookSession: &u.Session}
	switch it.Kind {
	case "exact", "portions":
		if !u.Finished {
			if err := cookDelta(it, &u); err != nil {
				return out, err
			}
		}
		t, err := take(ctx, tx, it, u.Quantity, u.Portions, u.Finished)
		if err != nil {
			return out, err
		}
		out.Quantity, out.Portions, out.Finished = t.Quantity, t.Portions, t.Finished
		ev.Delta = delta(true, t.Quantity, t.Portions)
	case "level":
		out.LevelBefore = it.Level
		switch {
		case u.Finished:
			err = finish(ctx, tx, it.ID)
		case u.Level != nil && levels[*u.Level]:
			out.LevelAfter = u.Level
			ev.Kind = "adjust" // reason stays "cook"; the new level is on the item and in level_after
			_, err = tx.Exec(ctx, `UPDATE stock_item SET level = $2 WHERE id = $1`, it.ID, *u.Level)
		default:
			return out, commands.Invalid("a rough-level item takes a level or finished")
		}
		if err != nil {
			return out, err
		}
	}
	return out, writeEvent(ctx, tx, hh, ev)
}

func cookDelta(it *item, u *CookUse) error {
	if it.Kind == "exact" {
		if u.Quantity == nil || u.Portions != nil {
			return commands.Invalid("use a quantity of an exact amount")
		}
		q, err := validQty(*u.Quantity)
		u.Quantity = &q
		return err
	}
	if u.Portions == nil || u.Quantity != nil || *u.Portions <= 0 || *u.Portions > maxPortions {
		return commands.Invalid("use a number of portions")
	}
	return nil
}

func validQty(s string) (string, error) {
	a := Amount{Kind: "exact", Quantity: &s, Unit: ptr("g")}
	if err := a.validate(false); err != nil {
		return "", err
	}
	return *a.Quantity, nil
}

// laterEvents reports whether the item has events newer than after (by rev)
// that the cook session didn't write: someone used or changed it since.
func laterEvents(ctx context.Context, tx pgx.Tx, item, after, session uuid.UUID) (bool, error) {
	var later bool
	err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM stock_event e WHERE e.stock_item_id = $1
		AND e.rev > (SELECT rev FROM stock_event WHERE id = $2) AND e.cook_session_id IS DISTINCT FROM $3)`, item, after, session).Scan(&later)
	return later, err
}

// UndoCookUse gives back what a use took, as a compensating event with id
// eventID. Deltas commute, so quantity and portions always go back onto a live
// item; an item the cook finished comes back only if nothing touched it
// since, and a level is reset only if nobody changed it since. It reports
// whether anything was restored.
func UndoCookUse(ctx context.Context, tx pgx.Tx, hh, session, eventID uuid.UUID, u CookUsed) (bool, error) {
	var (
		deleted bool
		loc     *uuid.UUID
	)
	err := tx.QueryRow(ctx, `SELECT deleted_at IS NOT NULL, location_id FROM stock_item WHERE id = $1 AND household_id = $2 FOR UPDATE`,
		u.Item, hh).Scan(&deleted, &loc)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	later, err := laterEvents(ctx, tx, u.Item, u.Event, session)
	if err != nil {
		return false, err
	}
	if deleted && (!u.Finished || later) {
		return false, nil // gone for another reason, or used up again since
	}
	ev := event{ID: eventID, Item: u.Item, Kind: "adjust", Unit: u.Unit, To: loc, Reason: ptr("cook_undo"), CookSession: &session}
	switch u.Kind {
	case "exact":
		if u.Quantity == nil {
			return false, nil
		}
		if deleted {
			_, err = tx.Exec(ctx, `UPDATE stock_item SET deleted_at = NULL, quantity = $2::text::numeric WHERE id = $1`, u.Item, *u.Quantity)
		} else {
			_, err = tx.Exec(ctx, `UPDATE stock_item SET quantity = quantity + $2::text::numeric WHERE id = $1`, u.Item, *u.Quantity)
		}
		ev.Delta = u.Quantity
	case "portions":
		if u.Portions == nil {
			return false, nil
		}
		if deleted {
			_, err = tx.Exec(ctx, `UPDATE stock_item SET deleted_at = NULL, portions = $2 WHERE id = $1`, u.Item, *u.Portions)
		} else {
			_, err = tx.Exec(ctx, `UPDATE stock_item SET portions = portions + $2 WHERE id = $1`, u.Item, *u.Portions)
		}
		ev.Delta = delta(false, nil, u.Portions)
	case "level":
		if later || u.LevelBefore == nil {
			return false, nil
		}
		_, err = tx.Exec(ctx, `UPDATE stock_item SET deleted_at = NULL, level = $2 WHERE id = $1`, u.Item, *u.LevelBefore)
		ev.Unit = nil
	default:
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, writeEvent(ctx, tx, hh, ev)
}

// CookPortions are leftover portions stored by a cook.
type CookPortions struct {
	Item, Event, Product, Location, Session uuid.UUID
	Portions                                int
	On                                      time.Time
}

// AddCookPortions stores leftover portions as a new portions item, with the
// expiry estimated from food rules (leftovers: fridge 4 days, freezer 90).
func AddCookPortions(ctx context.Context, tx pgx.Tx, hh uuid.UUID, p CookPortions) (*time.Time, error) {
	if p.Portions <= 0 || p.Portions > maxPortions {
		return nil, commands.Invalid("store 1-%d portions", maxPortions)
	}
	kind, err := locationKind(ctx, tx, hh, p.Location)
	if err != nil {
		return nil, err
	}
	c, err := LoadChain(ctx, tx, hh, p.Product)
	if err != nil {
		return nil, err
	}
	var (
		expires    *time.Time
		expiryKind *string
		frozen     *time.Time
		portions   = p.Portions
	)
	if d, ok := Estimate(c, OpAdd, kind, p.On); ok {
		expires, expiryKind = &d, ptr("estimated")
	}
	if kind == Freezer {
		frozen = &p.On
	}
	n := newItem{
		ID: p.Item, Product: p.Product, Location: p.Location, Amount: Amount{Kind: "portions", Portions: &portions},
		ExpiresOn: expires, ExpiryKind: expiryKind, FrozenOn: frozen, Source: "cook", CookSession: &p.Session,
	}
	if err := insertItem(ctx, tx, hh, n); err != nil {
		return nil, err
	}
	return expires, writeEvent(ctx, tx, hh, event{ID: p.Event, Item: p.Item, Kind: "add", Delta: delta(false, nil, &portions), To: &p.Location, CookSession: &p.Session})
}

// RemoveCookPortions takes back leftovers a cook stored, unless someone
// used, moved or changed them since (then they are real stock now). It
// reports whether the item was removed.
func RemoveCookPortions(ctx context.Context, tx pgx.Tx, hh, session, item, addEvent, eventID uuid.UUID) (bool, error) {
	var loc *uuid.UUID
	var portions *int
	err := tx.QueryRow(ctx, `SELECT location_id, portions FROM stock_item WHERE id = $1 AND household_id = $2 AND deleted_at IS NULL FOR UPDATE`,
		item, hh).Scan(&loc, &portions)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	later, err := laterEvents(ctx, tx, item, addEvent, session)
	if err != nil || later {
		return false, err
	}
	if err := finish(ctx, tx, item); err != nil {
		return false, err
	}
	return true, writeEvent(ctx, tx, hh, event{ID: eventID, Item: item, Kind: "discard", Delta: delta(true, nil, portions), From: loc, Reason: ptr("cook_undo"), CookSession: &session})
}
