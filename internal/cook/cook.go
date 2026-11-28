// Package cook is "I cooked this" (architecture-m2 §9): one recipe.cook
// command deducts what was used, stores leftover portions, adds used-up
// items to the shopping list and records the cook session; recipe.cook.undo
// reverses it within 24 hours.
package cook

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/a-jay98/stockpot/internal/commands"
	"github.com/a-jay98/stockpot/internal/identity"
	"github.com/a-jay98/stockpot/internal/inventory"
	"github.com/a-jay98/stockpot/internal/media"
	"github.com/a-jay98/stockpot/internal/shopping"
)

// ToShopping puts the used item's product on a list (it was used up).
type ToShopping struct {
	ID     uuid.UUID `json:"id"`
	ListID uuid.UUID `json:"listId"`
	Qty    *string   `json:"qty"`
	Unit   *string   `json:"unit"`
}

// Use is one stock item used by the cook: a delta in the item's own amount
// kind (quantity in its unit, portions), a new rough level, or finished.
type Use struct {
	LineID      *uuid.UUID  `json:"lineId"`
	StockItemID uuid.UUID   `json:"stockItemId"`
	EventID     uuid.UUID   `json:"eventId"`
	Quantity    *string     `json:"quantity"`
	Portions    *int        `json:"portions"`
	Level       *string     `json:"level"`
	Finished    bool        `json:"finished"`
	ToShopping  *ToShopping `json:"toShopping"`
}

// Store is leftover portions put in a location.
type Store struct {
	StockItemID uuid.UUID `json:"stockItemId"`
	EventID     uuid.UUID `json:"eventId"`
	LocationID  uuid.UUID `json:"locationId"`
	Portions    int       `json:"portions"`
}

// Sub records that a line was swapped for a substitution.
type Sub struct {
	LineID         uuid.UUID `json:"lineId"`
	SubstitutionID uuid.UUID `json:"substitutionId"`
}

// Photo is a photo of the cook (an uploaded or pending blob ref).
type Photo struct {
	ID      uuid.UUID `json:"id"`
	BlobRef string    `json:"blobRef"`
}

// Cook is recipe.cook. ID is the cook session's id.
type Cook struct {
	ID           uuid.UUID `json:"id"`
	RecipeID     uuid.UUID `json:"recipeId"`
	VersionID    uuid.UUID `json:"versionId"`
	CookedOn     string    `json:"cookedOn"`
	PortionsMade int       `json:"portionsMade"`
	// LeftoverProductID is the client's id for the recipe's leftover
	// product, used only when the recipe has none yet.
	LeftoverProductID *uuid.UUID `json:"leftoverProductId"`
	Uses              []Use      `json:"uses"`
	Store             []Store    `json:"store"`
	Subs              []Sub      `json:"subs"`
	Photos            []Photo    `json:"photos"`
	Note              *string    `json:"note"`
}

// Undo is recipe.cook.undo.
type Undo struct {
	ID uuid.UUID `json:"id"`
}

// UseRecord is one entry of cook_session.uses (snake_case jsonb; the client
// reads it for the cook history and undo).
type UseRecord struct {
	LineID *uuid.UUID `json:"line_id,omitempty"`
	inventory.CookUsed
	ShoppingItemID *uuid.UUID `json:"shopping_item_id,omitempty"`
	// Skipped: the item was gone when the cook synced (used or deleted on
	// another device). The cook still happened, so the rest applies.
	Skipped bool `json:"skipped,omitempty"`
}

// StoreRecord is one entry of cook_session.stored.
type StoreRecord struct {
	LocationID  uuid.UUID `json:"location_id"`
	Portions    int       `json:"portions"`
	StockItemID uuid.UUID `json:"stock_item_id"`
	EventID     uuid.UUID `json:"event_id"`
	ExpiresOn   *string   `json:"expires_on,omitempty"`
	// Skipped: no live location of that kind when the cook synced (deleted on
	// another device), so these portions weren't stored.
	Skipped bool `json:"skipped,omitempty"`
}

