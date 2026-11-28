package reports_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/a-jay98/stockpot/internal/catalog"
	"github.com/a-jay98/stockpot/internal/identity"
	"github.com/a-jay98/stockpot/internal/llm"
	"github.com/a-jay98/stockpot/internal/llm/openaicompat"
	"github.com/a-jay98/stockpot/internal/reports"
	"github.com/a-jay98/stockpot/internal/settings"
	"github.com/a-jay98/stockpot/internal/testdb"
)

// fakeLLM answers each report by its item name.
type fakeLLM struct {
	hits    atomic.Int32
	answers map[string]map[string]any
}

type env struct {
	pool  *pgxpool.Pool
	store *settings.Store
	svc   *reports.Service
	fake  *fakeLLM
	hh    uuid.UUID
	ctx   context.Context
}

func newEnv(t *testing.T, answers map[string]map[string]any, reqCap int) *env {
	t.Helper()
	pool := testdb.New(t)
	if err := catalog.Seed(t.Context(), pool); err != nil {
		t.Fatal(err)
	}
	// Seeds may carry purchase units; start these from none.
	if _, err := pool.Exec(t.Context(), `UPDATE ingredient SET default_unit = NULL WHERE slug IN ('salt', 'olive-oil', 'flour', 'spice')`); err != nil {
		t.Fatal(err)
	}
	f := &fakeLLM{answers: answers}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.hits.Add(1)
		var body struct {
			Messages []struct{ Content string } `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		var prompt struct {
			Items []struct{ Ref, Item string } `json:"items"`
		}
		_ = json.Unmarshal([]byte(body.Messages[len(body.Messages)-1].Content), &prompt)
		items := []any{}
		for _, it := range prompt.Items {
			a, ok := f.answers[it.Item]
			if !ok {
				continue
			}
			full := map[string]any{"ref": it.Ref, "verdict": "unsure", "value": "", "confidence": 0.9, "reason": "because"}
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
	t.Cleanup(srv.Close)
	store, err := settings.NewStore(pool, []byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Load(t.Context()); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal([]llm.ProviderConfig{{
		ID: "fake", Kind: llm.KindOpenAICompat, BaseURL: srv.URL, Model: "m", Enabled: true,
		ResetTZ: "UTC", JSONSchema: true, DailyRequestCap: reqCap,
	}})
	if err := store.Set(t.Context(), llm.SettingProviders.Key(), raw, nil); err != nil {
		t.Fatal(err)
	}
	router := llm.NewRouter(llm.Deps{
		Pool: pool, Settings: store,
		Factories: map[string]llm.Factory{llm.KindOpenAICompat: openaicompat.Factory(nil)},
	})
	if err := router.SetAPIKey(t.Context(), "fake", "k"); err != nil {
		t.Fatal(err)
	}
	e := &env{pool: pool, store: store, fake: f, svc: reports.New(reports.Deps{Pool: pool, Settings: store, LLM: router})}
	if err := pool.QueryRow(t.Context(), `INSERT INTO household (id, name) VALUES (gen_random_uuid(), 'Home') RETURNING id`).Scan(&e.hh); err != nil {
		t.Fatal(err)
	}
	user := uuid.New()
	if _, err := pool.Exec(t.Context(), `INSERT INTO app_user (id, display_name, status) VALUES ($1, 'Sam', 'active')`, user); err != nil {
		t.Fatal(err)
	}
	e.ctx = identity.WithPrincipal(t.Context(), &identity.Principal{UserID: user, Status: identity.StatusActive, HouseholdID: e.hh})
	return e
}

// item adds a product matched to the ingredient slug and a shopping item for it.
func (e *env) item(t *testing.T, name, slug, unit string, productUnit *string) (item, product uuid.UUID) {
	t.Helper()
	product, item = uuid.New(), uuid.New()
	ctx := t.Context()
	if _, err := e.pool.Exec(ctx, `INSERT INTO product (id, household_id, name, ingredient_id, default_unit, match_source)
		VALUES ($1, $2, $3, (SELECT id FROM ingredient WHERE slug = $4 AND household_id IS NULL), $5, 'rule')`, product, e.hh, name, slug, productUnit); err != nil {
		t.Fatal(err)
	}
	if _, err := e.pool.Exec(ctx, `INSERT INTO shopping_item (id, household_id, list_id, product_id, text, qty, unit)
		VALUES ($1, $2, (SELECT id FROM shopping_list WHERE household_id = $2 LIMIT 1), $3, $4, 500, $5)`, item, e.hh, product, name, unit); err != nil {
		t.Fatal(err)
	}
	return item, product
}

func (e *env) report(t *testing.T, item uuid.UUID, suggested string) uuid.UUID {
	t.Helper()
	id, err := e.svc.Submit(e.ctx, reports.Input{Kind: reports.KindWrongUnit, Source: reports.SourceShoppingItem, SubjectID: item, Suggested: suggested})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func (e *env) status(t *testing.T, id uuid.UUID) string {
	t.Helper()
	var s string
	if err := e.pool.QueryRow(t.Context(), `SELECT status FROM problem_report WHERE id = $1`, id).Scan(&s); err != nil {
		t.Fatal(err)
	}
	return s
}

func (e *env) unitOf(t *testing.T, table string, where string, arg any) *string {
	t.Helper()
	var u *string
	if err := e.pool.QueryRow(t.Context(), `SELECT default_unit FROM `+table+` WHERE `+where, arg).Scan(&u); err != nil {
		t.Fatal(err)
	}
	return u
}

func ptr(s string) *string { return &s }

func TestReviewAppliesRejectsAndEscalates(t *testing.T) {
	e := newEnv(t, map[string]map[string]any{
		"Salt":      {"verdict": "approve", "value": "bag", "confidence": 0.95},
		"Olive oil": {"verdict": "reject", "confidence": 0.9},
		"Flour":     {"verdict": "approve", "value": "bag", "confidence": 0.5},
	}, 1000)
	salt, saltProd := e.item(t, "Salt", "salt", "g", nil)
	oil, _ := e.item(t, "Olive oil", "olive-oil", "bottle", nil)
	flour, _ := e.item(t, "Flour", "flour", "g", nil)
	// Picked by hand and not the unit reported: stays.
	other, otherProd := e.item(t, "Sea salt", "salt", "g", ptr("kg"))

	rs, ro, rf := e.report(t, salt, "bag"), e.report(t, oil, ""), e.report(t, flour, "bag")
	ra := e.report(t, other, "")
	var ctxSnap reports.Context
	var raw []byte
	_ = e.pool.QueryRow(t.Context(), `SELECT context FROM problem_report WHERE id = $1`, rs).Scan(&raw)
	_ = json.Unmarshal(raw, &ctxSnap)
	if ctxSnap.IngredientSlug != "salt" || ctxSnap.Unit != "g" || !ctxSnap.Global || ctxSnap.ProductName != "Salt" {
		t.Fatalf("snapshot: %+v", ctxSnap)
	}

	res, err := e.svc.Review(t.Context(), true)
	if err != nil {
		t.Fatal(err)
	}
	if res.Calls != 1 || res.Applied != 1 || res.Rejected != 1 || res.ToAdmin != 1 || res.Failed != 1 {
		t.Fatalf("run: %+v", res)
	}
	if got := []string{e.status(t, rs), e.status(t, ro), e.status(t, rf), e.status(t, ra)}; got[0] != "applied" || got[1] != "rejected" || got[2] != "needs_admin" || got[3] != "open" {
		t.Fatalf("statuses: %v", got)
	}
	if u := e.unitOf(t, "ingredient", "slug = $1 AND household_id IS NULL", "salt"); u == nil || *u != "bag" {
		t.Fatalf("ingredient unit: %v", u)
	}
	if u := e.unitOf(t, "product", "id = $1", saltProd); u == nil || *u != "bag" {
		t.Fatalf("product unit: %v", u)
	}
	if u := e.unitOf(t, "product", "id = $1", otherProd); u == nil || *u != "kg" {
		t.Fatalf("hand-picked unit changed: %v", u)
	}
	var audits int
	_ = e.pool.QueryRow(t.Context(), `SELECT count(*) FROM audit_event WHERE action = 'catalog.default.update'`).Scan(&audits)
	if audits != 2 {
		t.Fatalf("audit: %d", audits)
	}

	// The admin sees it, with the change and the history.
	o, err := e.svc.List(t.Context(), "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if o.Counts["applied"] != 1 || o.Reports[0].Status != "needs_admin" {
		t.Fatalf("list: %+v", o.Counts)
	}
	rows, hist, err := e.svc.Defaults(t.Context(), "salt", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) == 0 || len(hist) != 2 || hist[0].Actor != reports.Actor {
		t.Fatalf("defaults: %d rows, history %+v", len(rows), hist)
	}

	// The admin applies the one the LLM was unsure about.
	if err := e.svc.Resolve(t.Context(), rf, reports.ActionApply, "bag", "ok"); err != nil {
		t.Fatal(err)
	}
	if e.status(t, rf) != "applied" {
		t.Fatal("not applied")
	}
	if err := e.svc.Resolve(t.Context(), rf, reports.ActionApply, "nope", ""); !errors.Is(err, reports.ErrBadUnit) {
		t.Fatalf("bad unit: %v", err)
	}
	if err := e.svc.Resolve(t.Context(), ro, reports.ActionReopen, "", ""); err != nil || e.status(t, ro) != "open" {
		t.Fatalf("reopen: %v", err)
	}
}

func TestReviewDefersWithoutHeadroom(t *testing.T) {
	e := newEnv(t, map[string]map[string]any{"Salt": {"verdict": "approve", "value": "bag"}}, 10)
	// 6 of 10 requests used by interactive features: only the 40% reserve is left.
	for range 6 {
		if _, err := e.pool.Exec(t.Context(), `INSERT INTO llm_call (time, feature, provider, model, tokens_in, tokens_out, latency_ms, status, cache_hit)
			VALUES (now(), 'x', 'fake', 'm', 1, 1, 1, 'ok', false)`); err != nil {
			t.Fatal(err)
		}
	}
	salt, _ := e.item(t, "Salt", "salt", "g", nil)
	id := e.report(t, salt, "")
	res, err := e.svc.Review(t.Context(), true)
	if err != nil {
		t.Fatal(err)
	}
	if res.Deferred == "" || e.fake.hits.Load() != 0 || e.status(t, id) != "open" {
		t.Fatalf("deferred: %+v hits %d", res, e.fake.hits.Load())
	}
}

func TestSubmitValidatesAndScopes(t *testing.T) {
	e := newEnv(t, nil, 1000)
	salt, _ := e.item(t, "Salt", "salt", "g", nil)
	cases := []struct {
		in   reports.Input
		want error
	}{
		{reports.Input{Kind: "bogus", Source: reports.SourceShoppingItem, SubjectID: salt}, reports.ErrInvalid},
		{reports.Input{Kind: reports.KindWrongUnit, Source: reports.SourceShoppingItem, SubjectID: salt, Suggested: "furlong"}, reports.ErrBadUnit},
		{reports.Input{Kind: reports.KindOther, Source: reports.SourceShoppingItem, SubjectID: salt}, reports.ErrInvalid},
		{reports.Input{Kind: reports.KindWrongUnit, Source: reports.SourceShoppingItem, SubjectID: uuid.New()}, reports.ErrNoSubject},
	}
	for _, c := range cases {
		if _, err := e.svc.Submit(e.ctx, c.in); !errors.Is(err, c.want) {
			t.Errorf("%+v: got %v, want %v", c.in, err, c.want)
		}
	}
	// Another household cannot report this household's item.
	var other uuid.UUID
	_ = e.pool.QueryRow(t.Context(), `INSERT INTO household (id, name) VALUES (gen_random_uuid(), 'Other') RETURNING id`).Scan(&other)
	octx := identity.WithPrincipal(t.Context(), &identity.Principal{UserID: uuid.New(), Status: identity.StatusActive, HouseholdID: other})
	if _, err := e.svc.Submit(octx, reports.Input{Kind: reports.KindWrongUnit, Source: reports.SourceShoppingItem, SubjectID: salt}); !errors.Is(err, reports.ErrNoSubject) {
		t.Fatalf("cross-household: %v", err)
	}
}

func TestAdminSetDefaultUnit(t *testing.T) {
	e := newEnv(t, nil, 1000)
	var id uuid.UUID
	_ = e.pool.QueryRow(t.Context(), `SELECT id FROM ingredient WHERE slug = 'salt' AND household_id IS NULL`).Scan(&id)
	if err := e.svc.SetDefaultUnit(t.Context(), id, ptr("bag")); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.SetDefaultUnit(t.Context(), id, ptr("bag")); err != nil { // no-op, no second audit row
		t.Fatal(err)
	}
	if err := e.svc.SetDefaultUnit(t.Context(), id, ptr("parsec")); !errors.Is(err, reports.ErrBadUnit) {
		t.Fatalf("bad unit: %v", err)
	}
	_, hist, err := e.svc.Defaults(t.Context(), "", 10)
	if err != nil || len(hist) != 1 || hist[0].After == nil || *hist[0].After != "bag" {
		t.Fatalf("history: %+v %v", hist, err)
	}
	if err := e.svc.SetDefaultUnit(t.Context(), id, nil); err != nil {
		t.Fatal(err)
	}
	if u := e.unitOf(t, "ingredient", "id = $1", id); u != nil {
		t.Fatalf("not cleared: %v", *u)
	}
}
