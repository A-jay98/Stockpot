package catalog

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/a-jay98/stockpot/internal/catalog/resolve"
	"github.com/a-jay98/stockpot/internal/commands"
	"github.com/a-jay98/stockpot/internal/identity"
	"github.com/a-jay98/stockpot/internal/units"
)

// Opt re-exports commands.Opt for payload structs.
type Opt[T any] = commands.Opt[T]

// ProductFields are the writable product columns; absent = unchanged.
type ProductFields struct {
	Name                Opt[string]      `json:"name"`
	Brand               Opt[string]      `json:"brand"`
	IngredientID        Opt[uuid.UUID]   `json:"ingredientId"`
	ExtraIngredientIDs  Opt[[]uuid.UUID] `json:"extraIngredientIds"`
	CategoryID          Opt[uuid.UUID]   `json:"categoryId"`
	Icon                Opt[string]      `json:"icon"`
	DefaultStoreID      Opt[uuid.UUID]   `json:"defaultStoreId"`
	DefaultQty          Opt[string]      `json:"defaultQty"`
	DefaultUnit         Opt[string]      `json:"defaultUnit"`
	Package             Opt[string]      `json:"package"`
	DefaultLocationKind Opt[string]      `json:"defaultLocationKind"`
	Note                Opt[string]      `json:"note"`
}

// ProductCreate is catalog.product.create.
type ProductCreate struct {
	ID uuid.UUID `json:"id"`
	ProductFields
}

// ProductUpdate is catalog.product.update.
type ProductUpdate struct {
	ID uuid.UUID `json:"id"`
	ProductFields
}

// IDPayload targets one row (the delete commands).
type IDPayload struct {
	ID uuid.UUID `json:"id"`
}

// ProductSetIngredient is catalog.product.set_ingredient: the user's pick of
// what recipes this product matches; never overwritten by the resolver.
type ProductSetIngredient struct {
	ID                 uuid.UUID   `json:"id"`
	IngredientID       *uuid.UUID  `json:"ingredientId"`
	ExtraIngredientIDs []uuid.UUID `json:"extraIngredientIds,omitempty"`
}

// StoreCreate is catalog.store.create.
type StoreCreate struct {
	ID    uuid.UUID `json:"id"`
	Name  string    `json:"name"`
	Sort  *int      `json:"sort"`
	Color *string   `json:"color"`
}

// StoreUpdate is catalog.store.update.
type StoreUpdate struct {
	ID    uuid.UUID   `json:"id"`
	Name  Opt[string] `json:"name"`
	Sort  Opt[int]    `json:"sort"`
	Color Opt[string] `json:"color"`
}

// LocationCreate is catalog.location.create.
type LocationCreate struct {
	ID   uuid.UUID `json:"id"`
	Name string    `json:"name"`
	Kind string    `json:"kind"`
	Sort *int      `json:"sort"`
}

// LocationUpdate is catalog.location.update.
type LocationUpdate struct {
	ID   uuid.UUID   `json:"id"`
	Name Opt[string] `json:"name"`
	Kind Opt[string] `json:"kind"`
	Sort Opt[int]    `json:"sort"`
}

// IngredientCreate is catalog.ingredient.create (household ingredient).
type IngredientCreate struct {
	ID         uuid.UUID  `json:"id"`
	Name       string     `json:"name"`
	Plural     *string    `json:"plural"`
	ParentID   *uuid.UUID `json:"parentId"`
	CategoryID *uuid.UUID `json:"categoryId"`
	Icon       *string    `json:"icon"`
	Aliases    []string   `json:"aliases"`
}

// Register adds the catalog commands to r. res links products to ingredients.
func Register(r *commands.Registry, res Resolver) {
	h := &handlers{res: res}
	commands.Register(r, "catalog.product.create", h.productCreate)
	commands.Register(r, "catalog.product.update", h.productUpdate)
	commands.Register(r, "catalog.product.delete", softDelete("product", func(p IDPayload) uuid.UUID { return p.ID }, nil))
	commands.Register(r, "catalog.product.set_ingredient", h.productSetIngredient)
	commands.Register(r, "catalog.store.create", h.storeCreate)
	commands.Register(r, "catalog.store.update", h.storeUpdate)
	commands.Register(r, "catalog.store.delete", softDelete("store", func(p IDPayload) uuid.UUID { return p.ID }, releaseStore))
	commands.Register(r, "catalog.location.create", h.locationCreate)
	commands.Register(r, "catalog.location.update", h.locationUpdate)
	commands.Register(r, "catalog.location.delete", softDelete("location", func(p IDPayload) uuid.UUID { return p.ID }, guardLocation))
	commands.Register(r, "catalog.ingredient.create", h.ingredientCreate)
}

