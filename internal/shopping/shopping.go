// Package shopping holds the shopping-list commands and the purchase history
// (product_stat) that the autocomplete and the guess list learn from.
package shopping

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/a-jay98/stockpot/internal/catalog"
	"github.com/a-jay98/stockpot/internal/commands"
	"github.com/a-jay98/stockpot/internal/identity"
	"github.com/a-jay98/stockpot/internal/inventory"
	"github.com/a-jay98/stockpot/internal/units"
)

// Opt re-exports commands.Opt for payload structs.
type Opt[T any] = commands.Opt[T]

// ListCreate is shopping.list.create.
type ListCreate struct {
	ID   uuid.UUID `json:"id"`
	Name string    `json:"name"`
	Sort *int      `json:"sort"`
}

// ListRename is shopping.list.rename.
type ListRename struct {
	ID   uuid.UUID `json:"id"`
	Name string    `json:"name"`
}

// IDPayload targets one row (list.delete, item.delete, item.uncheck).
type IDPayload struct {
	ID uuid.UUID `json:"id"`
}

// ItemFields are the writable item columns; absent = unchanged.
type ItemFields struct {
	ProductID  Opt[uuid.UUID]   `json:"productId"`
	Text       Opt[string]      `json:"text"`
	Qty        Opt[string]      `json:"qty"`
	Unit       Opt[string]      `json:"unit"`
	CategoryID Opt[uuid.UUID]   `json:"categoryId"`
	StoreIDs   Opt[[]uuid.UUID] `json:"storeIds"`
	Note       Opt[string]      `json:"note"`
}

// ItemAdd is shopping.item.add. A ProductID the household doesn't have yet is
// the client's id for a new product named Text (as on check), so every item is
// linked to a product and shows its match and icon from the start.
// IngredientID is the catalog ingredient the user picked for that new product.
type ItemAdd struct {
	ID           uuid.UUID  `json:"id"`
	ListID       uuid.UUID  `json:"listId"`
	Sort         *int       `json:"sort"`
	IngredientID *uuid.UUID `json:"ingredientId"`
	// RecipeID is the recipe the item is for ("Missing to list").
	RecipeID *uuid.UUID `json:"recipeId"`
	ItemFields
}

// ItemUpdate is shopping.item.update.
type ItemUpdate struct {
	ID uuid.UUID `json:"id"`
	ItemFields
}

// ItemReorder is shopping.item.reorder: ids in their new order.
type ItemReorder struct {
	ListID uuid.UUID   `json:"listId"`
	IDs    []uuid.UUID `json:"ids"`
}

// InventoryMove is the "put it away" part of a check.
type InventoryMove struct {
	StockItemID uuid.UUID  `json:"stockItemId"`
	LocationID  *uuid.UUID `json:"locationId"`
	ExpiresOn   *string    `json:"expiresOn"`
	Quantity    *string    `json:"quantity"`
	Unit        *string    `json:"unit"`
}

// ItemCheck is shopping.item.check: tick the item (learning from the
// purchase) and optionally add it to inventory. ProductID is the client's
// pick for an item with no product: an existing household product, or the id
// for a new one, so an offline client can show it before syncing.
type ItemCheck struct {
	ID        uuid.UUID      `json:"id"`
	StoreID   *uuid.UUID     `json:"storeId"`
	ProductID *uuid.UUID     `json:"productId"`
	Inventory *InventoryMove `json:"inventory"`
}

// ListClear is shopping.item.clear_checked.
type ListClear struct {
	ListID uuid.UUID `json:"listId"`
}

// Register adds the shopping commands to r. res links products created from
// free text to an ingredient.
func Register(r *commands.Registry, res catalog.Resolver) {
	h := &handlers{res: res}
	commands.Register(r, "shopping.list.create", h.listCreate)
	commands.Register(r, "shopping.list.rename", h.listRename)
	commands.Register(r, "shopping.list.delete", h.listDelete)
	commands.Register(r, "shopping.item.add", h.itemAdd)
	commands.Register(r, "shopping.item.update", h.itemUpdate)
	commands.Register(r, "shopping.item.delete", h.itemDelete)
	commands.Register(r, "shopping.item.reorder", h.itemReorder)
	commands.Register(r, "shopping.item.check", h.itemCheck)
	commands.Register(r, "shopping.item.uncheck", h.itemUncheck)
	commands.Register(r, "shopping.item.clear_checked", h.clearChecked)
}

