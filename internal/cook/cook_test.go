package cook_test

import (
	"context"
	"encoding/json"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/a-jay98/stockpot/internal/catalog"
	"github.com/a-jay98/stockpot/internal/catalog/resolve"
	"github.com/a-jay98/stockpot/internal/commands"
	"github.com/a-jay98/stockpot/internal/cook"
	"github.com/a-jay98/stockpot/internal/identity"
	"github.com/a-jay98/stockpot/internal/inventory"
	"github.com/a-jay98/stockpot/internal/recipes"
	"github.com/a-jay98/stockpot/internal/shopping"
	"github.com/a-jay98/stockpot/internal/testdb"
)

func TestMain(m *testing.M) { os.Exit(testdb.Main(m)) }

type env struct {
	pool      *pgxpool.Pool
	svc       *commands.Service
	hh        uuid.UUID
	ctx       context.Context
	loc       map[string]uuid.UUID
	list      uuid.UUID
	recipe    uuid.UUID
	version   uuid.UUID
	leftover  uuid.UUID
	fridgeDay time.Time
}

func newEnv(t *testing.T) *env {
	t.Helper()
	pool := testdb.New(t)
	if err := catalog.Seed(t.Context(), pool); err != nil {
		t.Fatal(err)
	}
	reg := commands.NewRegistry()
	catalog.Register(reg, resolve.Resolver{})
	inventory.Register(reg, resolve.Resolver{}, func(ctx context.Context, tx pgx.Tx, hh, product uuid.UUID, store *uuid.UUID, at time.Time, qty, unit *string) error {
		return shopping.RecordPurchase(ctx, tx, hh, product, store, uuid.Nil, at, qty, unit)
	})
	shopping.Register(reg, resolve.Resolver{})
	recipes.Register(reg, resolve.Resolver{})
	cook.Register(reg)
	e := &env{pool: pool, svc: commands.NewService(pool, reg), loc: map[string]uuid.UUID{}, recipe: uuid.New(), version: uuid.New(), leftover: uuid.New()}
	if err := pool.QueryRow(t.Context(), `INSERT INTO household (id, name) VALUES (gen_random_uuid(), 'Home') RETURNING id`).Scan(&e.hh); err != nil {
		t.Fatal(err)
	}
	e.ctx = identity.WithPrincipal(t.Context(), &identity.Principal{UserID: uuid.New(), Status: identity.StatusActive, HouseholdID: e.hh})
	rows, err := pool.Query(t.Context(), `SELECT kind, id FROM location WHERE household_id = $1`, e.hh)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var k string
		var id uuid.UUID
		_ = rows.Scan(&k, &id)
		e.loc[k] = id
	}
	rows.Close()
	if err := pool.QueryRow(t.Context(), `SELECT id FROM shopping_list WHERE household_id = $1`, e.hh).Scan(&e.list); err != nil {
		t.Fatal(err)
	}
	e.ok(t, "recipe.create", map[string]any{
		"id": e.recipe, "versionId": e.version, "title": "Chicken curry", "servings": 4,
		"ingredients": []string{"800 g chicken thighs", "2 onions", "1 tbsp olive oil"}, "steps": []any{},
	})
	return e
}

func cmd(typ string, payload any) commands.Command {
	b, _ := json.Marshal(payload)
	return commands.Command{ID: uuid.New(), Type: typ, Payload: b, ClientTime: time.Now()}
}

func (e *env) do(t *testing.T, c commands.Command) commands.Result {
	t.Helper()
	rs, err := e.svc.Apply(e.ctx, e.hh, []commands.Command{c})
	if err != nil {
		t.Fatal(err)
	}
	return rs[0]
}

func (e *env) ok(t *testing.T, typ string, p any) {
	t.Helper()
	if r := e.do(t, cmd(typ, p)); r.Status != commands.StatusApplied {
		t.Fatalf("%s: %+v %v", typ, r, r.Problem)
	}
}

func (e *env) rejected(t *testing.T, typ string, p any, code string) {
	t.Helper()
	r := e.do(t, cmd(typ, p))
	if r.Status != commands.StatusRejected || r.Problem.Code != code {
		t.Fatalf("%s: got %+v %v, want rejected %s", typ, r, r.Problem, code)
	}
}