// CreateProduct is the catalog.product.create handler, for commands that
// create a product as part of their own work (stock add by name).
func CreateProduct(res Resolver) commands.Handler[ProductCreate] {
	return (&handlers{res: res}).productCreate
}

type handlers struct{ res Resolver }

// sortOK keeps sort inside the spec's bound (and well inside int4).
func sortOK(n int) bool { return n >= -1_000_000 && n <= 1_000_000 }

var locationKinds = map[string]bool{"fridge": true, "freezer": true, "pantry": true, "other": true}

// setter builds a dynamic UPDATE of the columns a payload actually set.
type setter struct {
	cols []string
	args []any
}

func (s *setter) add(col, cast string, v any) {
	s.args = append(s.args, v)
	s.cols = append(s.cols, fmt.Sprintf("%s = $%d%s", col, len(s.args), cast))
}

func optStr(s *setter, col string, o Opt[string]) {
	if o.Set {
		s.add(col, "", o.Value)
	}
}

func clean(field string, o Opt[string], maxLen int, required bool) error {
	if !o.Set {
		return nil
	}
	if o.Value == nil {
		if required {
			return commands.Invalid("%s is required", field)
		}
		return nil
	}
	v := strings.TrimSpace(*o.Value)
	if (required && v == "") || len(v) > maxLen {
		return commands.Invalid("%s must be 1-%d characters", field, maxLen)
	}
	*o.Value = v
	return nil
}

func exists(ctx context.Context, tx pgx.Tx, q string, args ...any) (bool, error) {
	var ok bool
	err := tx.QueryRow(ctx, "SELECT EXISTS ("+q+")", args...).Scan(&ok)
	return ok, err
}