type handlers struct{ res catalog.Resolver }

func exists(ctx context.Context, tx pgx.Tx, q string, args ...any) (bool, error) {
	var ok bool
	err := tx.QueryRow(ctx, "SELECT EXISTS ("+q+")", args...).Scan(&ok)
	return ok, err
}

func cleanName(field, s string, limit int) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" || len([]rune(s)) > limit {
		return "", commands.Invalid("%s must be 1-%d characters", field, limit)
	}
	return s, nil
}

func (h *handlers) listCreate(ctx context.Context, tx pgx.Tx, p ListCreate) error {
	hh, err := identity.HouseholdID(ctx)
	if err != nil {
		return err
	}
	name, err := cleanName("name", p.Name, 100)
	if err != nil {
		return err
	}
	sort := 100
	if p.Sort != nil {
		sort = *p.Sort
	}
	tag, err := tx.Exec(ctx, `INSERT INTO shopping_list (id, household_id, name, sort) VALUES ($1, $2, $3, $4) ON CONFLICT (id) DO NOTHING`, p.ID, hh, name, sort)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return commands.CheckOwned(ctx, tx, "shopping_list", p.ID, hh)
	}
	return nil
}

func (h *handlers) listRename(ctx context.Context, tx pgx.Tx, p ListRename) error {
	hh, err := identity.HouseholdID(ctx)
	if err != nil {
		return err
	}
	name, err := cleanName("name", p.Name, 100)
	if err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `UPDATE shopping_list SET name = $3 WHERE id = $1 AND household_id = $2 AND deleted_at IS NULL`, p.ID, hh, name)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return commands.Gone("list no longer exists")
	}
	return nil
}

func (h *handlers) listDelete(ctx context.Context, tx pgx.Tx, p IDPayload) error {
	hh, err := identity.HouseholdID(ctx)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE shopping_item SET deleted_at = now() WHERE list_id = $1 AND household_id = $2 AND deleted_at IS NULL`, p.ID, hh); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE shopping_list SET deleted_at = now() WHERE id = $1 AND household_id = $2 AND deleted_at IS NULL`, p.ID, hh)
	return err
}

// checkItemRefs validates the references and sizes of the fields a payload set.
func checkItemRefs(ctx context.Context, tx pgx.Tx, hh uuid.UUID, f *ItemFields) error {
	if f.ProductID.Set && f.ProductID.Value != nil {
		ok, err := exists(ctx, tx, `SELECT 1 FROM product WHERE id = $1 AND household_id = $2 AND deleted_at IS NULL`, *f.ProductID.Value, hh)
		if err != nil {
			return err
		}
		if !ok {
			return commands.Invalid("unknown product")
		}
	}
	if f.CategoryID.Set && f.CategoryID.Value != nil {
		ok, err := exists(ctx, tx, `SELECT 1 FROM category WHERE id = $1 AND deleted_at IS NULL AND (household_id = $2 OR household_id IS NULL)`, *f.CategoryID.Value, hh)
		if err != nil {
			return err
		}
		if !ok {
			return commands.Invalid("unknown category")
		}
	}
	if f.StoreIDs.Set && f.StoreIDs.Value != nil {
		for _, s := range *f.StoreIDs.Value {
			ok, err := exists(ctx, tx, `SELECT 1 FROM store WHERE id = $1 AND household_id = $2 AND deleted_at IS NULL`, s, hh)
			if err != nil {
				return err
			}
			if !ok {
				return commands.Invalid("unknown store")
			}
		}
	}
	if f.Unit.Set && f.Unit.Value != nil && !units.Valid(*f.Unit.Value) {
		return commands.Invalid("unknown unit %q", *f.Unit.Value)
	}
	if f.Qty.Set && f.Qty.Value != nil {
		q, err := units.ParseQuantity(*f.Qty.Value)
		if err != nil {
			return commands.Invalid("%v", err)
		}
		*f.Qty.Value = q
	}
	if f.Text.Set && f.Text.Value != nil {
		t := strings.TrimSpace(*f.Text.Value)
		if len([]rune(t)) > 200 {
			return commands.Invalid("text is too long")
		}
		*f.Text.Value = t
	}
	if f.Note.Set && f.Note.Value != nil && len([]rune(*f.Note.Value)) > 1000 {
		return commands.Invalid("note is too long")
	}
	return nil
}