// add stocks an item of a new product and returns its id.
func (e *env) add(t *testing.T, name string, amount map[string]any) uuid.UUID {
	t.Helper()
	id := uuid.New()
	p := map[string]any{"id": id, "eventId": uuid.New(), "newProduct": map[string]any{"id": uuid.New(), "name": name}, "locationId": e.loc["fridge"], "source": "manual"}
	for k, v := range amount {
		p[k] = v
	}
	e.ok(t, "inventory.stock.add", p)
	return id
}

func exact(q, u string) map[string]any { return map[string]any{"amountKind": "exact", "quantity": q, "unit": u} }
func portions(n int) map[string]any     { return map[string]any{"amountKind": "portions", "portions": n} }
func level(l string) map[string]any     { return map[string]any{"amountKind": "level", "level": l} }

type item struct {
	Qty      *string
	Portions *int
	Level    *string
	Deleted  bool
}

func (e *env) item(t *testing.T, id uuid.UUID) item {
	t.Helper()
	var it item
	if err := e.pool.QueryRow(t.Context(), `SELECT quantity::text, portions, level, deleted_at IS NOT NULL FROM stock_item WHERE id = $1`, id).
		Scan(&it.Qty, &it.Portions, &it.Level, &it.Deleted); err != nil {
		t.Fatal(err)
	}
	return it
}

func (e *env) cookPayload(uses []map[string]any, store []map[string]any) map[string]any {
	return map[string]any{
		"id": uuid.New(), "recipeId": e.recipe, "versionId": e.version, "cookedOn": time.Now().UTC().Format("2006-01-02"),
		"portionsMade": 4, "leftoverProductId": e.leftover, "uses": uses, "store": store,
	}
}

func use(item uuid.UUID, kv ...any) map[string]any {
	u := map[string]any{"stockItemId": item, "eventId": uuid.New()}
	for i := 0; i < len(kv); i += 2 {
		u[kv[i].(string)] = kv[i+1]
	}
	return u
}

type session struct {
	Uses     []cook.UseRecord
	Stored   []cook.StoreRecord
	Leftover *uuid.UUID
	Undone   bool
}

func (e *env) session(t *testing.T, id any) session {
	t.Helper()
	var s session
	var uses, stored []byte
	if err := e.pool.QueryRow(t.Context(), `SELECT uses, stored, leftover_product_id, undone_at IS NOT NULL FROM cook_session WHERE id = $1`, id).
		Scan(&uses, &stored, &s.Leftover, &s.Undone); err != nil {
		t.Fatal(err)
	}
	_ = json.Unmarshal(uses, &s.Uses)
	_ = json.Unmarshal(stored, &s.Stored)
	return s
}

func (e *env) counts(t *testing.T) (n int, last *time.Time) {
	t.Helper()
	if err := e.pool.QueryRow(t.Context(), `SELECT cooked_count, last_cooked_at FROM recipe WHERE id = $1`, e.recipe).Scan(&n, &last); err != nil {
		t.Fatal(err)
	}
	return n, last
}

func TestCookExactPortionsLevel(t *testing.T) {
	e := newEnv(t)
	chicken := e.add(t, "Chicken thighs", exact("900", "g"))
	tortillas := e.add(t, "Tortillas", portions(6))
	oil := e.add(t, "Olive oil", level("most"))
	onions := e.add(t, "Onions", exact("2", "ea"))
	p := e.cookPayload([]map[string]any{
		use(chicken, "quantity", "800"),
		use(tortillas, "portions", 2),
		use(oil, "level", "half"),
		use(onions, "finished", true, "toShopping", map[string]any{"id": uuid.New(), "listId": e.list}),
	}, nil)
	p["note"] = "  Spicier next time  "
	e.ok(t, "recipe.cook", p)

	if it := e.item(t, chicken); *it.Qty != "100" || it.Deleted {
		t.Errorf("chicken: %+v", it)
	}
	if it := e.item(t, tortillas); *it.Portions != 4 {
		t.Errorf("tortillas: %+v", it)
	}
	if it := e.item(t, oil); *it.Level != "half" {
		t.Errorf("oil: %+v", it)
	}
	if it := e.item(t, onions); !it.Deleted {
		t.Errorf("onions should be finished: %+v", it)
	}
	var events int
	_ = e.pool.QueryRow(t.Context(), `SELECT count(*) FROM stock_event WHERE cook_session_id = $1`, p["id"]).Scan(&events)
	if events != 4 {
		t.Errorf("%d cook events, want 4", events)
	}
	var recipeID *uuid.UUID
	var product uuid.UUID
	if err := e.pool.QueryRow(t.Context(), `SELECT recipe_id, product_id FROM shopping_item WHERE household_id = $1 AND deleted_at IS NULL`, e.hh).Scan(&recipeID, &product); err != nil {
		t.Fatal(err)
	}
	if recipeID == nil || *recipeID != e.recipe {
		t.Errorf("shopping item recipe = %v", recipeID)
	}
	s := e.session(t, p["id"])
	if len(s.Uses) != 4 || *s.Uses[0].Quantity != "800" || *s.Uses[1].Portions != 2 || *s.Uses[2].LevelBefore != "most" || s.Uses[3].ShoppingItemID == nil {
		t.Errorf("uses: %+v", s.Uses)
	}
	var note string
	_ = e.pool.QueryRow(t.Context(), `SELECT note FROM cook_session WHERE id = $1`, p["id"]).Scan(&note)
	if note != "Spicier next time" {
		t.Errorf("note %q", note)
	}
	if n, last := e.counts(t); n != 1 || last == nil {
		t.Errorf("counts %d %v", n, last)
	}
}

