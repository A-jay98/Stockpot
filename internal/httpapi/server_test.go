package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/getkin/kin-openapi/openapi3"

	"github.com/a-jay98/stockpot/internal/reqctx"
)

type pingFunc func(context.Context) error

func (f pingFunc) Ping(ctx context.Context) error { return f(ctx) }

func newHandler(t *testing.T, si StrictServerInterface, opts Options) http.Handler {
	t.Helper()
	h, err := NewHandler(si, opts)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func do(h http.Handler, r *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec
}

func decodeProblem(t *testing.T, rec *httptest.ResponseRecorder) Problem {
	t.Helper()
	if ct := rec.Header().Get("Content-Type"); ct != "application/problem+json" {
		t.Fatalf("Content-Type = %q, body %s", ct, rec.Body)
	}
	var p Problem
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatal(err)
	}
	if p.Status != rec.Code || p.Type != "about:blank" || p.Title == "" || p.Instance == nil || *p.Instance != rec.Header().Get("X-Request-Id") {
		t.Fatalf("problem %+v for status %d", p, rec.Code)
	}
	return p
}

func TestHealth(t *testing.T) {
	tests := []struct {
		name       string
		db, schema error
		wantStatus int
		want       HealthStatus
	}{
		{name: "healthy", wantStatus: http.StatusOK, want: HealthStatusOk},
		{name: "database down", db: errors.New("connection refused"), wantStatus: http.StatusServiceUnavailable, want: HealthStatusDegraded},
		{name: "schema behind", schema: errors.New("migrations pending"), wantStatus: http.StatusServiceUnavailable, want: HealthStatusDegraded},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHandler(t, NewServer(Deps{Checks: map[string]Pinger{
				"database": pingFunc(func(context.Context) error { return tt.db }),
				"schema":   pingFunc(func(context.Context) error { return tt.schema }),
			}}), Options{})
			rec := do(h, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/health", nil))
			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d; body %s", rec.Code, tt.wantStatus, rec.Body)
			}
			var got Health
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if got.Status != tt.want || got.Version == "" || len(got.Checks) != 2 {
				t.Fatalf("got %+v", got)
			}
			if strings.Contains(rec.Body.String(), "refused") || strings.Contains(rec.Body.String(), "pending") {
				t.Fatalf("health leaks error details: %s", rec.Body)
			}
		})
	}
}

func TestMeta(t *testing.T) {
	s := NewServer(Deps{})
	fixed := time.Date(2026, 11, 23, 8, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return fixed }
	rec := do(newHandler(t, s, Options{}), httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/meta", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var m Meta
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatal(err)
	}
	if m.Version != "0.1.0" || m.Tag != "dev" || !m.ServerTime.Equal(fixed) {
		t.Fatalf("meta %+v", m)
	}
}

func TestProblems(t *testing.T) {
	h := newHandler(t, NewServer(Deps{}), Options{PublicURL: &url.URL{Scheme: "https", Host: "stockpot.test"}})
	tests := []struct {
		name   string
		method string
		path   string
		header map[string]string
		status int
		code   string
	}{
		{"unknown route", http.MethodGet, "/api/nope", nil, http.StatusNotFound, CodeNotFound},
		{"outside api", http.MethodGet, "/elsewhere", nil, http.StatusNotFound, CodeNotFound},
		{"wrong method", http.MethodPost, "/api/v1/meta", map[string]string{"Origin": "https://stockpot.test"}, http.StatusMethodNotAllowed, CodeMethodNotAllowed},
		{"cross origin", http.MethodPost, "/api/v1/meta", map[string]string{"Origin": "https://evil.test"}, http.StatusForbidden, CodeCrossOrigin},
		{"cross site fetch", http.MethodDelete, "/api/v1/meta", map[string]string{"Sec-Fetch-Site": "cross-site"}, http.StatusForbidden, CodeCrossOrigin},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequestWithContext(t.Context(), tt.method, tt.path, nil)
			for k, v := range tt.header {
				r.Header.Set(k, v)
			}
			rec := do(h, r)
			if rec.Code != tt.status {
				t.Fatalf("status = %d, want %d; body %s", rec.Code, tt.status, rec.Body)
			}
			if p := decodeProblem(t, rec); p.Code != tt.code {
				t.Fatalf("code = %q, want %q", p.Code, tt.code)
			}
		})
	}
}

type panicky struct{ *Server }

func (panicky) GetMeta(context.Context, GetMetaRequestObject) (GetMetaResponseObject, error) {
	panic("boom")
}

type failing struct{ *Server }

func (failing) GetMeta(context.Context, GetMetaRequestObject) (GetMetaResponseObject, error) {
	return nil, errors.New("db exploded at /secret/path")
}