// itemCols turns the set fields into column assignments (placeholders from n+1).
func itemCols(f *ItemFields, args []any) (cols []string, out []any) {
	add := func(col, cast string, v any) {
		args = append(args, v)
		cols = append(cols, fmt.Sprintf("%s = $%d%s", col, len(args), cast))
	}
	if f.ProductID.Set {
		add("product_id", "", f.ProductID.Value)
	}
	if f.Text.Set {
		t := ""
		if f.Text.Value != nil {
			t = *f.Text.Value
		}
		add("text", "", t)
	}
	if f.Qty.Set {
		add("qty", "::text::numeric", f.Qty.Value)
	}
	if f.Unit.Set {
		add("unit", "", f.Unit.Value)
	}
	if f.CategoryID.Set {
		add("category_id", "", f.CategoryID.Value)
	}
	if f.StoreIDs.Set {
		v := []uuid.UUID{}
		if f.StoreIDs.Value != nil {
			v = *f.StoreIDs.Value
		}
		add("store_ids", "", v)
	}
	if f.Note.Set {
		add("note", "", f.Note.Value)
	}
	return cols, args
}

func (h *handlers) itemAdd(ctx context.Context, tx pgx.Tx, p ItemAdd) error {
	hh, err := identity.HouseholdID(ctx)
	if err != nil {
		return err
	}
	if err := h.linkNewProduct(ctx, tx, hh, &p); err != nil {
		return err
	}
	if err := checkItemRefs(ctx, tx, hh, &p.ItemFields); err != nil {
		return err
	}
	hasProduct := p.ProductID.Set && p.ProductID.Value != nil
	hasText := p.Text.Set && p.Text.Value != nil && *p.Text.Value != ""
	if !hasProduct && !hasText {
		return commands.Invalid("an item needs a product or text")
	}
	ok, err := exists(ctx, tx, `SELECT 1 FROM shopping_list WHERE id = $1 AND household_id = $2 AND deleted_at IS NULL`, p.ListID, hh)
	if err != nil {
		return err
	}
	if !ok {
		return commands.Gone("list no longer exists")
	}
	if p.RecipeID != nil {
		// A recipe deleted meanwhile (another device) only drops the link, so the
		// queued item isn't lost; another household's recipe is refused.
		var owner uuid.UUID
		var deleted bool
		err := tx.QueryRow(ctx, `SELECT household_id, deleted_at IS NOT NULL FROM recipe WHERE id = $1`, *p.RecipeID).Scan(&owner, &deleted)
		switch {
		case errors.Is(err, pgx.ErrNoRows) || (err == nil && owner != hh):
			return commands.Invalid("unknown recipe")
		case err != nil:
			return err
		case deleted:
			p.RecipeID = nil
		}
	}
	cols, args := itemCols(&p.ItemFields, []any{p.ID, hh, p.ListID})
	names := []string{"id", "household_id", "list_id"}
	vals := []string{"$1", "$2", "$3"}
	for _, c := range cols {
		name, rest, _ := strings.Cut(c, " = ")
		names, vals = append(names, name), append(vals, rest)
	}
	if p.RecipeID != nil {
		args = append(args, *p.RecipeID)
		names, vals = append(names, "recipe_id"), append(vals, fmt.Sprintf("$%d", len(args)))
	}
	if p.Sort != nil {
		args = append(args, *p.Sort)
		names, vals = append(names, "sort"), append(vals, fmt.Sprintf("$%d", len(args)))
	} else {
		names, vals = append(names, "sort"), append(vals, "(SELECT coalesce(max(sort), 0) + 10 FROM shopping_item WHERE list_id = $3)")
	}
	tag, err := tx.Exec(ctx, fmt.Sprintf(`INSERT INTO shopping_item (%s) VALUES (%s) ON CONFLICT (id) DO NOTHING`,
		strings.Join(names, ", "), strings.Join(vals, ", ")), args...)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return commands.CheckOwned(ctx, tx, "shopping_item", p.ID, hh)
	}
	return nil
}

