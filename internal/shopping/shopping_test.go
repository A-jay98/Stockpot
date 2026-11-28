package shopping_test

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/a-jay98/stockpot/internal/catalog"
	"github.com/a-jay98/stockpot/internal/catalog/resolve"
	"github.com/a-jay98/stockpot/internal/commands"
	"github.com/a-jay98/stockpot/internal/identity"
	"github.com/a-jay98/stockpot/internal/shopping"
	"github.com/a-jay98/stockpot/internal/testdb"
)

func TestMain(m *testing.M) { os.Exit(testdb.Main(m)) }

func at(day int) time.Time { return time.Date(2026, 1, day, 15, 0, 0, 0, time.UTC) }

func TestComputeStat(t *testing.T) {
	f := func(v float64) *float64 { return &v }
	tests := []struct {
		name     string
		ps       []shopping.Purchase // newest first
		times    int
		qty      string
		unit     string
		interval *float64
	}{
		{"none", nil, 0, "", "", nil},
		{"one purchase has no interval", []shopping.Purchase{{at(10), "2", "lb"}}, 1, "2", "lb", nil},
		{"mode beats latest", []shopping.Purchase{{at(21), "3", "lb"}, {at(14), "2", "lb"}, {at(7), "2", "lb"}}, 3, "2", "lb", f(7)},
		{"tie goes to the most recent", []shopping.Purchase{{at(14), "1", "bag"}, {at(7), "2", "lb"}}, 2, "1", "bag", f(7)},
		{"median of odd gaps", []shopping.Purchase{{at(30), "", ""}, {at(20), "", ""}, {at(18), "", ""}, {at(4), "", ""}}, 4, "", "", f(10)}, // gaps 10, 2, 14
		{"median of even gaps averages", []shopping.Purchase{{at(20), "", ""}, {at(10), "", ""}, {at(6), "", ""}}, 3, "", "", f(7)},          // gaps 10, 4
		{"same day counts once", []shopping.Purchase{{at(14), "1", "ea"}, {at(14).Add(time.Hour), "1", "ea"}, {at(7), "1", "ea"}}, 3, "1", "ea", f(7)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := shopping.ComputeStat(tc.ps)
			if s.Times != tc.times || s.TypicalQty != tc.qty || s.TypicalUnit != tc.unit {
				t.Fatalf("got %+v", s)
			}
			if (s.MedianInterv == nil) != (tc.interval == nil) || (tc.interval != nil && math.Abs(*s.MedianInterv-*tc.interval) > 1e-9) {
				t.Fatalf("interval = %v, want %v", s.MedianInterv, tc.interval)
			}
		})
	}
}

func TestComputeStatUsesOnlyTheLastTen(t *testing.T) {
	var ps []shopping.Purchase
	for i := 0; i < 12; i++ { // newest first, one per day; the two oldest used another qty
		q := "1"
		if i >= 10 {
			q = "9"
		}
		ps = append(ps, shopping.Purchase{At: at(28 - i), Qty: q, Unit: "ea"})
	}
	s := shopping.ComputeStat(ps)
	if s.Times != 12 || s.TypicalQty != "1" {
		t.Fatalf("got %+v", s)
	}
}

type env struct {
	pool *pgxpool.Pool
	svc  *commands.Service
	hh   uuid.UUID
	ctx  context.Context
	list uuid.UUID
}

func newEnv(t *testing.T) *env {
	t.Helper()
	pool := testdb.New(t)
	if err := catalog.Seed(t.Context(), pool); err != nil {
		t.Fatal(err)
	}
	reg := commands.NewRegistry()
	catalog.Register(reg, resolve.Resolver{})
	shopping.Register(reg, resolve.Resolver{})
	e := &env{pool: pool, svc: commands.NewService(pool, reg)}
	if err := pool.QueryRow(t.Context(), `INSERT INTO household (id, name) VALUES (gen_random_uuid(), 'Home') RETURNING id`).Scan(&e.hh); err != nil {
		t.Fatal(err)
	}
	e.ctx = identity.WithPrincipal(t.Context(), &identity.Principal{UserID: uuid.New(), Status: identity.StatusActive, HouseholdID: e.hh})
	if err := pool.QueryRow(t.Context(), `SELECT id FROM shopping_list WHERE household_id = $1`, e.hh).Scan(&e.list); err != nil {
		t.Fatal(err)
	}
	return e
}