func TestCookStoresLeftovers(t *testing.T) {
	e := newEnv(t)
	p := e.cookPayload(nil, []map[string]any{
		{"stockItemId": uuid.New(), "eventId": uuid.New(), "locationId": e.loc["fridge"], "portions": 2},
		{"stockItemId": uuid.New(), "eventId": uuid.New(), "locationId": e.loc["freezer"], "portions": 1},
	})
	e.ok(t, "recipe.cook", p)
	s := e.session(t, p["id"])
	if s.Leftover == nil || *s.Leftover != e.leftover || len(s.Stored) != 2 {
		t.Fatalf("session: %+v", s)
	}
	var name, cat string
	_ = e.pool.QueryRow(t.Context(), `SELECT p.name, c.slug FROM product p JOIN category c ON c.id = p.category_id WHERE p.id = $1`, e.leftover).Scan(&name, &cat)
	if name != "Leftover chicken curry" || cat != "leftovers" {
		t.Errorf("leftover product %q %q", name, cat)
	}
	today := time.Now().UTC().Truncate(24 * time.Hour)
	want := map[string]string{e.loc["fridge"].String(): today.AddDate(0, 0, 4).Format("2006-01-02"), e.loc["freezer"].String(): today.AddDate(0, 0, 90).Format("2006-01-02")}
	for _, st := range s.Stored {
		var exp, src string
		var n int
		var sess uuid.UUID
		if err := e.pool.QueryRow(t.Context(), `SELECT expires_on::text, source, portions, cook_session_id FROM stock_item WHERE id = $1`, st.StockItemID).Scan(&exp, &src, &n, &sess); err != nil {
			t.Fatal(err)
		}
		if exp != want[st.LocationID.String()] || src != "cook" || n != st.Portions || sess.String() != p["id"].(uuid.UUID).String() {
			t.Errorf("stored %+v: expires %s source %s", st, exp, src)
		}
		if st.ExpiresOn == nil || *st.ExpiresOn != exp {
			t.Errorf("session expires_on %v, item %s", st.ExpiresOn, exp)
		}
	}
	// The second cook reuses the recipe's leftover product, whatever id the client proposes.
	p2 := e.cookPayload(nil, []map[string]any{{"stockItemId": uuid.New(), "eventId": uuid.New(), "locationId": e.loc["fridge"], "portions": 1}})
	p2["leftoverProductId"] = uuid.New()
	e.ok(t, "recipe.cook", p2)
	if s2 := e.session(t, p2["id"]); *s2.Leftover != e.leftover {
		t.Errorf("second cook made another leftover product")
	}
	if n, _ := e.counts(t); n != 2 {
		t.Errorf("cooked_count %d", n)
	}
}

