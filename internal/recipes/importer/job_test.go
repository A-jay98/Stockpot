package importer

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
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/a-jay98/stockpot/internal/blob"
	"github.com/a-jay98/stockpot/internal/catalog"
	"github.com/a-jay98/stockpot/internal/catalog/resolve"
	"github.com/a-jay98/stockpot/internal/commands"
	"github.com/a-jay98/stockpot/internal/identity"
	"github.com/a-jay98/stockpot/internal/jobs"
	"github.com/a-jay98/stockpot/internal/llm"
	"github.com/a-jay98/stockpot/internal/recipes"
	"github.com/a-jay98/stockpot/internal/settings"
	"github.com/a-jay98/stockpot/internal/testdb"
)

func TestMain(m *testing.M) { os.Exit(testdb.Main(m)) }

// fakeLLM answers by feature from scripted replies.
type fakeLLM struct {
	mu       sync.Mutex
	replies  map[string][]string // feature → JSON replies, in order
	err      error
	requests []llm.Request
	audio    []llm.TranscribeRequest
	script   string
}

func (f *fakeLLM) Complete(_ context.Context, req llm.Request) (llm.Response, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, req)
	if f.err != nil {
		return llm.Response{}, f.err
	}
	rs := f.replies[req.Feature.Name]
	if len(rs) == 0 {
		return llm.Response{}, errors.New("unexpected llm call " + req.Feature.Name)
	}
	f.replies[req.Feature.Name] = rs[1:]
	return llm.Response{JSON: json.RawMessage(rs[0]), Text: rs[0]}, nil
}

func (f *fakeLLM) Transcribe(_ context.Context, req llm.TranscribeRequest) (llm.Transcription, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.audio = append(f.audio, req)
	return llm.Transcription{Text: f.script}, nil
}

func (f *fakeLLM) features() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, r := range f.requests {
		out = append(out, r.Feature.Name)
	}
	return out
}

type fakeReels struct {
	reel Reel
	err  error
	hits int
}

func (f *fakeReels) Read(_ context.Context, _ string, withAudio bool) (Reel, error) {
	f.hits++
	r := f.reel
	if !withAudio {
		r.Audio = nil
	}
	return r, f.err
}

func (f *fakeReels) Status(context.Context) ToolStatus {
	return ToolStatus{YtDLP: true, YtDLPVersion: "test"}
}

type env struct {
	pool  *pgxpool.Pool
	store *settings.Store
	svc   *Service
	cmds  *commands.Service
	llm   *fakeLLM
	reels *fakeReels
	blobs blob.Store
	site  *httptest.Server
	hh    uuid.UUID
	ctx   context.Context
}