func (e *env) do(t *testing.T, typ string, payload map[string]any) commands.Result {
	t.Helper()
	b, _ := json.Marshal(payload)
	rs, err := e.svc.Apply(e.ctx, e.hh, []commands.Command{{ID: uuid.New(), Type: typ, Payload: b, ClientTime: time.Now()}})
	if err != nil {
		t.Fatal(err)
	}
	return rs[0]
}

func (e *env) mustDo(t *testing.T, typ string, payload map[string]any) {
	t.Helper()
	if r := e.do(t, typ, payload); r.Status != commands.StatusApplied {
		t.Fatalf("%s: %+v %v", typ, r, r.Problem)
	}
}

func (e *env) addItem(t *testing.T, fields map[string]any) uuid.UUID {
	t.Helper()
	id := uuid.New()
	p := map[string]any{"id": id, "listId": e.list}
	for k, v := range fields {
		p[k] = v
	}
	e.mustDo(t, "shopping.item.add", p)
	return id
}

type stat struct {
	times    int
	qty      *string
	unit     *string
	interval *float64
}

func (e *env) stat(t *testing.T, product uuid.UUID, store *uuid.UUID) stat {
	t.Helper()
	var s stat
	err := e.pool.QueryRow(t.Context(), `SELECT times_bought, typical_qty::text, typical_unit, median_interval_days FROM product_stat
		WHERE product_id = $1 AND store_id IS NOT DISTINCT FROM $2`, product, store).Scan(&s.times, &s.qty, &s.unit, &s.interval)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	return s
}

func (e *env) store(t *testing.T, name string) uuid.UUID {
	t.Helper()
	id := uuid.New()
	e.mustDo(t, "catalog.store.create", map[string]any{"id": id, "name": name})
	return id
}

func (e *env) productByName(t *testing.T, name string) []uuid.UUID {
	t.Helper()
	rows, err := e.pool.Query(t.Context(), `SELECT id FROM product WHERE household_id = $1 AND lower(name) = lower($2) AND deleted_at IS NULL`, e.hh, name)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var ids []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		_ = rows.Scan(&id)
		ids = append(ids, id)
	}
	return ids
}

func TestCheckLearnsProductStatPerStore(t *testing.T) {
	e := newEnv(t)
	costco := e.store(t, "Costco")
	nofrills := e.store(t, "No Frills")
	pid := uuid.New()
	e.mustDo(t, "catalog.product.create", map[string]any{"id": pid, "name": "Mandarins", "brand": "Kirkland"})

	buy := func(store uuid.UUID, qty, unit string, daysAgo int) {
		id := e.addItem(t, map[string]any{"productId": pid, "text": "Mandarins", "qty": qty, "unit": unit})
		e.mustDo(t, "shopping.item.check", map[string]any{"id": id, "storeId": store})
		_, err := e.pool.Exec(t.Context(), `UPDATE purchase SET bought_at = now() - make_interval(days => $2) WHERE shopping_item_id = $1`, id, daysAgo)
		if err != nil {
			t.Fatal(err)
		}
	}
	buy(costco, "2", "lb", 28)
	buy(costco, "2", "lb", 14)
	buy(nofrills, "1", "bag", 7)
	// Purchases were backdated after ticking; this tick recomputes the product's stats from them.
	last := e.addItem(t, map[string]any{"productId": pid, "text": "Mandarins", "qty": "3", "unit": "lb"})
	e.mustDo(t, "shopping.item.check", map[string]any{"id": last, "storeId": costco})

	cs := e.stat(t, pid, &costco)
	if cs.times != 3 || cs.qty == nil || *cs.qty != "2" || cs.unit == nil || *cs.unit != "lb" {
		t.Fatalf("costco stat: %+v", cs)
	}
	if cs.interval == nil || math.Abs(*cs.interval-14) > 1 { // 28 → 14 → today(0): gaps 14, 14
		t.Fatalf("costco interval = %v", cs.interval)
	}
	if ns := e.stat(t, pid, &nofrills); ns.times != 1 || ns.interval != nil {
		t.Fatalf("no frills stat: %+v", ns)
	}
	all := e.stat(t, pid, nil)
	if all.times != 4 {
		t.Fatalf("store-less stat: %+v", all)
	}

	// Ticking twice does not double count; unticking removes the purchase.
	e.mustDo(t, "shopping.item.check", map[string]any{"id": last, "storeId": costco})
	if e.stat(t, pid, nil).times != 4 {
		t.Fatal("double tick counted twice")
	}
	e.mustDo(t, "shopping.item.uncheck", map[string]any{"id": last})
	if e.stat(t, pid, nil).times != 3 || e.stat(t, pid, &costco).times != 2 {
		t.Fatal("untick did not remove the purchase")
	}
}

