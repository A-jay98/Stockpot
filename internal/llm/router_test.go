package llm_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/a-jay98/stockpot/internal/blob"
	"github.com/a-jay98/stockpot/internal/llm"
	"github.com/a-jay98/stockpot/internal/llm/openaicompat"
	"github.com/a-jay98/stockpot/internal/reqctx"
	"github.com/a-jay98/stockpot/internal/settings"
	"github.com/a-jay98/stockpot/internal/testdb"
)

func TestMain(m *testing.M) { os.Exit(testdb.Main(m)) }

// fake is an OpenAI-compatible server whose replies a test scripts.
type fake struct {
	srv    *httptest.Server
	hits   atomic.Int32
	mu     sync.Mutex
	handle func(w http.ResponseWriter, body map[string]any)
	last   map[string]any
}

func newFake(t *testing.T, handle func(w http.ResponseWriter, body map[string]any)) *fake {
	t.Helper()
	f := &fake{handle: handle}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.hits.Add(1)
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		f.last = body
		h := f.handle
		f.mu.Unlock()
		h(w, body)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fake) set(h func(w http.ResponseWriter, body map[string]any)) {
	f.mu.Lock()
	f.handle = h
	f.mu.Unlock()
}

func ok(text string, tokensIn, tokensOut int) func(http.ResponseWriter, map[string]any) {
	return func(w http.ResponseWriter, _ map[string]any) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{"content": text}, "finish_reason": "stop"}},
			"usage":   map[string]any{"prompt_tokens": tokensIn, "completion_tokens": tokensOut},
		})
	}
}

func status(code int, body string) func(http.ResponseWriter, map[string]any) {
	return func(w http.ResponseWriter, _ map[string]any) {
		w.WriteHeader(code)
		_, _ = io.WriteString(w, body)
	}
}

const dailyQuota = `{"error":{"message":"Rate limit reached for model on requests per day (RPD): Limit 1000"}}`

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

type env struct {
	router *llm.Router
	store  *settings.Store
	pool   *pgxpool.Pool
	clock  *clock
	blobs  blob.Store
}

// provider is a ready test provider config pointing at f.
func provider(id string, f *fake) llm.ProviderConfig {
	return llm.ProviderConfig{ID: id, Kind: llm.KindOpenAICompat, BaseURL: f.srv.URL, Model: id + "-model", Enabled: true, ResetTZ: "America/Los_Angeles"}
}