func (h *handlers) checkRefs(ctx context.Context, tx pgx.Tx, hh uuid.UUID, f *ProductFields) error {
	if f.IngredientID.Set && f.IngredientID.Value != nil {
		if err := checkIngredient(ctx, tx, hh, *f.IngredientID.Value); err != nil {
			return err
		}
	}
	if f.ExtraIngredientIDs.Set && f.ExtraIngredientIDs.Value != nil {
		for _, id := range *f.ExtraIngredientIDs.Value {
			if err := checkIngredient(ctx, tx, hh, id); err != nil {
				return err
			}
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
	if f.DefaultStoreID.Set && f.DefaultStoreID.Value != nil {
		ok, err := exists(ctx, tx, `SELECT 1 FROM store WHERE id = $1 AND household_id = $2 AND deleted_at IS NULL`, *f.DefaultStoreID.Value, hh)
		if err != nil {
			return err
		}
		if !ok {
			return commands.Invalid("unknown store")
		}
	}
	if f.DefaultUnit.Set && f.DefaultUnit.Value != nil && !units.Valid(*f.DefaultUnit.Value) {
		return commands.Invalid("unknown unit %q", *f.DefaultUnit.Value)
	}
	if f.DefaultQty.Set && f.DefaultQty.Value != nil {
		q, err := units.ParseQuantity(*f.DefaultQty.Value)
		if err != nil {
			return commands.Invalid("%v", err)
		}
		*f.DefaultQty.Value = q
	}
	if f.Icon.Set && f.Icon.Value != nil && !IconExists(*f.Icon.Value) {
		return commands.Invalid("unknown icon %q", *f.Icon.Value)
	}
	if f.DefaultLocationKind.Set && f.DefaultLocationKind.Value != nil && !locationKinds[*f.DefaultLocationKind.Value] {
		return commands.Invalid("unknown location kind")
	}
	if err := clean("name", f.Name, 200, true); err != nil {
		return err
	}
	if err := clean("brand", f.Brand, 100, false); err != nil {
		return err
	}
	if err := clean("package", f.Package, 100, false); err != nil {
		return err
	}
	return clean("note", f.Note, 1000, false)
}

// CheckIngredient fails with Invalid unless id is a live global or household ingredient.
func CheckIngredient(ctx context.Context, tx pgx.Tx, hh, id uuid.UUID) error {
	return checkIngredient(ctx, tx, hh, id)
}

func checkIngredient(ctx context.Context, tx pgx.Tx, hh, id uuid.UUID) error {
	ok, err := exists(ctx, tx, `SELECT 1 FROM ingredient WHERE id = $1 AND deleted_at IS NULL AND (household_id = $2 OR household_id IS NULL)`, id, hh)
	if err != nil {
		return err
	}
	if !ok {
		return commands.Invalid("unknown ingredient")
	}
	return nil
}

func (s *setter) productCols(f *ProductFields) {
	optStr(s, "name", f.Name)
	optStr(s, "brand", f.Brand)
	if f.IngredientID.Set {
		s.add("ingredient_id", "", f.IngredientID.Value)
	}
	if f.ExtraIngredientIDs.Set {
		v := []uuid.UUID{}
		if f.ExtraIngredientIDs.Value != nil {
			v = *f.ExtraIngredientIDs.Value
		}
		s.add("extra_ingredient_ids", "", v)
	}
	if f.CategoryID.Set {
		s.add("category_id", "", f.CategoryID.Value)
	}
	optStr(s, "icon", f.Icon)
	if f.DefaultStoreID.Set {
		s.add("default_store_id", "", f.DefaultStoreID.Value)
	}
	if f.DefaultQty.Set {
		s.add("default_qty", "::text::numeric", f.DefaultQty.Value)
	}
	optStr(s, "default_unit", f.DefaultUnit)
	optStr(s, "package", f.Package)
	optStr(s, "default_location_kind", f.DefaultLocationKind)
	optStr(s, "note", f.Note)
}

func uniqueViolation(err error) bool {
	var pe *pgconn.PgError
	return errors.As(err, &pe) && pe.Code == "23505"
}

// resolve applies the resolver to a product row unless the user chose.
// Anything under resolve.AutoAccept waits for the background enricher.
func (h *handlers) resolve(ctx context.Context, tx pgx.Tx, hh, id uuid.UUID, name string, brand *string) error {
	m, err := h.res.Resolve(ctx, tx, hh, name, brand)
	if err != nil {
		return err
	}
	if m == nil {
		_, err = tx.Exec(ctx, `UPDATE product SET ingredient_id = NULL, match_source = 'none', match_confidence = NULL, enrich_state = 'pending', enrich_attempts = 0
			WHERE id = $1 AND match_source <> 'user'`, id)
		return err
	}
	state := "pending"
	if m.Confidence >= resolve.AutoAccept {
		state = "done"
	}
	var hint *string
	if m.LocationHint != "" {
		hint = &m.LocationHint
	}
	// A hint from the name ("frozen peas") only fills an empty default location.
	_, err = tx.Exec(ctx, `UPDATE product SET ingredient_id = $2, match_source = 'rule', match_confidence = $3, enrich_state = $4, enrich_attempts = 0,
		default_location_kind = coalesce(default_location_kind, $5)
		WHERE id = $1 AND match_source <> 'user'`, id, m.IngredientID, m.Confidence, state, hint)
	return err
}

func (h *handlers) productCreate(ctx context.Context, tx pgx.Tx, p ProductCreate) error {
	hh, err := identity.HouseholdID(ctx)
	if err != nil {
		return err
	}
	if !p.Name.Set || p.Name.Value == nil {
		return commands.Invalid("name is required")
	}
	if err := h.checkRefs(ctx, tx, hh, &p.ProductFields); err != nil {
		return err
	}
	var s setter
	s.productCols(&p.ProductFields)
	cols, vals := []string{"id", "household_id"}, []string{"$1", "$2"}
	args := []any{p.ID, hh}
	for _, c := range s.cols {
		name, rest, _ := strings.Cut(c, " = ")
		// shift placeholder numbers by two (id, household_id)
		var n int
		cast := ""
		if _, err := fmt.Sscanf(rest, "$%d", &n); err != nil {
			return err
		}
		cast = strings.TrimPrefix(rest, fmt.Sprintf("$%d", n))
		cols = append(cols, name)
		vals = append(vals, fmt.Sprintf("$%d%s", n+2, cast))
	}
	args = append(args, s.args...)
	tag, err := tx.Exec(ctx, fmt.Sprintf(`INSERT INTO product (%s) VALUES (%s) ON CONFLICT (id) DO NOTHING`,
		strings.Join(cols, ", "), strings.Join(vals, ", ")), args...)
	if uniqueViolation(err) {
		return commands.Invalid("a product with this name and brand already exists")
	}
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return commands.CheckOwned(ctx, tx, "product", p.ID, hh) // same id again: already created
	}
	if !p.IngredientID.Set {
		return h.resolve(ctx, tx, hh, p.ID, *p.Name.Value, p.Brand.Value)
	}
	_, err = tx.Exec(ctx, `UPDATE product SET match_source = 'user', match_confidence = 1, enrich_state = 'done'
		WHERE id = $1 AND ingredient_id IS NOT NULL`, p.ID)
	return err
}

func (h *handlers) productUpdate(ctx context.Context, tx pgx.Tx, p ProductUpdate) error {
	hh, err := identity.HouseholdID(ctx)
	if err != nil {
		return err
	}
	var name string
	var brand *string
	err = tx.QueryRow(ctx, `SELECT name, brand FROM product WHERE id = $1 AND household_id = $2 AND deleted_at IS NULL FOR UPDATE`, p.ID, hh).Scan(&name, &brand)
	if errors.Is(err, pgx.ErrNoRows) {
		return commands.Gone("product no longer exists")
	}
	if err != nil {
		return err
	}
	if p.Name.Set && p.Name.Value == nil {
		return commands.Invalid("name is required")
	}
	if err := h.checkRefs(ctx, tx, hh, &p.ProductFields); err != nil {
		return err
	}
	var s setter
	s.productCols(&p.ProductFields)
	if len(s.cols) == 0 {
		return nil
	}
	s.args = append(s.args, p.ID)
	_, err = tx.Exec(ctx, fmt.Sprintf("UPDATE product SET %s WHERE id = $%d", strings.Join(s.cols, ", "), len(s.args)), s.args...)
	if uniqueViolation(err) {
		return commands.Invalid("a product with this name and brand already exists")
	}
	if err != nil {
		return err
	}
	switch {
	case p.IngredientID.Set:
		_, err = tx.Exec(ctx, `UPDATE product SET match_source = CASE WHEN ingredient_id IS NULL THEN 'none' ELSE 'user' END,
			match_confidence = CASE WHEN ingredient_id IS NULL THEN NULL ELSE 1 END,
			enrich_state = CASE WHEN ingredient_id IS NULL THEN 'pending' ELSE 'done' END WHERE id = $1`, p.ID)
		return err
	case p.Name.Set || p.Brand.Set:
		if p.Name.Set {
			name = *p.Name.Value
		}
		if p.Brand.Set {
			brand = p.Brand.Value
		}
		return h.resolve(ctx, tx, hh, p.ID, name, brand)
	}
	return nil
}

func (h *handlers) productSetIngredient(ctx context.Context, tx pgx.Tx, p ProductSetIngredient) error {
	hh, err := identity.HouseholdID(ctx)
	if err != nil {
		return err
	}
	for _, id := range append(append([]uuid.UUID{}, p.ExtraIngredientIDs...), ptrs(p.IngredientID)...) {
		if err := checkIngredient(ctx, tx, hh, id); err != nil {
			return err
		}
	}
	extra := p.ExtraIngredientIDs
	if extra == nil {
		extra = []uuid.UUID{}
	}
	tag, err := tx.Exec(ctx, `UPDATE product SET ingredient_id = $3, extra_ingredient_ids = $4,
		match_source = 'user', match_confidence = 1, enrich_state = 'done'
		WHERE id = $1 AND household_id = $2 AND deleted_at IS NULL`, p.ID, hh, p.IngredientID, extra)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return commands.Gone("product no longer exists")
	}
	return nil
}