func TestHandlerFailuresBecome500Problems(t *testing.T) {
	var logged []AccessRecord
	for name, si := range map[string]StrictServerInterface{"panic": panicky{NewServer(Deps{})}, "error": failing{NewServer(Deps{})}} {
		t.Run(name, func(t *testing.T) {
			h := newHandler(t, si, Options{AccessLog: accessFunc(func(rec AccessRecord) { logged = append(logged, rec) })})
			rec := do(h, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/meta", nil))
			if rec.Code != http.StatusInternalServerError {
				t.Fatalf("status %d", rec.Code)
			}
			if p := decodeProblem(t, rec); p.Code != CodeInternal || strings.Contains(rec.Body.String(), "secret") {
				t.Fatalf("problem %s", rec.Body)
			}
			if last := logged[len(logged)-1]; last.Status != http.StatusInternalServerError || last.Route != "/api/v1/meta" {
				t.Fatalf("access record %+v", last)
			}
		})
	}
}

type accessFunc func(AccessRecord)

func (f accessFunc) LogAccess(_ context.Context, rec AccessRecord) { f(rec) }

func TestAccessLogSeesSessionFields(t *testing.T) {
	var got AccessRecord
	session := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			reqctx.From(r.Context()).HouseholdID[0] = 7
			next.ServeHTTP(w, r)
		})
	}
	h := newHandler(t, NewServer(Deps{}), Options{
		Session:   session,
		AccessLog: accessFunc(func(rec AccessRecord) { got = rec }),
	})
	r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/meta", nil)
	r.Header.Set("User-Agent", "test-agent")
	rec := do(h, r)
	if got.Status != http.StatusOK || got.Route != "/api/v1/meta" || got.Request.HouseholdID[0] != 7 ||
		got.Request.RequestID != rec.Header().Get("X-Request-Id") || got.Request.ClientIP != "192.0.2.1" || got.Request.UserAgent != "test-agent" {
		t.Fatalf("access record %+v", got)
	}
}

func TestClientIP(t *testing.T) {
	unix := &net.UnixAddr{Name: "/run/sandboxed/stockpot.sock", Net: "unix"}
	tcp := &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 8080}
	tests := []struct {
		name  string
		local net.Addr
		xff   []string
		want  string
	}{
		{"unix socket trusts caddy", unix, []string{"203.0.113.9"}, "203.0.113.9"},
		{"unix socket takes last hop", unix, []string{"1.1.1.1, 203.0.113.9"}, "203.0.113.9"},
		{"unix socket, last header wins", unix, []string{"1.1.1.1", "2001:db8::1"}, "2001:db8::1"},
		{"unix socket without header", unix, nil, ""},
		{"tcp ignores spoofed header", tcp, []string{"203.0.113.9"}, "192.0.2.1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.WithValue(t.Context(), http.LocalAddrContextKey, tt.local)
			r := httptest.NewRequestWithContext(ctx, http.MethodGet, "/", nil)
			for _, v := range tt.xff {
				r.Header.Add("X-Forwarded-For", v)
			}
			if got := clientIP(r); got != tt.want {
				t.Fatalf("clientIP = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestEveryOperationDeclaresRoles(t *testing.T) {
	spec, err := GetSpec()
	if err != nil {
		t.Fatal(err)
	}
	roles, err := ParseRoles(spec)
	if err != nil {
		t.Fatal(err)
	}
	for _, op := range []string{"GetHealth", "GetMeta"} { // the embedded spec carries normalized ids
		if len(roles[op]) != 1 || roles[op][0] != RolePublic {
			t.Errorf("%s roles = %v, want [public]", op, roles[op])
		}
	}
}

const testSpec = `
openapi: 3.1.0
info: {title: t, version: "1"}
paths:
  /open:
    get: {operationId: open, x-roles: [public], responses: {"204": {description: ok}}}
  /private:
    get: {operationId: private, x-roles: [user], responses: {"204": {description: ok}}}
  /admin:
    get: {operationId: admin, x-roles: [admin], responses: {"204": {description: ok}}}
`

func loadSpec(t *testing.T, src string) *openapi3.T {
	t.Helper()
	spec, err := openapi3.NewLoader().LoadFromData([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	return spec
}

func TestParseRolesRejectsMissingOrUnknown(t *testing.T) {
	for name, ext := range map[string]string{"missing": "", "empty": "x-roles: [],", "unknown": "x-roles: [root],"} {
		spec := loadSpec(t, `
openapi: 3.1.0
info: {title: t, version: "1"}
paths:
  /x:
    get: {operationId: x, `+ext+` responses: {"204": {description: ok}}}
`)
		if _, err := ParseRoles(spec); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}

type adminOnly struct{}

func (adminOnly) Authorize(_ *http.Request, roles []string) error {
	for _, r := range roles {
		if r == RolePublic || r == RoleUser {
			return nil
		}
	}
	return ErrForbidden
}

func TestAuthorize(t *testing.T) {
	spec := loadSpec(t, testSpec)
	roles, err := ParseRoles(spec)
	if err != nil {
		t.Fatal(err)
	}
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	tests := []struct {
		az   Authorizer
		path string
		want int
	}{
		{PublicOnly{}, "/open", http.StatusNoContent},
		{PublicOnly{}, "/private", http.StatusUnauthorized},
		{PublicOnly{}, "/admin", http.StatusUnauthorized},
		{PublicOnly{}, "/unknown", http.StatusNotFound}, // fails closed
		{adminOnly{}, "/private", http.StatusNoContent},
		{adminOnly{}, "/admin", http.StatusForbidden},
	}
	for _, tt := range tests {
		mw, err := authorize(spec, roles, tt.az)
		if err != nil {
			t.Fatal(err)
		}
		rec := do(mw(ok), httptest.NewRequestWithContext(t.Context(), http.MethodGet, tt.path, nil))
		if rec.Code != tt.want {
			t.Errorf("%T %s: status %d, want %d", tt.az, tt.path, rec.Code, tt.want)
		}
	}
}
