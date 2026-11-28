package chat_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/a-jay98/stockpot/internal/catalog"
	"github.com/a-jay98/stockpot/internal/catalog/resolve"
	"github.com/a-jay98/stockpot/internal/commands"
	"github.com/a-jay98/stockpot/internal/identity"
	"github.com/a-jay98/stockpot/internal/llm"
	"github.com/a-jay98/stockpot/internal/llm/openaicompat"
	"github.com/a-jay98/stockpot/internal/recipes"
	"github.com/a-jay98/stockpot/internal/recipes/chat"
	"github.com/a-jay98/stockpot/internal/recipes/lines"
	"github.com/a-jay98/stockpot/internal/settings"
	"github.com/a-jay98/stockpot/internal/testdb"
)

func TestMain(m *testing.M) { os.Exit(testdb.Main(m)) }

// fakeLLM is an OpenAI-compatible provider that answers with queued replies
// and records each request's messages.
type fakeLLM struct {
	mu       sync.Mutex
	replies  []any
	requests [][]string // message contents per call
}

func (f *fakeLLM) queue(r any) { f.mu.Lock(); f.replies = append(f.replies, r); f.mu.Unlock() }

func (f *fakeLLM) last() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.requests[len(f.requests)-1]
}

func (f *fakeLLM) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Messages []struct{ Content string } `json:"messages"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	f.mu.Lock()
	var msgs []string
	for _, m := range body.Messages {
		msgs = append(msgs, m.Content)
	}
	f.requests = append(f.requests, msgs)
	var reply any = map[string]any{"reply": "ok", "proposal": nil}
	if len(f.replies) > 0 {
		reply, f.replies = f.replies[0], f.replies[1:]
	}
	f.mu.Unlock()
	text, _ := json.Marshal(reply)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"choices": []any{map[string]any{"message": map[string]any{"content": string(text)}, "finish_reason": "stop"}},
		"usage":   map[string]any{"prompt_tokens": 100, "completion_tokens": 50},
	})
}

type env struct {
	pool   *pgxpool.Pool
	cmds   *commands.Service
	svc    *chat.Service
	fake   *fakeLLM
	hh     uuid.UUID
	user   uuid.UUID
	ctx    context.Context
	recipe uuid.UUID
	v1     uuid.UUID
}

func newEnv(t *testing.T, withLLM bool) *env {
	t.Helper()
	pool := testdb.New(t)
	ctx := t.Context()
	if err := catalog.Seed(ctx, pool); err != nil {
		t.Fatal(err)
	}
	reg := commands.NewRegistry()
	h := recipes.Register(reg, resolve.Resolver{})
	h.OnChatVersion = chat.MarkSaved
	e := &env{pool: pool, cmds: commands.NewService(pool, reg), fake: &fakeLLM{}, user: uuid.New(), recipe: uuid.New(), v1: uuid.New()}
	if err := pool.QueryRow(ctx, `INSERT INTO household (id, name) VALUES (gen_random_uuid(), 'Home') RETURNING id`).Scan(&e.hh); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO app_user (id, display_name, status) VALUES ($1, 'AJ', 'active')`, e.user); err != nil {
		t.Fatal(err)
	}
	e.ctx = identity.WithPrincipal(context.Background(), &identity.Principal{UserID: e.user, Status: identity.StatusActive, HouseholdID: e.hh})

	store, err := settings.NewStore(pool, []byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Load(ctx); err != nil {
		t.Fatal(err)
	}
	if withLLM {
		srv := httptest.NewServer(e.fake)
		t.Cleanup(srv.Close)
		raw, _ := json.Marshal([]llm.ProviderConfig{{
			ID: "fake", Kind: llm.KindOpenAICompat, BaseURL: srv.URL, Model: "m", Enabled: true, ResetTZ: "UTC", JSONSchema: true,
		}})
		if err := store.Set(ctx, llm.SettingProviders.Key(), raw, nil); err != nil {
			t.Fatal(err)
		}
	}
	router := llm.NewRouter(llm.Deps{
		Pool: pool, Settings: store,
		Factories: map[string]llm.Factory{llm.KindOpenAICompat: openaicompat.Factory(nil)},
	})
	if withLLM {
		if err := router.SetAPIKey(ctx, "fake", "k"); err != nil {
			t.Fatal(err)
		}
	}
	e.svc = &chat.Service{Pool: pool, LLM: router}

	e.apply(t, "recipe.create", recipes.Create{ID: e.recipe, VersionID: e.v1, Content: recipes.Content{
		Title: "Khoresh gheymeh", Ingredients: []string{"1 tbsp tomato paste", "For the stew:", "500 g beef", "1 cup split peas"},
		Steps: []recipes.Step{{Text: "Fry the onion."}, {Text: "Add tomato paste.", Minutes: ptr(2)}, {Text: "Simmer."}},
	}})
	return e
}