func ptrs(id *uuid.UUID) []uuid.UUID {
	if id == nil {
		return nil
	}
	return []uuid.UUID{*id}
}

// softDelete builds a delete command for table. Deleting a row that is
// already deleted succeeds; one that never existed is gone.
//
// before (optional) runs first and may reject the delete or detach references.
func softDelete[P any](table string, id func(P) uuid.UUID, before func(ctx context.Context, tx pgx.Tx, hh, id uuid.UUID) error) commands.Handler[P] {
	q := fmt.Sprintf(`UPDATE %s SET deleted_at = now() WHERE id = $1 AND household_id = $2 AND deleted_at IS NULL`, table)
	q2 := fmt.Sprintf(`SELECT EXISTS (SELECT 1 FROM %s WHERE id = $1 AND household_id = $2)`, table)
	return func(ctx context.Context, tx pgx.Tx, p P) error {
		hh, err := identity.HouseholdID(ctx)
		if err != nil {
			return err
		}
		if before != nil {
			if err := before(ctx, tx, hh, id(p)); err != nil {
				return err
			}
		}
		tag, err := tx.Exec(ctx, q, id(p), hh)
		if err != nil {
			return err
		}
		if tag.RowsAffected() > 0 {
			return nil
		}
		var ok bool
		if err := tx.QueryRow(ctx, q2, id(p), hh).Scan(&ok); err != nil {
			return err
		}
		if !ok {
			return commands.Gone("%s no longer exists", table)
		}
		return nil
	}
}