func (h *handlers) itemUpdate(ctx context.Context, tx pgx.Tx, p ItemUpdate) error {
	hh, err := identity.HouseholdID(ctx)
	if err != nil {
		return err
	}
	if err := checkItemRefs(ctx, tx, hh, &p.ItemFields); err != nil {
		return err
	}
	cols, args := itemCols(&p.ItemFields, []any{p.ID, hh})
	if len(cols) == 0 {
		ok, err := exists(ctx, tx, `SELECT 1 FROM shopping_item WHERE id = $1 AND household_id = $2 AND deleted_at IS NULL`, p.ID, hh)
		if err != nil {
			return err
		}
		if !ok {
			return commands.Gone("item no longer exists")
		}
		return nil
	}
	tag, err := tx.Exec(ctx, fmt.Sprintf(`UPDATE shopping_item SET %s WHERE id = $1 AND household_id = $2 AND deleted_at IS NULL`, strings.Join(cols, ", ")), args...)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return commands.Gone("item no longer exists")
	}
	return nil
}

// itemDelete is idempotent: deleting what is already gone is a no-op.
func (h *handlers) itemDelete(ctx context.Context, tx pgx.Tx, p IDPayload) error {
	hh, err := identity.HouseholdID(ctx)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE shopping_item SET deleted_at = now() WHERE id = $1 AND household_id = $2 AND deleted_at IS NULL`, p.ID, hh)
	return err
}

func (h *handlers) itemReorder(ctx context.Context, tx pgx.Tx, p ItemReorder) error {
	hh, err := identity.HouseholdID(ctx)
	if err != nil {
		return err
	}
	if len(p.IDs) > 500 {
		return commands.Invalid("too many items")
	}
	_, err = tx.Exec(ctx, `UPDATE shopping_item s SET sort = u.ord * 10
		FROM unnest($1::uuid[]) WITH ORDINALITY AS u(id, ord)
		WHERE s.id = u.id AND s.list_id = $2 AND s.household_id = $3 AND s.deleted_at IS NULL AND s.sort <> u.ord * 10`, p.IDs, p.ListID, hh)
	return err
}

func (h *handlers) clearChecked(ctx context.Context, tx pgx.Tx, p ListClear) error {
	hh, err := identity.HouseholdID(ctx)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE shopping_item SET deleted_at = now()
		WHERE list_id = $1 AND household_id = $2 AND deleted_at IS NULL AND checked_at IS NOT NULL`, p.ListID, hh)
	return err
}

func (h *handlers) itemUncheck(ctx context.Context, tx pgx.Tx, p IDPayload) error {
	hh, err := identity.HouseholdID(ctx)
	if err != nil {
		return err
	}
	var stock *uuid.UUID
	err = tx.QueryRow(ctx, `SELECT stock_item_id FROM shopping_item WHERE id = $1 AND household_id = $2 AND deleted_at IS NULL FOR UPDATE`, p.ID, hh).Scan(&stock)
	if errors.Is(err, pgx.ErrNoRows) {
		return commands.Gone("item no longer exists")
	}
	if err != nil {
		return err
	}
	if stock != nil {
		return commands.Invalid("the item is already in inventory")
	}
	if _, err := tx.Exec(ctx, `UPDATE shopping_item SET checked_at = NULL, checked_by = NULL WHERE id = $1`, p.ID); err != nil {
		return err
	}
	return ForgetPurchase(ctx, tx, hh, p.ID)
}