// UndoWindow is how long after cooking undo is offered and accepted.
const UndoWindow = 24 * time.Hour

const (
	maxPortionsMade = 100
	maxLines        = 200
	maxPhotos       = 10
	maxNote         = 2000
	dateLayout      = "2006-01-02"
)

type handlers struct{}

// Register adds recipe.cook and recipe.cook.undo to r.
func Register(r *commands.Registry) {
	h := &handlers{}
	commands.Register(r, "recipe.cook", h.cook)
	commands.Register(r, "recipe.cook.undo", h.undo)
}

// UndoEventID is the id of the compensating event undo writes for an
// event: derived, so the client's optimistic undo writes the same row.
func UndoEventID(event uuid.UUID) uuid.UUID {
	return uuid.NewSHA1(event, []byte("cook.undo"))
}

func (h *handlers) validate(p *Cook) (time.Time, error) {
	on, err := time.Parse(dateLayout, p.CookedOn)
	if err != nil {
		return on, commands.Invalid("cookedOn is YYYY-MM-DD")
	}
	if on.Year() < 2020 || on.After(time.Now().UTC().AddDate(0, 0, 2)) {
		return on, commands.Invalid("cookedOn out of range")
	}
	if p.PortionsMade < 1 || p.PortionsMade > maxPortionsMade {
		return on, commands.Invalid("portions made must be 1-%d", maxPortionsMade)
	}
	if len(p.Uses) > maxLines || len(p.Subs) > maxLines || len(p.Store) > 20 || len(p.Photos) > maxPhotos {
		return on, commands.Invalid("too many entries")
	}
	stored := 0
	for _, s := range p.Store {
		if s.Portions < 1 {
			return on, commands.Invalid("store at least one portion")
		}
		stored += s.Portions
	}
	if stored > p.PortionsMade {
		return on, commands.Invalid("more portions stored than made")
	}
	if stored > 0 && p.LeftoverProductID == nil {
		return on, commands.Invalid("storing portions needs leftoverProductId")
	}
	// Two lines may use the same item (garlic in the marinade and the sauce):
	// their uses apply one after another, each with its own event.
	for _, u := range p.Uses {
		n := 0
		for _, set := range []bool{u.Quantity != nil, u.Portions != nil, u.Level != nil, u.Finished} {
			if set {
				n++
			}
		}
		if n != 1 {
			return on, commands.Invalid("a use is one of quantity, portions, level or finished")
		}
	}
	if p.Note != nil {
		t := strings.TrimSpace(*p.Note)
		if len([]rune(t)) > maxNote {
			return on, commands.Invalid("note is longer than %d characters", maxNote)
		}
		if t == "" {
			p.Note = nil
		} else {
			p.Note = &t
		}
	}
	return on, nil
}

