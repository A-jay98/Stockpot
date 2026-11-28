package recipes_test

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/a-jay98/stockpot/internal/catalog"
	"github.com/a-jay98/stockpot/internal/catalog/resolve"
	"github.com/a-jay98/stockpot/internal/llm"
	"github.com/a-jay98/stockpot/internal/recipes"
)

// fakeLLM answers every request with reply (or err) and records requests.
type fakeLLM struct {
	reply string
	err   error
	reqs  []llm.Request
}

func (f *fakeLLM) Complete(_ context.Context, req llm.Request) (llm.Response, error) {
	f.reqs = append(f.reqs, req)
	if f.err != nil {
		return llm.Response{}, f.err
	}
	return llm.Response{JSON: json.RawMessage(f.reply), Provider: "fake"}, nil
}

// The swap seed is data; every slug must name a seeded ingredient.
func TestSubstitutionSeedNamesSeededIngredients(t *testing.T) {
	ings, err := catalog.SeedIngredients()
	if err != nil {
		t.Fatal(err)
	}
	slugs := map[string]bool{}
	for _, i := range ings {
		slugs[i.Slug] = true
	}
	subs, err := recipes.SeedSubstitutions()
	if err != nil {
		t.Fatal(err)
	}
	if len(subs) < 120 {
		t.Fatalf("only %d swaps", len(subs))
	}
	keys := map[string]bool{}
	for _, s := range subs {
		if keys[s.Key()] {
			t.Errorf("duplicate swap %s", s.Key())
		}
		keys[s.Key()] = true
		if !slugs[s.Missing] {
			t.Errorf("%s: unknown ingredient %q", s.Key(), s.Missing)
		}
		if s.Note == "" || len(s.Uses) == 0 {
			t.Errorf("%s: needs a note and uses", s.Key())
		}
		for _, u := range s.Uses {
			if !slugs[u.Slug] {
				t.Errorf("%s: unknown ingredient %q", s.Key(), u.Slug)
			}
			if u.Slug == s.Missing || u.Ratio <= 0 {
				t.Errorf("%s: bad use %+v", s.Key(), u)
			}
		}
	}
}

func TestSeedSubstitutionRowsIsIdempotent(t *testing.T) {
	e := newEnv(t)
	ctx := t.Context()
	for range 2 {
		if err := recipes.SeedSubstitutionRows(ctx, e.pool); err != nil {
			t.Fatal(err)
		}
	}
	subs, _ := recipes.SeedSubstitutions()
	var n int
	var maxRev int64
	if err := e.pool.QueryRow(ctx, `SELECT count(*), max(rev) FROM substitution WHERE household_id IS NULL AND source = 'seed' AND deleted_at IS NULL`).Scan(&n, &maxRev); err != nil {
		t.Fatal(err)
	}
	if n != len(subs) {
		t.Fatalf("%d rows, want %d", n, len(subs))
	}
	if err := recipes.SeedSubstitutionRows(ctx, e.pool); err != nil {
		t.Fatal(err)
	}
	var after int64
	_ = e.pool.QueryRow(ctx, `SELECT max(rev) FROM substitution`).Scan(&after)
	if after != maxRev {
		t.Fatalf("reseeding touched rows: rev %d → %d", maxRev, after)
	}
	var note string
	if err := e.pool.QueryRow(ctx, `SELECT s.note FROM substitution s JOIN ingredient i ON i.id = s.ingredient_id
		WHERE i.slug = 'dried-lime' AND s.household_id IS NULL ORDER BY s.note LIMIT 1`).Scan(&note); err != nil {
		t.Fatal(err)
	}
	if note == "" {
		t.Fatal("dried lime swap has no note")
	}
}

func (e *env) ingredient(t *testing.T, slug string) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := e.pool.QueryRow(t.Context(), `SELECT id FROM ingredient WHERE slug = $1 AND household_id IS NULL`, slug).Scan(&id); err != nil {
		t.Fatalf("ingredient %s: %v", slug, err)
	}
	return id
}

