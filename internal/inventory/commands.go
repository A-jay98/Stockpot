package inventory

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/a-jay98/stockpot/internal/catalog"
	"github.com/a-jay98/stockpot/internal/commands"
	"github.com/a-jay98/stockpot/internal/identity"
	"github.com/a-jay98/stockpot/internal/units"
)

// Opt re-exports commands.Opt for payload structs.
type Opt[T any] = commands.Opt[T]

// Amount is how much of an item there is: exact (quantity + unit), counted
// portions, or a rough level. Rough levels never force counting.
type Amount struct {
	Kind     string  `json:"amountKind"`
	Quantity *string `json:"quantity"`
	Unit     *string `json:"unit"`
	Portions *int    `json:"portions"`
	Level    *string `json:"level"`
}

// NewProduct creates the product a stock item is of, by name.
type NewProduct struct {
	ID           uuid.UUID  `json:"id"`
	Name         string     `json:"name"`
	Brand        *string    `json:"brand"`
	IngredientID *uuid.UUID `json:"ingredientId"`
	CategoryID   *uuid.UUID `json:"categoryId"`
}

// StockAdd is inventory.stock.add. Give productId, or newProduct to create it
// by name. Without expiresOn the expiry is estimated from food rules. On is
// the local date the user acted (default: today, UTC).
type StockAdd struct {
	ID         uuid.UUID   `json:"id"`
	EventID    uuid.UUID   `json:"eventId"`
	ProductID  *uuid.UUID  `json:"productId"`
	NewProduct *NewProduct `json:"newProduct"`
	LocationID uuid.UUID   `json:"locationId"`
	Amount
	ExpiresOn  *string    `json:"expiresOn"`
	ExpiryKind *string    `json:"expiryKind"`
	OpenedOn   *string    `json:"openedOn"`
	Source     string     `json:"source"`
	StoreID    *uuid.UUID `json:"storeId"`
	Note       *string    `json:"note"`
	On         *string    `json:"on"`
}

// StockConsume is inventory.stock.consume: a delta. Reaching zero finishes the item.
type StockConsume struct {
	ID       uuid.UUID `json:"id"`
	EventID  uuid.UUID `json:"eventId"`
	Quantity *string   `json:"quantity"`
	Unit     *string   `json:"unit"`
	Portions *int      `json:"portions"`
	Reason   *string   `json:"reason"`
}

// StockRestock is inventory.stock.restock: a positive delta (the + stepper).
type StockRestock struct {
	ID       uuid.UUID `json:"id"`
	EventID  uuid.UUID `json:"eventId"`
	Quantity *string   `json:"quantity"`
	Unit     *string   `json:"unit"`
	Portions *int      `json:"portions"`
}

// StockAdjust is inventory.stock.adjust: set the amount (zero finishes the item).
type StockAdjust struct {
	ID       uuid.UUID `json:"id"`
	EventID  uuid.UUID `json:"eventId"`
	Quantity *string   `json:"quantity"`
	Portions *int      `json:"portions"`
}

// StockSetLevel is inventory.stock.set_level for a rough-level item.
type StockSetLevel struct {
	ID      uuid.UUID `json:"id"`
	EventID uuid.UUID `json:"eventId"`
	Level   string    `json:"level"`
}

// StockMark is inventory.stock.mark_low and unmark_low.
type StockMark struct {
	ID      uuid.UUID `json:"id"`
	EventID uuid.UUID `json:"eventId"`
}

// StockMove is inventory.stock.move.
type StockMove struct {
	ID         uuid.UUID `json:"id"`
	EventID    uuid.UUID `json:"eventId"`
	LocationID uuid.UUID `json:"locationId"`
	On         *string   `json:"on"`
}

// SplitPart is one new item cut from the parent.
type SplitPart struct {
	ID       uuid.UUID `json:"id"`
	EventID  uuid.UUID `json:"eventId"`
	Quantity *string   `json:"quantity"`
	Portions *int      `json:"portions"`
}

// StockSplit is inventory.stock.split: cut parts (e.g. a meat pack into
// freezer portions) out of an item. Parts take the parent's product, expiry
// and unit; the parent keeps the rest and is finished when nothing is left.
type StockSplit struct {
	ID         uuid.UUID   `json:"id"`
	EventID    uuid.UUID   `json:"eventId"`
	Parts      []SplitPart `json:"parts"`
	LocationID *uuid.UUID  `json:"locationId"`
	On         *string     `json:"on"`
}

// StockOpen is inventory.stock.open.
type StockOpen struct {
	ID      uuid.UUID `json:"id"`
	EventID uuid.UUID `json:"eventId"`
	On      *string   `json:"on"`
}

// StockTransfer is inventory.stock.thaw and freeze: move to a location and
// start the thawed or frozen clock.
type StockTransfer struct {
	ID         uuid.UUID `json:"id"`
	EventID    uuid.UUID `json:"eventId"`
	LocationID uuid.UUID `json:"locationId"`
	On         *string   `json:"on"`
}