func (h *handlers) cook(ctx context.Context, tx pgx.Tx, p Cook) error {
	hh, err := identity.HouseholdID(ctx)
	if err != nil {
		return err
	}
	on, err := h.validate(&p)
	if err != nil {
		return err
	}
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM cook_session WHERE id = $1)`, p.ID).Scan(&exists); err != nil {
		return err
	}
	if exists {
		return commands.Invalid("id already in use")
	}
	var title string
	err = tx.QueryRow(ctx, `SELECT r.title FROM recipe r JOIN recipe_version v ON v.recipe_id = r.id
		WHERE r.id = $1 AND v.id = $2 AND r.household_id = $3 AND r.deleted_at IS NULL`, p.RecipeID, p.VersionID, hh).Scan(&title)
	if errors.Is(err, pgx.ErrNoRows) {
		return commands.Gone("recipe no longer exists")
	}
	if err != nil {
		return err
	}
	for _, s := range p.Subs {
		var ok bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM substitution WHERE id = $1 AND deleted_at IS NULL AND (household_id IS NULL OR household_id = $2))`,
			s.SubstitutionID, hh).Scan(&ok); err != nil {
			return err
		}
		if !ok {
			return commands.Invalid("unknown substitution")
		}
	}
	for _, ph := range p.Photos {
		ok, err := media.Owned(ctx, tx, hh, ph.BlobRef)
		if err != nil {
			return err
		}
		if !ok {
			return commands.Invalid("unknown photo")
		}
	}
	user := identity.PrincipalFrom(ctx)
	var cookedBy *uuid.UUID
	if user != nil {
		cookedBy = &user.UserID
	}
	// The session row first: events and items reference it.
	subs, _ := json.Marshal(subRecords(p.Subs))
	if _, err := tx.Exec(ctx, `INSERT INTO cook_session (id, household_id, recipe_id, version_id, cooked_on, cooked_by, portions_made, subs, note)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`, p.ID, hh, p.RecipeID, p.VersionID, on, cookedBy, p.PortionsMade, subs, p.Note); err != nil {
		return err
	}

	uses := make([]UseRecord, 0, len(p.Uses))
	for _, u := range p.Uses {
		rec, err := h.use(ctx, tx, hh, p.ID, p.RecipeID, u)
		if err != nil {
			return err
		}
		uses = append(uses, rec)
	}

	stored := make([]StoreRecord, 0, len(p.Store))
	var leftover *uuid.UUID
	if len(p.Store) > 0 {
		id, err := ensureLeftover(ctx, tx, hh, p.RecipeID, title, *p.LeftoverProductID)
		if err != nil {
			return err
		}
		leftover = &id
		for _, s := range p.Store {
			rec := StoreRecord{LocationID: s.LocationID, Portions: s.Portions, StockItemID: s.StockItemID, EventID: s.EventID}
			loc, ok, err := liveLocation(ctx, tx, hh, s.LocationID)
			if err != nil {
				return err
			}
			if !ok {
				rec.Skipped = true
				stored = append(stored, rec)
				continue
			}
			rec.LocationID = loc
			exp, err := inventory.AddCookPortions(ctx, tx, hh, inventory.CookPortions{
				Item: s.StockItemID, Event: s.EventID, Product: id, Location: loc, Session: p.ID, Portions: s.Portions, On: on,
			})
			if err != nil {
				return err
			}
			if exp != nil {
				d := exp.Format(dateLayout)
				rec.ExpiresOn = &d
			}
			stored = append(stored, rec)
		}
	}

	for _, ph := range p.Photos {
		if _, err := tx.Exec(ctx, `INSERT INTO recipe_photo (id, household_id, recipe_id, cook_session_id, blob_ref, taken_on, created_by)
			VALUES ($1, $2, $3, $4, $5, $6, $7)`, ph.ID, hh, p.RecipeID, p.ID, ph.BlobRef, on, cookedBy); err != nil {
			return err
		}
	}

	usesJSON, _ := json.Marshal(uses)
	storedJSON, _ := json.Marshal(stored)
	if _, err := tx.Exec(ctx, `UPDATE cook_session SET uses = $2, stored = $3, leftover_product_id = $4 WHERE id = $1`,
		p.ID, usesJSON, storedJSON, leftover); err != nil {
		return err
	}
	return refreshCounts(ctx, tx, p.RecipeID)
}

func subRecords(subs []Sub) []map[string]uuid.UUID {
	out := make([]map[string]uuid.UUID, 0, len(subs))
	for _, s := range subs {
		out = append(out, map[string]uuid.UUID{"line_id": s.LineID, "substitution_id": s.SubstitutionID})
	}
	return out
}