func setup(t *testing.T, providers ...llm.ProviderConfig) *env {
	t.Helper()
	ctx := t.Context()
	pool := testdb.New(t)
	store, err := settings.NewStore(pool, []byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Load(ctx); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(providers)
	if err := store.Set(ctx, llm.SettingProviders.Key(), raw, nil); err != nil {
		t.Fatal(err)
	}
	blobs, err := blob.NewFS(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	c := &clock{t: time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)} // 04:00 in Los Angeles
	r := llm.NewRouter(llm.Deps{
		Pool: pool, Settings: store, Blobs: blobs, Now: c.now,
		Factories: map[string]llm.Factory{llm.KindOpenAICompat: openaicompat.Factory(nil)},
	})
	for _, p := range providers {
		if err := r.SetAPIKey(ctx, p.ID, "key-"+p.ID); err != nil {
			t.Fatal(err)
		}
	}
	return &env{router: r, store: store, pool: pool, clock: c, blobs: blobs}
}

func ask(text string) llm.Request {
	return llm.Request{Feature: llm.Feature{Name: "test"}, Messages: []llm.Message{{Role: llm.RoleUser, Text: text}}}
}

type callRow struct {
	Provider, Status string
	TokensIn         int
	CacheHit         bool
	UserID           *uuid.UUID
}

func calls(t *testing.T, e *env) []callRow {
	t.Helper()
	rows, err := e.pool.Query(t.Context(), "SELECT provider, status, tokens_in, cache_hit, user_id FROM llm_call ORDER BY id")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []callRow
	for rows.Next() {
		var c callRow
		if err := rows.Scan(&c.Provider, &c.Status, &c.TokensIn, &c.CacheHit, &c.UserID); err != nil {
			t.Fatal(err)
		}
		out = append(out, c)
	}
	return out
}

func TestCompleteMetersTheCall(t *testing.T) {
	a := newFake(t, ok("hi there", 10, 3))
	e := setup(t, provider("a", a))
	user := uuid.New()
	ctx := reqctx.With(t.Context(), &reqctx.Info{UserID: user})

	resp, err := e.router.Complete(ctx, ask("hello"))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != "hi there" || resp.Provider != "a" || resp.Model != "a-model" || resp.Cached {
		t.Fatalf("resp = %+v", resp)
	}
	got := calls(t, e)
	if len(got) != 1 || got[0].Status != "ok" || got[0].TokensIn != 10 || got[0].UserID == nil || *got[0].UserID != user {
		t.Fatalf("llm_call = %+v", got)
	}
}

const itemSchema = `{"type":"object","required":["name","qty"],"properties":{"name":{"type":"string"},"qty":{"type":"number"}},"additionalProperties":false}`

func TestStructuredOutput(t *testing.T) {
	a := newFake(t, ok("```json\n{\"name\": \"milk\", \"qty\": 2}\n```", 5, 5))
	b := newFake(t, ok(`{"name":"eggs","qty":12}`, 5, 5))
	e := setup(t, provider("a", a), provider("b", b))
	req := ask("2L milk")
	req.Schema = json.RawMessage(itemSchema)

	resp, err := e.router.Complete(t.Context(), req)
	if err != nil || string(resp.JSON) != `{"name":"milk","qty":2}` {
		t.Fatalf("resp %+v err %v", resp, err)
	}
	if a.last["response_format"] == nil {
		t.Fatal("structured request sent without response_format")
	}

	// a's reply breaks the schema: b answers.
	a.set(ok(`{"name":"milk"}`, 5, 5))
	resp, err = e.router.Complete(t.Context(), req)
	if err != nil || resp.Provider != "b" || string(resp.JSON) != `{"name":"eggs","qty":12}` {
		t.Fatalf("resp %+v err %v", resp, err)
	}

	// Both break it: ErrBadOutput, and a is not penalized for it.
	b.set(ok(`not json`, 5, 5))
	if _, err := e.router.Complete(t.Context(), req); !errors.Is(err, llm.ErrBadOutput) || errors.Is(err, llm.ErrUnavailable) {
		t.Fatalf("err = %v", err)
	}
	st, _ := e.router.Status(t.Context())
	if st.Providers[0].State != "ready" {
		t.Fatalf("a after bad output: %+v", st.Providers[0])
	}

	req.Schema = json.RawMessage(`{"type": 12}`)
	if _, err := e.router.Complete(t.Context(), req); !errors.Is(err, llm.ErrInvalidRequest) {
		t.Fatalf("invalid schema: %v", err)
	}
}

func TestQuotaFailoverAndReset(t *testing.T) {
	a := newFake(t, status(http.StatusTooManyRequests, dailyQuota))
	b := newFake(t, ok("from b", 1, 1))
	e := setup(t, provider("a", a), provider("b", b))

	for range 2 {
		resp, err := e.router.Complete(t.Context(), ask("x"))
		if err != nil || resp.Provider != "b" {
			t.Fatalf("resp %+v err %v", resp, err)
		}
	}
	if a.hits.Load() != 1 {
		t.Fatalf("exhausted provider called %d times", a.hits.Load())
	}
	st, err := e.router.Status(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	wantReset := time.Date(2026, 11, 24, 8, 0, 0, 0, time.UTC) // midnight in Los Angeles
	if p := st.Providers[0]; p.State != "exhausted" || p.Until == nil || !p.Until.Equal(wantReset) {
		t.Fatalf("a = %+v", p)
	}
	// The refused attempt doesn't count against a's quota.
	if st.Providers[0].RequestsToday != 0 || st.Providers[1].RequestsToday != 2 {
		t.Fatalf("usage a=%d b=%d", st.Providers[0].RequestsToday, st.Providers[1].RequestsToday)
	}

	// After a's reset it is tried again.
	a.set(ok("from a", 1, 1))
	e.clock.add(20 * time.Hour)
	if resp, err := e.router.Complete(t.Context(), ask("x")); err != nil || resp.Provider != "a" {
		t.Fatalf("after reset: %+v %v", resp, err)
	}

	// A new key lifts the block before the reset.
	a.set(status(http.StatusTooManyRequests, dailyQuota))
	if resp, _ := e.router.Complete(t.Context(), ask("x")); resp.Provider != "b" {
		t.Fatalf("not failed over: %+v", resp)
	}
	a.set(ok("from a", 1, 1))
	if err := e.router.SetAPIKey(t.Context(), "a", "new-key"); err != nil {
		t.Fatal(err)
	}
	if resp, err := e.router.Complete(t.Context(), ask("x")); err != nil || resp.Provider != "a" {
		t.Fatalf("after new key: %+v %v", resp, err)
	}
}

func TestPerMinuteQuotaUsesRetryHint(t *testing.T) {
	a := newFake(t, func(w http.ResponseWriter, _ map[string]any) {
		w.Header().Set("Retry-After", "30")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"error":{"message":"requests per minute"}}`)
	})
	e := setup(t, provider("a", a))
	if _, err := e.router.Complete(t.Context(), ask("x")); !errors.Is(err, llm.ErrUnavailable) {
		t.Fatalf("err = %v", err)
	}
	a.set(ok("back", 1, 1))
	e.clock.add(10 * time.Second)
	if _, err := e.router.Complete(t.Context(), ask("x")); !errors.Is(err, llm.ErrUnavailable) {
		t.Fatalf("within retry window: %v", err)
	}
	e.clock.add(25 * time.Second)
	if _, err := e.router.Complete(t.Context(), ask("x")); err != nil {
		t.Fatalf("after retry window: %v", err)
	}
}

func TestCapsAreEnforced(t *testing.T) {
	a := newFake(t, ok("a", 40, 10))
	b := newFake(t, ok("b", 1, 1))
	pa, pb := provider("a", a), provider("b", b)
	pa.DailyRequestCap = 2
	pb.DailyTokenCap = 3
	e := setup(t, pa, pb)

	for i, want := range []string{"a", "a", "b", "b"} {
		resp, err := e.router.Complete(t.Context(), ask("x"))
		if err != nil || resp.Provider != want {
			t.Fatalf("call %d: %+v %v", i, resp, err)
		}
	}
	// b has spent 4 of 3 tokens (the cap is checked before calling).
	if _, err := e.router.Complete(t.Context(), ask("x")); !errors.Is(err, llm.ErrUnavailable) || !strings.Contains(err.Error(), "daily cap") {
		t.Fatalf("err = %v", err)
	}
	if a.hits.Load() != 2 {
		t.Fatalf("a called %d times past its cap", a.hits.Load())
	}
	st, _ := e.router.Status(t.Context())
	if p := st.Providers[0]; p.State != "capped" || p.RemainingRequests == nil || *p.RemainingRequests != 0 {
		t.Fatalf("a = %+v", p)
	}
	if st.Available {
		t.Fatal("status says available with every provider capped")
	}
}

func TestGlobalCap(t *testing.T) {
	a := newFake(t, ok("a", 1, 1))
	e := setup(t, provider("a", a))
	if err := settings.SetValue(t.Context(), e.store, llm.SettingGlobalDailyRequestCap, 1, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := e.router.Complete(t.Context(), ask("x")); err != nil {
		t.Fatal(err)
	}
	if _, err := e.router.Complete(t.Context(), ask("y")); !errors.Is(err, llm.ErrUnavailable) || !strings.Contains(err.Error(), "global") {
		t.Fatalf("err = %v", err)
	}
	if a.hits.Load() != 1 {
		t.Fatalf("hits = %d", a.hits.Load())
	}
}

func TestCache(t *testing.T) {
	a := newFake(t, ok("cached answer", 7, 7))
	e := setup(t, provider("a", a))
	req := ask("  parse this\r\nrecipe ")
	req.Feature = llm.Feature{Name: "recipe_parse", CacheTTL: time.Hour}

	first, err := e.router.Complete(t.Context(), req)
	if err != nil || first.Cached {
		t.Fatalf("first %+v %v", first, err)
	}
	// Same request after normalization: served from the cache.
	again := ask("parse this\nrecipe")
	again.Feature = req.Feature
	second, err := e.router.Complete(t.Context(), again)
	if err != nil || !second.Cached || second.Text != "cached answer" || second.Provider != "a" {
		t.Fatalf("second %+v %v", second, err)
	}
	if a.hits.Load() != 1 {
		t.Fatalf("provider called %d times", a.hits.Load())
	}
	got := calls(t, e)
	if len(got) != 2 || got[1].Status != "ok" || !got[1].CacheHit || got[1].TokensIn != 0 {
		t.Fatalf("llm_call = %+v", got)
	}
	st, _ := e.router.Status(t.Context())
	if st.RequestsToday != 1 {
		t.Fatalf("cache hit counted against quota: %d", st.RequestsToday)
	}

	// Another feature, or the TTL passing, misses.
	other := again
	other.Feature = llm.Feature{Name: "other", CacheTTL: time.Hour}
	if r, _ := e.router.Complete(t.Context(), other); r.Cached {
		t.Fatal("cache shared across features")
	}
	e.clock.add(2 * time.Hour)
	if r, _ := e.router.Complete(t.Context(), again); r.Cached {
		t.Fatal("expired entry served")
	}

	// Retention prunes expired entries.
	var n int
	for _, s := range llm.Sweepers(e.pool) {
		if s.Name == "llm_cache" {
			if _, err := s.Sweep(t.Context(), e.clock.now().Add(2*time.Hour)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := e.pool.QueryRow(t.Context(), "SELECT count(*) FROM llm_cache").Scan(&n); err != nil || n != 0 {
		t.Fatalf("cache rows after sweep = %d (%v)", n, err)
	}
}

func TestUnavailable(t *testing.T) {
	a := newFake(t, status(http.StatusBadGateway, "down"))
	b := newFake(t, status(http.StatusInternalServerError, "down"))
	e := setup(t, provider("a", a), provider("b", b))

	if _, err := e.router.Complete(t.Context(), ask("x")); !errors.Is(err, llm.ErrUnavailable) {
		t.Fatalf("all failing: %v", err)
	}
	// Both cool down: no calls for a while.
	if _, err := e.router.Complete(t.Context(), ask("x")); !errors.Is(err, llm.ErrUnavailable) || a.hits.Load() != 1 || b.hits.Load() != 1 {
		t.Fatalf("cooling down: %v (hits %d, %d)", err, a.hits.Load(), b.hits.Load())
	}

	// No key: skipped without a call.
	if err := e.router.SetAPIKey(t.Context(), "a", ""); err != nil {
		t.Fatal(err)
	}
	a.set(ok("a", 1, 1))
	if _, err := e.router.Complete(t.Context(), ask("x")); !errors.Is(err, llm.ErrUnavailable) || !strings.Contains(err.Error(), "no API key") || a.hits.Load() != 1 {
		t.Fatalf("no key: %v", err)
	}

	// Flag off: nothing is called at all.
	if err := e.router.SetAPIKey(t.Context(), "a", "k"); err != nil {
		t.Fatal(err)
	}
	if err := settings.SetValue(t.Context(), e.store, llm.FlagEnabled, false, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := e.router.Complete(t.Context(), ask("x")); !errors.Is(err, llm.ErrUnavailable) || a.hits.Load() != 1 {
		t.Fatalf("flag off: %v", err)
	}

	if err := e.router.SetAPIKey(t.Context(), "nope", "k"); !errors.Is(err, llm.ErrUnknownProvider) {
		t.Fatalf("unknown provider: %v", err)
	}
}

func TestVisionRouting(t *testing.T) {
	text := newFake(t, ok("text only", 1, 1))
	vision := newFake(t, ok("a carton of milk", 1, 1))
	pt, pv := provider("text", text), provider("vision", vision)
	pv.Vision = true
	e := setup(t, pt, pv)

	ref, _, err := e.blobs.Put(t.Context(), bytes.NewReader([]byte("fake png")))
	if err != nil {
		t.Fatal(err)
	}
	req := llm.Request{Feature: llm.Feature{Name: "receipt"}, Messages: []llm.Message{{
		Role: llm.RoleUser, Text: "what is this", Images: []llm.Image{{Ref: ref, MIME: "image/png"}},
	}}}
	resp, err := e.router.Complete(t.Context(), req)
	if err != nil || resp.Provider != "vision" || text.hits.Load() != 0 {
		t.Fatalf("resp %+v err %v", resp, err)
	}
	parts, _ := vision.last["messages"].([]any)[0].(map[string]any)["content"].([]any)
	if url, _ := parts[1].(map[string]any)["image_url"].(map[string]any)["url"].(string); url != "data:image/png;base64,ZmFrZSBwbmc=" {
		t.Fatalf("image part = %v", parts)
	}
	if req.Messages[0].Images[0].Data != nil {
		t.Fatal("caller's request was modified")
	}

	req.Messages[0].Images[0].Ref = blob.Ref(strings.Repeat("0", 64))
	if _, err := e.router.Complete(t.Context(), req); !errors.Is(err, llm.ErrInvalidRequest) {
		t.Fatalf("missing blob: %v", err)
	}
}

func TestPinnedProviderAndUsage(t *testing.T) {
	a := newFake(t, ok("a", 3, 4))
	b := newFake(t, ok("b", 5, 6))
	e := setup(t, provider("a", a), provider("b", b))

	resp, err := e.router.TestPrompt(t.Context(), "b", "ping")
	if err != nil || resp.Provider != "b" || a.hits.Load() != 0 {
		t.Fatalf("pinned: %+v %v", resp, err)
	}
	if _, err := e.router.TestPrompt(t.Context(), "zzz", "ping"); !errors.Is(err, llm.ErrUnavailable) {
		t.Fatalf("unknown pinned provider: %v", err)
	}
	if _, err := e.router.Complete(t.Context(), ask("x")); err != nil {
		t.Fatal(err)
	}

	rows, tz, err := e.router.Usage(t.Context(), 7)
	if err != nil {
		t.Fatal(err)
	}
	if tz != "America/Toronto" || len(rows) != 2 {
		t.Fatalf("usage tz %s rows %+v", tz, rows)
	}
	r0, r1 := rows[0], rows[1]
	if r0.Provider != "a" || r0.Feature != "test" || r0.Requests != 1 || r0.TokensOut != 4 ||
		r1.Provider != "b" || r1.Feature != "admin_test" || r1.TokensIn != 5 ||
		r0.Day.Format(time.DateOnly) != "2026-11-23" {
		t.Fatalf("rows = %+v", rows)
	}
	st, _ := e.router.Status(t.Context())
	if !st.Available || st.RequestsToday != 2 || st.TokensToday != 18 || st.GlobalRemaining == nil || *st.GlobalRemaining != 998 {
		t.Fatalf("status = %+v", st)
	}
}

func TestDefaultProvidersNeedKeys(t *testing.T) {
	pool := testdb.New(t)
	store, err := settings.NewStore(pool, []byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Load(t.Context()); err != nil {
		t.Fatal(err)
	}
	r := llm.NewRouter(llm.Deps{Pool: pool, Settings: store, Factories: map[string]llm.Factory{
		llm.KindOpenAICompat: func(llm.ProviderConfig, string) (llm.Provider, error) {
			t.Fatal("a provider without a key was built")
			return nil, nil
		},
	}})
	st, err := r.Status(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Providers) != 2 || st.Providers[0].Config.ID != "gemini" || st.Providers[0].State != "no_key" || st.Available {
		t.Fatalf("status = %+v", st)
	}
	if _, err := r.Complete(context.Background(), ask("x")); !errors.Is(err, llm.ErrUnavailable) {
		t.Fatalf("err = %v", err)
	}
}

func TestProviderSettingValidation(t *testing.T) {
	s := setup(t).store
	good := llm.DefaultProviders[0]
	cases := map[string]func(p *llm.ProviderConfig){
		"bad id":       func(p *llm.ProviderConfig) { p.ID = "Gemini!" },
		"bad kind":     func(p *llm.ProviderConfig) { p.Kind = "soap" },
		"plain http":   func(p *llm.ProviderConfig) { p.BaseURL = "http://example.com/v1" },
		"creds in url": func(p *llm.ProviderConfig) { p.BaseURL = "https://u:p@example.com" },
		"no model":     func(p *llm.ProviderConfig) { p.Model = "" },
		"bad tz":       func(p *llm.ProviderConfig) { p.ResetTZ = "Mars/Olympus" },
		"negative cap": func(p *llm.ProviderConfig) { p.DailyRequestCap = -1 },
	}
	for name, mut := range cases {
		p := good
		mut(&p)
		raw, _ := json.Marshal([]llm.ProviderConfig{p})
		var ve *settings.ValidationError
		if err := s.Set(t.Context(), llm.SettingProviders.Key(), raw, nil); !errors.As(err, &ve) {
			t.Errorf("%s: %v", name, err)
		}
	}
	raw, _ := json.Marshal([]llm.ProviderConfig{good, good})
	if err := s.Set(t.Context(), llm.SettingProviders.Key(), raw, nil); err == nil {
		t.Error("duplicate ids accepted")
	}
}
