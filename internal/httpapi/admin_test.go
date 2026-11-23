package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/a-jay98/stockpot/internal/admin"
	"github.com/a-jay98/stockpot/internal/db"
	"github.com/a-jay98/stockpot/internal/jobs"
	"github.com/a-jay98/stockpot/internal/settings"
	"github.com/a-jay98/stockpot/internal/testdb"
)

func TestMain(m *testing.M) { os.Exit(testdb.Main(m)) }

// roleHeader carries the caller's role in tests; production authorization
// comes from the session (identity).
const roleHeader = "X-Test-Role"

// headerRoles admits a request whose test role is listed in the operation's
// x-roles, mirroring the production policy: public needs nothing, admin
// satisfies user.
type headerRoles struct{}

func (headerRoles) Authorize(r *http.Request, roles []string) error {
	if slices.Contains(roles, RolePublic) {
		return nil
	}
	switch role := r.Header.Get(roleHeader); {
	case role == "":
		return ErrUnauthenticated
	case slices.Contains(roles, role), role == RoleAdmin && slices.Contains(roles, RoleUser):
		return nil
	}
	return ErrForbidden
}

var testOrigin = &url.URL{Scheme: "https", Host: "stockpot.test"}

type auditRecord struct {
	Action        string
	Target        admin.Target
	Before, After any
}

type adminEnv struct {
	h      http.Handler
	pool   *pgxpool.Pool
	store  *settings.Store
	jobs   *jobs.Client
	mu     sync.Mutex
	audits []auditRecord
}

func newAdminEnv(t *testing.T) *adminEnv {
	t.Helper()
	pool := testdb.New(t)
	store, err := settings.NewStore(pool, []byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Load(t.Context()); err != nil {
		t.Fatal(err)
	}
	jc, err := jobs.New(jobs.Deps{Pool: pool, Settings: store})
	if err != nil {
		t.Fatal(err)
	}
	schema, err := db.NewSchemaCheck(pool)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = schema.Close() })
	env := &adminEnv{pool: pool, store: store, jobs: jc}
	svc := admin.New(admin.Deps{
		Pool: pool, Settings: store, Jobs: jc, Schema: schema,
		StartedAt: time.Now().Add(-time.Hour),
		Audit: func(_ context.Context, _ pgx.Tx, action string, target admin.Target, before, after any) error {
			env.mu.Lock()
			defer env.mu.Unlock()
			env.audits = append(env.audits, auditRecord{action, target, before, after})
			return nil
		},
	})
	env.h = newHandler(t, NewServer(Deps{Admin: svc}), Options{PublicURL: testOrigin, Authorizer: headerRoles{}})
	return env
}

// call makes a request as role ("" = signed out) and decodes a 2xx JSON body into out.
func (e *adminEnv) call(t *testing.T, role, method, path string, body any, out any) *httptest.ResponseRecorder {
	t.Helper()
	var rd *bytes.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	r := httptest.NewRequestWithContext(t.Context(), method, path, rd)
	r.Header.Set("Origin", testOrigin.String())
	if body != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	if role != "" {
		r.Header.Set(roleHeader, role)
	}
	rec := do(e.h, r)
	if out != nil && rec.Code/100 == 2 {
		if err := json.Unmarshal(rec.Body.Bytes(), out); err != nil {
			t.Fatalf("decode %s: %v", rec.Body, err)
		}
	}
	return rec
}