func (h *handlers) use(ctx context.Context, tx pgx.Tx, hh, session, recipe uuid.UUID, u Use) (UseRecord, error) {
	used, err := inventory.ApplyCookUse(ctx, tx, hh, inventory.CookUse{
		Item: u.StockItemID, Event: u.EventID, Session: session,
		Quantity: u.Quantity, Portions: u.Portions, Level: u.Level, Finished: u.Finished,
	})
	var rej *commands.Error
	if errors.As(err, &rej) && rej.Code == commands.CodeGone {
		return UseRecord{LineID: u.LineID, CookUsed: inventory.CookUsed{Item: u.StockItemID, Event: u.EventID}, Skipped: true}, nil
	}
	if err != nil {
		return UseRecord{}, err
	}
	rec := UseRecord{LineID: u.LineID, CookUsed: used}
	if u.ToShopping == nil {
		return rec, nil
	}
	ts := u.ToShopping
	list, ok, err := liveList(ctx, tx, hh, ts.ListID)
	if err != nil || !ok {
		return rec, err // no list left: the stock update still stands
	}
	add := shopping.ItemAdd{ID: ts.ID, ListID: list, RecipeID: &recipe}
	add.ProductID = commands.Opt[uuid.UUID]{Set: true, Value: &used.Product}
	if ts.Qty != nil {
		add.Qty = commands.Opt[string]{Set: true, Value: ts.Qty}
	}
	if ts.Unit != nil {
		add.Unit = commands.Opt[string]{Set: true, Value: ts.Unit}
	}
	if err := shopping.AddItem(ctx, tx, add); err != nil {
		return UseRecord{}, err
	}
	rec.ShoppingItemID = &ts.ID
	return rec, nil
}

// liveLocation returns loc if it is live, else the household's first live
// location of the same kind (it was deleted on another device). ok is false
// when there is none.
func liveLocation(ctx context.Context, tx pgx.Tx, hh, loc uuid.UUID) (uuid.UUID, bool, error) {
	var id uuid.UUID
	err := tx.QueryRow(ctx, `SELECT l.id FROM location l, location want
		WHERE want.id = $1 AND want.household_id = $2 AND l.household_id = $2 AND l.deleted_at IS NULL AND l.kind = want.kind
		ORDER BY l.id = $1 DESC, l.sort, l.id LIMIT 1`, loc, hh).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return id, false, nil
	}
	return id, err == nil, err
}

// liveList returns list if it is live, else the household's first live list.
func liveList(ctx context.Context, tx pgx.Tx, hh, list uuid.UUID) (uuid.UUID, bool, error) {
	var id uuid.UUID
	err := tx.QueryRow(ctx, `SELECT id FROM shopping_list WHERE household_id = $2 AND deleted_at IS NULL
		ORDER BY id = $1 DESC, sort, id LIMIT 1`, list, hh).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return id, false, nil
	}
	return id, err == nil, err
}

// LeftoverName is "Leftover chicken curry"; a title starting with an acronym
// or proper name ("BBQ ribs") keeps its case. Mirrors leftoverName in the web
// client (shared cases in testdata/leftover_names.json).
func LeftoverName(title string) string {
	r := []rune(strings.TrimSpace(title))
	if len(r) == 0 {
		return "Leftovers"
	}
	if len(r) > 1 && unicode.IsUpper(r[1]) {
		return "Leftover " + string(r)
	}
	r[0] = unicode.ToLower(r[0])
	return "Leftover " + string(r)
}