func TestCookSkipsDeletedItem(t *testing.T) {
	e := newEnv(t)
	gone := e.add(t, "Yogurt", exact("500", "g"))
	kept := e.add(t, "Rice", exact("1000", "g"))
	e.ok(t, "inventory.stock.discard", map[string]any{"id": gone, "eventId": uuid.New(), "reason": "spoiled"})
	p := e.cookPayload([]map[string]any{use(gone, "quantity", "250"), use(kept, "quantity", "300")}, nil)
	e.ok(t, "recipe.cook", p)
	s := e.session(t, p["id"])
	if !s.Uses[0].Skipped || s.Uses[1].Skipped {
		t.Errorf("uses: %+v", s.Uses)
	}
	if it := e.item(t, kept); *it.Qty != "700" {
		t.Errorf("rice %+v", it)
	}
}

func TestCookAndConsumeCommute(t *testing.T) {
	e := newEnv(t)
	rice := e.add(t, "Rice", exact("1000", "g"))
	consume := cmd("inventory.stock.consume", map[string]any{"id": rice, "eventId": uuid.New(), "quantity": "200"})
	cooking := cmd("recipe.cook", e.cookPayload([]map[string]any{use(rice, "quantity", "300")}, nil))
	var wg sync.WaitGroup
	for _, c := range []commands.Command{consume, cooking} {
		wg.Go(func() {
			if r := e.do(t, c); r.Status != commands.StatusApplied {
				t.Errorf("%s: %+v %v", c.Type, r, r.Problem)
			}
		})
	}
	wg.Wait()
	if it := e.item(t, rice); *it.Qty != "500" {
		t.Errorf("rice %+v, want 500 either order", it)
	}
}

func TestUndo(t *testing.T) {
	e := newEnv(t)
	chicken := e.add(t, "Chicken thighs", exact("900", "g"))
	onions := e.add(t, "Onions", exact("2", "ea"))
	oil := e.add(t, "Olive oil", level("most"))
	shopID := uuid.New()
	fridge1, fridge2 := uuid.New(), uuid.New()
	p := e.cookPayload([]map[string]any{
		use(chicken, "quantity", "800"),
		use(onions, "finished", true, "toShopping", map[string]any{"id": shopID, "listId": e.list}),
		use(oil, "level", "low"),
	}, []map[string]any{
		{"stockItemId": fridge1, "eventId": uuid.New(), "locationId": e.loc["fridge"], "portions": 2},
		{"stockItemId": fridge2, "eventId": uuid.New(), "locationId": e.loc["fridge"], "portions": 2},
	})
	e.ok(t, "recipe.cook", p)
	// Meanwhile: someone uses more chicken and eats one stored portion.
	e.ok(t, "inventory.stock.consume", map[string]any{"id": chicken, "eventId": uuid.New(), "quantity": "50"})
	e.ok(t, "inventory.stock.consume", map[string]any{"id": fridge2, "eventId": uuid.New(), "portions": 1})

	e.ok(t, "recipe.cook.undo", map[string]any{"id": p["id"]})
	if it := e.item(t, chicken); *it.Qty != "850" {
		t.Errorf("chicken %+v, want 850 (900 - 50)", it)
	}
	if it := e.item(t, onions); it.Deleted || *it.Qty != "2" {
		t.Errorf("onions should be back: %+v", it)
	}
	if it := e.item(t, oil); *it.Level != "most" {
		t.Errorf("oil %+v", it)
	}
	if it := e.item(t, fridge1); !it.Deleted {
		t.Errorf("untouched leftovers should go")
	}
	if it := e.item(t, fridge2); it.Deleted || *it.Portions != 1 {
		t.Errorf("leftovers someone ate from are real stock now: %+v", it)
	}
	var shopGone bool
	_ = e.pool.QueryRow(t.Context(), `SELECT deleted_at IS NOT NULL FROM shopping_item WHERE id = $1`, shopID).Scan(&shopGone)
	if !shopGone {
		t.Errorf("unchecked shopping item should go")
	}
	var undoEvent int
	_ = e.pool.QueryRow(t.Context(), `SELECT count(*) FROM stock_event WHERE id = $1`, cook.UndoEventID(e.session(t, p["id"]).Uses[0].Event)).Scan(&undoEvent)
	if undoEvent != 1 {
		t.Errorf("compensating event with the derived id missing")
	}
	if s := e.session(t, p["id"]); !s.Undone {
		t.Errorf("session not undone")
	}
	if n, last := e.counts(t); n != 0 || last != nil {
		t.Errorf("counts %d %v", n, last)
	}
	// Undo again (another device) is a no-op.
	e.ok(t, "recipe.cook.undo", map[string]any{"id": p["id"]})
	if it := e.item(t, chicken); *it.Qty != "850" {
		t.Errorf("second undo changed stock: %+v", it)
	}
}