func (h *handlers) itemCheck(ctx context.Context, tx pgx.Tx, p ItemCheck) error {
	hh, err := identity.HouseholdID(ctx)
	if err != nil {
		return err
	}
	pr := identity.PrincipalFrom(ctx)
	var (
		product, stock *uuid.UUID
		text           string
		qty, unit      *string
		stores         []uuid.UUID
		checked        *time.Time
	)
	err = tx.QueryRow(ctx, `SELECT product_id, text, qty::text, unit, store_ids, checked_at, stock_item_id
		FROM shopping_item WHERE id = $1 AND household_id = $2 AND deleted_at IS NULL FOR UPDATE`, p.ID, hh).
		Scan(&product, &text, &qty, &unit, &stores, &checked, &stock)
	if errors.Is(err, pgx.ErrNoRows) {
		return commands.Gone("item no longer exists")
	}
	if err != nil {
		return err
	}
	if p.StoreID != nil {
		ok, err := exists(ctx, tx, `SELECT 1 FROM store WHERE id = $1 AND household_id = $2 AND deleted_at IS NULL`, *p.StoreID, hh)
		if err != nil {
			return err
		}
		if !ok {
			return commands.Invalid("unknown store")
		}
	}
	if checked == nil {
		if product == nil {
			if strings.TrimSpace(text) == "" {
				return commands.Invalid("the item has no name")
			}
			id, err := h.productFor(ctx, tx, hh, text, p.ProductID)
			if err != nil {
				return err
			}
			product = &id
		}
		at := commands.ClientTime(ctx) // when it was ticked, which may be hours before it synced
		var by *uuid.UUID
		if pr != nil {
			by = &pr.UserID
		}
		if _, err := tx.Exec(ctx, `UPDATE shopping_item SET checked_at = $4, checked_by = $2, product_id = $3 WHERE id = $1`, p.ID, by, product, at); err != nil {
			return err
		}
		store := p.StoreID
		if store == nil && len(stores) == 1 {
			store = &stores[0]
		}
		if err := RecordPurchase(ctx, tx, hh, *product, store, p.ID, at, qty, unit); err != nil {
			return err
		}
	}
	if p.Inventory != nil && stock == nil && product != nil {
		return h.addToInventory(ctx, tx, hh, p.ID, *product, qty, unit, p.Inventory)
	}
	return nil
}

// linkNewProduct creates the product an added item names when the client
// proposed a new one (an id the household doesn't have, plus text). EnsureProduct
// may land on an existing product of that name; only a product created here
// takes the user's ingredient pick.
func (h *handlers) linkNewProduct(ctx context.Context, tx pgx.Tx, hh uuid.UUID, p *ItemAdd) error {
	if !p.ProductID.Set || p.ProductID.Value == nil || !p.Text.Set || p.Text.Value == nil || strings.TrimSpace(*p.Text.Value) == "" {
		return nil
	}
	if p.IngredientID != nil {
		if err := catalog.CheckIngredient(ctx, tx, hh, *p.IngredientID); err != nil {
			return err
		}
	}
	want := *p.ProductID.Value
	ok, err := exists(ctx, tx, `SELECT 1 FROM product WHERE id = $1 AND household_id = $2 AND deleted_at IS NULL`, want, hh)
	if err != nil || ok {
		return err
	}
	id, err := catalog.EnsureProduct(ctx, tx, h.res, hh, *p.Text.Value, want)
	if err != nil {
		return err
	}
	p.ProductID.Value = &id
	if id != want || p.IngredientID == nil {
		return nil
	}
	_, err = tx.Exec(ctx, `UPDATE product p SET ingredient_id = i.id, match_source = 'user', match_confidence = 1,
		enrich_state = 'done', category_id = coalesce(i.category_id, p.category_id)
		FROM ingredient i WHERE p.id = $1 AND i.id = $2`, id, *p.IngredientID)
	return err
}