func (h *handlers) storeCreate(ctx context.Context, tx pgx.Tx, p StoreCreate) error {
	hh, err := identity.HouseholdID(ctx)
	if err != nil {
		return err
	}
	name := strings.TrimSpace(p.Name)
	if name == "" || len(name) > 100 {
		return commands.Invalid("name must be 1-100 characters")
	}
	sort := 0
	if p.Sort != nil {
		sort = *p.Sort
	}
	if !sortOK(sort) {
		return commands.Invalid("sort out of range")
	}
	tag, err := tx.Exec(ctx, `INSERT INTO store (id, household_id, name, sort, color) VALUES ($1, $2, $3, $4, $5) ON CONFLICT (id) DO NOTHING`,
		p.ID, hh, name, sort, p.Color)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return commands.CheckOwned(ctx, tx, "store", p.ID, hh)
	}
	return nil
}

func (h *handlers) storeUpdate(ctx context.Context, tx pgx.Tx, p StoreUpdate) error {
	hh, err := identity.HouseholdID(ctx)
	if err != nil {
		return err
	}
	if p.Name.Set && p.Name.Value == nil {
		return commands.Invalid("name is required")
	}
	if err := clean("name", p.Name, 100, true); err != nil {
		return err
	}
	var s setter
	optStr(&s, "name", p.Name)
	if p.Sort.Set {
		s.add("sort", "", p.Sort.Value)
	}
	optStr(&s, "color", p.Color)
	if p.Sort.Set && p.Sort.Value != nil && !sortOK(*p.Sort.Value) {
		return commands.Invalid("sort out of range")
	}
	return updateRow(ctx, tx, "store", hh, p.ID, &s)
}

func (h *handlers) locationCreate(ctx context.Context, tx pgx.Tx, p LocationCreate) error {
	hh, err := identity.HouseholdID(ctx)
	if err != nil {
		return err
	}
	name := strings.TrimSpace(p.Name)
	if name == "" || len(name) > 100 || !locationKinds[p.Kind] {
		return commands.Invalid("name must be 1-100 characters and kind one of fridge, freezer, pantry, other")
	}
	sort := 100
	if p.Sort != nil {
		sort = *p.Sort
	}
	if !sortOK(sort) {
		return commands.Invalid("sort out of range")
	}
	tag, err := tx.Exec(ctx, `INSERT INTO location (id, household_id, name, kind, sort) VALUES ($1, $2, $3, $4, $5) ON CONFLICT (id) DO NOTHING`,
		p.ID, hh, name, p.Kind, sort)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return commands.CheckOwned(ctx, tx, "location", p.ID, hh)
	}
	return nil
}

func (h *handlers) locationUpdate(ctx context.Context, tx pgx.Tx, p LocationUpdate) error {
	hh, err := identity.HouseholdID(ctx)
	if err != nil {
		return err
	}
	if (p.Name.Set && p.Name.Value == nil) || (p.Kind.Set && (p.Kind.Value == nil || !locationKinds[*p.Kind.Value])) {
		return commands.Invalid("name and kind can't be cleared and kind must be fridge, freezer, pantry or other")
	}
	if err := clean("name", p.Name, 100, true); err != nil {
		return err
	}
	var s setter
	optStr(&s, "name", p.Name)
	optStr(&s, "kind", p.Kind)
	if p.Sort.Set {
		s.add("sort", "", p.Sort.Value)
	}
	if p.Sort.Set && p.Sort.Value != nil && !sortOK(*p.Sort.Value) {
		return commands.Invalid("sort out of range")
	}
	return updateRow(ctx, tx, "location", hh, p.ID, &s)
}

