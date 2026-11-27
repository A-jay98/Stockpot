package inventory_test

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/a-jay98/stockpot/internal/catalog"
	"github.com/a-jay98/stockpot/internal/commands"
	"github.com/a-jay98/stockpot/internal/identity"
	"github.com/a-jay98/stockpot/internal/inventory"
	"github.com/a-jay98/stockpot/internal/shopping"
	"github.com/a-jay98/stockpot/internal/testdb"
)

func TestMain(m *testing.M) { os.Exit(testdb.Main(m)) }

type env struct {
	pool *pgxpool.Pool
	svc  *commands.Service
	hh   uuid.UUID
	ctx  context.Context
	loc  map[string]uuid.UUID
}

func newEnv(t *testing.T) *env {
	t.Helper()
	pool := testdb.New(t)
	if err := catalog.Seed(t.Context(), pool); err != nil {
		t.Fatal(err)
	}
	reg := commands.NewRegistry()
	catalog.Register(reg, catalog.ExactResolver{})
	inventory.Register(reg, catalog.ExactResolver{}, func(ctx context.Context, tx pgx.Tx, hh, product uuid.UUID, store *uuid.UUID, at time.Time, qty, unit *string) error {
		return shopping.RecordPurchase(ctx, tx, hh, product, store, uuid.Nil, at, qty, unit)
	})
	e := &env{pool: pool, svc: commands.NewService(pool, reg), loc: map[string]uuid.UUID{}}
	if err := pool.QueryRow(t.Context(), `INSERT INTO household (id, name) VALUES (gen_random_uuid(), 'Home') RETURNING id`).Scan(&e.hh); err != nil {
		t.Fatal(err)
	}
	e.ctx = identity.WithPrincipal(t.Context(), &identity.Principal{UserID: uuid.New(), Status: identity.StatusActive, HouseholdID: e.hh})
	rows, err := pool.Query(t.Context(), `SELECT kind, id FROM location WHERE household_id = $1`, e.hh)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var k string
		var id uuid.UUID
		_ = rows.Scan(&k, &id)
		e.loc[k] = id
	}
	return e
}