// ensureLeftover returns the recipe's leftover product ("Leftover chili",
// category leftovers), creating it under want the first time.
func ensureLeftover(ctx context.Context, tx pgx.Tx, hh, recipe uuid.UUID, title string, want uuid.UUID) (uuid.UUID, error) {
	var id uuid.UUID
	err := tx.QueryRow(ctx, `SELECT id FROM product WHERE household_id = $1 AND recipe_id = $2 AND deleted_at IS NULL`, hh, recipe).Scan(&id)
	if err == nil || !errors.Is(err, pgx.ErrNoRows) {
		return id, err
	}
	if err := commands.CheckOwned(ctx, tx, "product", want, hh); err != nil {
		return id, err
	}
	base := LeftoverName(title)
	// Product names are unique per household; another recipe may share the title.
	name := base
	for n := 2; ; n++ {
		var taken bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM product WHERE household_id = $1 AND lower(name) = lower($2) AND brand IS NULL AND deleted_at IS NULL)`,
			hh, name).Scan(&taken); err != nil {
			return id, err
		}
		if !taken {
			break
		}
		name = fmt.Sprintf("%s (%d)", base, n)
	}
	_, err = tx.Exec(ctx, `INSERT INTO product (id, household_id, name, category_id, default_location_kind, recipe_id, match_source, enrich_state)
		VALUES ($1, $2, $3, (SELECT id FROM category WHERE household_id IS NULL AND slug = 'leftovers'), 'fridge', $4, 'none', 'skipped')
		ON CONFLICT (id) DO NOTHING`, want, hh, name, recipe)
	if err != nil {
		return id, err
	}
	var owned uuid.UUID
	err = tx.QueryRow(ctx, `SELECT id FROM product WHERE household_id = $1 AND recipe_id = $2 AND deleted_at IS NULL`, hh, recipe).Scan(&owned)
	if errors.Is(err, pgx.ErrNoRows) {
		return id, commands.Invalid("leftoverProductId is another product")
	}
	return owned, err
}

// refreshCounts recomputes the recipe's cooked_count and last_cooked_at from
// its live sessions (sets, not increments, so undo is exact).
func refreshCounts(ctx context.Context, tx pgx.Tx, recipe uuid.UUID) error {
	_, err := tx.Exec(ctx, `UPDATE recipe r SET cooked_count = s.n, last_cooked_at = s.last
		FROM (SELECT count(*)::int AS n, max(cooked_on)::timestamptz AS last FROM cook_session
		      WHERE recipe_id = $1 AND undone_at IS NULL AND deleted_at IS NULL) s
		WHERE r.id = $1`, recipe)
	return err
}

func (h *handlers) undo(ctx context.Context, tx pgx.Tx, p Undo) error {
	hh, err := identity.HouseholdID(ctx)
	if err != nil {
		return err
	}
	var (
		recipe            uuid.UUID
		created           time.Time
		undone            *time.Time
		usesRaw, storeRaw []byte
	)
	err = tx.QueryRow(ctx, `SELECT recipe_id, created_at, undone_at, uses, stored FROM cook_session
		WHERE id = $1 AND household_id = $2 AND deleted_at IS NULL FOR UPDATE`, p.ID, hh).Scan(&recipe, &created, &undone, &usesRaw, &storeRaw)
	if errors.Is(err, pgx.ErrNoRows) {
		return commands.Gone("that cook no longer exists")
	}
	if err != nil {
		return err
	}
	if undone != nil {
		return nil // already undone (another device)
	}
	if commands.ClientTime(ctx).Sub(created) > UndoWindow {
		return commands.Invalid("a cook can only be undone within 24 hours")
	}
	var uses []UseRecord
	var stored []StoreRecord
	if err := json.Unmarshal(usesRaw, &uses); err != nil {
		return fmt.Errorf("cook session %s uses: %w", p.ID, err)
	}
	if err := json.Unmarshal(storeRaw, &stored); err != nil {
		return fmt.Errorf("cook session %s stored: %w", p.ID, err)
	}
	// Newest first: two uses of one item undo in reverse, so a use that
	// finished the item brings it back before the earlier delta goes on top.
	slices.Reverse(uses)
	for _, u := range uses {
		if u.Skipped {
			continue
		}
		if _, err := inventory.UndoCookUse(ctx, tx, hh, p.ID, UndoEventID(u.Event), u.CookUsed); err != nil {
			return err
		}
		if u.ShoppingItemID != nil {
			if _, err := tx.Exec(ctx, `UPDATE shopping_item SET deleted_at = now()
				WHERE id = $1 AND household_id = $2 AND checked_at IS NULL AND deleted_at IS NULL`, *u.ShoppingItemID, hh); err != nil {
				return err
			}
		}
	}
	for _, s := range stored {
		if s.Skipped {
			continue
		}
		if _, err := inventory.RemoveCookPortions(ctx, tx, hh, p.ID, s.StockItemID, s.EventID, UndoEventID(s.EventID)); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE recipe_photo SET deleted_at = now() WHERE cook_session_id = $1 AND deleted_at IS NULL`, p.ID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE cook_session SET undone_at = now() WHERE id = $1`, p.ID); err != nil {
		return err
	}
	return refreshCounts(ctx, tx, recipe)
}
