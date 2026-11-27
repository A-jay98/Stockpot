package catalog_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/a-jay98/stockpot/internal/catalog"
	"github.com/a-jay98/stockpot/internal/catalog/resolve"
	"github.com/a-jay98/stockpot/internal/commands"
	"github.com/a-jay98/stockpot/internal/identity"
	"github.com/a-jay98/stockpot/internal/syncdb"
	"github.com/a-jay98/stockpot/internal/testdb"
)

func TestMain(m *testing.M) { os.Exit(testdb.Main(m)) }

type env struct {
	pool *pgxpool.Pool
	svc  *commands.Service
	sync *syncdb.Store
	hh   uuid.UUID
	ctx  context.Context
}

func newEnv(t *testing.T) *env {
	t.Helper()
	pool := testdb.New(t)
	if err := catalog.Seed(t.Context(), pool); err != nil {
		t.Fatal(err)
	}
	reg := commands.NewRegistry()
	catalog.Register(reg, resolve.Resolver{})
	st, err := syncdb.New(t.Context(), pool)
	if err != nil {
		t.Fatal(err)
	}
	e := &env{pool: pool, svc: commands.NewService(pool, reg), sync: st}
	e.hh = e.household(t, "Home")
	e.ctx = principalCtx(t.Context(), e.hh)
	return e
}

func (e *env) household(t *testing.T, name string) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := e.pool.QueryRow(t.Context(), `INSERT INTO household (id, name) VALUES (gen_random_uuid(), $1) RETURNING id`, name).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func principalCtx(ctx context.Context, hh uuid.UUID) context.Context {
	return identity.WithPrincipal(ctx, &identity.Principal{UserID: uuid.New(), Status: identity.StatusActive, HouseholdID: hh})
}

func cmd(typ string, payload map[string]any) commands.Command {
	b, _ := json.Marshal(payload)
	return commands.Command{ID: uuid.New(), Type: typ, Payload: b, ClientTime: time.Now()}
}

func (e *env) apply(t *testing.T, cs ...commands.Command) []commands.Result {
	t.Helper()
	rs, err := e.svc.Apply(e.ctx, e.hh, cs)
	if err != nil {
		t.Fatal(err)
	}
	return rs
}

func (e *env) one(t *testing.T, c commands.Command) commands.Result { return e.apply(t, c)[0] }

func TestHouseholdDefaultsAndSeed(t *testing.T) {
	e := newEnv(t)
	var locs, lists int
	_ = e.pool.QueryRow(t.Context(), `SELECT count(*) FROM location WHERE household_id = $1`, e.hh).Scan(&locs)
	_ = e.pool.QueryRow(t.Context(), `SELECT count(*) FROM shopping_list WHERE household_id = $1 AND name = 'Groceries'`, e.hh).Scan(&lists)
	if locs != 3 || lists != 1 {
		t.Fatalf("defaults: %d locations, %d lists", locs, lists)
	}
	var n, orphans int
	_ = e.pool.QueryRow(t.Context(), `SELECT count(*) FROM ingredient WHERE household_id IS NULL`).Scan(&n)
	_ = e.pool.QueryRow(t.Context(), `SELECT count(*) FROM ingredient WHERE category_id IS NULL`).Scan(&orphans)
	if n < 400 || orphans != 0 {
		t.Fatalf("seed: %d ingredients, %d without category", n, orphans)
	}
	var parent string
	if err := e.pool.QueryRow(t.Context(), `SELECT p.slug FROM ingredient c JOIN ingredient p ON p.id = c.parent_id WHERE c.slug = 'clementine'`).Scan(&parent); err != nil || parent != "mandarin" {
		t.Fatalf("clementine parent = %q, %v", parent, err)
	}
}