func newEnv(t *testing.T) *env {
	t.Helper()
	pool := testdb.New(t)
	ctx := t.Context()
	if err := catalog.Seed(ctx, pool); err != nil {
		t.Fatal(err)
	}
	store, err := settings.NewStore(pool, []byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Load(ctx); err != nil {
		t.Fatal(err)
	}
	blobs, err := blob.NewFS(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	e := &env{pool: pool, store: store, llm: &fakeLLM{replies: map[string][]string{}}, reels: &fakeReels{}, blobs: blobs}
	mux := http.NewServeMux()
	for _, f := range []string{"bbcgoodfood", "marmiton"} {
		b, err := os.ReadFile("testdata/" + f + ".html")
		if err != nil {
			t.Fatal(err)
		}
		// Point the recipe's image at our server.
		body := strings.NewReplacer("https://images.production.immediate.co.uk", "IMG", "https://assets.afcdn.com", "IMG").Replace(string(b))
		mux.HandleFunc("/"+f, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte(strings.ReplaceAll(body, "IMG", "http://"+r.Host+"/img")))
		})
	}
	mux.HandleFunc("/img/", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write([]byte("\xff\xd8\xff\xe0 fake jpeg"))
	})
	mux.HandleFunc("/blog", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<html><body><article><h1>Grandma's dal</h1><p>This is the dal my grandmother made every Sunday, slow and fragrant, and here is how.</p>
		<p>Rinse a cup of red lentils, fry an onion with cumin, add the lentils and three cups of water and simmer for twenty minutes.</p></article></body></html>`))
	})
	e.site = httptest.NewServer(mux)
	t.Cleanup(e.site.Close)

	e.svc = New(Deps{
		Pool: pool, Settings: store, LLM: e.llm, Fetcher: NewFetcher(loopbackOK), Reels: e.reels,
		Blobs: blobs, Covers: BlobCovers{Store: blobs}, Writer: recipes.Writer{Res: resolve.Resolver{}},
	})
	jc, err := jobs.New(jobs.Deps{Pool: pool, Settings: store, Workers: []func(*river.Workers) error{e.svc.Registrar()}, Queues: Queues()})
	if err != nil {
		t.Fatal(err)
	}
	e.svc.Attach(jc)
	reg := commands.NewRegistry()
	recipes.Register(reg, resolve.Resolver{})
	e.svc.Register(reg)
	e.cmds = commands.NewService(pool, reg)
	if err := pool.QueryRow(ctx, `INSERT INTO household (id, name) VALUES (gen_random_uuid(), 'Home') RETURNING id`).Scan(&e.hh); err != nil {
		t.Fatal(err)
	}
	e.ctx = identity.WithPrincipal(context.Background(), &identity.Principal{UserID: uuid.New(), Status: identity.StatusActive, HouseholdID: e.hh})
	return e
}

func (e *env) do(t *testing.T, typ string, payload any) commands.Result {
	t.Helper()
	b, _ := json.Marshal(payload)
	rs, err := e.cmds.Apply(e.ctx, e.hh, []commands.Command{{ID: uuid.New(), Type: typ, Payload: b, ClientTime: time.Now()}})
	if err != nil {
		t.Fatal(err)
	}
	return rs[0]
}

func (e *env) create(t *testing.T, kind, input string, extra ...func(map[string]any)) uuid.UUID {
	t.Helper()
	id := uuid.Must(uuid.NewV7())
	p := map[string]any{"id": id, "kind": kind, "input": input}
	for _, f := range extra {
		f(p)
	}
	if r := e.do(t, "recipe.import.create", p); r.Status != commands.StatusApplied {
		t.Fatalf("create: %+v %v", r, r.Problem)
	}
	return id
}

// work runs the queued job for an import, like River would.
func (e *env) work(t *testing.T, id uuid.UUID) error {
	t.Helper()
	var jobID int64
	var raw []byte
	var attempt int
	err := e.pool.QueryRow(t.Context(), `SELECT id, args, attempt FROM river_job WHERE kind = 'recipe.import' AND args->>'import_id' = $1
		AND state IN ('available', 'scheduled', 'retryable') ORDER BY id DESC LIMIT 1`, id.String()).Scan(&jobID, &raw, &attempt)
	if err != nil {
		t.Fatalf("no job for %s: %v", id, err)
	}
	var args Args
	_ = json.Unmarshal(raw, &args)
	job := &river.Job[Args]{JobRow: &rivertype.JobRow{ID: jobID, Attempt: attempt + 1, MaxAttempts: maxAttempts}, Args: args}
	werr := (&Worker{s: e.svc}).Work(t.Context(), job)
	// Mark it done so the next enqueue isn't deduplicated against it.
	if _, err := e.pool.Exec(t.Context(), `UPDATE river_job SET state = 'completed', finalized_at = now(), attempt = $2 WHERE id = $1`, jobID, attempt+1); err != nil {
		t.Fatal(err)
	}
	return werr
}

type importRow struct {
	Status   string
	Code     string
	RecipeID *uuid.UUID
	Lang     *string
}

func (e *env) row(t *testing.T, id uuid.UUID) importRow {
	t.Helper()
	var r importRow
	var raw []byte
	if err := e.pool.QueryRow(t.Context(), `SELECT status, error, recipe_id, detected_lang FROM recipe_import WHERE id = $1`, id).Scan(&r.Status, &raw, &r.RecipeID, &r.Lang); err != nil {
		t.Fatal(err)
	}
	var p problem
	_ = json.Unmarshal(raw, &p)
	r.Code = p.Code
	return r
}

const parsedDal = `{"is_recipe":true,"lang":"en","title":"Grandma's dal","subtitle":"","servings":4,"total_minutes":30,
 "ingredients":[{"group":"","items":["1 cup red lentils","1 onion","1 tsp cumin seeds"]}],
 "steps":[{"text":"Rinse the lentils.","minutes":0},{"text":"Simmer with 3 cups water.","minutes":20}],"notes":"","tags":["indian"]}`

func TestURLWithRecipeDataNeedsNoLLM(t *testing.T) {
	e := newEnv(t)
	id := e.create(t, KindURL, e.site.URL+"/bbcgoodfood")
	if err := e.work(t, id); err != nil {
		t.Fatal(err)
	}
	r := e.row(t, id)
	if r.Status != StatusDone || r.RecipeID == nil || *r.Lang != "en" {
		t.Fatalf("import = %+v", r)
	}
	if f := e.llm.features(); len(f) != 0 {
		t.Fatalf("llm called: %v", f)
	}
	var (
		title, kind, src string
		cover            *string
		lines            int
		origin           string
		original         []byte
		importID         uuid.UUID
		saved            bool
	)
	err := e.pool.QueryRow(t.Context(), `SELECT r.title, r.source_kind, r.source_url, r.cover_blob, r.saved, r.import_id, v.origin, jsonb_array_length(v.ingredients), v.original
		FROM recipe r JOIN recipe_version v ON v.id = r.current_version_id WHERE r.id = $1`, *r.RecipeID).
		Scan(&title, &kind, &src, &cover, &saved, &importID, &origin, &lines, &original)
	if err != nil {
		t.Fatal(err)
	}
	if title != "Easy chicken curry" || kind != KindURL || src != e.site.URL+"/bbcgoodfood" || !saved || importID != id || origin != "import" || lines != 11 || original != nil {
		t.Fatalf("recipe %q %s %s saved=%v import=%s origin=%s lines=%d", title, kind, src, saved, importID, origin, lines)
	}
	if cover == nil {
		t.Fatal("no cover")
	}
	if rc, err := e.blobs.Open(t.Context(), blob.Ref(*cover)); err != nil {
		t.Fatalf("cover not stored: %v", err)
	} else {
		_ = rc.Close()
	}
	var fetches, audits int
	_ = e.pool.QueryRow(t.Context(), `SELECT count(*) FROM recipe_fetch WHERE import_id = $1 AND status = 200`, id).Scan(&fetches)
	_ = e.pool.QueryRow(t.Context(), `SELECT count(*) FROM audit_event WHERE action = 'recipe.import.done' AND household_id = $1`, e.hh).Scan(&audits)
	if fetches != 2 || audits != 1 {
		t.Fatalf("fetch log %d, audits %d", fetches, audits)
	}
	// A rerun of the same job is a no-op.
	if _, err := e.pool.Exec(t.Context(), `UPDATE river_job SET state = 'available', finalized_at = NULL WHERE args->>'import_id' = $1`, id.String()); err != nil {
		t.Fatal(err)
	}
	if err := e.work(t, id); err != nil {
		t.Fatal(err)
	}
	var n int
	_ = e.pool.QueryRow(t.Context(), `SELECT count(*) FROM recipe WHERE import_id = $1`, id).Scan(&n)
	if n != 1 {
		t.Fatalf("%d recipes", n)
	}
}

func TestForeignRecipeIsTranslatedAndOriginalKept(t *testing.T) {
	e := newEnv(t)
	e.llm.replies[FeatureTranslate.Name] = []string{`{"lang":"en","title":"Simple crêpe batter","subtitle":"","servings":20,"total_minutes":45,
	 "ingredients":[{"group":"","items":["5 eggs","500 g flour","1 l semi-skimmed milk","2 tbsp oil","1 dash beer"]}],
	 "steps":[{"text":"Put half the flour in a bowl, then add the eggs one by one.","minutes":0},{"text":"Add the rest.","minutes":0},{"text":"Rest for 1 hour.","minutes":60}],"notes":""}`}
	id := e.create(t, KindURL, e.site.URL+"/marmiton")
	if err := e.work(t, id); err != nil {
		t.Fatal(err)
	}
	r := e.row(t, id)
	if r.Status != StatusDone || *r.Lang != "fr" {
		t.Fatalf("import = %+v", r)
	}
	if f := e.llm.features(); len(f) != 1 || f[0] != FeatureTranslate.Name {
		t.Fatalf("llm calls %v", f)
	}
	var title, lang string
	var orig Original
	var raw []byte
	if err := e.pool.QueryRow(t.Context(), `SELECT r.title, r.source_lang, v.original FROM recipe r JOIN recipe_version v ON v.id = r.current_version_id WHERE r.id = $1`,
		*r.RecipeID).Scan(&title, &lang, &raw); err != nil {
		t.Fatal(err)
	}
	_ = json.Unmarshal(raw, &orig)
	if title != "Simple crêpe batter" || lang != "fr" || orig.Lang != "fr" || orig.Ingredients[0] != "5 oeufs" || !strings.HasPrefix(orig.Steps[0].Text, "Dans un saladier") {
		t.Fatalf("title %q lang %q original %+v", title, lang, orig)
	}
}

func TestPageWithoutRecipeDataUsesTheLLM(t *testing.T) {
	e := newEnv(t)
	e.llm.replies[FeatureParse.Name] = []string{parsedDal}
	id := e.create(t, KindURL, e.site.URL+"/blog")
	if err := e.work(t, id); err != nil {
		t.Fatal(err)
	}
	if r := e.row(t, id); r.Status != StatusDone {
		t.Fatalf("import = %+v", r)
	}
	req := e.llm.requests[0]
	if req.Priority != llm.Interactive || !strings.Contains(req.Messages[1].Text, "my grandmother made every Sunday") || strings.Contains(req.Messages[1].Text, "<p>") {
		t.Fatalf("request %+v", req)
	}
}

func TestBlockedAddressFailsWithoutRetry(t *testing.T) {
	e := newEnv(t)
	e.svc.d.Fetcher = NewFetcher(PublicOnly(nil))
	id := e.create(t, KindURL, e.site.URL+"/bbcgoodfood")
	if err := e.work(t, id); err != nil {
		t.Fatal(err)
	}
	if r := e.row(t, id); r.Status != StatusFailed || r.Code != "blocked_address" {
		t.Fatalf("import = %+v", r)
	}
	// Retry queues it again.
	if res := e.do(t, "recipe.import.retry", map[string]any{"id": id}); res.Status != commands.StatusApplied {
		t.Fatalf("retry %+v", res)
	}
	if r := e.row(t, id); r.Status != StatusQueued {
		t.Fatalf("after retry %+v", r)
	}
}

func TestQuotaSnoozes(t *testing.T) {
	e := newEnv(t)
	e.llm.err = fmtErr(llm.ErrUnavailable)
	id := e.create(t, KindText, "so we had this lovely soup at a friend's and she said just boil some stuff")
	err := e.work(t, id)
	var snooze *rivertype.JobSnoozeError
	if !errors.As(err, &snooze) {
		t.Fatalf("err = %v", err)
	}
	if r := e.row(t, id); r.Status != StatusWaitingQuota || r.Code != "llm_unavailable" {
		t.Fatalf("import = %+v", r)
	}
}

func fmtErr(e error) error { return errors.Join(e, errors.New("no provider")) }

func TestStructuredTextNeedsNoLLM(t *testing.T) {
	e := newEnv(t)
	id := e.create(t, KindText, "Weeknight dal\nIngredients\n1 cup red lentils\n1 onion\n2 tsp cumin\nMethod\n1. Fry the onion.\n2. Add lentils and water.")
	if err := e.work(t, id); err != nil {
		t.Fatal(err)
	}
	if r := e.row(t, id); r.Status != StatusDone || len(e.llm.requests) != 0 {
		t.Fatalf("import = %+v, llm %v", r, e.llm.features())
	}
}

func TestBlockedReelNeedsInputThenUsesPastedCaption(t *testing.T) {
	e := newEnv(t)
	e.reels.err = ErrReelBlocked
	id := e.create(t, KindURL, "https://www.instagram.com/reel/C9xAbc123/?igsh=x") // detected as instagram
	var kind string
	_ = e.pool.QueryRow(t.Context(), `SELECT kind FROM recipe_import WHERE id = $1`, id).Scan(&kind)
	if kind != KindInstagram {
		t.Fatalf("kind = %s", kind)
	}
	if err := e.work(t, id); err != nil {
		t.Fatal(err)
	}
	if r := e.row(t, id); r.Status != StatusNeedsInput {
		t.Fatalf("import = %+v", r)
	}
	e.llm.replies[FeatureParse.Name] = []string{strings.Replace(parsedDal, `"lang":"en"`, `"lang":"fa"`, 1)}
	e.llm.replies[FeatureTranslate.Name] = []string{strings.Replace(parsedDal, `"is_recipe":true,`, "", 1)}
	if res := e.do(t, "recipe.import.provide", map[string]any{"id": id, "text": "دال عدس\n۱ پیمانه عدس قرمز"}); res.Status != commands.StatusApplied {
		t.Fatalf("provide %+v %v", res, res.Problem)
	}
	if err := e.work(t, id); err != nil {
		t.Fatal(err)
	}
	r := e.row(t, id)
	if r.Status != StatusDone || *r.Lang != "fa" || e.reels.hits != 1 {
		t.Fatalf("import = %+v, reel reads %d", r, e.reels.hits)
	}
	if !strings.Contains(e.llm.requests[0].Messages[1].Text, "عدس قرمز") {
		t.Fatal("pasted text not sent")
	}
	var kindOut, src string
	_ = e.pool.QueryRow(t.Context(), `SELECT source_kind, source_url FROM recipe WHERE id = $1`, *r.RecipeID).Scan(&kindOut, &src)
	if kindOut != KindInstagram || !strings.HasPrefix(src, "https://www.instagram.com/reel/C9xAbc123") {
		t.Fatalf("recipe source %s %s", kindOut, src)
	}
}

func TestReelCaptionAndTranscript(t *testing.T) {
	e := newEnv(t)
	e.reels.reel = Reel{Caption: "Easy dal 🍲 recipe below", Title: "Dal", Seconds: 40, Audio: []byte("m4a")}
	e.llm.script = "Today we rinse one cup of lentils and simmer them twenty minutes."
	e.llm.replies[FeatureParse.Name] = []string{parsedDal}
	id := e.create(t, KindInstagram, "https://www.instagram.com/reel/C9xAbc123/")
	if err := e.work(t, id); err != nil {
		t.Fatal(err)
	}
	if r := e.row(t, id); r.Status != StatusDone {
		t.Fatalf("import = %+v", r)
	}
	if len(e.llm.audio) != 1 || e.llm.audio[0].Feature.Name != FeatureTranscribe.Name {
		t.Fatalf("transcribe calls %+v", e.llm.audio)
	}
	msg := e.llm.requests[0].Messages[1].Text
	if !strings.Contains(msg, "Easy dal") || !strings.Contains(msg, "simmer them twenty minutes") {
		t.Fatalf("parse input %q", msg)
	}
	var caption, transcript string
	_ = e.pool.QueryRow(t.Context(), `SELECT caption, transcript FROM recipe_import_detail WHERE import_id = $1`, id).Scan(&caption, &transcript)
	if caption == "" || transcript == "" {
		t.Fatal("detail not kept")
	}
}

func TestInstagramFlagOffAsksForCaption(t *testing.T) {
	e := newEnv(t)
	if err := e.store.Set(t.Context(), FlagInstagram.Key(), json.RawMessage(`false`), nil); err != nil {
		t.Fatal(err)
	}
	id := e.create(t, KindInstagram, "https://www.instagram.com/reel/C9xAbc123/")
	if err := e.work(t, id); err != nil {
		t.Fatal(err)
	}
	if r := e.row(t, id); r.Status != StatusNeedsInput || e.reels.hits != 0 {
		t.Fatalf("import = %+v", r)
	}
}

func TestCancelStopsTheJob(t *testing.T) {
	e := newEnv(t)
	id := e.create(t, KindURL, e.site.URL+"/bbcgoodfood")
	if res := e.do(t, "recipe.import.cancel", map[string]any{"id": id}); res.Status != commands.StatusApplied {
		t.Fatalf("cancel %+v", res)
	}
	if err := e.work(t, id); err != nil {
		t.Fatal(err)
	}
	var n int
	_ = e.pool.QueryRow(t.Context(), `SELECT count(*) FROM recipe WHERE import_id = $1`, id).Scan(&n)
	if n != 0 {
		t.Fatal("cancelled import made a recipe")
	}
}

func TestBulkTailRunsInBackground(t *testing.T) {
	e := newEnv(t)
	batch := uuid.New()
	var ids []uuid.UUID
	for i := range 5 {
		ids = append(ids, e.create(t, KindURL, e.site.URL+"/blog?n="+string(rune('a'+i)), func(p map[string]any) { p["batchId"] = batch }))
	}
	for i, id := range ids {
		var bg bool
		if err := e.pool.QueryRow(t.Context(), `SELECT coalesce((args->>'background')::bool, false) FROM river_job WHERE args->>'import_id' = $1`, id.String()).Scan(&bg); err != nil {
			t.Fatal(err)
		}
		if bg != (i >= backgroundFrom) {
			t.Errorf("row %d background = %v", i, bg)
		}
	}
	var q string
	_ = e.pool.QueryRow(t.Context(), `SELECT DISTINCT queue FROM river_job WHERE kind = 'recipe.import'`).Scan(&q)
	if q != QueueName {
		t.Fatalf("queue %q", q)
	}
}

func TestCreateValidation(t *testing.T) {
	e := newEnv(t)
	for name, p := range map[string]map[string]any{
		"bad url":        {"id": uuid.New(), "kind": "url", "input": "javascript:alert(1)"},
		"no text":        {"id": uuid.New(), "kind": "text", "input": "  "},
		"bad kind":       {"id": uuid.New(), "kind": "fax", "input": "x"},
		"no photos":      {"id": uuid.New(), "kind": "photo", "blobRefs": []string{}},
		"unknown photo":  {"id": uuid.New(), "kind": "photo", "blobRefs": []string{strings.Repeat("a", 64)}},
		"not a reel":     {"id": uuid.New(), "kind": "url", "input": "https://www.instagram.com/someone/"},
		"photos on link": {"id": uuid.New(), "kind": "url", "input": "https://x.example/", "blobRefs": []string{strings.Repeat("a", 64)}},
	} {
		if r := e.do(t, "recipe.import.create", p); r.Status != commands.StatusRejected {
			t.Errorf("%s: %+v", name, r)
		}
	}
	if err := e.store.Set(t.Context(), FlagWebImport.Key(), json.RawMessage(`false`), nil); err != nil {
		t.Fatal(err)
	}
	if r := e.do(t, "recipe.import.create", map[string]any{"id": uuid.New(), "kind": "url", "input": "https://x.example/r"}); r.Status != commands.StatusRejected {
		t.Errorf("web import off: %+v", r)
	}
}

func TestPhotoImportUsesVision(t *testing.T) {
	e := newEnv(t)
	ref, _, err := e.blobs.Put(t.Context(), strings.NewReader("\x89PNG\r\n\x1a\n fake png"))
	if err != nil {
		t.Fatal(err)
	}
	e.llm.replies[FeaturePhoto.Name] = []string{parsedDal}
	id := e.create(t, KindPhoto, "", func(p map[string]any) { p["blobRefs"] = []string{string(ref)}; delete(p, "input") })
	if err := e.work(t, id); err != nil {
		t.Fatal(err)
	}
	r := e.row(t, id)
	if r.Status != StatusDone {
		t.Fatalf("import = %+v", r)
	}
	img := e.llm.requests[0].Messages[1].Images
	if len(img) != 1 || img[0].Ref != ref || img[0].MIME != "image/png" {
		t.Fatalf("images %+v", img)
	}
	var cover string
	_ = e.pool.QueryRow(t.Context(), `SELECT cover_blob FROM recipe WHERE id = $1`, *r.RecipeID).Scan(&cover)
	if cover != string(ref) {
		t.Fatalf("cover %q", cover)
	}
}

func TestDeletingTheRecipeTombstonesItsImport(t *testing.T) {
	e := newEnv(t)
	id := e.create(t, KindURL, e.site.URL+"/bbcgoodfood")
	if err := e.work(t, id); err != nil {
		t.Fatal(err)
	}
	r := e.row(t, id)
	if res := e.do(t, "recipe.delete", map[string]any{"id": *r.RecipeID}); res.Status != commands.StatusApplied {
		t.Fatalf("delete %+v", res)
	}
	var deleted bool
	_ = e.pool.QueryRow(t.Context(), `SELECT deleted_at IS NOT NULL FROM recipe_import WHERE id = $1`, id).Scan(&deleted)
	if !deleted {
		t.Fatal("import still live")
	}
}

func TestAdminStatusAndRetry(t *testing.T) {
	e := newEnv(t)
	e.svc.d.Fetcher = NewFetcher(PublicOnly(nil))
	id := e.create(t, KindURL, e.site.URL+"/bbcgoodfood")
	if err := e.work(t, id); err != nil {
		t.Fatal(err)
	}
	st, err := e.svc.AdminStatus(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if st.Last24h[StatusFailed] != 1 || len(st.Failures) != 1 || st.Failures[0].Code != "blocked_address" || len(st.Fetches) != 1 || !st.Tools.YtDLP {
		t.Fatalf("status %+v", st)
	}
	if err := e.svc.AdminRetry(e.ctx, id); err != nil {
		t.Fatal(err)
	}
	if r := e.row(t, id); r.Status != StatusQueued {
		t.Fatalf("after admin retry %+v", r)
	}
	if err := e.svc.AdminRetry(e.ctx, uuid.New()); !errors.Is(err, ErrImportNotFound) {
		t.Fatalf("unknown: %v", err)
	}
}