// stock puts a product of the ingredient in stock, expiring in days (nil: no date).
func (e *env) stock(t *testing.T, slug string, days *int) {
	t.Helper()
	ctx := t.Context()
	p := uuid.New()
	if _, err := e.pool.Exec(ctx, `INSERT INTO product (id, household_id, name, ingredient_id) VALUES ($1, $2, $3, $4)`,
		p, e.hh, slug, e.ingredient(t, slug)); err != nil {
		t.Fatal(err)
	}
	var exp *time.Time
	if days != nil {
		d := time.Now().AddDate(0, 0, *days)
		exp = &d
	}
	if _, err := e.pool.Exec(ctx, `INSERT INTO stock_item (id, household_id, product_id, quantity, unit, expires_on) VALUES ($1, $2, $3, 1, 'ea', $4)`,
		uuid.New(), e.hh, p, exp); err != nil {
		t.Fatal(err)
	}
}

func lineNamed(t *testing.T, e *env, version uuid.UUID, name string) uuid.UUID {
	t.Helper()
	for _, l := range e.version(t, version).Lines {
		if l.Name == name {
			return l.LineID
		}
	}
	t.Fatalf("no line %q", name)
	return uuid.Nil
}

func TestSubstituteSavesOnlyInStockSwaps(t *testing.T) {
	e := newEnv(t)
	e.stock(t, "greek-yogurt", nil)
	e.stock(t, "cream", nil)
	recipe, version := e.create(t, "Chicken curry", "1 (400 mL) can coconut milk", "800 g chicken thighs", "1 cup water")
	line := lineNamed(t, e, version, "coconut milk")
	fake := &fakeLLM{reply: `{"swaps": [
		{"uses": [{"slug": "greek-yogurt", "ratio": 0.5, "unit": ""}, {"slug": "water", "ratio": 0.25, "unit": ""}], "note": "Use 200 g Greek yogurt + 100 mL water, off the heat."},
		{"uses": [{"slug": "coconut-cream", "ratio": 1, "unit": ""}], "note": "Use coconut cream."},
		{"uses": [{"slug": "cream", "ratio": 1, "unit": ""}], "note": "Use the same amount of cream."}
	]}`}
	a := &recipes.Assistant{Pool: e.pool, LLM: fake, W: recipes.Writer{Res: resolve.Resolver{}}}
	got, err := a.Substitute(e.ctx, e.hh, recipe, nil, line)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Note[:7] != "Use 200" {
		t.Fatalf("swaps %+v", got)
	}
	req := fake.reqs[0]
	if req.Feature != recipes.FeatureSubstitute || req.Feature.CacheTTL != 7*24*time.Hour || req.Schema == nil {
		t.Fatalf("request %+v", req.Feature)
	}
	prompt := req.Messages[1].Text
	var sent struct {
		InStock []string `json:"in_stock"`
		Missing string   `json:"missing"`
	}
	_ = json.Unmarshal([]byte(prompt), &sent)
	if sent.Missing != "1 (400 mL) can coconut milk" || len(sent.InStock) < 3 {
		t.Fatalf("prompt %s", prompt)
	}
	for _, s := range sent.InStock {
		if s == "coconut-milk (Coconut milk)" {
			t.Fatal("the missing ingredient was offered as its own swap")
		}
	}

	// Asking again replaces the earlier model answers for that ingredient.
	if _, err := a.Substitute(e.ctx, e.hh, recipe, &version, line); err != nil {
		t.Fatal(err)
	}
	var live, all int
	_ = e.pool.QueryRow(t.Context(), `SELECT count(*) FILTER (WHERE deleted_at IS NULL), count(*) FROM substitution
		WHERE household_id = $1 AND recipe_id = $2 AND source = 'llm' AND ingredient_id = $3`,
		e.hh, recipe, e.ingredient(t, "coconut-milk")).Scan(&live, &all)
	if live != 2 || all != 4 {
		t.Fatalf("live %d of %d", live, all)
	}
	var audits int
	_ = e.pool.QueryRow(t.Context(), `SELECT count(*) FROM audit_event WHERE action = 'recipe.substitute'`).Scan(&audits)
	if audits != 2 {
		t.Fatalf("%d audit events", audits)
	}

	// Deleting the recipe tombstones its swaps.
	e.mustDo(t, "recipe.delete", map[string]any{"id": recipe})
	_ = e.pool.QueryRow(t.Context(), `SELECT count(*) FROM substitution WHERE recipe_id = $1 AND deleted_at IS NULL`, recipe).Scan(&live)
	if live != 0 {
		t.Fatalf("%d swaps outlived the recipe", live)
	}
	if _, err := a.Substitute(e.ctx, e.hh, recipe, nil, line); !errors.Is(err, recipes.ErrRecipeNotFound) {
		t.Fatalf("deleted recipe: %v", err)
	}
}