func TestSeedIsIdempotent(t *testing.T) {
	e := newEnv(t)
	var before, after int64
	_ = e.pool.QueryRow(t.Context(), `SELECT max(rev) FROM ingredient`).Scan(&before)
	if _, err := e.pool.Exec(t.Context(), `INSERT INTO ingredient (household_id, slug, name, source) VALUES ($1, 'dragon-stew', 'Dragon stew', 'household')`, e.hh); err != nil {
		t.Fatal(err)
	}
	var mine int64
	_ = e.pool.QueryRow(t.Context(), `SELECT max(rev) FROM ingredient`).Scan(&mine)
	if err := catalog.Seed(t.Context(), e.pool); err != nil {
		t.Fatal(err)
	}
	_ = e.pool.QueryRow(t.Context(), `SELECT max(rev) FROM ingredient`).Scan(&after)
	if after != mine || before >= mine {
		t.Fatalf("reseed changed rows: rev %d -> %d (household row at %d)", before, after, mine)
	}
	var kept int
	_ = e.pool.QueryRow(t.Context(), `SELECT count(*) FROM ingredient WHERE slug = 'dragon-stew' AND deleted_at IS NULL`).Scan(&kept)
	if kept != 1 {
		t.Fatal("seed touched a household ingredient")
	}
}

func TestCommandIdempotencyAndRejection(t *testing.T) {
	e := newEnv(t)
	pid := uuid.New()
	create := cmd("catalog.product.create", map[string]any{"id": pid, "name": "Manderins", "brand": "Kirkland"})
	if r := e.one(t, create); r.Status != commands.StatusApplied {
		t.Fatalf("create: %+v", r)
	}
	if r := e.one(t, create); r.Status != commands.StatusDuplicate {
		t.Fatalf("replay: %+v", r)
	}
	var n int
	_ = e.pool.QueryRow(t.Context(), `SELECT count(*) FROM audit_event WHERE action = 'catalog.product.create'`).Scan(&n)
	if n != 1 {
		t.Fatalf("audit rows = %d, want 1", n)
	}

	del := cmd("catalog.product.delete", map[string]any{"id": pid})
	if r := e.one(t, del); r.Status != commands.StatusApplied {
		t.Fatalf("delete: %+v", r)
	}
	upd := cmd("catalog.product.update", map[string]any{"id": pid, "note": "late edit"})
	r := e.one(t, upd)
	if r.Status != commands.StatusRejected || r.Problem.Code != commands.CodeGone {
		t.Fatalf("update after delete: %+v", r)
	}
	if again := e.one(t, upd); again.Status != commands.StatusRejected || again.Problem.Code != commands.CodeGone {
		t.Fatalf("retry of rejected command: %+v", again)
	}
	// A rejected command leaves no trace beyond its record.
	_ = e.pool.QueryRow(t.Context(), `SELECT count(*) FROM product WHERE id = $1 AND note IS NOT NULL`, pid).Scan(&n)
	if n != 0 {
		t.Fatal("rejected update was applied")
	}
}

func TestCommandValidation(t *testing.T) {
	e := newEnv(t)
	for name, c := range map[string]commands.Command{
		"unknown type":  cmd("catalog.nope", map[string]any{"id": uuid.New()}),
		"unknown field": cmd("catalog.store.create", map[string]any{"id": uuid.New(), "name": "X", "bogus": 1}),
		"bad unit":      cmd("catalog.product.create", map[string]any{"id": uuid.New(), "name": "X", "defaultUnit": "furlong"}),
		"other hh store": cmd("catalog.product.create", map[string]any{
			"id": uuid.New(), "name": "Y",
			"defaultStoreId": e.foreignStore(t),
		}),
	} {
		if r := e.one(t, c); r.Status != commands.StatusRejected || r.Problem.Code != commands.CodeInvalid {
			t.Errorf("%s: %+v", name, r)
		}
	}
	a := cmd("catalog.product.create", map[string]any{"id": uuid.New(), "name": "Milk"})
	b := cmd("catalog.product.create", map[string]any{"id": uuid.New(), "name": "milk"})
	if r := e.apply(t, a, b); r[0].Status != commands.StatusApplied || r[1].Status != commands.StatusRejected {
		t.Fatalf("duplicate name: %+v", r)
	}
}