// StockDiscard is inventory.stock.discard.
type StockDiscard struct {
	ID      uuid.UUID `json:"id"`
	EventID uuid.UUID `json:"eventId"`
	Reason  string    `json:"reason"`
}

// StockUpdate is inventory.stock.update: expiry, note, or switch the amount mode.
type StockUpdate struct {
	ID         uuid.UUID   `json:"id"`
	EventID    uuid.UUID   `json:"eventId"`
	ExpiresOn  Opt[string] `json:"expiresOn"`
	ExpiryKind Opt[string] `json:"expiryKind"`
	Note       Opt[string] `json:"note"`
	Amount     *Amount     `json:"amount"`
}

// Register adds the inventory commands to r. res links products created by
// name to ingredients.
func Register(r *commands.Registry, res catalog.Resolver, rec PurchaseRecorder) {
	h := &handlers{createProduct: catalog.CreateProduct(res), record: rec}
	commands.Register(r, "inventory.stock.add", h.add)
	commands.Register(r, "inventory.stock.consume", h.consume)
	commands.Register(r, "inventory.stock.restock", h.restock)
	commands.Register(r, "inventory.stock.adjust", h.adjust)
	commands.Register(r, "inventory.stock.set_level", h.setLevel)
	commands.Register(r, "inventory.stock.mark_low", h.mark(true))
	commands.Register(r, "inventory.stock.unmark_low", h.mark(false))
	commands.Register(r, "inventory.stock.move", h.move)
	commands.Register(r, "inventory.stock.split", h.split)
	commands.Register(r, "inventory.stock.open", h.open)
	commands.Register(r, "inventory.stock.thaw", h.transfer(OpThaw))
	commands.Register(r, "inventory.stock.freeze", h.transfer(OpFreeze))
	commands.Register(r, "inventory.stock.discard", h.discard)
	commands.Register(r, "inventory.stock.update", h.update)
}

// PurchaseRecorder logs a purchase and refreshes product_stat. The shopping
// package owns that ledger (it imports this one for expiry estimates), so
// the app injects shopping.RecordPurchase here.
type PurchaseRecorder func(ctx context.Context, tx pgx.Tx, hh, product uuid.UUID, store *uuid.UUID, at time.Time, qty, unit *string) error

type handlers struct {
	createProduct commands.Handler[catalog.ProductCreate]
	record        PurchaseRecorder
}

var (
	levels         = map[string]bool{"full": true, "most": true, "half": true, "low": true, "almost_out": true}
	addSources     = map[string]bool{"manual": true, "shopping": true, "capture": true, "cook": true}
	discardReasons = map[string]bool{"expired": true, "spoiled": true, "unwanted": true, "mistake": true, "other": true}
	givenKinds     = map[string]bool{"use_by": true, "best_before": true}
)

const maxPortions = 100000

func isZero(q string) bool { r, ok := new(big.Rat).SetString(q); return ok && r.Sign() == 0 }

// validate checks an amount and normalizes its quantity. Zero is only fine
// where the caller says so (adjust finishes an item).
func (a *Amount) validate(allowZero bool) error {
	switch a.Kind {
	case "exact":
		if a.Quantity == nil || a.Unit == nil || a.Portions != nil || a.Level != nil {
			return commands.Invalid("an exact amount is a quantity and a unit")
		}
		q, err := units.ParseQuantity(*a.Quantity)
		if err != nil {
			return commands.Invalid("%v", err)
		}
		if !allowZero && isZero(q) {
			return commands.Invalid("quantity must be above zero")
		}
		a.Quantity = &q
		if !units.Valid(*a.Unit) {
			return commands.Invalid("unknown unit %q", *a.Unit)
		}
	case "portions":
		if a.Portions == nil || a.Quantity != nil || a.Unit != nil || a.Level != nil {
			return commands.Invalid("a portions amount is a count of portions")
		}
		if *a.Portions < 0 || *a.Portions > maxPortions || (!allowZero && *a.Portions == 0) {
			return commands.Invalid("portions out of range")
		}
	case "level":
		if a.Level == nil || a.Quantity != nil || a.Unit != nil || a.Portions != nil {
			return commands.Invalid("a level amount is just a level")
		}
		if !levels[*a.Level] {
			return commands.Invalid("unknown level %q", *a.Level)
		}
	default:
		return commands.Invalid("amountKind must be exact, portions or level")
	}
	return nil
}

// day is the date the user acted: payload `on`, else today (UTC). A plausible
// window keeps a bad clock from writing dates years off.
func day(on *string) (time.Time, error) {
	if on == nil {
		n := time.Now().UTC()
		return time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, time.UTC), nil
	}
	d, err := time.Parse(DateLayout, *on)
	if err != nil {
		return d, commands.Invalid("dates are YYYY-MM-DD")
	}
	if d.Year() < 2020 || d.After(time.Now().UTC().AddDate(0, 0, 2)) {
		return d, commands.Invalid("date out of range")
	}
	return d, nil
}