func TestAdminOperationsRequireAdmin(t *testing.T) {
	spec, err := GetSpec()
	if err != nil {
		t.Fatal(err)
	}
	roles, err := ParseRoles(spec)
	if err != nil {
		t.Fatal(err)
	}
	// Every operation under /v1/admin is admin-only, so nobody can widen one by accident.
	var ops int
	for path, item := range spec.Paths.Map() {
		if !strings.HasPrefix(path, "/v1/admin/") {
			continue
		}
		for _, op := range item.Operations() {
			ops++
			if r := roles[op.OperationID]; len(r) != 1 || r[0] != RoleAdmin {
				t.Errorf("%s %s: x-roles %v, want [admin]", op.OperationID, path, r)
			}
		}
	}
	if ops == 0 {
		t.Fatal("no admin operations found")
	}

	// And the middleware refuses them to anyone else (no database needed: it
	// answers before the handler).
	h := newHandler(t, NewServer(Deps{}), Options{PublicURL: testOrigin, Authorizer: headerRoles{}})
	requests := []struct{ method, path string }{
		{http.MethodGet, "/api/v1/admin/overview"},
		{http.MethodGet, "/api/v1/admin/logs"},
		{http.MethodGet, "/api/v1/admin/jobs"},
		{http.MethodPost, "/api/v1/admin/jobs/1/retry"},
		{http.MethodPost, "/api/v1/admin/jobs/1/cancel"},
		{http.MethodGet, "/api/v1/admin/settings"},
		{http.MethodPut, "/api/v1/admin/settings/log.level"},
		{http.MethodDelete, "/api/v1/admin/settings/log.level"},
	}
	for _, rq := range requests {
		for role, want := range map[string]int{"": http.StatusUnauthorized, RoleUser: http.StatusForbidden} {
			r := httptest.NewRequestWithContext(t.Context(), rq.method, rq.path, strings.NewReader(`{"value":"debug"}`))
			r.Header.Set("Origin", testOrigin.String())
			r.Header.Set("Content-Type", "application/json")
			if role != "" {
				r.Header.Set(roleHeader, role)
			}
			rec := do(h, r)
			if rec.Code != want {
				t.Errorf("%s %s as %q: status %d, want %d", rq.method, rq.path, role, rec.Code, want)
			}
		}
	}
}

func TestAdminOverview(t *testing.T) {
	e := newAdminEnv(t)
	if _, err := e.pool.Exec(t.Context(), `
		INSERT INTO app_log (time, level, msg) VALUES
		  (now(), 8, 'boom'), (now(), 4, 'hmm'), (now() - interval '2 days', 8, 'old')`); err != nil {
		t.Fatal(err)
	}
	var o AdminOverview
	rec := e.call(t, RoleAdmin, http.MethodGet, "/api/v1/admin/overview", nil, &o)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if !o.Database.Reachable || !o.Database.SchemaCurrent || o.Database.AppVersion == nil || *o.Database.AppVersion != *o.Database.AppTarget ||
		o.Database.SizeBytes == nil || o.Version != "dev" || o.UptimeSeconds < 3500 {
		t.Fatalf("overview %+v / db %+v", o, o.Database)
	}
	if o.Errors24h == nil || *o.Errors24h != 1 || *o.Warnings24h != 1 || o.Jobs == nil || o.Llm != nil {
		t.Fatalf("counts %+v", o)
	}
}