func (e *env) foreignStore(t *testing.T) uuid.UUID {
	other := e.household(t, "Other")
	var id uuid.UUID
	if err := e.pool.QueryRow(t.Context(), `INSERT INTO store (id, household_id, name) VALUES (gen_random_uuid(), $1, 'Theirs') RETURNING id`, other).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestResolverAndUserPick(t *testing.T) {
	e := newEnv(t)
	pid := uuid.New()
	e.one(t, cmd("catalog.product.create", map[string]any{"id": pid, "name": "Avocados"}))
	var slug, src, state string
	q := `SELECT i.slug, p.match_source, p.enrich_state FROM product p JOIN ingredient i ON i.id = p.ingredient_id WHERE p.id = $1`
	if err := e.pool.QueryRow(t.Context(), q, pid).Scan(&slug, &src, &state); err != nil || slug != "avocado" || src != "rule" || state != "done" {
		t.Fatalf("resolved to %q/%q/%q: %v", slug, src, state, err)
	}
	e.one(t, cmd("catalog.product.create", map[string]any{"id": uuid.New(), "name": "Zorbo fruit"}))
	var pending int
	_ = e.pool.QueryRow(t.Context(), `SELECT count(*) FROM product WHERE name = 'Zorbo fruit' AND enrich_state = 'pending'`).Scan(&pending)
	if pending != 1 {
		t.Fatal("unmatched product should stay pending")
	}

	var tangerine uuid.UUID
	_ = e.pool.QueryRow(t.Context(), `SELECT id FROM ingredient WHERE slug = 'mandarin'`).Scan(&tangerine)
	if r := e.one(t, cmd("catalog.product.set_ingredient", map[string]any{"id": pid, "ingredientId": tangerine})); r.Status != commands.StatusApplied {
		t.Fatalf("set_ingredient: %+v", r)
	}
	e.one(t, cmd("catalog.product.update", map[string]any{"id": pid, "name": "Avocado bag"}))
	if err := e.pool.QueryRow(t.Context(), q, pid).Scan(&slug, &src, &state); err != nil || slug != "mandarin" || src != "user" {
		t.Fatalf("user pick overwritten: %q/%q: %v", slug, src, err)
	}
}

func TestSyncPagingTombstonesAndGlobalCursor(t *testing.T) {
	e := newEnv(t)
	var cs []commands.Command
	var ids []uuid.UUID
	for _, n := range []string{"Costco", "No Frills", "Loblaws", "Walmart", "Farm Boy"} {
		id := uuid.New()
		ids = append(ids, id)
		cs = append(cs, cmd("catalog.store.create", map[string]any{"id": id, "name": n}))
	}
	e.apply(t, cs...)

	p, err := e.sync.Pull(t.Context(), &e.hh, 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Rows["store"]) != 5 || len(p.Rows["location"]) != 3 || p.More {
		t.Fatalf("first pull: %d stores, %d locations, more=%v", len(p.Rows["store"]), len(p.Rows["location"]), p.More)
	}
	if len(p.Rows["ingredient"]) != 0 {
		t.Fatal("household pull must not carry global rows")
	}

	// Page through with a tiny limit: every row exactly once, in rev order.
	seen := map[string]bool{}
	var cursor int64
	var last int64
	for pages := 0; ; pages++ {
		pg, err := e.sync.Pull(t.Context(), &e.hh, cursor, 3)
		if err != nil {
			t.Fatal(err)
		}
		for table, rows := range pg.Rows {
			for _, r := range rows {
				var row struct {
					ID  string `json:"id"`
					Rev int64  `json:"rev"`
				}
				_ = json.Unmarshal(r, &row)
				if seen[table+row.ID] {
					t.Fatalf("row %s/%s twice", table, row.ID)
				}
				seen[table+row.ID] = true
				if row.Rev <= cursor {
					t.Fatalf("row at or before the cursor")
				}
			}
		}
		if pg.Next < last {
			t.Fatal("cursor went backwards")
		}
		cursor, last = pg.Next, pg.Next
		if !pg.More {
			break
		}
		if pages > 20 {
			t.Fatal("paging does not end")
		}
	}
	if len(seen) != 5+3+1 { // stores, locations, Groceries
		t.Fatalf("paged %d rows", len(seen))
	}

	// Delete shows up as a tombstone, with no row.
	e.one(t, cmd("catalog.store.delete", map[string]any{"id": ids[0]}))
	p, _ = e.sync.Pull(t.Context(), &e.hh, cursor, 1000)
	if len(p.Tombstones["store"]) != 1 || p.Tombstones["store"][0] != ids[0] || len(p.Rows["store"]) != 0 {
		t.Fatalf("tombstones: %+v rows %+v", p.Tombstones, p.Rows)
	}

	// Numerics travel as strings.
	e.one(t, cmd("catalog.product.create", map[string]any{"id": uuid.New(), "name": "Rice", "defaultQty": "2.5", "defaultUnit": "kg"}))
	p, _ = e.sync.Pull(t.Context(), &e.hh, p.Next, 1000)
	var prod map[string]any
	_ = json.Unmarshal(p.Rows["product"][0], &prod)
	if prod["default_qty"] != "2.5" {
		t.Fatalf("default_qty = %#v", prod["default_qty"])
	}

	// Global catalog rows have their own cursor and no household filter.
	g, err := e.sync.Pull(t.Context(), nil, 0, 100000)
	if err != nil {
		t.Fatal(err)
	}
	if len(g.Rows["ingredient"]) < 400 || len(g.Rows["category"]) < 10 || len(g.Rows["store"]) != 0 {
		t.Fatalf("global pull: %d ingredients %d categories", len(g.Rows["ingredient"]), len(g.Rows["category"]))
	}
	if again, _ := e.sync.Pull(t.Context(), nil, g.Next, 100); len(again.Rows) != 0 || again.Next != g.Next {
		t.Fatal("global cursor did not hold")
	}

	// Another household sees none of it.
	other := e.household(t, "Other")
	op, _ := e.sync.Pull(t.Context(), &other, 0, 1000)
	if len(op.Rows["store"]) != 0 || len(op.Rows["product"]) != 0 {
		t.Fatal("rows leaked across households")
	}
}

// Two writers on one household must commit in rev order, or a client that
// pulled between their commits would skip the later (lower-rev) row.
func TestConcurrentWritersCommitInRevOrder(t *testing.T) {
	e := newEnv(t)
	ctx := t.Context()

	txA, err := e.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := syncdb.LockHousehold(ctx, txA, e.hh); err != nil {
		t.Fatal(err)
	}
	// A draws its rev now but commits late.
	if _, err := txA.Exec(ctx, `INSERT INTO store (id, household_id, name) VALUES (gen_random_uuid(), $1, 'A')`, e.hh); err != nil {
		t.Fatal(err)
	}

	bLocked := make(chan struct{})
	bDone := make(chan error, 1)
	go func() {
		txB, err := e.pool.Begin(ctx)
		if err != nil {
			bDone <- err
			return
		}
		defer func() { _ = txB.Rollback(ctx) }()
		if err := syncdb.LockHousehold(ctx, txB, e.hh); err != nil { // blocks until A commits
			bDone <- err
			return
		}
		close(bLocked)
		if _, err := txB.Exec(ctx, `INSERT INTO store (id, household_id, name) VALUES (gen_random_uuid(), $1, 'B')`, e.hh); err != nil {
			bDone <- err
			return
		}
		bDone <- txB.Commit(ctx)
	}()

	select {
	case <-bLocked:
		t.Fatal("writer B got the lock while A held it")
	case <-time.After(300 * time.Millisecond):
	}
	if err := txA.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	<-bLocked // B proceeds only after A's commit

	// The reader's snapshot lands between the two commits (or after, if B is quick); either way
	// pulling from the cursor it got must still deliver B.
	mid, err := e.sync.Pull(ctx, &e.hh, 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if err := <-bDone; err != nil {
		t.Fatal(err)
	}
	rest, err := e.sync.Pull(ctx, &e.hh, mid.Next, 1000)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, p := range []syncdb.Page{mid, rest} {
		for _, r := range p.Rows["store"] {
			var s struct{ Name string }
			_ = json.Unmarshal(r, &s)
			names[s.Name] = true
		}
	}
	if !names["A"] || !names["B"] {
		t.Fatalf("a row was missed across the cursor: %v", names)
	}
}

func TestParallelCommandBatchesDoNotLoseRows(t *testing.T) {
	e := newEnv(t)
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 5 {
				if _, err := e.svc.Apply(e.ctx, e.hh, []commands.Command{cmd("catalog.store.create", map[string]any{"id": uuid.New(), "name": "S"})}); err != nil {
					t.Error(err)
				}
			}
		}()
	}
	wg.Wait()
	var cursor int64
	count := 0
	for {
		pg, err := e.sync.Pull(t.Context(), &e.hh, cursor, 7)
		if err != nil {
			t.Fatal(err)
		}
		count += len(pg.Rows["store"])
		cursor = pg.Next
		if !pg.More {
			break
		}
	}
	if count != 40 {
		t.Fatalf("pulled %d stores, want 40", count)
	}
}