func optDay(s *string) (*time.Time, error) {
	if s == nil {
		return nil, nil
	}
	d, err := time.Parse(DateLayout, *s)
	if err != nil || d.Year() < 2000 || d.Year() > 2100 {
		return nil, commands.Invalid("dates are YYYY-MM-DD")
	}
	return &d, nil
}

type item struct {
	ID         uuid.UUID
	ProductID  uuid.UUID
	LocationID *uuid.UUID
	LocKind    LocationKind
	Amount
	Low                          bool
	ExpiresOn                    *time.Time
	ExpiryKind                   *string
	OpenedOn, ThawedOn, FrozenOn *time.Time
	Note                         *string
}

// load locks and returns a live item of the caller's household.
func load(ctx context.Context, tx pgx.Tx, hh, id uuid.UUID) (*item, error) {
	it := item{ID: id}
	var lk *string
	err := tx.QueryRow(ctx, `
		SELECT s.product_id, s.location_id, l.kind, s.amount_kind, s.quantity::text, s.unit, s.portions, s.level, s.low,
		       s.expires_on, s.expiry_kind, s.opened_on, s.thawed_on, s.frozen_on, s.note
		FROM stock_item s LEFT JOIN location l ON l.id = s.location_id
		WHERE s.id = $1 AND s.household_id = $2 AND s.deleted_at IS NULL FOR UPDATE OF s`, id, hh).Scan(
		&it.ProductID, &it.LocationID, &lk, &it.Kind, &it.Quantity, &it.Unit, &it.Portions, &it.Level, &it.Low,
		&it.ExpiresOn, &it.ExpiryKind, &it.OpenedOn, &it.ThawedOn, &it.FrozenOn, &it.Note)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, commands.Gone("item no longer exists")
	}
	if lk != nil {
		it.LocKind = LocationKind(*lk)
	}
	return &it, err
}

// locationKind returns the kind of one of the household's live locations.
func locationKind(ctx context.Context, tx pgx.Tx, hh, id uuid.UUID) (LocationKind, error) {
	var k string
	err := tx.QueryRow(ctx, `SELECT kind FROM location WHERE id = $1 AND household_id = $2 AND deleted_at IS NULL`, id, hh).Scan(&k)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", commands.Gone("location no longer exists")
	}
	return LocationKind(k), err
}

// delta renders the event delta of an amount: quantity text, or portions count.
func delta(neg bool, qty *string, portions *int) *string {
	var s string
	switch {
	case qty != nil:
		s = *qty
	case portions != nil:
		s = fmt.Sprint(*portions)
	default:
		return nil
	}
	if neg {
		s = "-" + s
	}
	return &s
}

type event struct {
	ID       uuid.UUID
	Item     uuid.UUID
	Kind     string
	Delta    *string
	Unit     *string
	From, To *uuid.UUID
	Reason   *string
}

func writeEvent(ctx context.Context, tx pgx.Tx, hh uuid.UUID, e event) error {
	var actor *uuid.UUID
	if p := identity.PrincipalFrom(ctx); p != nil {
		actor = &p.UserID
	}
	var cmd *uuid.UUID
	if id, ok := commands.ID(ctx); ok {
		cmd = &id
	}
	_, err := tx.Exec(ctx, `INSERT INTO stock_event (id, household_id, stock_item_id, kind, delta, unit, from_location_id, to_location_id, reason, actor_id, command_id)
		VALUES ($1, $2, $3, $4, $5::text::numeric, $6, $7, $8, $9, $10, $11)`,
		e.ID, hh, e.Item, e.Kind, e.Delta, e.Unit, e.From, e.To, e.Reason, actor, cmd)
	return err
}

func finish(ctx context.Context, tx pgx.Tx, id uuid.UUID) error {
	_, err := tx.Exec(ctx, `UPDATE stock_item SET deleted_at = now() WHERE id = $1`, id)
	return err
}

func (h *handlers) chain(ctx context.Context, tx pgx.Tx, hh, product uuid.UUID) (Chain, error) {
	return LoadChain(ctx, tx, hh, product)
}

type newItem struct {
	ID, Product, Location uuid.UUID
	Amount
	ExpiresOn  *time.Time
	ExpiryKind *string
	OpenedOn   *time.Time
	FrozenOn   *time.Time
	Source     string
	Parent     *uuid.UUID
	Note       *string
}

func insertItem(ctx context.Context, tx pgx.Tx, hh uuid.UUID, n newItem) error {
	_, err := tx.Exec(ctx, `INSERT INTO stock_item (id, household_id, product_id, location_id, amount_kind, quantity, unit, portions, level,
			expires_on, expiry_kind, opened_on, frozen_on, source, parent_item_id, note)
		VALUES ($1, $2, $3, $4, $5, $6::text::numeric, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)`,
		n.ID, hh, n.Product, n.Location, n.Kind, n.Quantity, n.Unit, n.Portions, n.Level,
		n.ExpiresOn, n.ExpiryKind, n.OpenedOn, n.FrozenOn, n.Source, n.Parent, n.Note)
	return err
}