func ptr[T any](v T) *T { return &v }

func (e *env) apply(t *testing.T, typ string, payload any) commands.Result {
	t.Helper()
	b, _ := json.Marshal(payload)
	rs, err := e.cmds.Apply(e.ctx, e.hh, []commands.Command{{ID: uuid.New(), Type: typ, Payload: b, ClientTime: time.Now()}})
	if err != nil {
		t.Fatal(err)
	}
	if rs[0].Status != commands.StatusApplied {
		t.Fatalf("%s: %+v", typ, rs[0])
	}
	return rs[0]
}

func (e *env) post(t *testing.T, text string) []chat.Message {
	t.Helper()
	ms, err := e.svc.Post(e.ctx, e.hh, e.user, e.recipe, text)
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) != 2 || ms[0].Role != chat.RoleUser || ms[1].Role != chat.RoleAssistant {
		t.Fatalf("post returned %+v", ms)
	}
	return ms
}

func (e *env) message(t *testing.T, id uuid.UUID) chat.Message {
	t.Helper()
	ms, err := e.svc.List(e.ctx, e.hh, e.recipe)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range ms {
		if m.ID == id {
			return m
		}
	}
	t.Fatalf("message %s not listed", id)
	return chat.Message{}
}

func noChange() map[string]any {
	return map[string]any{"change_summary": "", "title": nil, "servings": nil, "total_minutes": nil, "ingredients": nil, "steps": nil, "notes": nil}
}

func proposal(fields map[string]any) map[string]any {
	p := noChange()
	for k, v := range fields {
		p[k] = v
	}
	return p
}

func TestQuestionWithoutProposal(t *testing.T) {
	e := newEnv(t, true)
	if _, err := e.pool.Exec(e.ctx, `INSERT INTO cook_session (id, household_id, recipe_id, version_id, cooked_on, note)
		VALUES (gen_random_uuid(), $1, $2, $3, '2026-11-20', 'Too salty with stock cubes')`, e.hh, e.recipe, e.v1); err != nil {
		t.Fatal(err)
	}
	e.fake.queue(map[string]any{"reply": "Yes, it freezes well for 3 months.", "proposal": nil})
	ms := e.post(t, "  Can I freeze this?  ")
	if ms[0].Text != "Can I freeze this?" || ms[0].UserID == nil || *ms[0].UserID != e.user || ms[0].VersionN != 1 {
		t.Fatalf("user message %+v", ms[0])
	}
	if ms[1].Text != "Yes, it freezes well for 3 months." || ms[1].Proposal != nil || ms[1].State != chat.StateNone {
		t.Fatalf("reply %+v", ms[1])
	}
	req := e.fake.last()
	ctxMsg := req[1]
	for _, want := range []string{`"title":"Khoresh gheymeh"`, `"For the stew:"`, `"minutes":2`, "Too salty with stock cubes"} {
		if !strings.Contains(ctxMsg, want) {
			t.Fatalf("context lacks %s: %s", want, ctxMsg)
		}
	}
	if req[len(req)-1] != "Can I freeze this?" {
		t.Fatalf("last turn %q", req[len(req)-1])
	}

	// The next call carries the conversation so far.
	e.post(t, "And thaw?")
	req = e.fake.last()
	if !slicesContain(req, "Can I freeze this?") || !slicesContain(req, "Yes, it freezes well for 3 months.") {
		t.Fatalf("history missing: %q", req)
	}
	var audits int
	if err := e.pool.QueryRow(e.ctx, `SELECT count(*) FROM audit_event WHERE action = 'recipe.chat.post' AND target_id = $1`, e.recipe.String()).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if audits != 2 {
		t.Fatalf("audit events %d", audits)
	}
}