func TestIngredientCreateSlugsAndLocationCommands(t *testing.T) {
	e := newEnv(t)
	for range 2 {
		if r := e.one(t, cmd("catalog.ingredient.create", map[string]any{"id": uuid.New(), "name": "Mandarin", "aliases": []string{"Mandarín "}})); r.Status != commands.StatusApplied {
			t.Fatalf("%+v", r)
		}
	}
	var slugs []string
	rows, _ := e.pool.Query(t.Context(), `SELECT slug FROM ingredient WHERE household_id = $1 ORDER BY slug`, e.hh)
	for rows.Next() {
		var s string
		_ = rows.Scan(&s)
		slugs = append(slugs, s)
	}
	if len(slugs) != 2 || slugs[0] != "mandarin-2" || slugs[1] != "mandarin-3" {
		t.Fatalf("slugs %v", slugs) // "mandarin" is taken by the global seed
	}

	lid := uuid.New()
	e.one(t, cmd("catalog.location.create", map[string]any{"id": lid, "name": "Garage freezer", "kind": "freezer"}))
	e.one(t, cmd("catalog.location.update", map[string]any{"id": lid, "name": "Basement freezer"}))
	if r := e.one(t, cmd("catalog.location.update", map[string]any{"id": lid, "kind": "garage"})); r.Status != commands.StatusRejected {
		t.Fatalf("bad kind accepted: %+v", r)
	}
	var name string
	_ = e.pool.QueryRow(t.Context(), `SELECT name FROM location WHERE id = $1`, lid).Scan(&name)
	if name != "Basement freezer" {
		t.Fatal(name)
	}
}