func TestFreeTextLinksExistingProductByName(t *testing.T) {
	e := newEnv(t)
	pid := uuid.New()
	e.mustDo(t, "catalog.product.create", map[string]any{"id": pid, "name": "Mandarins"})

	id := e.addItem(t, map[string]any{"text": "mandarins"})
	e.mustDo(t, "shopping.item.check", map[string]any{"id": id})
	var linked uuid.UUID
	_ = e.pool.QueryRow(t.Context(), `SELECT product_id FROM shopping_item WHERE id = $1`, id).Scan(&linked)
	if linked != pid || len(e.productByName(t, "mandarins")) != 1 {
		t.Fatalf("free text created a duplicate: linked %v want %v", linked, pid)
	}

	// An unknown name creates a product, resolved to its ingredient and category.
	id = e.addItem(t, map[string]any{"text": "Pomegranate"})
	e.mustDo(t, "shopping.item.check", map[string]any{"id": id})
	var ing, cat *uuid.UUID
	_ = e.pool.QueryRow(t.Context(), `SELECT p.ingredient_id, p.category_id FROM shopping_item s JOIN product p ON p.id = s.product_id WHERE s.id = $1`, id).Scan(&ing, &cat)
	if ing == nil || cat == nil {
		t.Fatalf("new product not resolved: ingredient %v category %v", ing, cat)
	}
}

func TestFreeTextCheckUsesClientsProductPick(t *testing.T) {
	e := newEnv(t)
	want := uuid.New()
	id := e.addItem(t, map[string]any{"text": "Pomegranate"})
	e.mustDo(t, "shopping.item.check", map[string]any{"id": id, "productId": want, "inventory": map[string]any{"stockItemId": uuid.New()}})
	var linked, stocked uuid.UUID
	_ = e.pool.QueryRow(t.Context(), `SELECT s.product_id, i.product_id FROM shopping_item s JOIN stock_item i ON i.id = s.stock_item_id WHERE s.id = $1`, id).Scan(&linked, &stocked)
	if linked != want || stocked != want {
		t.Fatalf("product %v, stock product %v, want %v", linked, stocked, want)
	}

	// An existing household product the client picked is used as is, whatever the text says.
	again := e.addItem(t, map[string]any{"text": "pomegranates"})
	e.mustDo(t, "shopping.item.check", map[string]any{"id": again, "productId": want})
	_ = e.pool.QueryRow(t.Context(), `SELECT product_id FROM shopping_item WHERE id = $1`, again).Scan(&linked)
	if linked != want {
		t.Fatalf("second item linked %v, want %v", linked, want)
	}

	// A new id is honoured even where the ingredient would match an existing product, so the
	// product an offline client already shows exists once it syncs.
	fresh := uuid.New()
	third := e.addItem(t, map[string]any{"text": "Pomegranate seeds"})
	e.mustDo(t, "shopping.item.check", map[string]any{"id": third, "productId": fresh})
	_ = e.pool.QueryRow(t.Context(), `SELECT product_id FROM shopping_item WHERE id = $1`, third).Scan(&linked)
	if linked != fresh {
		t.Fatalf("third item linked %v, want %v", linked, fresh)
	}

	// Without a pick, an exact name still wins over nothing.
	fourth := e.addItem(t, map[string]any{"text": "POMEGRANATE"})
	e.mustDo(t, "shopping.item.check", map[string]any{"id": fourth})
	_ = e.pool.QueryRow(t.Context(), `SELECT product_id FROM shopping_item WHERE id = $1`, fourth).Scan(&linked)
	if linked != want {
		t.Fatalf("fourth item linked %v, want %v", linked, want)
	}
}