func slicesContain(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

func TestProposalSavedAsNewVersion(t *testing.T) {
	e := newEnv(t, true)
	e.fake.queue(map[string]any{"reply": "I'd use 2 tbsp and fry it 2 minutes.", "proposal": proposal(map[string]any{
		"change_summary": "2 tbsp tomato paste, fried 2 minutes",
		"ingredients":    []string{"2 tbsp tomato paste", "For the stew:", "500 g beef", "1 cup split peas"},
		"steps":          []string{"Fry the onion.", "Add tomato paste.", "Fry the paste 2 minutes.", "Simmer."},
		"notes":          "Richer with more paste.",
	})})
	ms := e.post(t, "Tried it with 2 tbsp tomato paste and it was richer")
	p := ms[1].Proposal
	if p == nil || ms[1].State != chat.StatePending {
		t.Fatalf("reply %+v", ms[1])
	}
	if p.Title != "Khoresh gheymeh" || p.Ingredients[0] != "2 tbsp tomato paste" || p.Steps[1].Minutes == nil || *p.Steps[1].Minutes != 2 ||
		p.Steps[2].Minutes != nil || p.Notes == nil || *p.Notes != "Richer with more paste." {
		t.Fatalf("proposal not merged onto v1: %+v", p)
	}

	v2 := uuid.New()
	e.apply(t, "recipe.version.create", recipes.VersionCreate{
		RecipeID: e.recipe, VersionID: v2, Origin: recipes.OriginChat, ParentVersionID: &ms[1].VersionID,
		FromMessageID: &ms[1].ID, ChangeSummary: &p.ChangeSummary, Content: p.Content,
	})
	m := e.message(t, ms[1].ID)
	if m.State != chat.StateSaved || m.SavedVersionID == nil || *m.SavedVersionID != v2 || *m.SavedVersionN != 2 {
		t.Fatalf("message after save %+v", m)
	}
	var (
		n             int
		origin, title string
		raw           []byte
	)
	if err := e.pool.QueryRow(e.ctx, `SELECT n, origin, title, ingredients FROM recipe_version WHERE id = $1`, v2).Scan(&n, &origin, &title, &raw); err != nil {
		t.Fatal(err)
	}
	var v2Lines, v1Lines []lines.Line
	_ = json.Unmarshal(raw, &v2Lines)
	if err := e.pool.QueryRow(e.ctx, `SELECT ingredients FROM recipe_version WHERE id = $1`, e.v1).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	_ = json.Unmarshal(raw, &v1Lines)
	if n != 2 || origin != "chat" || v2Lines[1].Group != "For the stew" || v2Lines[1].LineID != v1Lines[1].LineID {
		t.Fatalf("v2 n=%d origin=%s lines=%+v", n, origin, v2Lines)
	}

	// A replayed save keeps the first version; dismissing a saved proposal is refused.
	e.markSaved(t, e.hh, ms[1].ID, e.v1)
	if m := e.message(t, ms[1].ID); *m.SavedVersionID != v2 {
		t.Fatalf("replay moved the saved version to %s", *m.SavedVersionID)
	}
	if _, err := e.svc.Dismiss(e.ctx, e.hh, e.recipe, ms[1].ID); !errors.Is(err, chat.ErrSaved) {
		t.Fatalf("dismiss saved: %v", err)
	}
}

// markSaved runs the version hook in its own committed transaction.
func (e *env) markSaved(t *testing.T, hh, msg, version uuid.UUID) {
	t.Helper()
	err := pgx.BeginFunc(e.ctx, e.pool, func(tx pgx.Tx) error { return chat.MarkSaved(e.ctx, tx, hh, msg, version) })
	if err != nil {
		t.Fatal(err)
	}
}

func TestStaleBaseProposal(t *testing.T) {
	e := newEnv(t, true)
	e.fake.queue(map[string]any{"reply": "Serves 2 then.", "proposal": proposal(map[string]any{"change_summary": "For two", "servings": 2})})
	ms := e.post(t, "Make it for 2")
	p := ms[1].Proposal
	if p == nil || *p.Servings != 2 {
		t.Fatalf("proposal %+v", p)
	}
	// Someone edits the recipe by hand before the proposal is saved.
	e.apply(t, "recipe.version.create", recipes.VersionCreate{
		RecipeID: e.recipe, VersionID: uuid.New(), Origin: recipes.OriginManual,
		Content: recipes.Content{Title: "Gheymeh", Ingredients: []string{"500 g lamb"}},
	})
	v3 := uuid.New()
	e.apply(t, "recipe.version.create", recipes.VersionCreate{
		RecipeID: e.recipe, VersionID: v3, Origin: recipes.OriginChat, ParentVersionID: &ms[1].VersionID,
		FromMessageID: &ms[1].ID, ChangeSummary: &p.ChangeSummary, Content: p.Content,
	})
	m := e.message(t, ms[1].ID)
	if m.State != chat.StateSaved || *m.SavedVersionN != 3 || m.VersionN != 1 {
		t.Fatalf("stale save %+v", m)
	}
	var title string
	var servings int
	if err := e.pool.QueryRow(e.ctx, `SELECT title, servings FROM recipe WHERE id = $1`, e.recipe).Scan(&title, &servings); err != nil {
		t.Fatal(err)
	}
	if title != "Khoresh gheymeh" || servings != 2 {
		t.Fatalf("recipe now %q %d: the snapshot must win, nothing merges", title, servings)
	}
	// New messages are about the current version.
	ms = e.post(t, "Thanks")
	if ms[0].VersionN != 3 {
		t.Fatalf("new message version %d", ms[0].VersionN)
	}
}

func TestLLMUnavailable(t *testing.T) {
	e := newEnv(t, false)
	_, err := e.svc.Post(e.ctx, e.hh, e.user, e.recipe, "Can I freeze this?")
	if !errors.Is(err, llm.ErrUnavailable) {
		t.Fatalf("err %v, want llm.ErrUnavailable", err)
	}
	ms, err := e.svc.List(e.ctx, e.hh, e.recipe)
	if err != nil || len(ms) != 0 {
		t.Fatalf("stored on failure: %v %+v", err, ms)
	}
}

func TestDismissAndAuthz(t *testing.T) {
	e := newEnv(t, true)
	e.fake.queue(map[string]any{"reply": "Swap in lamb?", "proposal": proposal(map[string]any{"change_summary": "Lamb", "ingredients": []string{"500 g lamb"}})})
	ms := e.post(t, "We used lamb")
	for range 2 { // idempotent
		m, err := e.svc.Dismiss(e.ctx, e.hh, e.recipe, ms[1].ID)
		if err != nil || m.State != chat.StateDismissed || m.Proposal == nil {
			t.Fatalf("dismiss: %v %+v", err, m)
		}
	}
	if _, err := e.svc.Dismiss(e.ctx, e.hh, e.recipe, ms[0].ID); !errors.Is(err, chat.ErrInvalid) {
		t.Fatalf("dismiss a user message: %v", err)
	}

	other := uuid.New()
	if _, err := e.pool.Exec(e.ctx, `INSERT INTO household (id, name) VALUES ($1, 'Other')`, other); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.List(e.ctx, other, e.recipe); !errors.Is(err, chat.ErrNotFound) {
		t.Fatalf("other household list: %v", err)
	}
	if _, err := e.svc.Post(e.ctx, other, e.user, e.recipe, "hi"); !errors.Is(err, chat.ErrNotFound) {
		t.Fatalf("other household post: %v", err)
	}
	if _, err := e.svc.Dismiss(e.ctx, other, e.recipe, ms[1].ID); !errors.Is(err, chat.ErrNotFound) {
		t.Fatalf("other household dismiss: %v", err)
	}
	// Another household's version create cannot mark this message saved.
	e.markSaved(t, other, ms[1].ID, e.v1)
	if m := e.message(t, ms[1].ID); m.State != chat.StateDismissed {
		t.Fatalf("state %s", m.State)
	}
	if _, err := e.svc.Post(e.ctx, e.hh, e.user, e.recipe, strings.Repeat("x", chat.MaxText+1)); !errors.Is(err, chat.ErrInvalid) {
		t.Fatalf("long text: %v", err)
	}
}

func TestUselessProposalsAreDropped(t *testing.T) {
	e := newEnv(t, true)
	e.fake.queue(map[string]any{"reply": "Same as before.", "proposal": proposal(map[string]any{"change_summary": "Nothing", "title": "Khoresh gheymeh"})})
	e.fake.queue(map[string]any{"reply": "Huge.", "proposal": proposal(map[string]any{"change_summary": "Too many", "servings": 1000})})
	for _, q := range []string{"same?", "for 1000"} {
		ms := e.post(t, q)
		if ms[1].Proposal != nil || ms[1].State != chat.StateNone {
			t.Fatalf("%s: proposal kept %+v", q, ms[1].Proposal)
		}
	}
}