func updateRow(ctx context.Context, tx pgx.Tx, table string, hh, id uuid.UUID, s *setter) error {
	if len(s.cols) == 0 {
		ok, err := exists(ctx, tx, fmt.Sprintf(`SELECT 1 FROM %s WHERE id = $1 AND household_id = $2 AND deleted_at IS NULL`, table), id, hh)
		if err != nil {
			return err
		}
		if !ok {
			return commands.Gone("%s no longer exists", table)
		}
		return nil
	}
	s.args = append(s.args, id, hh)
	n := len(s.args)
	tag, err := tx.Exec(ctx, fmt.Sprintf("UPDATE %s SET %s WHERE id = $%d AND household_id = $%d AND deleted_at IS NULL",
		table, strings.Join(s.cols, ", "), n-1, n), s.args...)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return commands.Gone("%s no longer exists", table)
	}
	return nil
}

var slugBad = regexp.MustCompile(`[^a-z0-9]+`)

// Slugify turns a name into an ingredient slug.
func Slugify(name string) string {
	s := strings.Trim(slugBad.ReplaceAllString(Normalize(name), "-"), "-")
	if s == "" {
		s = "ingredient"
	}
	return s
}

func (h *handlers) ingredientCreate(ctx context.Context, tx pgx.Tx, p IngredientCreate) error {
	hh, err := identity.HouseholdID(ctx)
	if err != nil {
		return err
	}
	name := strings.TrimSpace(p.Name)
	if name == "" || len(name) > 100 || strings.IndexFunc(name, unicode.IsControl) >= 0 {
		return commands.Invalid("name must be 1-100 characters")
	}
	if p.Icon != nil && !IconExists(*p.Icon) {
		return commands.Invalid("unknown icon %q", *p.Icon)
	}
	if p.ParentID != nil {
		if err := checkIngredient(ctx, tx, hh, *p.ParentID); err != nil {
			return err
		}
	}
	if p.CategoryID != nil {
		ok, err := exists(ctx, tx, `SELECT 1 FROM category WHERE id = $1 AND deleted_at IS NULL AND (household_id = $2 OR household_id IS NULL)`, *p.CategoryID, hh)
		if err != nil {
			return err
		}
		if !ok {
			return commands.Invalid("unknown category")
		}
	}
	aliases := make([]string, 0, len(p.Aliases))
	for _, a := range p.Aliases {
		if a = Normalize(a); a != "" {
			aliases = append(aliases, a)
		}
	}
	if err := commands.CheckOwned(ctx, tx, "ingredient", p.ID, hh); err != nil {
		return err
	}
	if done, err := exists(ctx, tx, `SELECT 1 FROM ingredient WHERE id = $1`, p.ID); err != nil || done {
		return err // replay: already created
	}
	base := Slugify(name)
	for i := 1; i <= 50; i++ {
		slug := base
		if i > 1 {
			slug = fmt.Sprintf("%s-%d", base, i)
		}
		taken, err := exists(ctx, tx, `SELECT 1 FROM ingredient WHERE slug = $1 AND (household_id = $2 OR household_id IS NULL)`, slug, hh)
		if err != nil {
			return err
		}
		if taken {
			continue
		}
		_, err = tx.Exec(ctx, `INSERT INTO ingredient (id, household_id, slug, name, plural, parent_id, category_id, icon, aliases, source)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, 'household')`, p.ID, hh, slug, name, p.Plural, p.ParentID, p.CategoryID, p.Icon, aliases)
		return err
	}
	return commands.Invalid("no free slug for %q", name)
}

// Reference rules for deletes (also in docs/architecture-m1.md §3):
//   - a location can't be deleted while live stock is in it: move the items first;
//   - deleting a store detaches it from products' default store and from shopping
//     items' store lists (purchase history in product_stat is kept).

func guardLocation(ctx context.Context, tx pgx.Tx, hh, id uuid.UUID) error {
	used, err := exists(ctx, tx, `SELECT 1 FROM stock_item WHERE household_id = $1 AND location_id = $2 AND deleted_at IS NULL`, hh, id)
	if err != nil {
		return err
	}
	if used {
		return commands.Invalid("move the items out of this location first")
	}
	return nil
}

func releaseStore(ctx context.Context, tx pgx.Tx, hh, id uuid.UUID) error {
	if _, err := tx.Exec(ctx, `UPDATE product SET default_store_id = NULL WHERE household_id = $1 AND default_store_id = $2`, hh, id); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `UPDATE shopping_item SET store_ids = array_remove(store_ids, $2) WHERE household_id = $1 AND $2 = ANY(store_ids)`, hh, id)
	return err
}