func (h *handlers) add(ctx context.Context, tx pgx.Tx, p StockAdd) error {
	hh, err := identity.HouseholdID(ctx)
	if err != nil {
		return err
	}
	if (p.ProductID == nil) == (p.NewProduct == nil) {
		return commands.Invalid("give productId or newProduct")
	}
	if !addSources[p.Source] {
		return commands.Invalid("source must be manual, shopping, capture or cook")
	}
	if err := p.validate(false); err != nil {
		return err
	}
	on, err := day(p.On)
	if err != nil {
		return err
	}
	expires, err := optDay(p.ExpiresOn)
	if err != nil {
		return err
	}
	opened, err := optDay(p.OpenedOn)
	if err != nil {
		return err
	}
	kind := p.ExpiryKind
	switch {
	case expires == nil && kind != nil:
		return commands.Invalid("expiryKind needs expiresOn")
	case expires != nil && kind == nil:
		k := "best_before"
		kind = &k
	case kind != nil && !givenKinds[*kind]:
		return commands.Invalid("expiryKind must be use_by or best_before")
	}
	if err := checkLen(p.Note, 1000, "note"); err != nil {
		return err
	}

	var product uuid.UUID
	if p.NewProduct != nil {
		np := p.NewProduct
		create := catalog.ProductCreate{ID: np.ID}
		create.Name = Opt[string]{Set: true, Value: &np.Name}
		if np.Brand != nil {
			create.Brand = Opt[string]{Set: true, Value: np.Brand}
		}
		if np.IngredientID != nil {
			create.IngredientID = Opt[uuid.UUID]{Set: true, Value: np.IngredientID}
		}
		if np.CategoryID != nil {
			create.CategoryID = Opt[uuid.UUID]{Set: true, Value: np.CategoryID}
		}
		if err := h.createProduct(ctx, tx, create); err != nil {
			return err
		}
		product = np.ID
	} else {
		product = *p.ProductID
		var ok bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM product WHERE id = $1 AND household_id = $2 AND deleted_at IS NULL)`, product, hh).Scan(&ok); err != nil {
			return err
		}
		if !ok {
			return commands.Gone("product no longer exists")
		}
	}
	if p.StoreID != nil {
		var ok bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM store WHERE id = $1 AND household_id = $2 AND deleted_at IS NULL)`, *p.StoreID, hh).Scan(&ok); err != nil {
			return err
		}
		if !ok {
			return commands.Gone("store no longer exists")
		}
	}
	loc, err := locationKind(ctx, tx, hh, p.LocationID)
	if err != nil {
		return err
	}

	if expires == nil {
		c, err := h.chain(ctx, tx, hh, product)
		if err != nil {
			return err
		}
		d, ok := Estimate(c, OpAdd, loc, on)
		if opened != nil && loc != Freezer {
			if o, ook := Estimate(c, OpOpen, loc, *opened); ook && (!ok || o.Before(d)) {
				d, ok = o, true
			}
		}
		if ok {
			expires, kind = &d, ptr("estimated")
		}
	}
	var frozen *time.Time
	if loc == Freezer {
		frozen = &on
	}
	if err := insertItem(ctx, tx, hh, newItem{
		ID: p.ID, Product: product, Location: p.LocationID, Amount: p.Amount,
		ExpiresOn: expires, ExpiryKind: kind, OpenedOn: opened, FrozenOn: frozen, Source: p.Source, Note: p.Note,
	}); err != nil {
		return err
	}
	if err := writeEvent(ctx, tx, hh, event{ID: p.EventID, Item: p.ID, Kind: "add", Delta: delta(false, p.Quantity, p.Portions), Unit: p.Unit, To: &p.LocationID}); err != nil {
		return err
	}
	if p.Source == "manual" || p.Source == "shopping" {
		var q, u *string
		if p.Kind == "exact" {
			q, u = p.Quantity, p.Unit
		}
		return h.record(ctx, tx, hh, product, p.StoreID, commands.ClientTime(ctx), q, u)
	}
	return nil
}

func ptr[T any](v T) *T { return &v }

func checkLen(s *string, limit int, field string) error {
	if s != nil && len(*s) > limit {
		return commands.Invalid("%s is too long", field)
	}
	return nil
}

// numOps does exact decimal arithmetic in the database: min(a, b) and max(a-b, 0).
func numOps(ctx context.Context, tx pgx.Tx, a, b string) (taken, left string, err error) {
	err = tx.QueryRow(ctx, `SELECT LEAST($1::text::numeric, $2::text::numeric)::text, GREATEST($1::text::numeric - $2::text::numeric, 0)::text`, a, b).Scan(&taken, &left)
	return
}