func TestSubstituteErrors(t *testing.T) {
	e := newEnv(t)
	e.stock(t, "greek-yogurt", nil)
	recipe, version := e.create(t, "Stew", "1 can coconut milk", "2 handfuls of mystery zorblax")
	fake := &fakeLLM{err: llm.ErrUnavailable}
	a := &recipes.Assistant{Pool: e.pool, LLM: fake, W: recipes.Writer{Res: resolve.Resolver{}}}

	if _, err := a.Substitute(e.ctx, e.hh, recipe, nil, lineNamed(t, e, version, "coconut milk")); !errors.Is(err, llm.ErrUnavailable) {
		t.Fatalf("unavailable: %v", err)
	}
	var unmatched uuid.UUID
	for _, l := range e.version(t, version).Lines {
		if l.IngredientID == nil {
			unmatched = l.LineID
		}
	}
	if _, err := a.Substitute(e.ctx, e.hh, recipe, nil, unmatched); !errors.Is(err, recipes.ErrLineUnmatched) {
		t.Fatalf("unmatched: %v", err)
	}
	if _, err := a.Substitute(e.ctx, e.hh, recipe, nil, uuid.New()); !errors.Is(err, recipes.ErrLineNotFound) {
		t.Fatalf("no line: %v", err)
	}
	other := e.household(t)
	if _, err := a.Substitute(e.as(other), other, recipe, nil, unmatched); !errors.Is(err, recipes.ErrRecipeNotFound) {
		t.Fatalf("other household: %v", err)
	}
}

const ideasReply = `{"ideas": [
	{"title": "Yogurt chicken", "subtitle": "Quick and tangy", "servings": 2, "total_minutes": 30,
	 "ingredients": ["400 g chicken thighs", "1 cup Greek yogurt", "1 tsp salt"], "steps": ["Marinate.", "Grill."]},
	{"title": "Chicken curry", "subtitle": "Already saved", "servings": 4, "total_minutes": 45,
	 "ingredients": ["800 g chicken thighs"], "steps": ["Cook."]},
	{"title": "", "subtitle": "", "servings": 0, "total_minutes": 0, "ingredients": [], "steps": []},
	{"title": "Spinach eggs", "subtitle": "", "servings": 500, "total_minutes": -3,
	 "ingredients": ["4 eggs", "2 cups spinach"], "steps": ["Wilt the spinach.", "Add eggs."]}
]}`

