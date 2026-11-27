package recipes_test

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/a-jay98/stockpot/internal/catalog"
	"github.com/a-jay98/stockpot/internal/catalog/resolve"
	"github.com/a-jay98/stockpot/internal/commands"
	"github.com/a-jay98/stockpot/internal/identity"
	"github.com/a-jay98/stockpot/internal/recipes"
	"github.com/a-jay98/stockpot/internal/recipes/lines"
	"github.com/a-jay98/stockpot/internal/shopping"
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
	shopping.Register(reg, resolve.Resolver{})
	recipes.Register(reg, resolve.Resolver{})
	st, err := syncdb.New(t.Context(), pool)
	if err != nil {
		t.Fatal(err)
	}
	e := &env{pool: pool, svc: commands.NewService(pool, reg), sync: st}
	e.hh = e.household(t)
	e.ctx = e.as(e.hh)
	return e
}

func (e *env) household(t *testing.T) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := e.pool.QueryRow(t.Context(), `INSERT INTO household (id, name) VALUES (gen_random_uuid(), 'Home') RETURNING id`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func (e *env) as(hh uuid.UUID) context.Context {
	return identity.WithPrincipal(context.Background(), &identity.Principal{UserID: uuid.New(), Status: identity.StatusActive, HouseholdID: hh})
}

func (e *env) apply(t *testing.T, id uuid.UUID, typ string, payload any) commands.Result {
	t.Helper()
	b, _ := json.Marshal(payload)
	rs, err := e.svc.Apply(e.ctx, e.hh, []commands.Command{{ID: id, Type: typ, Payload: b, ClientTime: time.Now()}})
	if err != nil {
		t.Fatal(err)
	}
	return rs[0]
}

func (e *env) do(t *testing.T, typ string, payload any) commands.Result {
	t.Helper()
	return e.apply(t, uuid.New(), typ, payload)
}

func (e *env) mustDo(t *testing.T, typ string, payload any) {
	t.Helper()
	if r := e.do(t, typ, payload); r.Status != commands.StatusApplied {
		t.Fatalf("%s: %+v %v", typ, r, r.Problem)
	}
}

func (e *env) create(t *testing.T, title string, ingredients ...string) (recipe, version uuid.UUID) {
	t.Helper()
	recipe, version = uuid.New(), uuid.New()
	e.mustDo(t, "recipe.create", map[string]any{
		"id": recipe, "versionId": version, "title": title, "servings": 4, "totalMinutes": 45,
		"ingredients": ingredients, "steps": []map[string]any{{"text": "Cook it."}, {"text": " "}},
		"tags": []string{"Dinner", "dinner", " curry "},
	})
	return recipe, version
}

type versionRow struct {
	N       int
	Origin  string
	Parent  *uuid.UUID
	Title   string
	Lines   []lines.Line
	Steps   []recipes.Step
	Summary *string
}

func (e *env) version(t *testing.T, id uuid.UUID) versionRow {
	t.Helper()
	var v versionRow
	var ing, steps []byte
	if err := e.pool.QueryRow(t.Context(), `SELECT n, origin, parent_version_id, title, ingredients, steps, change_summary FROM recipe_version WHERE id = $1`, id).
		Scan(&v.N, &v.Origin, &v.Parent, &v.Title, &ing, &steps, &v.Summary); err != nil {
		t.Fatal(err)
	}
	_ = json.Unmarshal(ing, &v.Lines)
	_ = json.Unmarshal(steps, &v.Steps)
	return v
}

func (e *env) current(t *testing.T, recipe uuid.UUID) (uuid.UUID, string) {
	t.Helper()
	var id uuid.UUID
	var title string
	if err := e.pool.QueryRow(t.Context(), `SELECT current_version_id, title FROM recipe WHERE id = $1`, recipe).Scan(&id, &title); err != nil {
		t.Fatal(err)
	}
	return id, title
}

func (e *env) slug(t *testing.T, id *uuid.UUID) string {
	t.Helper()
	if id == nil {
		return ""
	}
	var s string
	_ = e.pool.QueryRow(t.Context(), `SELECT slug FROM ingredient WHERE id = $1`, *id).Scan(&s)
	return s
}

func TestCreateParsesResolvesAndMarksStaples(t *testing.T) {
	e := newEnv(t)
	r, v := e.create(t, "Chicken curry", "800 g boneless chicken thighs", "For the rice:", "1½ cups basmati rice, rinsed", "salt to taste", "1 tsp ground turmeric")
	cur, title := e.current(t, r)
	if cur != v || title != "Chicken curry" {
		t.Fatalf("current = %s %q", cur, title)
	}
	got := e.version(t, v)
	if got.N != 1 || got.Origin != "manual" || got.Parent != nil || len(got.Lines) != 4 || len(got.Steps) != 1 {
		t.Fatalf("version: %+v", got)
	}
	rice := got.Lines[1]
	if rice.Group != "For the rice" || *rice.Qty != "1.5" || *rice.Unit != "cup" || e.slug(t, rice.IngredientID) != "basmati-rice" || rice.LineID == uuid.Nil {
		t.Fatalf("rice: %+v", rice)
	}
	salt := got.Lines[2]
	if !salt.Staple || !salt.Optional || e.slug(t, salt.IngredientID) != "salt" {
		t.Fatalf("salt: %+v", salt)
	}
	if got.Lines[0].Staple || e.slug(t, got.Lines[0].IngredientID) == "" {
		t.Fatalf("chicken: %+v", got.Lines[0])
	}
	var tags []string
	_ = e.pool.QueryRow(t.Context(), `SELECT tags FROM recipe WHERE id = $1`, r).Scan(&tags)
	if len(tags) != 2 || tags[0] != "dinner" || tags[1] != "curry" {
		t.Fatalf("tags = %v", tags)
	}
}

func TestHouseholdStaplesSetting(t *testing.T) {
	e := newEnv(t)
	if _, err := e.pool.Exec(t.Context(), `INSERT INTO setting (key, household_id, value) VALUES ('recipes.assumed_staples', $1, '["garlic"]')`, e.hh); err != nil {
		t.Fatal(err)
	}
	_, v := e.create(t, "Garlic bread", "2 cloves garlic", "salt")
	got := e.version(t, v)
	if !got.Lines[0].Staple || got.Lines[1].Staple {
		t.Fatalf("staples: garlic %v salt %v", got.Lines[0].Staple, got.Lines[1].Staple)
	}
}

func TestCreateIsIdempotentOnID(t *testing.T) {
	e := newEnv(t)
	r, v := e.create(t, "Soup", "1 onion")
	// Same recipe id from another command (a second device replaying): no-op.
	e.mustDo(t, "recipe.create", map[string]any{"id": r, "versionId": uuid.New(), "title": "Soup again", "ingredients": []string{}, "steps": []any{}})
	var n int
	_ = e.pool.QueryRow(t.Context(), `SELECT count(*) FROM recipe_version WHERE recipe_id = $1`, r).Scan(&n)
	if cur, title := e.current(t, r); n != 1 || cur != v || title != "Soup" {
		t.Fatalf("versions %d, current %s %q", n, cur, title)
	}
	// Same command id: duplicate.
	cmd := uuid.New()
	p := map[string]any{"id": uuid.New(), "versionId": uuid.New(), "title": "Stew", "ingredients": []string{}, "steps": []any{}}
	if r := e.apply(t, cmd, "recipe.create", p); r.Status != commands.StatusApplied {
		t.Fatal(r.Problem)
	}
	if r := e.apply(t, cmd, "recipe.create", p); r.Status != commands.StatusDuplicate {
		t.Fatalf("replay: %+v", r)
	}
	// Another household can't take the id.
	other := e.household(t)
	b, _ := json.Marshal(map[string]any{"id": r, "versionId": uuid.New(), "title": "Mine", "ingredients": []string{}, "steps": []any{}})
	rs, err := e.svc.Apply(e.as(other), other, []commands.Command{{ID: uuid.New(), Type: "recipe.create", Payload: b}})
	if err != nil || rs[0].Status != commands.StatusRejected {
		t.Fatalf("cross-household create: %+v %v", rs, err)
	}
}

func TestVersionsAreImmutableAndRestoreKeepsHistory(t *testing.T) {
	e := newEnv(t)
	r, v1 := e.create(t, "Curry", "2 tbsp tomato paste", "1 onion")
	v1Lines := e.version(t, v1).Lines

	v2 := uuid.New()
	e.mustDo(t, "recipe.version.create", map[string]any{
		"recipeId": r, "versionId": v2, "origin": "manual", "changeSummary": "More paste",
		"title": "Better curry", "servings": 2, "ingredients": []string{"3 tbsp tomato paste", "1 onion"}, "steps": []any{map[string]any{"text": "Simmer.", "minutes": 20}},
	})
	g2 := e.version(t, v2)
	if g2.N != 2 || g2.Parent == nil || *g2.Parent != v1 || *g2.Summary != "More paste" {
		t.Fatalf("v2: %+v", g2)
	}
	// Unchanged lines keep their id; changed ones get a new id.
	if g2.Lines[1].LineID != v1Lines[1].LineID || g2.Lines[0].LineID == v1Lines[0].LineID {
		t.Fatal("line ids not carried over")
	}
	if cur, title := e.current(t, r); cur != v2 || title != "Better curry" {
		t.Fatalf("current %s %q", cur, title)
	}
	// v1 untouched.
	if g1 := e.version(t, v1); g1.Title != "Curry" || *g1.Lines[0].Qty != "2" {
		t.Fatalf("v1 changed: %+v", g1)
	}

	v3 := uuid.New()
	e.mustDo(t, "recipe.version.restore", map[string]any{"recipeId": r, "versionId": v3, "fromVersionId": v1})
	g3 := e.version(t, v3)
	if g3.N != 3 || g3.Origin != "restore" || g3.Title != "Curry" || *g3.Parent != v2 || *g3.Summary != "Restored v1" {
		t.Fatalf("v3: %+v", g3)
	}
	if g3.Lines[0].LineID != v1Lines[0].LineID {
		t.Fatal("restore did not copy lines")
	}
	var n int
	_ = e.pool.QueryRow(t.Context(), `SELECT count(*) FROM recipe_version WHERE recipe_id = $1`, r).Scan(&n)
	if cur, _ := e.current(t, r); n != 3 || cur != v3 {
		t.Fatalf("versions %d current %s", n, cur)
	}
	// Restoring a version of another recipe is gone.
	other, _ := e.create(t, "Other")
	if res := e.do(t, "recipe.version.restore", map[string]any{"recipeId": other, "versionId": uuid.New(), "fromVersionId": v1}); res.Problem == nil || res.Problem.Code != commands.CodeGone {
		t.Fatalf("cross-recipe restore: %+v", res)
	}
	// A version id can't be reused.
	if res := e.do(t, "recipe.version.create", map[string]any{"recipeId": r, "versionId": v2, "origin": "manual", "title": "X", "ingredients": []string{}, "steps": []any{}}); res.Status != commands.StatusRejected {
		t.Fatalf("reused version id: %+v", res)
	}
}

func TestSetIngredientIsTheOnlyInPlaceChange(t *testing.T) {
	e := newEnv(t)
	r, v1 := e.create(t, "Dal", "1 cup yellow split peas (lappeh)", "2 cups water")
	ls := e.version(t, v1).Lines
	if e.slug(t, ls[0].IngredientID) != "split-peas" || !ls[1].Staple {
		t.Fatalf("lines: %+v", ls)
	}
	var lime uuid.UUID
	_ = e.pool.QueryRow(t.Context(), `SELECT id FROM ingredient WHERE slug = 'lime' AND household_id IS NULL`).Scan(&lime)
	e.mustDo(t, "recipe.line.set_ingredient", map[string]any{"versionId": v1, "lineId": ls[0].LineID, "ingredientId": lime})
	got := e.version(t, v1).Lines[0]
	if *got.IngredientID != lime || *got.MatchConfidence != 1 || got.Raw != ls[0].Raw {
		t.Fatalf("after pick: %+v", got)
	}
	// The pick survives a new version that keeps the line.
	v2 := uuid.New()
	e.mustDo(t, "recipe.version.create", map[string]any{"recipeId": r, "versionId": v2, "origin": "manual", "title": "Dal", "ingredients": []string{"1 cup yellow split peas (lappeh)", "3 cups water"}, "steps": []any{}})
	if l := e.version(t, v2).Lines[0]; *l.IngredientID != lime {
		t.Fatalf("pick lost: %+v", l)
	}
	if res := e.do(t, "recipe.line.set_ingredient", map[string]any{"versionId": v1, "lineId": uuid.New(), "ingredientId": lime}); res.Problem == nil || res.Problem.Code != commands.CodeGone {
		t.Fatalf("unknown line: %+v", res)
	}
	if res := e.do(t, "recipe.line.set_ingredient", map[string]any{"versionId": v1, "lineId": ls[0].LineID, "ingredientId": uuid.New()}); res.Problem == nil || res.Problem.Code != commands.CodeInvalid {
		t.Fatalf("unknown ingredient: %+v", res)
	}
}

func TestUpdateDeleteAndGone(t *testing.T) {
	e := newEnv(t)
	r, v := e.create(t, "Salad", "1 lettuce")
	ref := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	e.mustDo(t, "recipe.update", map[string]any{"id": r, "tags": []string{"Quick"}, "coverBlob": ref, "subtitle": "Crunchy", "saved": false})
	var tags []string
	var cover, sub *string
	var saved bool
	_ = e.pool.QueryRow(t.Context(), `SELECT tags, cover_blob, subtitle, saved FROM recipe WHERE id = $1`, r).Scan(&tags, &cover, &sub, &saved)
	if len(tags) != 1 || tags[0] != "quick" || *cover != ref || *sub != "Crunchy" || saved {
		t.Fatalf("after update: %v %v %v %v", tags, cover, sub, saved)
	}
	e.mustDo(t, "recipe.update", map[string]any{"id": r, "coverBlob": nil})
	_ = e.pool.QueryRow(t.Context(), `SELECT cover_blob, subtitle FROM recipe WHERE id = $1`, r).Scan(&cover, &sub)
	if cover != nil || sub == nil {
		t.Fatal("null must clear only that field")
	}
	if res := e.do(t, "recipe.update", map[string]any{"id": r, "coverBlob": "../etc/passwd"}); res.Status != commands.StatusRejected {
		t.Fatal("bad ref accepted")
	}

	e.mustDo(t, "recipe.delete", map[string]any{"id": r})
	e.mustDo(t, "recipe.delete", map[string]any{"id": r}) // idempotent
	var vDeleted bool
	_ = e.pool.QueryRow(t.Context(), `SELECT deleted_at IS NOT NULL FROM recipe_version WHERE id = $1`, v).Scan(&vDeleted)
	if !vDeleted {
		t.Fatal("versions not tombstoned")
	}
	for typ, p := range map[string]map[string]any{
		"recipe.update":              {"id": r, "saved": true},
		"recipe.version.create":      {"recipeId": r, "versionId": uuid.New(), "origin": "manual", "title": "X", "ingredients": []string{}, "steps": []any{}},
		"recipe.version.restore":     {"recipeId": r, "versionId": uuid.New(), "fromVersionId": v},
		"recipe.line.set_ingredient": {"versionId": v, "lineId": uuid.New(), "ingredientId": nil},
	} {
		if res := e.do(t, typ, p); res.Problem == nil || res.Problem.Code != commands.CodeGone {
			t.Errorf("%s on deleted recipe: %+v", typ, res)
		}
	}
}

func TestValidation(t *testing.T) {
	e := newEnv(t)
	r, _ := e.create(t, "Valid")
	for name, p := range map[string]map[string]any{
		"no title":     {"recipeId": r, "versionId": uuid.New(), "origin": "manual", "title": " ", "ingredients": []string{}, "steps": []any{}},
		"bad origin":   {"recipeId": r, "versionId": uuid.New(), "origin": "import", "title": "X", "ingredients": []string{}, "steps": []any{}},
		"servings":     {"recipeId": r, "versionId": uuid.New(), "origin": "manual", "title": "X", "servings": 0, "ingredients": []string{}, "steps": []any{}},
		"msg w/o chat": {"recipeId": r, "versionId": uuid.New(), "origin": "manual", "fromMessageId": uuid.New(), "title": "X", "ingredients": []string{}, "steps": []any{}},
	} {
		if res := e.do(t, "recipe.version.create", p); res.Status != commands.StatusRejected {
			t.Errorf("%s accepted", name)
		}
	}
	if res := e.do(t, "recipe.create", map[string]any{"id": uuid.New(), "versionId": uuid.New(), "title": "X", "sourceKind": "idea", "ingredients": []string{}, "steps": []any{}}); res.Status != commands.StatusRejected {
		t.Error("idea source accepted from a client")
	}
}

func TestShoppingItemRemembersRecipe(t *testing.T) {
	e := newEnv(t)
	r, _ := e.create(t, "Chili")
	var list uuid.UUID
	_ = e.pool.QueryRow(t.Context(), `SELECT id FROM shopping_list WHERE household_id = $1`, e.hh).Scan(&list)
	item := uuid.New()
	e.mustDo(t, "shopping.item.add", map[string]any{"id": item, "listId": list, "text": "kidney beans", "recipeId": r})
	var got *uuid.UUID
	_ = e.pool.QueryRow(t.Context(), `SELECT recipe_id FROM shopping_item WHERE id = $1`, item).Scan(&got)
	if got == nil || *got != r {
		t.Fatalf("recipe_id = %v", got)
	}
	if res := e.do(t, "shopping.item.add", map[string]any{"id": uuid.New(), "listId": list, "text": "x", "recipeId": uuid.New()}); res.Status != commands.StatusRejected {
		t.Fatal("unknown recipe accepted")
	}
	// A recipe deleted meanwhile drops the link, not the item.
	e.mustDo(t, "recipe.delete", map[string]any{"id": r})
	late := uuid.New()
	e.mustDo(t, "shopping.item.add", map[string]any{"id": late, "listId": list, "text": "rice", "recipeId": r})
	_ = e.pool.QueryRow(t.Context(), `SELECT recipe_id FROM shopping_item WHERE id = $1`, late).Scan(&got)
	if got != nil {
		t.Fatalf("link to a deleted recipe kept: %v", got)
	}
}

func TestRecipeTablesSync(t *testing.T) {
	e := newEnv(t)
	r, v := e.create(t, "Synced", "1 onion")
	p, err := e.sync.Pull(t.Context(), &e.hh, 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Rows["recipe"]) != 1 || len(p.Rows["recipe_version"]) != 1 {
		t.Fatalf("pulled %d recipes %d versions", len(p.Rows["recipe"]), len(p.Rows["recipe_version"]))
	}
	var row struct {
		CurrentVersionID uuid.UUID       `json:"current_version_id"`
		Ingredients      json.RawMessage `json:"ingredients"`
	}
	_ = json.Unmarshal(p.Rows["recipe"][0], &row)
	if row.CurrentVersionID != v {
		t.Fatalf("row: %s", p.Rows["recipe"][0])
	}
	other := e.household(t)
	if op, _ := e.sync.Pull(t.Context(), &other, 0, 1000); len(op.Rows["recipe"]) != 0 || len(op.Rows["recipe_version"]) != 0 {
		t.Fatal("recipes leaked across households")
	}
	e.mustDo(t, "recipe.delete", map[string]any{"id": r})
	p2, _ := e.sync.Pull(t.Context(), &e.hh, p.Next, 1000)
	if len(p2.Tombstones["recipe"]) != 1 || len(p2.Tombstones["recipe_version"]) != 1 {
		t.Fatalf("tombstones: %+v", p2.Tombstones)
	}
	// Global rows: the leftovers category, and substitution is a global table.
	g, _ := e.sync.Pull(t.Context(), nil, 0, 100000)
	found := false
	for _, c := range g.Rows["category"] {
		var cat struct{ Slug string }
		_ = json.Unmarshal(c, &cat)
		found = found || cat.Slug == "leftovers"
	}
	if !found {
		t.Fatal("leftovers category missing")
	}
	if _, err := e.pool.Exec(t.Context(), `INSERT INTO substitution (ingredient_id, uses, source) SELECT id, '[]', 'seed' FROM ingredient WHERE slug = 'lime' AND household_id IS NULL`); err != nil {
		t.Fatal(err)
	}
	if g2, _ := e.sync.Pull(t.Context(), nil, g.Next, 100); len(g2.Rows["substitution"]) != 1 {
		t.Fatalf("global substitution not pulled: %+v", g2.Rows)
	}
}

// Recipe writes take the household lock, so a concurrent writer commits
// after them and its rev is never hidden behind a client's cursor.
func TestRecipeWritesTakeTheHouseholdLock(t *testing.T) {
	e := newEnv(t)
	ctx := t.Context()
	txA, err := e.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = txA.Rollback(ctx) }()
	if err := syncdb.LockHousehold(ctx, txA, e.hh); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(map[string]any{"id": uuid.New(), "versionId": uuid.New(), "title": "Blocked", "ingredients": []string{}, "steps": []any{}})
	done := make(chan error, 1)
	go func() {
		_, err := e.svc.Apply(e.ctx, e.hh, []commands.Command{{ID: uuid.New(), Type: "recipe.create", Payload: b}})
		done <- err
	}()
	select {
	case <-done:
		t.Fatal("recipe.create ran while another writer held the household lock")
	case <-time.After(300 * time.Millisecond):
	}
	if err := txA.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