func cmd(typ string, payload map[string]any) commands.Command {
	if _, ok := payload["eventId"]; !ok && strings.HasPrefix(typ, "inventory.") {
		payload["eventId"] = uuid.New()
	}
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

func (e *env) ok(t *testing.T, typ string, p map[string]any) {
	t.Helper()
	if r := e.do(t, cmd(typ, p)); r.Status != commands.StatusApplied {
		t.Fatalf("%s: %+v %v", typ, r, r.Problem)
	}
}

func (e *env) rejected(t *testing.T, typ string, p map[string]any, code string) {
	t.Helper()
	r := e.do(t, cmd(typ, p))
	if r.Status != commands.StatusRejected || r.Problem.Code != code {
		t.Fatalf("%s: got %+v %v, want rejected %s", typ, r, r.Problem, code)
	}
}

func (e *env) ingredient(t *testing.T, slug string) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := e.pool.QueryRow(t.Context(), `SELECT id FROM ingredient WHERE slug = $1 AND household_id IS NULL`, slug).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

type stock struct {
	Qty        *string
	Portions   *int
	Level      *string
	Low        bool
	Deleted    bool
	Loc        uuid.UUID
	Expires    *string
	ExpiryKind *string
	FrozenOn   *string
	ThawedOn   *string
	OpenedOn   *string
	Parent     *uuid.UUID
}

func (e *env) stock(t *testing.T, id uuid.UUID) stock {
	t.Helper()
	var s stock
	err := e.pool.QueryRow(t.Context(), `SELECT quantity::text, portions, level, low, deleted_at IS NOT NULL, location_id, expires_on::text, expiry_kind,
		frozen_on::text, thawed_on::text, opened_on::text, parent_item_id FROM stock_item WHERE id = $1`, id).Scan(
		&s.Qty, &s.Portions, &s.Level, &s.Low, &s.Deleted, &s.Loc, &s.Expires, &s.ExpiryKind, &s.FrozenOn, &s.ThawedOn, &s.OpenedOn, &s.Parent)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func (e *env) events(t *testing.T, item uuid.UUID) []string {
	t.Helper()
	rows, err := e.pool.Query(t.Context(), `SELECT kind || ':' || coalesce(delta::text, '') FROM stock_event WHERE stock_item_id = $1 ORDER BY created_at, id`, item)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		_ = rows.Scan(&s)
		out = append(out, s)
	}
	return out
}

func today() string { return time.Now().UTC().Format(inventory.DateLayout) }

func plus(days int) string {
	return time.Now().UTC().AddDate(0, 0, days).Format(inventory.DateLayout)
}

// addMandarins stocks 2 lb of a new product matched to mandarin (21 days in the fridge).
func (e *env) addMandarins(t *testing.T, location string, extra map[string]any) (item, product uuid.UUID) {
	t.Helper()
	item, product = uuid.New(), uuid.New()
	p := map[string]any{
		"id": item, "newProduct": map[string]any{"id": product, "name": "Mandarins", "brand": "Kirkland", "ingredientId": e.ingredient(t, "mandarin")},
		"locationId": e.loc[location], "amountKind": "exact", "quantity": "2", "unit": "lb", "source": "manual", "on": today(),
	}
	for k, v := range extra {
		p[k] = v
	}
	e.ok(t, "inventory.stock.add", p)
	return
}

func TestAddByNameEstimatesExpiryAndRecordsPurchase(t *testing.T) {
	e := newEnv(t)
	item, product := e.addMandarins(t, "fridge", nil)
	s := e.stock(t, item)
	if s.Expires == nil || *s.Expires != plus(21) || *s.ExpiryKind != "estimated" {
		t.Fatalf("expiry = %v %v, want %s estimated", s.Expires, s.ExpiryKind, plus(21))
	}
	if got := e.events(t, item); len(got) != 1 || got[0] != "add:2" {
		t.Fatalf("events = %v", got)
	}
	var times int
	var typical, unit string
	if err := e.pool.QueryRow(t.Context(), `SELECT times_bought, typical_qty::text, typical_unit FROM product_stat WHERE product_id = $1 AND store_id IS NULL`, product).Scan(&times, &typical, &unit); err != nil {
		t.Fatal(err)
	}
	if times != 1 || typical != "2" || unit != "lb" {
		t.Fatalf("stat = %d %s %s", times, typical, unit)
	}
}

func TestAddWithGivenDateAndStoreStat(t *testing.T) {
	e := newEnv(t)
	store := uuid.New()
	e.ok(t, "catalog.store.create", map[string]any{"id": store, "name": "Costco"})
	item, product := e.addMandarins(t, "fridge", map[string]any{"expiresOn": plus(5), "storeId": store})
	s := e.stock(t, item)
	if *s.Expires != plus(5) || *s.ExpiryKind != "best_before" {
		t.Fatalf("given date kept as best_before: %v %v", *s.Expires, *s.ExpiryKind)
	}
	for _, scope := range []string{"store_id = $2", "store_id IS NULL"} {
		var n int
		args := []any{product}
		if scope == "store_id = $2" {
			args = append(args, store)
		}
		if err := e.pool.QueryRow(t.Context(), `SELECT count(*) FROM product_stat WHERE product_id = $1 AND `+scope+` AND times_bought = 1`, args...).Scan(&n); err != nil || n != 1 {
			t.Fatalf("stat row for %q: %d %v", scope, n, err)
		}
	}
	// A cooked-in item is not a purchase.
	other := uuid.New()
	e.ok(t, "inventory.stock.add", map[string]any{"id": other, "productId": product, "locationId": e.loc["fridge"], "amountKind": "exact", "quantity": "1", "unit": "lb", "source": "cook", "on": today()})
	var times int
	_ = e.pool.QueryRow(t.Context(), `SELECT times_bought FROM product_stat WHERE product_id = $1 AND store_id IS NULL`, product).Scan(&times)
	if times != 1 {
		t.Fatalf("a cook add changed the purchase count: %d", times)
	}
}

func TestAddValidationAndGone(t *testing.T) {
	e := newEnv(t)
	_, product := e.addMandarins(t, "fridge", nil)
	base := func() map[string]any {
		return map[string]any{"id": uuid.New(), "productId": product, "locationId": e.loc["fridge"], "amountKind": "exact", "quantity": "1", "unit": "lb", "source": "manual"}
	}
	for name, mut := range map[string]func(map[string]any){
		"zero quantity":    func(p map[string]any) { p["quantity"] = "0" },
		"unknown unit":     func(p map[string]any) { p["unit"] = "furlong" },
		"exact without it": func(p map[string]any) { delete(p, "unit") },
		"mixed amounts":    func(p map[string]any) { p["portions"] = 2 },
		"bad level":        func(p map[string]any) { p["amountKind"] = "level"; p["level"] = "overflowing" },
		"both products":    func(p map[string]any) { p["newProduct"] = map[string]any{"id": uuid.New(), "name": "X"} },
		"bad source":       func(p map[string]any) { p["source"] = "magic" },
		"kind w/o date":    func(p map[string]any) { p["expiryKind"] = "use_by" },
		"date format":      func(p map[string]any) { p["expiresOn"] = "tomorrow" },
		"far-off on":       func(p map[string]any) { p["on"] = "2099-01-01" },
	} {
		p := base()
		mut(p)
		if r := e.do(t, cmd("inventory.stock.add", p)); r.Status != commands.StatusRejected || r.Problem.Code != "invalid" {
			t.Errorf("%s: %+v %v", name, r, r.Problem)
		}
	}
	e.ok(t, "catalog.product.delete", map[string]any{"id": product})
	e.rejected(t, "inventory.stock.add", base(), "gone")
	loc := base()
	loc["locationId"] = uuid.New()
	e.rejected(t, "inventory.stock.add", loc, "gone")
}

func TestConsumeDeltasCommuteAndFinish(t *testing.T) {
	e := newEnv(t)
	item, _ := e.addMandarins(t, "fridge", map[string]any{"quantity": "5"})
	// Two devices consume while offline; either order lands on the same amount.
	e.ok(t, "inventory.stock.consume", map[string]any{"id": item, "quantity": "1.5", "unit": "lb"})
	e.ok(t, "inventory.stock.consume", map[string]any{"id": item, "quantity": "2"})
	if s := e.stock(t, item); s.Deleted || *s.Qty != "1.5" {
		t.Fatalf("after 5 - 1.5 - 2: %+v", s)
	}
	e.rejected(t, "inventory.stock.consume", map[string]any{"id": item, "quantity": "1", "unit": "kg"}, "invalid")
	// Eating more than is left finishes the item and records what was actually there.
	e.ok(t, "inventory.stock.consume", map[string]any{"id": item, "quantity": "9"})
	if s := e.stock(t, item); !s.Deleted {
		t.Fatalf("finished item: %+v", s)
	}
	if got := e.events(t, item); len(got) != 4 || got[3] != "consume:-1.5" {
		t.Fatalf("events = %v", got)
	}
	e.rejected(t, "inventory.stock.consume", map[string]any{"id": item, "quantity": "1"}, "gone")
}

func TestPortionsAndLevelItems(t *testing.T) {
	e := newEnv(t)
	item, product := e.addMandarins(t, "freezer", map[string]any{"amountKind": "portions", "portions": 4, "unit": nil, "quantity": nil})
	e.ok(t, "inventory.stock.consume", map[string]any{"id": item, "portions": 3})
	if s := e.stock(t, item); *s.Portions != 1 {
		t.Fatalf("portions = %d", *s.Portions)
	}
	e.rejected(t, "inventory.stock.consume", map[string]any{"id": item, "quantity": "1"}, "invalid")
	e.ok(t, "inventory.stock.adjust", map[string]any{"id": item, "portions": 6})
	if got := e.events(t, item); got[len(got)-1] != "adjust:5" {
		t.Fatalf("events = %v", got)
	}

	lvl := uuid.New()
	e.ok(t, "inventory.stock.add", map[string]any{"id": lvl, "productId": product, "locationId": e.loc["pantry"], "amountKind": "level", "level": "full", "source": "manual"})
	e.ok(t, "inventory.stock.set_level", map[string]any{"id": lvl, "level": "half"})
	e.rejected(t, "inventory.stock.set_level", map[string]any{"id": item, "level": "half"}, "invalid")
	e.rejected(t, "inventory.stock.consume", map[string]any{"id": lvl, "quantity": "1"}, "invalid")
	if s := e.stock(t, lvl); *s.Level != "half" {
		t.Fatalf("level = %s", *s.Level)
	}
	e.ok(t, "inventory.stock.mark_low", map[string]any{"id": lvl})
	if !e.stock(t, lvl).Low {
		t.Fatal("mark_low did not mark")
	}
	e.ok(t, "inventory.stock.unmark_low", map[string]any{"id": lvl})
	if e.stock(t, lvl).Low {
		t.Fatal("unmark_low did not clear")
	}
}

func TestAdjustToZeroFinishes(t *testing.T) {
	e := newEnv(t)
	item, _ := e.addMandarins(t, "fridge", nil)
	e.ok(t, "inventory.stock.adjust", map[string]any{"id": item, "quantity": "0.5"})
	if s := e.stock(t, item); *s.Qty != "0.5" {
		t.Fatalf("qty %s", *s.Qty)
	}
	e.ok(t, "inventory.stock.adjust", map[string]any{"id": item, "quantity": "0"})
	if !e.stock(t, item).Deleted {
		t.Fatal("adjust to zero should finish the item")
	}
}

func TestFreezeAndThawRestartTheClock(t *testing.T) {
	e := newEnv(t)
	chicken := uuid.New()
	item := uuid.New()
	e.ok(t, "inventory.stock.add", map[string]any{
		"id": item, "newProduct": map[string]any{"id": chicken, "name": "Chicken thighs", "ingredientId": e.ingredient(t, "chicken-thigh")},
		"locationId": e.loc["fridge"], "amountKind": "exact", "quantity": "1", "unit": "kg", "source": "manual", "on": today(),
		"expiresOn": plus(1), "expiryKind": "use_by",
	})
	e.ok(t, "inventory.stock.move", map[string]any{"id": item, "locationId": e.loc["freezer"], "on": today()})
	s := e.stock(t, item)
	if s.Loc != e.loc["freezer"] || *s.FrozenOn != today() || *s.Expires != plus(270) || *s.ExpiryKind != "estimated" {
		t.Fatalf("after move to freezer: %+v expires=%v", s, *s.Expires)
	}
	e.ok(t, "inventory.stock.thaw", map[string]any{"id": item, "locationId": e.loc["fridge"], "on": today()})
	s = e.stock(t, item)
	if s.Loc != e.loc["fridge"] || *s.ThawedOn != today() || *s.Expires != plus(2) {
		t.Fatalf("after thaw: %+v expires=%v", s, *s.Expires)
	}
	e.rejected(t, "inventory.stock.freeze", map[string]any{"id": item, "locationId": e.loc["pantry"]}, "invalid")
	e.rejected(t, "inventory.stock.thaw", map[string]any{"id": item, "locationId": e.loc["freezer"]}, "invalid")
	e.ok(t, "inventory.stock.freeze", map[string]any{"id": item, "locationId": e.loc["freezer"], "on": today()})
	if got := e.events(t, item); got[len(got)-1] != "freeze:" || len(got) != 4 {
		t.Fatalf("events = %v", got)
	}
	// Moving between fridge and pantry keeps a date the user gave.
	other, _ := e.addMandarins(t, "fridge", map[string]any{"expiresOn": plus(3), "newProduct": map[string]any{"id": uuid.New(), "name": "Clementines", "ingredientId": e.ingredient(t, "mandarin")}})
	e.ok(t, "inventory.stock.move", map[string]any{"id": other, "locationId": e.loc["pantry"], "on": today()})
	if s := e.stock(t, other); *s.Expires != plus(3) || *s.ExpiryKind != "best_before" {
		t.Fatalf("given date changed on a fridge → pantry move: %v %v", *s.Expires, *s.ExpiryKind)
	}
	e.ok(t, "inventory.stock.move", map[string]any{"id": other, "locationId": e.loc["pantry"]}) // same place: nothing changes
	if s := e.stock(t, other); *s.Expires != plus(3) {
		t.Fatalf("a move to the same location changed the date: %v", *s.Expires)
	}
}

func TestOpenOnlyShortens(t *testing.T) {
	e := newEnv(t)
	milk := uuid.New()
	item := uuid.New()
	e.ok(t, "inventory.stock.add", map[string]any{
		"id": item, "newProduct": map[string]any{"id": milk, "name": "Milk 2%", "ingredientId": e.ingredient(t, "milk")},
		"locationId": e.loc["fridge"], "amountKind": "exact", "quantity": "2", "unit": "l", "source": "manual", "on": today(),
	})
	if s := e.stock(t, item); *s.Expires != plus(7) {
		t.Fatalf("milk expiry %s", *s.Expires)
	}
	e.ok(t, "inventory.stock.open", map[string]any{"id": item, "on": today()})
	s := e.stock(t, item)
	if *s.OpenedOn != today() || *s.Expires != plus(5) || *s.ExpiryKind != "estimated" { // dairy default: 5 days once opened
		t.Fatalf("after open: %+v expires=%v", s, *s.Expires)
	}
	// Opening twice changes nothing but still records the event the client logged.
	n := len(e.events(t, item))
	e.ok(t, "inventory.stock.open", map[string]any{"id": item, "on": today(), "eventId": uuid.New()})
	if s2 := e.stock(t, item); *s2.Expires != plus(5) || len(e.events(t, item)) != n+1 {
		t.Fatalf("second open: %v, %d events", *s2.Expires, len(e.events(t, item)))
	}
	// A use-by sooner than the opened estimate stays.
	soon := uuid.New()
	e.ok(t, "inventory.stock.add", map[string]any{
		"id": soon, "productId": milk, "locationId": e.loc["fridge"], "amountKind": "exact", "quantity": "1", "unit": "l",
		"source": "manual", "on": today(), "expiresOn": plus(2), "expiryKind": "use_by",
	})
	e.ok(t, "inventory.stock.open", map[string]any{"id": soon, "on": today()})
	if s := e.stock(t, soon); *s.Expires != plus(2) || *s.ExpiryKind != "use_by" {
		t.Fatalf("open overrode a nearer use-by: %v %v", *s.Expires, *s.ExpiryKind)
	}
}

func TestHouseholdRuleChangesEstimates(t *testing.T) {
	e := newEnv(t)
	rules := inventory.NewRuleService(e.pool)
	mandarin := e.ingredient(t, "mandarin")
	if err := rules.Put(e.ctx, "ingredient", mandarin, inventory.ShelfLife{"fridge": 10}); err != nil {
		t.Fatal(err)
	}
	item, _ := e.addMandarins(t, "fridge", nil)
	if s := e.stock(t, item); *s.Expires != plus(10) {
		t.Fatalf("household rule ignored: %s", *s.Expires)
	}
	if err := rules.Delete(e.ctx, "ingredient", mandarin); err != nil {
		t.Fatal(err)
	}
	other, _ := e.addMandarins(t, "fridge", map[string]any{"newProduct": map[string]any{"id": uuid.New(), "name": "Mandarins big", "ingredientId": mandarin}})
	if s := e.stock(t, other); *s.Expires != plus(21) {
		t.Fatalf("reset should return to the seed: %s", *s.Expires)
	}
}

func TestSplitExactAndPortions(t *testing.T) {
	e := newEnv(t)
	beef := uuid.New()
	item := uuid.New()
	e.ok(t, "inventory.stock.add", map[string]any{
		"id": item, "newProduct": map[string]any{"id": beef, "name": "Ground beef", "ingredientId": e.ingredient(t, "ground-beef")},
		"locationId": e.loc["fridge"], "amountKind": "exact", "quantity": "1.5", "unit": "kg", "source": "manual", "on": today(),
	})
	parts := []map[string]any{}
	ids := []uuid.UUID{}
	for i := 0; i < 3; i++ {
		id := uuid.New()
		ids = append(ids, id)
		parts = append(parts, map[string]any{"id": id, "eventId": uuid.New(), "quantity": "0.5"})
	}
	e.rejected(t, "inventory.stock.split", map[string]any{"id": item, "parts": append(parts, map[string]any{"id": uuid.New(), "eventId": uuid.New(), "quantity": "0.1"}), "locationId": e.loc["freezer"]}, "invalid")
	e.ok(t, "inventory.stock.split", map[string]any{"id": item, "parts": parts, "locationId": e.loc["freezer"], "on": today()})
	if !e.stock(t, item).Deleted {
		t.Fatal("parent should be finished when every gram was split off")
	}
	for _, id := range ids {
		s := e.stock(t, id)
		if *s.Qty != "0.5" || s.Loc != e.loc["freezer"] || s.Parent == nil || *s.Parent != item || *s.FrozenOn != today() || *s.Expires != plus(120) {
			t.Fatalf("part: %+v expires=%v", s, *s.Expires)
		}
	}
	if got := e.events(t, item); got[len(got)-1] != "split:-1.5" {
		t.Fatalf("parent events: %v", got)
	}

	// Splitting part of a portions item leaves the rest behind.
	pk, _ := e.addMandarins(t, "fridge", map[string]any{"amountKind": "portions", "portions": 6, "unit": nil, "quantity": nil, "newProduct": map[string]any{"id": uuid.New(), "name": "Soup"}})
	child := uuid.New()
	e.ok(t, "inventory.stock.split", map[string]any{"id": pk, "parts": []map[string]any{{"id": child, "eventId": uuid.New(), "portions": 2}}})
	if s := e.stock(t, pk); s.Deleted || *s.Portions != 4 {
		t.Fatalf("parent after split: %+v", s)
	}
	if s := e.stock(t, child); *s.Portions != 2 || s.Loc != e.loc["fridge"] {
		t.Fatalf("child: %+v", s)
	}
}

func TestDiscardAndUpdate(t *testing.T) {
	e := newEnv(t)
	item, _ := e.addMandarins(t, "fridge", nil)
	e.ok(t, "inventory.stock.update", map[string]any{"id": item, "expiresOn": plus(4), "note": "half eaten"})
	s := e.stock(t, item)
	if *s.Expires != plus(4) || *s.ExpiryKind != "best_before" {
		t.Fatalf("typed date should be the user's, not an estimate: %v %v", *s.Expires, *s.ExpiryKind)
	}
	e.ok(t, "inventory.stock.update", map[string]any{"id": item, "expiryKind": "use_by"})
	e.ok(t, "inventory.stock.update", map[string]any{"id": item, "amount": map[string]any{"amountKind": "level", "level": "half"}})
	s = e.stock(t, item)
	if *s.ExpiryKind != "use_by" || *s.Level != "half" || s.Qty != nil {
		t.Fatalf("after update: %+v", s)
	}
	e.rejected(t, "inventory.stock.update", map[string]any{"id": item, "amount": map[string]any{"amountKind": "exact", "quantity": "3"}}, "invalid")
	e.ok(t, "inventory.stock.update", map[string]any{"id": item, "expiresOn": nil})
	if s := e.stock(t, item); s.Expires != nil || s.ExpiryKind != nil {
		t.Fatalf("expiry not cleared: %+v", s)
	}
	e.rejected(t, "inventory.stock.discard", map[string]any{"id": item, "reason": "boredom"}, "invalid")
	e.ok(t, "inventory.stock.discard", map[string]any{"id": item, "reason": "spoiled"})
	if !e.stock(t, item).Deleted {
		t.Fatal("discard should finish the item")
	}
	e.rejected(t, "inventory.stock.update", map[string]any{"id": item, "note": "x"}, "gone")
	var reason, actor, cmdID any
	if err := e.pool.QueryRow(t.Context(), `SELECT reason, actor_id, command_id FROM stock_event WHERE stock_item_id = $1 AND kind = 'discard'`, item).Scan(&reason, &actor, &cmdID); err != nil {
		t.Fatal(err)
	}
	if reason != "spoiled" || actor == nil || cmdID == nil {
		t.Fatalf("discard event: %v %v %v", reason, actor, cmdID)
	}
}

func TestStockIsHouseholdScoped(t *testing.T) {
	e := newEnv(t)
	item, _ := e.addMandarins(t, "fridge", nil)
	var other uuid.UUID
	_ = e.pool.QueryRow(t.Context(), `INSERT INTO household (id, name) VALUES (gen_random_uuid(), 'Cabin') RETURNING id`).Scan(&other)
	ctx := identity.WithPrincipal(t.Context(), &identity.Principal{UserID: uuid.New(), Status: identity.StatusActive, HouseholdID: other})
	rs, err := e.svc.Apply(ctx, other, []commands.Command{cmd("inventory.stock.consume", map[string]any{"id": item, "quantity": "1"})})
	if err != nil || rs[0].Problem == nil || rs[0].Problem.Code != "gone" {
		t.Fatalf("another household's item must look gone: %+v %v", rs, err)
	}
}

func TestReplayedCommandWritesOnce(t *testing.T) {
	e := newEnv(t)
	item, _ := e.addMandarins(t, "fridge", map[string]any{"quantity": "5"})
	c := cmd("inventory.stock.consume", map[string]any{"id": item, "quantity": "1"})
	if r := e.do(t, c); r.Status != commands.StatusApplied {
		t.Fatalf("%+v", r)
	}
	if r := e.do(t, c); r.Status != commands.StatusDuplicate {
		t.Fatalf("%+v", r)
	}
	if s := e.stock(t, item); *s.Qty != "4" {
		t.Fatalf("a replayed consume changed the amount twice: %s", *s.Qty)
	}
}

func TestConcurrentConsumesAreSerialized(t *testing.T) {
	e := newEnv(t)
	item, _ := e.addMandarins(t, "fridge", map[string]any{"quantity": "10"})
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rs, err := e.svc.Apply(e.ctx, e.hh, []commands.Command{cmd("inventory.stock.consume", map[string]any{"id": item, "quantity": "1"})})
			if err != nil || rs[0].Status != commands.StatusApplied {
				t.Errorf("%v %+v", err, rs)
			}
		}()
	}
	wg.Wait()
	if s := e.stock(t, item); *s.Qty != "2" {
		t.Fatalf("8 concurrent consumes of 1 from 10 left %s", *s.Qty)
	}
}