func TestIdeasCreateUnsavedRecipesThatExpire(t *testing.T) {
	e := newEnv(t)
	soon := 1
	e.stock(t, "chicken-thigh", &soon)
	e.stock(t, "greek-yogurt", nil)
	e.create(t, "Chicken curry", "800 g chicken thighs")
	now := time.Now()
	fake := &fakeLLM{reply: ideasReply}
	a := &recipes.Assistant{Pool: e.pool, LLM: fake, W: recipes.Writer{Res: resolve.Resolver{}}, Now: func() time.Time { return now }}

	ids, err := a.Ideas(e.ctx, e.hh, nil, "  quick dinners ")
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 2 {
		t.Fatalf("%d ideas, want 2 (duplicate and empty dropped)", len(ids))
	}
	var sent struct {
		InStock []string `json:"in_stock"`
		UseSoon []string `json:"use_soon"`
		Staples []string `json:"staples"`
		Avoid   []string `json:"avoid"`
		Note    string   `json:"note"`
	}
	_ = json.Unmarshal([]byte(fake.reqs[0].Messages[1].Text), &sent)
	if len(sent.InStock) != 2 || len(sent.UseSoon) != 1 || sent.Avoid[0] != "Chicken curry" || sent.Note != "quick dinners" {
		t.Fatalf("prompt %+v", sent)
	}
	// Only the household's staples are assumed, never oil.
	if !slices.Equal(sent.Staples, []string{"black-pepper", "ice", "salt", "water"}) || strings.Contains(fake.reqs[0].Messages[0].Text, "pepper, oil") {
		t.Fatalf("staples %v", sent.Staples)
	}
	if fake.reqs[0].Feature != recipes.FeatureIdeas {
		t.Fatalf("feature %+v", fake.reqs[0].Feature)
	}
	var (
		kind     string
		saved    bool
		expires  *time.Time
		servings *int
		version  uuid.UUID
	)
	if err := e.pool.QueryRow(t.Context(), `SELECT source_kind, saved, idea_expires_at, servings, current_version_id FROM recipe WHERE id = $1`, ids[1]).
		Scan(&kind, &saved, &expires, &servings, &version); err != nil {
		t.Fatal(err)
	}
	if kind != "idea" || saved || expires == nil || expires.Sub(now) < 6*24*time.Hour || servings != nil {
		t.Fatalf("idea %s saved=%v expires=%v servings=%v", kind, saved, expires, servings)
	}
	v := e.version(t, version)
	if v.Origin != "idea" || len(v.Lines) != 2 || len(v.Steps) != 2 {
		t.Fatalf("version %+v", v)
	}

	// Save keeps one; the daily sweep removes the other after a week.
	e.mustDo(t, "recipe.update", map[string]any{"id": ids[0], "saved": true})
	if n, err := recipes.ExpireIdeas(t.Context(), e.pool, now.Add(24*time.Hour)); err != nil || n != 0 {
		t.Fatalf("early sweep: %d %v", n, err)
	}
	// A cooked idea stays: its cook log points at it.
	if _, err := e.pool.Exec(t.Context(), `UPDATE recipe SET cooked_count = 1 WHERE id = $1`, ids[1]); err != nil {
		t.Fatal(err)
	}
	if n, err := recipes.ExpireIdeas(t.Context(), e.pool, now.Add(8*24*time.Hour)); err != nil || n != 0 {
		t.Fatalf("cooked sweep: %d %v", n, err)
	}
	if _, err := e.pool.Exec(t.Context(), `UPDATE recipe SET cooked_count = 0 WHERE id = $1`, ids[1]); err != nil {
		t.Fatal(err)
	}
	n, err := recipes.ExpireIdeas(t.Context(), e.pool, now.Add(8*24*time.Hour))
	if err != nil || n != 1 {
		t.Fatalf("sweep: %d %v", n, err)
	}
	var gone, kept, versionsLeft int
	_ = e.pool.QueryRow(t.Context(), `SELECT count(*) FILTER (WHERE deleted_at IS NOT NULL AND id = $1), count(*) FILTER (WHERE deleted_at IS NULL AND id = $2) FROM recipe`, ids[1], ids[0]).Scan(&gone, &kept)
	_ = e.pool.QueryRow(t.Context(), `SELECT count(*) FROM recipe_version WHERE recipe_id = $1 AND deleted_at IS NULL`, ids[1]).Scan(&versionsLeft)
	if gone != 1 || kept != 1 || versionsLeft != 0 {
		t.Fatalf("gone %d kept %d versions %d", gone, kept, versionsLeft)
	}
}

func TestIdeasWithEmptyKitchenSkipTheModel(t *testing.T) {
	e := newEnv(t)
	fake := &fakeLLM{reply: ideasReply}
	a := &recipes.Assistant{Pool: e.pool, LLM: fake, W: recipes.Writer{Res: resolve.Resolver{}}}
	ids, err := a.Ideas(e.ctx, e.hh, nil, "")
	if err != nil || len(ids) != 0 || len(fake.reqs) != 0 {
		t.Fatalf("ids %v err %v calls %d", ids, err, len(fake.reqs))
	}
	fake.err = llm.ErrUnavailable
	e.stock(t, "egg", nil)
	if _, err := a.Ideas(e.ctx, e.hh, nil, ""); !errors.Is(err, llm.ErrUnavailable) {
		t.Fatalf("unavailable: %v", err)
	}
}