func TestUndoKeepsLevelChangedSince(t *testing.T) {
	e := newEnv(t)
	oil := e.add(t, "Olive oil", level("most"))
	p := e.cookPayload([]map[string]any{use(oil, "level", "half")}, nil)
	e.ok(t, "recipe.cook", p)
	e.ok(t, "inventory.stock.set_level", map[string]any{"id": oil, "eventId": uuid.New(), "level": "low"})
	e.ok(t, "recipe.cook.undo", map[string]any{"id": p["id"]})
	if it := e.item(t, oil); *it.Level != "low" {
		t.Errorf("undo overwrote a newer level: %+v", it)
	}
}

func TestUndoWindow(t *testing.T) {
	e := newEnv(t)
	p := e.cookPayload(nil, nil)
	e.ok(t, "recipe.cook", p)
	if _, err := e.pool.Exec(t.Context(), `UPDATE cook_session SET created_at = now() - interval '25 hours' WHERE id = $1`, p["id"]); err != nil {
		t.Fatal(err)
	}
	e.rejected(t, "recipe.cook.undo", map[string]any{"id": p["id"]}, commands.CodeInvalid)
}

func TestCookValidation(t *testing.T) {
	e := newEnv(t)
	rice := e.add(t, "Rice", exact("1000", "g"))
	tooMany := e.cookPayload(nil, []map[string]any{{"stockItemId": uuid.New(), "eventId": uuid.New(), "locationId": e.loc["fridge"], "portions": 5}})
	e.rejected(t, "recipe.cook", tooMany, commands.CodeInvalid)
	two := e.cookPayload([]map[string]any{use(rice, "quantity", "1", "finished", true)}, nil)
	e.rejected(t, "recipe.cook", two, commands.CodeInvalid)
	wrongKind := e.cookPayload([]map[string]any{use(rice, "portions", 1)}, nil)
	e.rejected(t, "recipe.cook", wrongKind, commands.CodeInvalid)
	e.ok(t, "recipe.delete", map[string]any{"id": e.recipe})
	e.rejected(t, "recipe.cook", e.cookPayload(nil, nil), commands.CodeGone)
}

func TestRecipeDeleteTombstonesSessions(t *testing.T) {
	e := newEnv(t)
	p := e.cookPayload(nil, nil)
	e.ok(t, "recipe.cook", p)
	e.ok(t, "recipe.delete", map[string]any{"id": e.recipe})
	var deleted bool
	_ = e.pool.QueryRow(t.Context(), `SELECT deleted_at IS NOT NULL FROM cook_session WHERE id = $1`, p["id"]).Scan(&deleted)
	if !deleted {
		t.Error("cook session survived its recipe")
	}
}

func TestSetConversion(t *testing.T) {
	e := newEnv(t)
	rice := e.add(t, "Rice", exact("1000", "g"))
	var product uuid.UUID
	_ = e.pool.QueryRow(t.Context(), `SELECT product_id FROM stock_item WHERE id = $1`, rice).Scan(&product)
	e.ok(t, "catalog.product.set_conversion", map[string]any{"id": product, "unit": "cup", "amount": "185.0 g"})
	e.ok(t, "catalog.product.set_conversion", map[string]any{"id": product, "unit": "ea", "amount": "5 g"})
	e.ok(t, "catalog.product.set_conversion", map[string]any{"id": product, "unit": "ea", "amount": nil})
	var conv map[string]string
	_ = e.pool.QueryRow(t.Context(), `SELECT conversions FROM product WHERE id = $1`, product).Scan(&conv)
	if len(conv) != 1 || conv["cup"] != "185 g" {
		t.Errorf("conversions %v", conv)
	}
	e.rejected(t, "catalog.product.set_conversion", map[string]any{"id": product, "unit": "cup", "amount": "1 ea"}, commands.CodeInvalid)
	e.rejected(t, "catalog.product.set_conversion", map[string]any{"id": product, "unit": "cup", "amount": "250 ml"}, commands.CodeInvalid)
	e.rejected(t, "catalog.product.set_conversion", map[string]any{"id": uuid.New(), "unit": "cup", "amount": "1 g"}, commands.CodeGone)
}