func TestCheckIntoInventory(t *testing.T) {
	e := newEnv(t)
	var fridge, freezer uuid.UUID
	_ = e.pool.QueryRow(t.Context(), `SELECT id FROM location WHERE household_id = $1 AND kind = 'fridge'`, e.hh).Scan(&fridge)
	_ = e.pool.QueryRow(t.Context(), `SELECT id FROM location WHERE household_id = $1 AND kind = 'freezer'`, e.hh).Scan(&freezer)

	id := e.addItem(t, map[string]any{"text": "Milk", "qty": "2", "unit": "l"})
	stock := uuid.New()
	move := map[string]any{"stockItemId": stock, "locationId": fridge}
	e.mustDo(t, "shopping.item.check", map[string]any{"id": id, "inventory": move})

	var qty, unit, kind, src *string
	var loc *uuid.UUID
	var expires *time.Time
	err := e.pool.QueryRow(t.Context(), `SELECT quantity::text, unit, location_id, expires_on, expiry_kind, source FROM stock_item WHERE id = $1`, stock).
		Scan(&qty, &unit, &loc, &expires, &kind, &src)
	if err != nil {
		t.Fatal(err)
	}
	if qty == nil || *qty != "2" || unit == nil || *unit != "l" || loc == nil || *loc != fridge || src == nil || *src != "shopping" {
		t.Fatalf("stock item: qty %v unit %v loc %v src %v", qty, unit, loc, src)
	}
	if expires == nil || kind == nil || *kind != "estimated" {
		t.Fatalf("no estimated expiry: %v %v (milk's seed shelf life should apply)", expires, kind)
	}
	var events int
	_ = e.pool.QueryRow(t.Context(), `SELECT count(*) FROM stock_event WHERE stock_item_id = $1 AND kind = 'add'`, stock).Scan(&events)
	var linked *uuid.UUID
	_ = e.pool.QueryRow(t.Context(), `SELECT stock_item_id FROM shopping_item WHERE id = $1`, id).Scan(&linked)
	if events != 1 || linked == nil || *linked != stock {
		t.Fatalf("events %d, linked %v", events, linked)
	}

	// Putting it away twice adds nothing; an item already in inventory cannot be unticked.
	e.mustDo(t, "shopping.item.check", map[string]any{"id": id, "inventory": map[string]any{"stockItemId": uuid.New()}})
	var stocks int
	_ = e.pool.QueryRow(t.Context(), `SELECT count(*) FROM stock_item WHERE household_id = $1`, e.hh).Scan(&stocks)
	if stocks != 1 {
		t.Fatalf("%d stock items", stocks)
	}
	if r := e.do(t, "shopping.item.uncheck", map[string]any{"id": id}); r.Status != commands.StatusRejected {
		t.Fatalf("uncheck after put away: %+v", r)
	}

	// A given date wins over the estimate; the freezer sets frozen_on.
	id2 := e.addItem(t, map[string]any{"text": "Peas"})
	stock2 := uuid.New()
	e.mustDo(t, "shopping.item.check", map[string]any{"id": id2, "inventory": map[string]any{"stockItemId": stock2, "locationId": freezer, "expiresOn": "2027-03-01"}})
	var frozen *time.Time
	_ = e.pool.QueryRow(t.Context(), `SELECT expires_on, expiry_kind, frozen_on FROM stock_item WHERE id = $1`, stock2).Scan(&expires, &kind, &frozen)
	if expires == nil || expires.Format(time.DateOnly) != "2027-03-01" || *kind != "best_before" || frozen == nil {
		t.Fatalf("explicit expiry: %v %v frozen %v", expires, kind, frozen)
	}
}