func TestFoodRuleService(t *testing.T) {
	e := newEnv(t)
	rules := inventory.NewRuleService(e.pool)
	var cat uuid.UUID
	_ = e.pool.QueryRow(t.Context(), `SELECT id FROM category WHERE slug = 'dairy-eggs'`).Scan(&cat)

	list, err := rules.List(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defaults := 0
	for _, r := range list {
		if r.HouseholdID == nil {
			defaults++
		}
		if r.Subject == "category" && r.SubjectID == cat && r.ShelfLife["opened_fridge"] == 0 {
			t.Fatal("the dairy default should carry an opened rule")
		}
	}
	if defaults < 15 {
		t.Fatalf("defaults seeded: %d", defaults)
	}

	for _, bad := range []inventory.ShelfLife{{}, {"fridge": 0}, {"fridge": 99999}, {"shed": 3}} {
		if err := rules.Put(e.ctx, "category", cat, bad); err == nil {
			t.Fatalf("accepted %v", bad)
		}
	}
	if err := rules.Put(e.ctx, "category", uuid.New(), inventory.ShelfLife{"fridge": 3}); err == nil {
		t.Fatal("accepted a subject that does not exist")
	}
	if err := rules.Put(e.ctx, "category", cat, inventory.ShelfLife{"fridge": 3}); err != nil {
		t.Fatal(err)
	}
	if err := rules.Put(e.ctx, "category", cat, inventory.ShelfLife{"fridge": 4, "opened_fridge": 2}); err != nil { // upsert
		t.Fatal(err)
	}
	list, _ = rules.List(e.ctx)
	mine := 0
	for _, r := range list {
		if r.HouseholdID != nil {
			mine++
			if r.ShelfLife["fridge"] != 4 {
				t.Fatalf("override = %v", r.ShelfLife)
			}
		}
	}
	if mine != 1 {
		t.Fatalf("overrides = %d", mine)
	}
	defs, overs, err := rules.Admin(e.ctx)
	if err != nil || len(overs) != 1 || overs[0].HouseholdName != "Home" || len(defs) < 15 {
		t.Fatalf("admin view: %d defaults, %v overrides, %v", len(defs), overs, err)
	}
	if err := rules.Delete(e.ctx, "category", cat); err != nil {
		t.Fatal(err)
	}
	if err := rules.Delete(e.ctx, "category", cat); err != nil {
		t.Fatalf("deleting twice should be fine: %v", err)
	}
	var audits int
	_ = e.pool.QueryRow(t.Context(), `SELECT count(*) FROM audit_event WHERE action LIKE 'inventory.food_rule.%'`).Scan(&audits)
	if audits != 3 {
		t.Fatalf("audit events = %d, want 3 (set, set, reset)", audits)
	}
}

func TestRestockIsADeltaThatCommutesWithConsume(t *testing.T) {
	e := newEnv(t)
	item, _ := e.addMandarins(t, "fridge", map[string]any{"quantity": "12", "unit": "ea"})
	// Offline + (restock 1) and a concurrent consume of 6 land in either order.
	e.ok(t, "inventory.stock.consume", map[string]any{"id": item, "quantity": "6"})
	e.ok(t, "inventory.stock.restock", map[string]any{"id": item, "quantity": "1"})
	if s := e.stock(t, item); *s.Qty != "7" {
		t.Fatalf("12 - 6 + 1 = %s", *s.Qty)
	}
	e.rejected(t, "inventory.stock.restock", map[string]any{"id": item, "quantity": "0"}, "invalid")
	e.rejected(t, "inventory.stock.restock", map[string]any{"id": item, "portions": 1}, "invalid")
}

func TestMoveNeverLengthensExpiry(t *testing.T) {
	e := newEnv(t)
	item, _ := e.addMandarins(t, "fridge", map[string]any{"quantity": "3"})
	e.ok(t, "inventory.stock.open", map[string]any{"id": item, "on": today()})
	before := *e.stock(t, item).Expires
	e.ok(t, "inventory.stock.move", map[string]any{"id": item, "locationId": e.loc["pantry"], "on": today()})
	if s := e.stock(t, item); *s.Expires != before {
		t.Fatalf("move changed expiry %s -> %s", before, *s.Expires)
	}
	// Moving to where it already is still records the event.
	n := len(e.events(t, item))
	e.ok(t, "inventory.stock.move", map[string]any{"id": item, "locationId": e.loc["pantry"], "eventId": uuid.New()})
	if len(e.events(t, item)) != n+1 {
		t.Fatal("no-op move wrote no event")
	}
}

func TestOpenInFreezerKeepsDate(t *testing.T) {
	e := newEnv(t)
	item, _ := e.addMandarins(t, "freezer", nil)
	before := *e.stock(t, item).Expires
	e.ok(t, "inventory.stock.open", map[string]any{"id": item, "on": today()})
	if s := e.stock(t, item); *s.Expires != before || s.OpenedOn == nil {
		t.Fatalf("open in freezer: %+v", s)
	}
}

func TestHouseholdCategoryRuleBeatsSeedIngredient(t *testing.T) {
	e := newEnv(t)
	var cat uuid.UUID
	if err := e.pool.QueryRow(e.ctx, `SELECT category_id FROM ingredient WHERE slug = 'mandarin'`).Scan(&cat); err != nil {
		t.Fatal(err)
	}
	if err := inventory.NewRuleService(e.pool).Put(e.ctx, "category", cat, inventory.ShelfLife{"fridge": 4}); err != nil {
		t.Fatal(err)
	}
	item, _ := e.addMandarins(t, "fridge", nil)
	if s := e.stock(t, item); *s.Expires != plus(4) {
		t.Fatalf("category rule ignored: %s", *s.Expires)
	}
}