func TestPostgresErrorsAreRejectedNotRetriedForever(t *testing.T) {
	e := newEnv(t)
	// A NUL byte in text is refused by Postgres (22021): a deterministic failure the
	// handlers don't pre-check, which must reject rather than 500 forever.
	r := e.one(t, cmd("catalog.product.create", map[string]any{"id": uuid.New(), "name": "S", "note": "a\u0000b"}))
	if r.Status != commands.StatusRejected || r.Problem.Code != commands.CodeInvalid {
		t.Fatalf("NUL byte: %+v", r)
	}
	if r := e.one(t, cmd("catalog.store.create", map[string]any{"id": uuid.New(), "name": "S", "sort": 3000000000})); r.Status != commands.StatusRejected {
		t.Fatalf("overflow: %+v", r)
	}
	if again := e.one(t, cmd("catalog.store.create", map[string]any{"id": uuid.New(), "name": "T"})); again.Status != commands.StatusApplied {
		t.Fatalf("queue stays usable: %+v", again)
	}
}

func TestCreateWithAnotherHouseholdsIdIsRejected(t *testing.T) {
	e := newEnv(t)
	other := e.household(t, "Other")
	var theirs uuid.UUID
	_ = e.pool.QueryRow(t.Context(), `INSERT INTO store (id, household_id, name) VALUES (gen_random_uuid(), $1, 'Theirs') RETURNING id`, other).Scan(&theirs)
	var theirProduct uuid.UUID
	_ = e.pool.QueryRow(t.Context(), `INSERT INTO product (id, household_id, name) VALUES (gen_random_uuid(), $1, 'Theirs') RETURNING id`, other).Scan(&theirProduct)
	var theirLoc, theirIng uuid.UUID
	_ = e.pool.QueryRow(t.Context(), `SELECT id FROM location WHERE household_id = $1 LIMIT 1`, other).Scan(&theirLoc)
	_ = e.pool.QueryRow(t.Context(), `INSERT INTO ingredient (household_id, slug, name) VALUES ($1, 'theirs', 'Theirs') RETURNING id`, other).Scan(&theirIng)
	for name, c := range map[string]commands.Command{
		"store":      cmd("catalog.store.create", map[string]any{"id": theirs, "name": "Mine"}),
		"product":    cmd("catalog.product.create", map[string]any{"id": theirProduct, "name": "Mine"}),
		"location":   cmd("catalog.location.create", map[string]any{"id": theirLoc, "name": "Mine", "kind": "pantry"}),
		"ingredient": cmd("catalog.ingredient.create", map[string]any{"id": theirIng, "name": "Mine"}),
	} {
		if r := e.one(t, c); r.Status != commands.StatusRejected || r.Problem.Code != commands.CodeInvalid {
			t.Errorf("%s: %+v", name, r)
		}
	}
	// A replay in the owning household is still a quiet success.
	id := uuid.New()
	e.one(t, cmd("catalog.store.create", map[string]any{"id": id, "name": "Mine"}))
	if r := e.one(t, cmd("catalog.store.create", map[string]any{"id": id, "name": "Mine"})); r.Status != commands.StatusApplied {
		t.Fatalf("replay with a new command id: %+v", r)
	}
}