func TestListAndItemLifecycle(t *testing.T) {
	e := newEnv(t)
	if r := e.do(t, "shopping.item.add", map[string]any{"id": uuid.New(), "listId": e.list}); r.Status != commands.StatusRejected {
		t.Fatalf("empty item accepted: %+v", r)
	}
	if r := e.do(t, "shopping.item.add", map[string]any{"id": uuid.New(), "listId": uuid.New(), "text": "x"}); r.Status != commands.StatusRejected || r.Problem.Code != commands.CodeGone {
		t.Fatalf("missing list: %+v", r)
	}
	a := e.addItem(t, map[string]any{"text": "Eggs"})
	b := e.addItem(t, map[string]any{"text": "Bread"})
	c := e.addItem(t, map[string]any{"text": "Salt"})
	e.mustDo(t, "shopping.item.reorder", map[string]any{"listId": e.list, "ids": []uuid.UUID{c, a, b}})
	rows, _ := e.pool.Query(t.Context(), `SELECT text FROM shopping_item WHERE list_id = $1 ORDER BY sort`, e.list)
	var order []string
	for rows.Next() {
		var s string
		_ = rows.Scan(&s)
		order = append(order, s)
	}
	rows.Close()
	if len(order) != 3 || order[0] != "Salt" || order[1] != "Eggs" || order[2] != "Bread" {
		t.Fatalf("order = %v", order)
	}

	e.mustDo(t, "shopping.item.update", map[string]any{"id": a, "qty": "12", "note": "large", "storeIds": []uuid.UUID{}})
	e.mustDo(t, "shopping.item.update", map[string]any{"id": a, "note": nil})
	if r := e.do(t, "shopping.item.update", map[string]any{"id": a, "unit": "parsec"}); r.Status != commands.StatusRejected {
		t.Fatalf("bad unit accepted: %+v", r)
	}

	e.mustDo(t, "shopping.item.check", map[string]any{"id": a})
	e.mustDo(t, "shopping.item.check", map[string]any{"id": b})
	e.mustDo(t, "shopping.item.clear_checked", map[string]any{"listId": e.list})
	var left int
	_ = e.pool.QueryRow(t.Context(), `SELECT count(*) FROM shopping_item WHERE list_id = $1 AND deleted_at IS NULL`, e.list).Scan(&left)
	if left != 1 {
		t.Fatalf("%d items left after clear_checked", left)
	}
	// Purchases survive clearing the list.
	var purchases int
	_ = e.pool.QueryRow(t.Context(), `SELECT count(*) FROM purchase WHERE household_id = $1`, e.hh).Scan(&purchases)
	if purchases != 2 {
		t.Fatalf("%d purchases", purchases)
	}
	e.mustDo(t, "shopping.item.delete", map[string]any{"id": a}) // already gone: no-op
	if r := e.do(t, "shopping.item.check", map[string]any{"id": a}); r.Status != commands.StatusRejected || r.Problem.Code != commands.CodeGone {
		t.Fatalf("check of deleted item: %+v", r)
	}

	extra := uuid.New()
	e.mustDo(t, "shopping.list.create", map[string]any{"id": extra, "name": "Costco run"})
	e.mustDo(t, "shopping.list.rename", map[string]any{"id": extra, "name": "Costco"})
	e.mustDo(t, "shopping.list.delete", map[string]any{"id": extra})
}