func (h *handlers) consume(ctx context.Context, tx pgx.Tx, p StockConsume) error {
	hh, err := identity.HouseholdID(ctx)
	if err != nil {
		return err
	}
	if err := checkLen(p.Reason, 200, "reason"); err != nil {
		return err
	}
	it, err := load(ctx, tx, hh, p.ID)
	if err != nil {
		return err
	}
	ev := event{ID: p.EventID, Item: p.ID, Kind: "consume", Unit: it.Unit, From: it.LocationID, Reason: p.Reason}
	switch it.Kind {
	case "exact":
		if p.Quantity == nil || p.Portions != nil {
			return commands.Invalid("consume a quantity from an exact amount")
		}
		q, err := units.ParseQuantity(*p.Quantity)
		if err != nil || isZero(q) {
			return commands.Invalid("quantity must be above zero")
		}
		if p.Unit != nil && *p.Unit != *it.Unit {
			return commands.Invalid("unit must be %s", *it.Unit)
		}
		taken, left, err := numOps(ctx, tx, *it.Quantity, q)
		if err != nil {
			return err
		}
		if isZero(left) {
			err = finish(ctx, tx, p.ID)
		} else {
			_, err = tx.Exec(ctx, `UPDATE stock_item SET quantity = $2::text::numeric WHERE id = $1`, p.ID, left)
		}
		if err != nil {
			return err
		}
		ev.Delta = delta(true, &taken, nil)
	case "portions":
		if p.Portions == nil || p.Quantity != nil || *p.Portions <= 0 {
			return commands.Invalid("consume a number of portions")
		}
		taken := min(*p.Portions, *it.Portions)
		if left := *it.Portions - taken; left == 0 {
			err = finish(ctx, tx, p.ID)
		} else {
			_, err = tx.Exec(ctx, `UPDATE stock_item SET portions = $2 WHERE id = $1`, p.ID, left)
		}
		if err != nil {
			return err
		}
		ev.Delta = delta(true, nil, &taken)
	default:
		return commands.Invalid("a rough-level item changes by set_level")
	}
	return writeEvent(ctx, tx, hh, ev)
}

func (h *handlers) restock(ctx context.Context, tx pgx.Tx, p StockRestock) error {
	hh, err := identity.HouseholdID(ctx)
	if err != nil {
		return err
	}
	it, err := load(ctx, tx, hh, p.ID)
	if err != nil {
		return err
	}
	ev := event{ID: p.EventID, Item: p.ID, Kind: "adjust", Unit: it.Unit, To: it.LocationID}
	switch it.Kind {
	case "exact":
		if p.Quantity == nil || p.Portions != nil {
			return commands.Invalid("restock a quantity onto an exact amount")
		}
		q, err := units.ParseQuantity(*p.Quantity)
		if err != nil || isZero(q) {
			return commands.Invalid("quantity must be above zero")
		}
		if p.Unit != nil && *p.Unit != *it.Unit {
			return commands.Invalid("unit must be %s", *it.Unit)
		}
		if _, err := tx.Exec(ctx, `UPDATE stock_item SET quantity = quantity + $2::text::numeric WHERE id = $1`, p.ID, q); err != nil {
			return err
		}
		ev.Delta = delta(false, &q, nil)
	case "portions":
		if p.Portions == nil || p.Quantity != nil || *p.Portions <= 0 {
			return commands.Invalid("restock a number of portions")
		}
		if _, err := tx.Exec(ctx, `UPDATE stock_item SET portions = portions + $2 WHERE id = $1`, p.ID, *p.Portions); err != nil {
			return err
		}
		ev.Delta = delta(false, nil, p.Portions)
	default:
		return commands.Invalid("a rough-level item changes by set_level")
	}
	return writeEvent(ctx, tx, hh, ev)
}

func (h *handlers) adjust(ctx context.Context, tx pgx.Tx, p StockAdjust) error {
	hh, err := identity.HouseholdID(ctx)
	if err != nil {
		return err
	}
	it, err := load(ctx, tx, hh, p.ID)
	if err != nil {
		return err
	}
	next := Amount{Kind: it.Kind, Quantity: p.Quantity, Portions: p.Portions, Unit: it.Unit}
	if it.Kind == "portions" {
		next.Unit = nil
	}
	if it.Kind == "level" {
		return commands.Invalid("a rough-level item changes by set_level")
	}
	if err := next.validate(true); err != nil {
		return err
	}
	ev := event{ID: p.EventID, Item: p.ID, Kind: "adjust", Unit: it.Unit}
	var zero bool
	if it.Kind == "exact" {
		if err := tx.QueryRow(ctx, `SELECT ($1::text::numeric - $2::text::numeric)::text`, *next.Quantity, *it.Quantity).Scan(&ev.Delta); err != nil {
			return err
		}
		zero = isZero(*next.Quantity)
		if !zero {
			_, err = tx.Exec(ctx, `UPDATE stock_item SET quantity = $2::text::numeric WHERE id = $1`, p.ID, *next.Quantity)
		}
	} else {
		d := fmt.Sprint(*next.Portions - *it.Portions)
		ev.Delta = &d
		zero = *next.Portions == 0
		if !zero {
			_, err = tx.Exec(ctx, `UPDATE stock_item SET portions = $2 WHERE id = $1`, p.ID, *next.Portions)
		}
	}
	if err != nil {
		return err
	}
	if zero {
		if err := finish(ctx, tx, p.ID); err != nil {
			return err
		}
	}
	return writeEvent(ctx, tx, hh, ev)
}