func TestBatchForAnotherHouseholdIsRefused(t *testing.T) {
	e := newEnv(t)
	other := e.household(t, "Other")
	_, err := e.svc.Apply(e.ctx, other, []commands.Command{cmd("catalog.store.create", map[string]any{"id": uuid.New(), "name": "X"})})
	if !errors.Is(err, commands.ErrHouseholdMismatch) {
		t.Fatalf("err = %v", err)
	}
	var n int
	_ = e.pool.QueryRow(t.Context(), `SELECT count(*) FROM store`).Scan(&n)
	if n != 0 {
		t.Fatal("something was applied")
	}
}

func TestIconsAndCategoryIcons(t *testing.T) {
	e := newEnv(t)
	if r := e.one(t, cmd("catalog.product.create", map[string]any{"id": uuid.New(), "name": "Thing", "icon": "not-an-icon"})); r.Status != commands.StatusRejected {
		t.Fatalf("bad icon accepted: %+v", r)
	}
	if r := e.one(t, cmd("catalog.product.create", map[string]any{"id": uuid.New(), "name": "Thing2", "icon": "tangerine"})); r.Status != commands.StatusApplied {
		t.Fatalf("good icon refused: %+v", r)
	}
	rows, err := e.pool.Query(t.Context(), `SELECT slug, icon FROM category WHERE household_id IS NULL`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var slug string
		var icon *string
		_ = rows.Scan(&slug, &icon)
		if icon == nil || *icon != catalog.CategoryIconKey(slug) || !catalog.IconExists(*icon) {
			t.Errorf("category %s icon = %v", slug, icon)
		}
	}
}

func TestDeleteRules(t *testing.T) {
	e := newEnv(t)
	var loc uuid.UUID
	_ = e.pool.QueryRow(t.Context(), `SELECT id FROM location WHERE household_id = $1 AND kind = 'fridge'`, e.hh).Scan(&loc)
	pid, sid := uuid.New(), uuid.New()
	e.apply(t,
		cmd("catalog.store.create", map[string]any{"id": sid, "name": "Costco"}),
		cmd("catalog.product.create", map[string]any{"id": pid, "name": "Rice", "defaultStoreId": sid}),
	)
	if _, err := e.pool.Exec(t.Context(), `INSERT INTO stock_item (id, household_id, product_id, location_id) VALUES (gen_random_uuid(), $1, $2, $3)`, e.hh, pid, loc); err != nil {
		t.Fatal(err)
	}
	if r := e.one(t, cmd("catalog.location.delete", map[string]any{"id": loc})); r.Status != commands.StatusRejected || r.Problem.Code != commands.CodeInvalid {
		t.Fatalf("location with stock deleted: %+v", r)
	}
	if r := e.one(t, cmd("catalog.store.delete", map[string]any{"id": sid})); r.Status != commands.StatusApplied {
		t.Fatalf("store delete: %+v", r)
	}
	var def *uuid.UUID
	_ = e.pool.QueryRow(t.Context(), `SELECT default_store_id FROM product WHERE id = $1`, pid).Scan(&def)
	if def != nil {
		t.Fatal("store reference not detached")
	}
}

func TestResolverMatchesPunctuatedAliases(t *testing.T) {
	e := newEnv(t)
	for name, want := range map[string]string{"2% Milk": "milk", "Half & Half": "cream", "Jell-O": "jelly-dessert", "All-Purpose Flour": "flour"} {
		pid := uuid.New()
		e.one(t, cmd("catalog.product.create", map[string]any{"id": pid, "name": name}))
		var slug string
		if err := e.pool.QueryRow(t.Context(), `SELECT i.slug FROM product p JOIN ingredient i ON i.id = p.ingredient_id WHERE p.id = $1`, pid).Scan(&slug); err != nil || slug != want {
			t.Errorf("%q resolved to %q (%v), want %s", name, slug, err, want)
		}
	}
}