func TestPurchaseUsesClientTimeClamped(t *testing.T) {
	e := newEnv(t)
	pid := uuid.New()
	e.mustDo(t, "catalog.product.create", map[string]any{"id": pid, "name": "Milk"})
	tick := func(ago time.Duration) time.Time {
		id := e.addItem(t, map[string]any{"productId": pid, "text": "Milk"})
		b, _ := json.Marshal(map[string]any{"id": id})
		when := time.Now().Add(-ago)
		rs, err := e.svc.Apply(e.ctx, e.hh, []commands.Command{{ID: uuid.New(), Type: "shopping.item.check", Payload: b, ClientTime: when}})
		if err != nil || rs[0].Status != commands.StatusApplied {
			t.Fatalf("check: %v %+v", err, rs)
		}
		var got time.Time
		_ = e.pool.QueryRow(t.Context(), `SELECT bought_at FROM purchase WHERE shopping_item_id = $1`, id).Scan(&got)
		return got
	}
	// Ticked offline two days ago: recorded two days ago, not at sync time.
	if got := tick(48 * time.Hour); time.Since(got) < 47*time.Hour || time.Since(got) > 49*time.Hour {
		t.Fatalf("bought_at %v, want ~2 days ago", got)
	}
	// A wildly old clock is clamped to a week; a future one to now.
	if got := tick(90 * 24 * time.Hour); time.Since(got) > 8*24*time.Hour {
		t.Fatalf("old client time not clamped: %v", got)
	}
	if got := tick(-time.Hour); time.Since(got) > time.Minute {
		t.Fatalf("future client time not clamped: %v", got)
	}
}

func TestFreeTextMatchesSameIngredientProduct(t *testing.T) {
	e := newEnv(t)
	pid := uuid.New()
	e.mustDo(t, "catalog.product.create", map[string]any{"id": pid, "name": "Mandarins"})
	id := e.addItem(t, map[string]any{"text": "mandarin"})
	e.mustDo(t, "shopping.item.check", map[string]any{"id": id})
	var linked uuid.UUID
	_ = e.pool.QueryRow(t.Context(), `SELECT product_id FROM shopping_item WHERE id = $1`, id).Scan(&linked)
	var n int
	_ = e.pool.QueryRow(t.Context(), `SELECT count(*) FROM product WHERE household_id = $1`, e.hh).Scan(&n)
	if linked != pid || n != 1 {
		t.Fatalf("linked %v (want %v), %d products", linked, pid, n)
	}
}

func TestTypicalQtyIgnoresPurchasesWithoutQuantity(t *testing.T) {
	s := shopping.ComputeStat([]shopping.Purchase{{At: at(20)}, {At: at(13), Qty: "2", Unit: "lb"}, {At: at(6), Qty: "2", Unit: "lb"}})
	if s.TypicalQty != "2" || s.TypicalUnit != "lb" {
		t.Fatalf("%+v", s)
	}
}