func TestAdminLogs(t *testing.T) {
	e := newAdminEnv(t)
	base := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
	for i := range 5 {
		lvl, msg, rid := 0, "request served", "r-"+strconv.Itoa(i)
		if i == 3 {
			lvl, msg = 8, "boom 100%_done"
		}
		if _, err := e.pool.Exec(t.Context(),
			`INSERT INTO app_log (time, level, msg, attrs, request_id) VALUES ($1, $2, $3, $4, $5)`,
			base.Add(time.Duration(i)*time.Minute), lvl, msg, `{"route":"/api/x`+strconv.Itoa(i)+`"}`, rid); err != nil {
			t.Fatal(err)
		}
	}
	list := func(query string) AdminLogPage {
		t.Helper()
		var p AdminLogPage
		if rec := e.call(t, RoleAdmin, http.MethodGet, "/api/v1/admin/logs?"+query, nil, &p); rec.Code != http.StatusOK {
			t.Fatalf("%s: status %d: %s", query, rec.Code, rec.Body)
		}
		return p
	}
	msgs := func(p AdminLogPage) []int64 {
		var ids []int64
		for _, it := range p.Items {
			ids = append(ids, it.Id)
		}
		return ids
	}

	// Keyset pages, newest first, no overlap.
	p1 := list("limit=2")
	if len(p1.Items) != 2 || p1.NextCursor == nil || p1.Items[0].Id < p1.Items[1].Id {
		t.Fatalf("page 1 %+v", p1)
	}
	p2 := list("limit=2&cursor=" + url.QueryEscape(*p1.NextCursor))
	p3 := list("limit=2&cursor=" + url.QueryEscape(*p2.NextCursor))
	all := append(append(msgs(p1), msgs(p2)...), msgs(p3)...)
	if len(all) != 5 || p3.NextCursor != nil || !slices.IsSortedFunc(all, func(a, b int64) int { return int(b - a) }) {
		t.Fatalf("pages %v, last cursor %v", all, p3.NextCursor)
	}

	if p := list("level=error"); len(p.Items) != 1 || p.Items[0].Level != AdminLogLevelError || p.Items[0].Msg != "boom 100%_done" {
		t.Fatalf("level filter %+v", p.Items)
	}
	// Text is literal (% and _ don't act as wildcards) and also searches attributes.
	if p := list("q=" + url.QueryEscape("100%_")); len(p.Items) != 1 {
		t.Fatalf("text filter %+v", p.Items)
	}
	if p := list("q=0%25"); len(p.Items) != 1 { // "0%" only appears in "100%_done"
		t.Fatalf("literal percent %+v", p.Items)
	}
	if p := list("q=API%2FX2"); len(p.Items) != 1 || p.Items[0].Attrs["route"] != "/api/x2" {
		t.Fatalf("attr filter %+v", p.Items)
	}
	if p := list("requestId=r-1"); len(p.Items) != 1 || *p.Items[0].RequestId != "r-1" {
		t.Fatalf("request id filter %+v", p.Items)
	}
	since := url.QueryEscape(base.Add(time.Minute).Format(time.RFC3339))
	until := url.QueryEscape(base.Add(3 * time.Minute).Format(time.RFC3339))
	if p := list("since=" + since + "&until=" + until); len(p.Items) != 2 {
		t.Fatalf("time range %+v", p.Items)
	}
	// Live tail: only lines newer than the newest the client holds.
	newest := p1.Items[0].Id
	if p := list("after=" + strconv.FormatInt(newest, 10)); len(p.Items) != 0 {
		t.Fatalf("tail without news %+v", p.Items)
	}
	if _, err := e.pool.Exec(t.Context(), `INSERT INTO app_log (time, level, msg) VALUES (now(), 0, 'fresh')`); err != nil {
		t.Fatal(err)
	}
	if p := list("after=" + strconv.FormatInt(newest, 10)); len(p.Items) != 1 || p.Items[0].Msg != "fresh" {
		t.Fatalf("tail %+v", p.Items)
	}

	if rec := e.call(t, RoleAdmin, http.MethodGet, "/api/v1/admin/logs?cursor=garbage!", nil, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad cursor: status %d", rec.Code)
	}
	if rec := e.call(t, RoleAdmin, http.MethodGet, "/api/v1/admin/logs?level=loud", nil, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad level: status %d", rec.Code)
	}
}

