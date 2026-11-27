package catalog_test

import (
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"

	"github.com/a-jay98/stockpot/internal/catalog"
	"github.com/a-jay98/stockpot/internal/catalog/resolve"
	"github.com/a-jay98/stockpot/internal/llm"
	"github.com/a-jay98/stockpot/internal/llm/openaicompat"
	"github.com/a-jay98/stockpot/internal/settings"
)

// fakeLLM answers enrichment requests from a name → answer table.
type fakeLLM struct {
	srv     *httptest.Server
	hits    atomic.Int32
	answers map[string]map[string]any // product name (as sent) → item fields
}

func newFakeLLM(t *testing.T, answers map[string]map[string]any) *fakeLLM {
	f := &fakeLLM{answers: answers}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.hits.Add(1)
		var body struct {
			Messages []struct{ Content string } `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		var prompt struct {
			Items []struct{ Ref, Name string } `json:"items"`
		}
		_ = json.Unmarshal([]byte(body.Messages[len(body.Messages)-1].Content), &prompt)
		items := []any{}
		for _, it := range prompt.Items {
			a, ok := f.answers[it.Name]
			if !ok {
				continue // missing from the reply
			}
			full := map[string]any{
				"ref": it.Ref, "ingredient_slug": "", "extra_ingredient_slugs": []string{}, "category_slug": "", "icon": "", "brand": "",
				"new_ingredient": map[string]any{"name": "", "plural": "", "parent_slug": "", "category_slug": "", "aliases": []string{}},
				"shelf_life":     map[string]any{"fridge": 0, "freezer": 0, "pantry": 0, "opened_fridge": 0}, "confidence": 0.9,
			}
			for k, v := range a {
				full[k] = v
			}
			items = append(items, full)
		}
		text, _ := json.Marshal(map[string]any{"items": items})
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{"content": string(text)}, "finish_reason": "stop"}},
			"usage":   map[string]any{"prompt_tokens": 100, "completion_tokens": 50},
		})
	}))
	t.Cleanup(f.srv.Close)
	return f
}

type enrichEnv struct {
	*env
	en    *catalog.Enricher
	fake  *fakeLLM
	store *settings.Store
}

func newEnrichEnv(t *testing.T, answers map[string]map[string]any, cap int) *enrichEnv {
	t.Helper()
	e := newEnv(t)
	f := newFakeLLM(t, answers)
	store, err := settings.NewStore(e.pool, []byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Load(t.Context()); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal([]llm.ProviderConfig{{ID: "fake", Kind: llm.KindOpenAICompat, BaseURL: f.srv.URL, Model: "m", Enabled: true,
		ResetTZ: "UTC", JSONSchema: true, DailyRequestCap: cap}})
	if err := store.Set(t.Context(), llm.SettingProviders.Key(), raw, nil); err != nil {
		t.Fatal(err)
	}
	router := llm.NewRouter(llm.Deps{Pool: e.pool, Settings: store,
		Factories: map[string]llm.Factory{llm.KindOpenAICompat: openaicompat.Factory(nil)}})
	if err := router.SetAPIKey(t.Context(), "fake", "k"); err != nil {
		t.Fatal(err)
	}
	return &enrichEnv{env: e, fake: f, store: store, en: catalog.NewEnricher(catalog.EnrichDeps{Pool: e.pool, Settings: store, LLM: router})}
}

func (e *enrichEnv) product(t *testing.T, name string) uuid.UUID {
	t.Helper()
	id := uuid.New()
	if r := e.one(t, cmd("catalog.product.create", map[string]any{"id": id, "name": name})); r.Status != "applied" {
		t.Fatalf("create %s: %+v", name, r)
	}
	return id
}

type prodRow struct {
	slug, source, state string
	attempts            int
	conf                *float64
	category, icon      *string
}

func (e *enrichEnv) row(t *testing.T, id uuid.UUID) prodRow {
	t.Helper()
	var r prodRow
	err := e.pool.QueryRow(t.Context(), `SELECT coalesce(i.slug, ''), p.match_source, p.enrich_state, p.enrich_attempts, p.match_confidence, c.slug, p.icon
		FROM product p LEFT JOIN ingredient i ON i.id = p.ingredient_id LEFT JOIN category c ON c.id = p.category_id WHERE p.id = $1`, id,
	).Scan(&r.slug, &r.source, &r.state, &r.attempts, &r.conf, &r.category, &r.icon)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func (e *enrichEnv) lastRun(t *testing.T) (outcome string, items, applied, failed, cached, calls int) {
	t.Helper()
	err := e.pool.QueryRow(t.Context(), `SELECT outcome, items, applied, failed, cached, calls FROM enrich_run ORDER BY started_at DESC LIMIT 1`).
		Scan(&outcome, &items, &applied, &failed, &cached, &calls)
	if err != nil {
		t.Fatal(err)
	}
	return
}

func TestEnrichBatchSuccessInvalidAndIdempotent(t *testing.T) {
	e := newEnrichEnv(t, map[string]map[string]any{
		"Zorbo fruit": {"ingredient_slug": "avocado", "category_slug": "produce-fruit", "icon": "avocado", "brand": "Zorbo", "confidence": 0.7,
			"shelf_life": map[string]any{"fridge": 4, "freezer": 0, "pantry": 0, "opened_fridge": 0}},
		"Kumquats":     {"new_ingredient": map[string]any{"name": "Kumquat", "plural": "Kumquats", "parent_slug": "citrus", "category_slug": "", "aliases": []string{"cumquat"}}},
		"Weird thing":  {"ingredient_slug": "no-such-ingredient"},
		"Bad icon":     {"ingredient_slug": "milk", "icon": "not-an-icon"},
		"Both set":     {"ingredient_slug": "milk", "new_ingredient": map[string]any{"name": "Cream", "plural": "", "parent_slug": "milk", "category_slug": "", "aliases": []string{}}},
		"Also missing": nil, // the model leaves it out
	}, 100)
	ctx := t.Context()
	zorbo := e.product(t, "Zorbo fruit")
	kum := e.product(t, "Kumquats")
	weird := e.product(t, "Weird thing")
	badIcon := e.product(t, "Bad icon")
	both := e.product(t, "Both set")
	missing := e.product(t, "Also missing")
	delete(e.fake.answers, "Also missing")
	// A product the user matched is never picked up.
	user := e.product(t, "Mystery bar")
	var milk uuid.UUID
	_ = e.pool.QueryRow(ctx, `SELECT id FROM ingredient WHERE slug = 'milk'`).Scan(&milk)
	e.one(t, cmd("catalog.product.set_ingredient", map[string]any{"id": user, "ingredientId": milk}))

	if err := e.en.Run(ctx, catalog.TriggerManual, nil); err != nil {
		t.Fatal(err)
	}
	if got := e.row(t, zorbo); got.slug != "avocado" || got.source != "llm" || got.state != "done" || got.conf == nil || math.Abs(*got.conf-0.7) > 1e-6 || got.category == nil || *got.category != "produce-fruit" || got.icon == nil || *got.icon != "avocado" {
		t.Fatalf("zorbo = %+v", got)
	}
	var brand *string
	var shelf []byte
	_ = e.pool.QueryRow(ctx, `SELECT brand, shelf_life FROM product WHERE id = $1`, zorbo).Scan(&brand, &shelf)
	if brand == nil || *brand != "Zorbo" || string(shelf) != `{"fridge": 4}` {
		t.Fatalf("brand %v shelf %s", brand, shelf)
	}
	k := e.row(t, kum)
	if k.slug != "kumquat" || k.source != "llm" || k.state != "done" {
		t.Fatalf("kumquat = %+v", k)
	}
	var src, parent string
	var hh *uuid.UUID
	if err := e.pool.QueryRow(ctx, `SELECT i.source, p.slug, i.household_id FROM ingredient i JOIN ingredient p ON p.id = i.parent_id WHERE i.slug = 'kumquat'`).Scan(&src, &parent, &hh); err != nil ||
		src != "llm" || parent != "citrus" || hh == nil || *hh != e.hh {
		t.Fatalf("new ingredient: %s %s %v %v", src, parent, hh, err)
	}
	for name, id := range map[string]uuid.UUID{"weird": weird, "bad icon": badIcon, "both": both, "missing": missing} {
		if r := e.row(t, id); r.state != "pending" || r.attempts != 1 || r.source == "llm" {
			t.Errorf("%s = %+v, want pending with one attempt", name, r)
		}
	}
	if r := e.row(t, user); r.source != "user" || r.slug != "milk" {
		t.Fatalf("user pick changed: %+v", r)
	}
	if outcome, items, applied, failed, _, calls := e.lastRun(t); outcome != "partial" || items != 6 || applied != 2 || failed != 4 || calls != 1 {
		t.Fatalf("run = %s items %d applied %d failed %d calls %d", outcome, items, applied, failed, calls)
	}
	var audits int
	_ = e.pool.QueryRow(ctx, `SELECT count(*) FROM audit_event WHERE action = 'catalog.enrich.apply' AND after->>'actor' = 'system:enricher' AND household_id = $1`, e.hh).Scan(&audits)
	if audits != 2 {
		t.Fatalf("audit events = %d", audits)
	}

	// Rerun: the invalid ones are tried again (attempts 2, then 3 = failed); the
	// applied ones are not touched and cost nothing.
	hits := e.fake.hits.Load()
	for range 2 {
		if err := e.en.Run(ctx, catalog.TriggerScheduled, nil); err != nil {
			t.Fatal(err)
		}
	}
	if r := e.row(t, weird); r.state != "failed" || r.attempts != 3 {
		t.Fatalf("weird after 3 attempts = %+v", r)
	}
	if e.fake.hits.Load() != hits+2 {
		t.Fatalf("hits %d, want %d", e.fake.hits.Load(), hits+2)
	}
	if err := e.en.Run(ctx, catalog.TriggerScheduled, nil); err != nil { // nothing left: no call, no run row
		t.Fatal(err)
	}
	var runs int
	_ = e.pool.QueryRow(ctx, `SELECT count(*) FROM enrich_run`).Scan(&runs)
	if e.fake.hits.Load() != hits+2 || runs != 3 {
		t.Fatalf("idle rerun called the LLM or recorded a run: hits %d runs %d", e.fake.hits.Load(), runs)
	}
	if r := e.row(t, zorbo); r.slug != "avocado" || math.Abs(*r.conf-0.7) > 1e-6 {
		t.Fatalf("zorbo changed on rerun: %+v", r)
	}

	// Retry failed puts them back.
	n, err := e.en.RetryFailed(ctx)
	if err != nil || n != 4 {
		t.Fatalf("RetryFailed = %d, %v", n, err)
	}
}

func TestEnrichCachesAcrossHouseholds(t *testing.T) {
	e := newEnrichEnv(t, map[string]map[string]any{"Kumquats": {"ingredient_slug": "orange"}}, 100)
	ctx := t.Context()
	a := e.product(t, "Kumquats")
	if err := e.en.Run(ctx, catalog.TriggerScheduled, nil); err != nil {
		t.Fatal(err)
	}
	hits := e.fake.hits.Load()

	// Another household adds the same name: answered from the cache.
	other := e.household(t, "Other")
	var b uuid.UUID = uuid.New()
	if _, err := e.pool.Exec(ctx, `INSERT INTO product (id, household_id, name) VALUES ($1, $2, 'kumquats')`, b, other); err != nil {
		t.Fatal(err)
	}
	if err := e.en.Run(ctx, catalog.TriggerScheduled, nil); err != nil {
		t.Fatal(err)
	}
	if e.fake.hits.Load() != hits {
		t.Fatal("cached name called the LLM again")
	}
	if r := e.row(t, b); r.slug != "orange" || r.source != "llm" || r.state != "done" {
		t.Fatalf("other household's product = %+v", r)
	}
	if _, _, applied, _, cached, calls := e.lastRun(t); applied != 0 || cached != 1 || calls != 0 {
		t.Fatalf("run applied %d cached %d calls %d", applied, cached, calls)
	}
	_ = a
}

func TestEnrichDeferredWithoutHeadroom(t *testing.T) {
	e := newEnrichEnv(t, map[string]map[string]any{"Kumquats": {"ingredient_slug": "orange"}}, 10)
	ctx := t.Context()
	p := e.product(t, "Kumquats")
	// 6 of 10 requests used by interactive features: exactly the 40% reserve is left.
	for range 6 {
		if _, err := e.pool.Exec(ctx, `INSERT INTO llm_call (time, feature, provider, model, tokens_in, tokens_out, latency_ms, status, cache_hit)
			VALUES (now(), 'x', 'fake', 'm', 1, 1, 1, 'ok', false)`); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.en.Run(ctx, catalog.TriggerScheduled, nil); err != nil {
		t.Fatal(err)
	}
	if e.fake.hits.Load() != 0 {
		t.Fatal("background run called the LLM under the reserve")
	}
	var outcome string
	var reason *string
	_ = e.pool.QueryRow(ctx, `SELECT outcome, reason FROM enrich_run`).Scan(&outcome, &reason)
	if outcome != "deferred" || reason == nil {
		t.Fatalf("outcome %s reason %v", outcome, reason)
	}
	if r := e.row(t, p); r.state != "pending" || r.attempts != 0 {
		t.Fatalf("deferral used an attempt: %+v", r)
	}
	// Flag off: scheduled runs do nothing at all.
	if err := settings.SetValue(ctx, e.store, catalog.FlagEnrich, false, nil); err != nil {
		t.Fatal(err)
	}
	if err := e.en.Run(ctx, catalog.TriggerScheduled, nil); err != nil {
		t.Fatal(err)
	}
	var runs int
	_ = e.pool.QueryRow(ctx, `SELECT count(*) FROM enrich_run`).Scan(&runs)
	if runs != 1 {
		t.Fatalf("runs = %d", runs)
	}
}

func TestAdminIngredientOps(t *testing.T) {
	e := newEnrichEnv(t, map[string]map[string]any{
		"Kumquats": {"new_ingredient": map[string]any{"name": "Kumquat", "plural": "Kumquats", "parent_slug": "citrus", "category_slug": "", "aliases": []string{}}},
	}, 100)
	ctx := t.Context()
	p := e.product(t, "Kumquats")
	if err := e.en.Run(ctx, catalog.TriggerManual, nil); err != nil {
		t.Fatal(err)
	}
	var kum, orange uuid.UUID
	_ = e.pool.QueryRow(ctx, `SELECT id FROM ingredient WHERE slug = 'kumquat'`).Scan(&kum)
	_ = e.pool.QueryRow(ctx, `SELECT id FROM ingredient WHERE slug = 'orange'`).Scan(&orange)
	st, err := e.en.Status(ctx)
	if err != nil || len(st.LLMIngredients) != 1 || st.LLMIngredients[0].Products != 1 || st.Counts["done"] != 1 {
		t.Fatalf("status = %+v, %v", st, err)
	}
	// Global ingredients can't be touched here.
	if err := e.en.DeleteIngredient(ctx, orange); err != catalog.ErrNotReviewable {
		t.Fatalf("delete global: %v", err)
	}
	if err := e.en.Promote(ctx, kum); err != nil {
		t.Fatal(err)
	}
	var hh *uuid.UUID
	var src string
	_ = e.pool.QueryRow(ctx, `SELECT household_id, source FROM ingredient WHERE id = $1`, kum).Scan(&hh, &src)
	if hh != nil || src != "admin" {
		t.Fatalf("promoted: %v %s", hh, src)
	}
	// It is no longer an enricher ingredient: merge now refuses.
	if err := e.en.Merge(ctx, kum, orange); err != catalog.ErrNotReviewable {
		t.Fatalf("merge promoted: %v", err)
	}

	// A second one, merged away: products move, the name becomes an alias.
	e2 := e.product(t, "Jabuticabas")
	e.fake.answers["Jabuticabas"] = map[string]any{"new_ingredient": map[string]any{"name": "Jabuticaba", "plural": "Jabuticabas", "parent_slug": "citrus", "category_slug": "", "aliases": []string{}}}
	if err := e.en.Run(ctx, catalog.TriggerManual, nil); err != nil {
		t.Fatal(err)
	}
	var pom uuid.UUID
	_ = e.pool.QueryRow(ctx, `SELECT id FROM ingredient WHERE slug = 'jabuticaba'`).Scan(&pom)
	if err := e.en.Merge(ctx, pom, kum); err != nil {
		t.Fatal(err)
	}
	if r := e.row(t, e2); r.slug != "kumquat" {
		t.Fatalf("product after merge = %+v", r)
	}
	var alias bool
	_ = e.pool.QueryRow(ctx, `SELECT 'jabuticaba' = ANY(aliases) FROM ingredient WHERE id = $1`, kum).Scan(&alias)
	if !alias {
		t.Fatal("merged name is not an alias")
	}
	_ = p
}

func TestMatchSemantics(t *testing.T) {
	e := newEnv(t)
	ctx := e.ctx
	cl := uuid.New()
	e.one(t, cmd("catalog.product.create", map[string]any{"id": cl, "name": "Cuties clementines"}))
	ap := uuid.New()
	e.one(t, cmd("catalog.product.create", map[string]any{"id": ap, "name": "Apples"}))
	for _, pid := range []uuid.UUID{cl, ap} {
		if _, err := e.pool.Exec(ctx, `INSERT INTO stock_item (id, household_id, product_id, quantity) VALUES (gen_random_uuid(), $1, $2, 3)`, e.hh, pid); err != nil {
			t.Fatal(err)
		}
	}
	// A recipe asking for mandarin matches the clementines in stock (a kind of
	// mandarin), not the apples.
	res, err := catalog.MatchText(ctx, e.pool, resolve0{}, e.hh, "good mandarins")
	if err != nil || res.Ingredient == nil || res.Ingredient.Slug != "mandarin" || len(res.Products) != 1 || res.Products[0].ID != cl {
		t.Fatalf("mandarin: %+v, %v", res, err)
	}
	// The other way round does not hold: asking for clementine finds no mandarin product.
	if res, _ = catalog.MatchText(ctx, e.pool, resolve0{}, e.hh, "clementines"); len(res.Products) != 1 {
		t.Fatalf("clementine: %+v", res)
	}
	// Out of stock products do not match.
	if _, err := e.pool.Exec(ctx, `UPDATE stock_item SET deleted_at = now() WHERE product_id = $1`, cl); err != nil {
		t.Fatal(err)
	}
	if res, _ = catalog.MatchText(ctx, e.pool, resolve0{}, e.hh, "mandarin"); len(res.Products) != 0 {
		t.Fatalf("out of stock matched: %+v", res)
	}
	if res, err = catalog.MatchText(ctx, e.pool, resolve0{}, e.hh, "zzqx"); err != nil || res.Ingredient != nil || res.Products == nil {
		t.Fatalf("unknown: %+v %v", res, err)
	}
}

type resolve0 = resolve.Resolver