// productFor is the product a free-text item becomes: the client's pick when
// it is a household product, else the named one, created under the client's
// id when new.
func (h *handlers) productFor(ctx context.Context, tx pgx.Tx, hh uuid.UUID, text string, pick *uuid.UUID) (uuid.UUID, error) {
	var want uuid.UUID
	if pick != nil {
		ok, err := exists(ctx, tx, `SELECT 1 FROM product WHERE id = $1 AND household_id = $2 AND deleted_at IS NULL`, *pick, hh)
		if err != nil || ok {
			return *pick, err
		}
		want = *pick
	}
	return catalog.EnsureProduct(ctx, tx, h.res, hh, text, want)
}

// addToInventory creates the stock item for a ticked item and links it.
func (h *handlers) addToInventory(ctx context.Context, tx pgx.Tx, hh, item, product uuid.UUID, qty, unit *string, m *InventoryMove) error {
	if m.Quantity != nil {
		q, err := units.ParseQuantity(*m.Quantity)
		if err != nil {
			return commands.Invalid("%v", err)
		}
		qty = &q
	}
	if m.Unit != nil {
		if !units.Valid(*m.Unit) {
			return commands.Invalid("unknown unit %q", *m.Unit)
		}
		unit = m.Unit
	}
	var expires *time.Time
	kind := ""
	if m.ExpiresOn != nil {
		t, err := time.Parse(time.DateOnly, *m.ExpiresOn)
		if err != nil {
			return commands.Invalid("expiresOn must be YYYY-MM-DD")
		}
		expires, kind = &t, "best_before"
	}

	// Location: the one asked for, else the product's usual kind, else the first.
	var loc *uuid.UUID
	var locKind string
	if m.LocationID != nil {
		err := tx.QueryRow(ctx, `SELECT id, kind FROM location WHERE id = $1 AND household_id = $2 AND deleted_at IS NULL`, *m.LocationID, hh).Scan(&loc, &locKind)
		if errors.Is(err, pgx.ErrNoRows) {
			return commands.Invalid("unknown location")
		}
		if err != nil {
			return err
		}
	} else {
		err := tx.QueryRow(ctx, `SELECT l.id, l.kind FROM location l
			WHERE l.household_id = $1 AND l.deleted_at IS NULL
			ORDER BY coalesce(l.kind = (SELECT default_location_kind FROM product WHERE id = $2), false) DESC, l.sort, l.created_at LIMIT 1`, hh, product).Scan(&loc, &locKind)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
	}
	if expires == nil && loc != nil {
		d, ok, err := inventory.EstimateAdd(ctx, tx, hh, product, inventory.LocationKind(locKind), today(ctx))
		if err != nil {
			return err
		}
		if ok {
			expires, kind = &d, "estimated"
		}
	}
	var frozen *time.Time
	if locKind == "freezer" {
		t := today(ctx)
		frozen = &t
	}
	var expiry, kindArg any
	if expires != nil {
		expiry, kindArg = *expires, kind
	}
	tag, err := tx.Exec(ctx, `INSERT INTO stock_item (id, household_id, product_id, location_id, amount_kind, quantity, unit, expires_on, expiry_kind, frozen_on, source)
		VALUES ($1, $2, $3, $4, 'exact', $5::text::numeric, $6, $7, $8, $9, 'shopping') ON CONFLICT (id) DO NOTHING`,
		m.StockItemID, hh, product, loc, qty, unit, expiry, kindArg, frozen)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return commands.CheckOwned(ctx, tx, "stock_item", m.StockItemID, hh)
	}
	var actor *uuid.UUID
	if pr := identity.PrincipalFrom(ctx); pr != nil {
		actor = &pr.UserID
	}
	if _, err := tx.Exec(ctx, `INSERT INTO stock_event (id, household_id, stock_item_id, kind, delta, unit, to_location_id, actor_id)
		VALUES ($1, $2, $3, 'add', $4::text::numeric, $5, $6, $7)`, uuid.New(), hh, m.StockItemID, qty, unit, loc, actor); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE shopping_item SET stock_item_id = $2 WHERE id = $1`, item, m.StockItemID)
	return err
}

// today is the household-local date the command was performed, as a UTC-midnight date value.
func today(ctx context.Context) time.Time {
	y, m, d := commands.ClientTime(ctx).In(HouseholdZone).Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}