func TestAdminJobs(t *testing.T) {
	e := newAdminEnv(t)
	ctx := t.Context()
	res, err := e.jobs.Insert(ctx, jobs.RetentionArgs{}, &river.InsertOpts{ScheduledAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	id := res.Job.ID
	path := "/api/v1/admin/jobs/" + strconv.FormatInt(id, 10)

	var page AdminJobPage
	if rec := e.call(t, RoleAdmin, http.MethodGet, "/api/v1/admin/jobs?state=scheduled,available&kind=retention", nil, &page); rec.Code != http.StatusOK {
		t.Fatalf("list: %d %s", rec.Code, rec.Body)
	}
	if len(page.Items) != 1 || page.Items[0].Id != id || page.Items[0].State != AdminJobStateScheduled || page.NextCursor != nil {
		t.Fatalf("list %+v", page)
	}
	if rec := e.call(t, RoleAdmin, http.MethodGet, "/api/v1/admin/jobs?state=completed", nil, &page); rec.Code != http.StatusOK || len(page.Items) != 0 {
		t.Fatalf("state filter: %d %+v", rec.Code, page)
	}

	var j AdminJob
	if rec := e.call(t, RoleAdmin, http.MethodPost, path+"/cancel", nil, &j); rec.Code != http.StatusOK || j.State != AdminJobStateCancelled {
		t.Fatalf("cancel: %d %s", rec.Code, rec.Body)
	}
	if rec := e.call(t, RoleAdmin, http.MethodPost, path+"/retry", nil, &j); rec.Code != http.StatusOK || j.State != AdminJobStateAvailable {
		t.Fatalf("retry: %d %s", rec.Code, rec.Body)
	}
	if rec := e.call(t, RoleAdmin, http.MethodPost, "/api/v1/admin/jobs/999999/retry", nil, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("missing job: %d", rec.Code)
	}

	if len(e.audits) != 2 || e.audits[0].Action != "job.cancel" || e.audits[1].Action != "job.retry" ||
		e.audits[0].Target != (admin.Target{Type: "job", ID: strconv.FormatInt(id, 10)}) {
		t.Fatalf("audits %+v", e.audits)
	}
}

func TestAdminSettings(t *testing.T) {
	e := newAdminEnv(t)
	find := func(list AdminSettingList, key string) AdminSetting {
		t.Helper()
		for _, s := range list.Items {
			if s.Key == key {
				return s
			}
		}
		t.Fatalf("%s not listed", key)
		return AdminSetting{}
	}

	var list AdminSettingList
	if rec := e.call(t, RoleAdmin, http.MethodGet, "/api/v1/admin/settings", nil, &list); rec.Code != http.StatusOK {
		t.Fatalf("list: %d", rec.Code)
	}
	lvl := find(list, "log.level")
	if lvl.Kind != AdminSettingKindString || lvl.Value != "info" || lvl.Default != "info" || lvl.IsSet || lvl.Options == nil || len(*lvl.Options) != 4 {
		t.Fatalf("log.level %+v", lvl)
	}
	if d := find(list, "retention.app_log_days"); d.Kind != AdminSettingKindInt || d.Value != float64(14) {
		t.Fatalf("retention %+v", d)
	}

	var got AdminSetting
	if rec := e.call(t, RoleAdmin, http.MethodPut, "/api/v1/admin/settings/retention.app_log_days", map[string]any{"value": 30}, &got); rec.Code != http.StatusOK {
		t.Fatalf("put: %d %s", rec.Code, rec.Body)
	}
	if got.Value != float64(30) || !got.IsSet || got.UpdatedAt == nil || settings.RetentionAppLogDays.Get(e.store) != 30 {
		t.Fatalf("after put %+v", got)
	}

	for name, body := range map[string]string{
		"out of range": `{"value": 0}`,
		"wrong type":   `{"value": "thirty"}`,
		"null":         `{"value": null}`,
	} {
		r := httptest.NewRequestWithContext(t.Context(), http.MethodPut, "/api/v1/admin/settings/retention.app_log_days", strings.NewReader(body))
		r.Header.Set("Origin", testOrigin.String())
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set(roleHeader, RoleAdmin)
		rec := do(e.h, r)
		if rec.Code != http.StatusUnprocessableEntity {
			t.Fatalf("%s: status %d %s", name, rec.Code, rec.Body)
		}
		if p := decodeProblem(t, rec); p.Code != CodeInvalidValue || p.Detail == nil {
			t.Fatalf("%s: problem %+v", name, p)
		}
	}
	if rec := e.call(t, RoleAdmin, http.MethodPut, "/api/v1/admin/settings/log.level", map[string]any{"value": "loud"}, nil); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("enum: %d", rec.Code)
	}
	if rec := e.call(t, RoleAdmin, http.MethodPut, "/api/v1/admin/settings/no.such_key", map[string]any{"value": 1}, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown key: %d", rec.Code)
	}

	if rec := e.call(t, RoleAdmin, http.MethodDelete, "/api/v1/admin/settings/retention.app_log_days", nil, &got); rec.Code != http.StatusOK {
		t.Fatalf("reset: %d", rec.Code)
	}
	if got.IsSet || got.Value != float64(14) || settings.RetentionAppLogDays.Get(e.store) != 14 {
		t.Fatalf("after reset %+v", got)
	}

	// Only the two successful changes are audited, with before/after.
	want := []auditRecord{
		{"setting.update", admin.Target{Type: "setting", ID: "retention.app_log_days"}, map[string]any{"set": false}, map[string]any{"set": true, "value": 30}},
		{"setting.reset", admin.Target{Type: "setting", ID: "retention.app_log_days"}, map[string]any{"set": true, "value": 30}, map[string]any{"set": false}},
	}
	gotJSON, _ := json.Marshal(e.audits)
	wantJSON, _ := json.Marshal(want)
	if !bytes.Equal(gotJSON, wantJSON) {
		t.Fatalf("audits\n got %s\nwant %s", gotJSON, wantJSON)
	}
}