func (h *handlers) setLevel(ctx context.Context, tx pgx.Tx, p StockSetLevel) error {
	hh, err := identity.HouseholdID(ctx)
	if err != nil {
		return err
	}
	if !levels[p.Level] {
		return commands.Invalid("unknown level %q", p.Level)
	}
	it, err := load(ctx, tx, hh, p.ID)
	if err != nil {
		return err
	}
	if it.Kind != "level" {
		return commands.Invalid("only rough-level items have a level")
	}
	if _, err := tx.Exec(ctx, `UPDATE stock_item SET level = $2 WHERE id = $1`, p.ID, p.Level); err != nil {
		return err
	}
	return writeEvent(ctx, tx, hh, event{ID: p.EventID, Item: p.ID, Kind: "adjust", Reason: &p.Level})
}

func (h *handlers) mark(low bool) commands.Handler[StockMark] {
	reason := "cleared"
	if low {
		reason = "marked"
	}
	return func(ctx context.Context, tx pgx.Tx, p StockMark) error {
		hh, err := identity.HouseholdID(ctx)
		if err != nil {
			return err
		}
		if _, err := load(ctx, tx, hh, p.ID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE stock_item SET low = $2 WHERE id = $1`, p.ID, low); err != nil {
			return err
		}
		return writeEvent(ctx, tx, hh, event{ID: p.EventID, Item: p.ID, Kind: "low", Reason: &reason})
	}
}

// relocate moves it to a location and applies what that means for its
// clocks: into a freezer starts the frozen clock, out of one the thawed
// clock, and an estimated expiry is re-estimated. A date the user gave stays
// (their use-by beats our guess) except across the freezer boundary, where
// it no longer applies.
func (h *handlers) relocate(ctx context.Context, tx pgx.Tx, hh uuid.UUID, it *item, to uuid.UUID, op *Op, on time.Time) (LocationKind, error) {
	kind, err := locationKind(ctx, tx, hh, to)
	if err != nil {
		return "", err
	}
	nextOp := OpAdd
	freezing, thawing := kind == Freezer && it.LocKind != Freezer, kind != Freezer && it.LocKind == Freezer
	switch {
	case op != nil:
		nextOp = *op
	case freezing:
		nextOp = OpFreeze
	case thawing:
		nextOp = OpThaw
	}
	crosses := nextOp == OpFreeze || nextOp == OpThaw
	set := "location_id = $2"
	args := []any{it.ID, to}
	if nextOp == OpFreeze {
		args = append(args, on)
		set += fmt.Sprintf(", frozen_on = $%d", len(args))
	}
	if nextOp == OpThaw {
		args = append(args, on)
		set += fmt.Sprintf(", thawed_on = $%d", len(args))
	}
	// A plain move never re-estimates an existing date: the clock does not
	// restart, so a move can only keep an expiry, never lengthen it.
	if crosses || it.ExpiresOn == nil {
		c, err := h.chain(ctx, tx, hh, it.ProductID)
		if err != nil {
			return "", err
		}
		if d, ok := Estimate(c, nextOp, kind, on); ok {
			args = append(args, d)
			set += fmt.Sprintf(", expires_on = $%d, expiry_kind = 'estimated'", len(args))
		} else if crosses {
			set += ", expires_on = NULL, expiry_kind = NULL"
		}
	}
	_, err = tx.Exec(ctx, "UPDATE stock_item SET "+set+" WHERE id = $1", args...)
	return kind, err
}

func (h *handlers) move(ctx context.Context, tx pgx.Tx, p StockMove) error {
	hh, err := identity.HouseholdID(ctx)
	if err != nil {
		return err
	}
	on, err := day(p.On)
	if err != nil {
		return err
	}
	it, err := load(ctx, tx, hh, p.ID)
	if err != nil {
		return err
	}
	if it.LocationID == nil || *it.LocationID != p.LocationID {
		if _, err := h.relocate(ctx, tx, hh, it, p.LocationID, nil, on); err != nil {
			return err
		}
	}
	return writeEvent(ctx, tx, hh, event{ID: p.EventID, Item: p.ID, Kind: "move", From: it.LocationID, To: &p.LocationID})
}

func (h *handlers) transfer(op Op) commands.Handler[StockTransfer] {
	kind := "thaw"
	if op == OpFreeze {
		kind = "freeze"
	}
	return func(ctx context.Context, tx pgx.Tx, p StockTransfer) error {
		hh, err := identity.HouseholdID(ctx)
		if err != nil {
			return err
		}
		on, err := day(p.On)
		if err != nil {
			return err
		}
		it, err := load(ctx, tx, hh, p.ID)
		if err != nil {
			return err
		}
		to, err := locationKind(ctx, tx, hh, p.LocationID)
		if err != nil {
			return err
		}
		if (op == OpFreeze) != (to == Freezer) {
			if op == OpFreeze {
				return commands.Invalid("freeze into a freezer")
			}
			return commands.Invalid("thaw into a location that is not a freezer")
		}
		if _, err := h.relocate(ctx, tx, hh, it, p.LocationID, &op, on); err != nil {
			return err
		}
		return writeEvent(ctx, tx, hh, event{ID: p.EventID, Item: p.ID, Kind: kind, From: it.LocationID, To: &p.LocationID})
	}
}

func (h *handlers) open(ctx context.Context, tx pgx.Tx, p StockOpen) error {
	hh, err := identity.HouseholdID(ctx)
	if err != nil {
		return err
	}
	on, err := day(p.On)
	if err != nil {
		return err
	}
	it, err := load(ctx, tx, hh, p.ID)
	if err != nil {
		return err
	}
	if it.OpenedOn == nil { // already open: opening again only logs the event
		set, args := "opened_on = $2", []any{p.ID, on}
		c, err := h.chain(ctx, tx, hh, it.ProductID)
		if err != nil {
			return err
		}
		// Opening only shortens: a nearer date the user gave, or one already estimated, stays.
		if d, ok := Estimate(c, OpOpen, it.LocKind, on); ok && (it.ExpiresOn == nil || d.Before(*it.ExpiresOn)) {
			args = append(args, d)
			set += ", expires_on = $3, expiry_kind = 'estimated'"
		}
		if _, err := tx.Exec(ctx, "UPDATE stock_item SET "+set+" WHERE id = $1", args...); err != nil {
			return err
		}
	}
	return writeEvent(ctx, tx, hh, event{ID: p.EventID, Item: p.ID, Kind: "open", From: it.LocationID})
}

func (h *handlers) discard(ctx context.Context, tx pgx.Tx, p StockDiscard) error {
	hh, err := identity.HouseholdID(ctx)
	if err != nil {
		return err
	}
	if !discardReasons[p.Reason] {
		return commands.Invalid("reason must be expired, spoiled, unwanted, mistake or other")
	}
	it, err := load(ctx, tx, hh, p.ID)
	if err != nil {
		return err
	}
	if err := finish(ctx, tx, p.ID); err != nil {
		return err
	}
	return writeEvent(ctx, tx, hh, event{ID: p.EventID, Item: p.ID, Kind: "discard", Delta: delta(true, it.Quantity, it.Portions), Unit: it.Unit, From: it.LocationID, Reason: &p.Reason})
}

func (h *handlers) update(ctx context.Context, tx pgx.Tx, p StockUpdate) error {
	hh, err := identity.HouseholdID(ctx)
	if err != nil {
		return err
	}
	it, err := load(ctx, tx, hh, p.ID)
	if err != nil {
		return err
	}
	var (
		cols []string
		args = []any{p.ID}
	)
	set := func(col, cast string, v any) {
		args = append(args, v)
		cols = append(cols, fmt.Sprintf("%s = $%d%s", col, len(args), cast))
	}
	if p.ExpiresOn.Set || p.ExpiryKind.Set {
		expires, kind := it.ExpiresOn, it.ExpiryKind
		if p.ExpiresOn.Set {
			if expires, err = optDay(p.ExpiresOn.Value); err != nil {
				return err
			}
			if expires == nil {
				kind = nil
			} else if kind == nil || *kind == "estimated" {
				kind = ptr("best_before") // a date the user typed is theirs, not ours
			}
		}
		if p.ExpiryKind.Set {
			if p.ExpiryKind.Value == nil || !givenKinds[*p.ExpiryKind.Value] || expires == nil {
				return commands.Invalid("expiryKind must be use_by or best_before, on an item with an expiry")
			}
			kind = p.ExpiryKind.Value
		}
		set("expires_on", "", expires)
		set("expiry_kind", "", kind)
	}
	if p.Note.Set {
		if err := checkLen(p.Note.Value, 1000, "note"); err != nil {
			return err
		}
		set("note", "", p.Note.Value)
	}
	if a := p.Amount; a != nil {
		if err := a.validate(false); err != nil {
			return err
		}
		set("amount_kind", "", a.Kind)
		set("quantity", "::text::numeric", a.Quantity)
		set("unit", "", a.Unit)
		set("portions", "", a.Portions)
		set("level", "", a.Level)
	}
	if len(cols) > 0 {
		if _, err := tx.Exec(ctx, "UPDATE stock_item SET "+join(cols)+" WHERE id = $1", args...); err != nil {
			return err
		}
	}
	return writeEvent(ctx, tx, hh, event{ID: p.EventID, Item: p.ID, Kind: "adjust", Reason: ptr("edit")})
}

func join(cols []string) string {
	out := ""
	for i, c := range cols {
		if i > 0 {
			out += ", "
		}
		out += c
	}
	return out
}

func (h *handlers) split(ctx context.Context, tx pgx.Tx, p StockSplit) error {
	hh, err := identity.HouseholdID(ctx)
	if err != nil {
		return err
	}
	if len(p.Parts) == 0 || len(p.Parts) > 50 {
		return commands.Invalid("split into 1-50 parts")
	}
	on, err := day(p.On)
	if err != nil {
		return err
	}
	it, err := load(ctx, tx, hh, p.ID)
	if err != nil {
		return err
	}
	if it.Kind == "level" {
		return commands.Invalid("a rough-level item can't be split")
	}
	to := it.LocationID
	if p.LocationID != nil {
		to = p.LocationID
	}
	if to == nil {
		return commands.Invalid("the parts need a location")
	}
	toKind, err := locationKind(ctx, tx, hh, *to)
	if err != nil {
		return err
	}

	// Validate every part and total them before writing anything.
	amounts := make([]Amount, len(p.Parts))
	sumQ, sumP := "0", 0
	for i, part := range p.Parts {
		a := Amount{Kind: it.Kind, Quantity: part.Quantity, Portions: part.Portions}
		if it.Kind == "exact" {
			a.Unit = it.Unit
		}
		if err := a.validate(false); err != nil {
			return err
		}
		amounts[i] = a
		if it.Kind == "exact" {
			if err := tx.QueryRow(ctx, `SELECT ($1::text::numeric + $2::text::numeric)::text`, sumQ, *a.Quantity).Scan(&sumQ); err != nil {
				return err
			}
		} else {
			sumP += *a.Portions
		}
	}
	left := ""
	if it.Kind == "exact" {
		taken, rest, err := numOps(ctx, tx, *it.Quantity, sumQ)
		if err != nil {
			return err
		}
		if taken != sumQ && !sameNum(ctx, tx, taken, sumQ) {
			return commands.Invalid("the parts add up to more than the item has")
		}
		left = rest
	} else {
		if sumP > *it.Portions {
			return commands.Invalid("the parts add up to more than the item has")
		}
		left = fmt.Sprint(*it.Portions - sumP)
	}

	// Parts that cross the freezer boundary get its clocks; otherwise they keep the parent's dates.
	var (
		expires = it.ExpiresOn
		kind    = it.ExpiryKind
		frozen  = it.FrozenOn
	)
	if toKind == Freezer && it.LocKind != Freezer {
		c, err := h.chain(ctx, tx, hh, it.ProductID)
		if err != nil {
			return err
		}
		frozen = &on
		if d, ok := Estimate(c, OpFreeze, toKind, on); ok {
			expires, kind = &d, ptr("estimated")
		} else {
			expires, kind = nil, nil
		}
	}
	for i, part := range p.Parts {
		if err := insertItem(ctx, tx, hh, newItem{
			ID: part.ID, Product: it.ProductID, Location: *to, Amount: amounts[i],
			ExpiresOn: expires, ExpiryKind: kind, OpenedOn: it.OpenedOn, FrozenOn: frozen,
			Source: "split", Parent: &p.ID, Note: it.Note,
		}); err != nil {
			return err
		}
		if err := writeEvent(ctx, tx, hh, event{ID: part.EventID, Item: part.ID, Kind: "split", Delta: delta(false, amounts[i].Quantity, amounts[i].Portions), Unit: amounts[i].Unit, From: it.LocationID, To: to}); err != nil {
			return err
		}
	}
	var gone bool
	if it.Kind == "exact" {
		gone = isZero(left)
		if !gone {
			_, err = tx.Exec(ctx, `UPDATE stock_item SET quantity = $2::text::numeric WHERE id = $1`, p.ID, left)
		}
	} else {
		gone = left == "0"
		if !gone {
			_, err = tx.Exec(ctx, `UPDATE stock_item SET portions = $2::int WHERE id = $1`, p.ID, left)
		}
	}
	if err != nil {
		return err
	}
	if gone {
		if err := finish(ctx, tx, p.ID); err != nil {
			return err
		}
	}
	total := sumQ
	var totalP *int
	if it.Kind != "exact" {
		totalP = &sumP
	}
	return writeEvent(ctx, tx, hh, event{ID: p.EventID, Item: p.ID, Kind: "split", Delta: delta(true, ptrIf(it.Kind == "exact", total), totalP), Unit: it.Unit, From: it.LocationID, To: to})
}

func ptrIf(ok bool, s string) *string {
	if !ok {
		return nil
	}
	return &s
}

func sameNum(ctx context.Context, tx pgx.Tx, a, b string) bool {
	var eq bool
	_ = tx.QueryRow(ctx, `SELECT $1::text::numeric = $2::text::numeric`, a, b).Scan(&eq)
	return eq
}