func TestAddCreatesTheProposedProduct(t *testing.T) {
	e := newEnv(t)
	var cream uuid.UUID
	if err := e.pool.QueryRow(t.Context(), `SELECT id FROM ingredient WHERE slug = 'cream-cheese' AND household_id IS NULL`).Scan(&cream); err != nil {
		t.Fatal(err)
	}
	type row struct {
		name, source string
		ingredient   *uuid.UUID
		category     *uuid.UUID
	}
	get := func(id uuid.UUID) (r row) {
		t.Helper()
		if err := e.pool.QueryRow(t.Context(), `SELECT name, ingredient_id, match_source, category_id FROM product WHERE id = $1`, id).
			Scan(&r.name, &r.ingredient, &r.source, &r.category); err != nil {
			t.Fatal(err)
		}
		return r
	}
	itemProduct := func(item uuid.UUID) (p uuid.UUID) {
		t.Helper()
		if err := e.pool.QueryRow(t.Context(), `SELECT product_id FROM shopping_item WHERE id = $1`, item).Scan(&p); err != nil {
			t.Fatal(err)
		}
		return p
	}

	// A picked ingredient is the user's match, and the item keeps what was typed.
	want := uuid.New()
	item := e.addItem(t, map[string]any{"text": "Philadelphia cheese", "productId": want, "ingredientId": cream})
	if got := itemProduct(item); got != want {
		t.Fatalf("item product = %s, want %s", got, want)
	}
	if r := get(want); r.name != "Philadelphia cheese" || r.ingredient == nil || *r.ingredient != cream || r.source != "user" || r.category == nil {
		t.Fatalf("product = %+v", r)
	}

	// Free text: the resolver links it.
	milk := uuid.New()
	e.addItem(t, map[string]any{"text": "Milk", "productId": milk})
	if r := get(milk); r.ingredient == nil || r.source != "rule" {
		t.Fatalf("product = %+v", r)
	}

	// The same name again lands on the existing product, never the new id or the pick.
	again := e.addItem(t, map[string]any{"text": "milk", "productId": uuid.New(), "ingredientId": cream})
	if got := itemProduct(again); got != milk {
		t.Fatalf("item product = %s, want %s", got, milk)
	}
	if r := get(milk); r.source != "rule" {
		t.Fatalf("existing product changed: %+v", r)
	}

	// A hand-picked amount becomes the product's default, new product or not.
	chicken := uuid.New()
	e.addItem(t, map[string]any{"text": "Chicken thighs", "productId": chicken, "qty": "2", "unit": "kg", "rememberAmount": true})
	e.addItem(t, map[string]any{"text": "Milk", "productId": milk, "qty": "2", "unit": "l"})
	for id, want := range map[uuid.UUID][2]string{chicken: {"kg", "2"}, milk: {"", ""}} {
		var unit, qty *string
		if err := e.pool.QueryRow(t.Context(), `SELECT default_unit, default_qty::text FROM product WHERE id = $1`, id).Scan(&unit, &qty); err != nil {
			t.Fatal(err)
		}
		if deref(unit) != want[0] || deref(qty) != want[1] {
			t.Fatalf("defaults = %v %v, want %v", deref(unit), deref(qty), want)
		}
	}

	// An unknown ingredient is rejected.
	if r := e.do(t, "shopping.item.add", map[string]any{"id": uuid.New(), "listId": e.list, "text": "x", "productId": uuid.New(), "ingredientId": uuid.New()}); r.Status == commands.StatusApplied {
		t.Fatal("unknown ingredient accepted")
	}
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func TestAddFillsTheBuyUnit(t *testing.T) {
	e := newEnv(t)
	item := func(id uuid.UUID) (qty, unit *string) {
		t.Helper()
		if err := e.pool.QueryRow(t.Context(), `SELECT qty::text, unit FROM shopping_item WHERE id = $1`, id).Scan(&qty, &unit); err != nil {
			t.Fatal(err)
		}
		return qty, unit
	}
	str := func(s *string) string {
		if s == nil {
			return "<nil>"
		}
		return *s
	}

	// "salt" is bought as a bag: one bag unless a quantity was given.
	if q, u := item(e.addItem(t, map[string]any{"text": "Salt", "productId": uuid.New()})); str(q) != "1" || str(u) != "bag" {
		t.Fatalf("salt = %s %s", str(q), str(u))
	}
	if q, u := item(e.addItem(t, map[string]any{"text": "olive oil", "productId": uuid.New(), "qty": "2"})); str(q) != "2" || str(u) != "bottle" {
		t.Fatalf("olive oil = %s %s", str(q), str(u))
	}
	// A unit the user gave stays; a measure unit gets no made-up quantity.
	if q, u := item(e.addItem(t, map[string]any{"text": "rice", "productId": uuid.New(), "unit": "kg"})); q != nil || str(u) != "kg" {
		t.Fatalf("rice = %s %s", str(q), str(u))
	}
	if q, u := item(e.addItem(t, map[string]any{"text": "milk", "productId": uuid.New()})); q != nil || str(u) != "l" {
		t.Fatalf("milk = %s %s", str(q), str(u))
	}
	// The product's hand-picked default beats the ingredient's.
	var p uuid.UUID
	if err := e.pool.QueryRow(t.Context(), `SELECT product_id FROM shopping_item WHERE household_id = $1 AND text = 'Salt'`, e.hh).Scan(&p); err != nil {
		t.Fatal(err)
	}
	e.mustDo(t, "catalog.product.update", map[string]any{"id": p, "defaultUnit": "kg", "defaultQty": "1"})
	salt := e.addItem(t, map[string]any{"text": "Salt", "productId": p, "note": "second"})
	if q, u := item(salt); str(q) != "1" || str(u) != "kg" {
		t.Fatalf("salt again = %s %s", str(q), str(u))
	}
}
