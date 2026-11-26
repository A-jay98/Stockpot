package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/a-jay98/stockpot/internal/llm"
)

type fakeGateway struct {
	status  llm.Status
	testErr error
	keys    map[string]string
}

func (f *fakeGateway) Status(context.Context) (llm.Status, error) { return f.status, nil }

func (f *fakeGateway) Usage(_ context.Context, days int) ([]llm.UsageRow, string, error) {
	loc, _ := time.LoadLocation("America/Toronto")
	return []llm.UsageRow{{Day: time.Date(2026, 11, 23, 0, 0, 0, 0, loc), Provider: "gemini", Feature: "test", Requests: int64(days)}}, "America/Toronto", nil
}

func (f *fakeGateway) TestPrompt(_ context.Context, provider, prompt string) (llm.Response, error) {
	if f.testErr != nil {
		return llm.Response{}, f.testErr
	}
	return llm.Response{Text: "echo " + prompt, Provider: provider, Model: "m", Usage: llm.Usage{InputTokens: 1, OutputTokens: 2}, Latency: 15 * time.Millisecond}, nil
}

func (f *fakeGateway) SetAPIKey(_ context.Context, id, key string) error {
	if id != "gemini" {
		return fmt.Errorf("%w: %q", llm.ErrUnknownProvider, id)
	}
	f.keys[id] = key
	return nil
}

type allowAll struct{}

func (allowAll) Authorize(*http.Request, []string) error { return nil }

func llmHandler(t *testing.T, gw LLMGateway, az Authorizer) http.Handler {
	t.Helper()
	d := Deps{}
	if gw != nil {
		d.LLM = gw
	}
	return newHandler(t, NewServer(d), Options{PublicURL: &url.URL{Scheme: "https", Host: "stockpot.test"}, Authorizer: az})
}

func llmRequest(t *testing.T, method, path, body string) *http.Request {
	t.Helper()
	r := httptest.NewRequestWithContext(t.Context(), method, path, strings.NewReader(body))
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	r.Header.Set("Origin", testOrigin.String())
	return r
}

func TestLLMAdminNeedsAdmin(t *testing.T) {
	h := llmHandler(t, &fakeGateway{}, nil) // PublicOnly until identity lands
	for _, path := range []string{"/api/v1/admin/llm/status", "/api/v1/admin/llm/usage"} {
		if rec := do(h, llmRequest(t, http.MethodGet, path, "")); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s: %d", path, rec.Code)
		}
	}
}

func TestLLMStatusAndUsage(t *testing.T) {
	rem := int64(3)
	gw := &fakeGateway{status: llm.Status{
		Enabled: true, Available: true, TZ: "America/Toronto", GlobalRequestCap: 10, GlobalRemaining: &rem,
		Providers: []llm.ProviderStatus{{Config: llm.DefaultProviders[0], State: "no_key", Detail: "no API key"}},
	}}
	h := llmHandler(t, gw, allowAll{})

	rec := do(h, llmRequest(t, http.MethodGet, "/api/v1/admin/llm/status", ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d %s", rec.Code, rec.Body)
	}
	var st LLMStatus
	if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil {
		t.Fatal(err)
	}
	p := st.Providers[0]
	if !st.Available || *st.GlobalRemaining != 3 || p.Config.Id != "gemini" || p.Config.BaseUrl == "" || p.State != "no_key" || *p.Detail != "no API key" {
		t.Fatalf("status = %+v", st)
	}

	rec = do(h, llmRequest(t, http.MethodGet, "/api/v1/admin/llm/usage?days=30", ""))
	var u LLMUsage
	if err := json.Unmarshal(rec.Body.Bytes(), &u); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("usage %d %s", rec.Code, rec.Body)
	}
	if u.Tz != "America/Toronto" || u.Rows[0].Requests != 30 || u.Rows[0].Day.String() != "2026-11-23" {
		t.Fatalf("usage = %+v", u)
	}
	if rec := do(h, llmRequest(t, http.MethodGet, "/api/v1/admin/llm/usage?days=400", "")); rec.Code != http.StatusBadRequest {
		t.Fatalf("days=400: %d", rec.Code)
	}
}

func TestLLMTestPrompt(t *testing.T) {
	gw := &fakeGateway{}
	h := llmHandler(t, gw, allowAll{})
	rec := do(h, llmRequest(t, http.MethodPost, "/api/v1/admin/llm/test", `{"prompt":"ping","provider":"groq"}`))
	var res LLMTestResult
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if res.Text != "echo ping" || res.Provider != "groq" || res.TokensOut != 2 || res.LatencyMs != 15 {
		t.Fatalf("result = %+v", res)
	}

	gw.testErr = fmt.Errorf("%w: gemini: no API key", llm.ErrUnavailable)
	rec = do(h, llmRequest(t, http.MethodPost, "/api/v1/admin/llm/test", `{"prompt":"ping"}`))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("unavailable: %d", rec.Code)
	}
	if p := decodeProblem(t, rec); p.Code != CodeLLMUnavailable || !strings.Contains(*p.Detail, "no API key") {
		t.Fatalf("problem = %+v", p)
	}

	if rec := do(h, llmRequest(t, http.MethodPost, "/api/v1/admin/llm/test", `{"prompt":""}`)); rec.Code != http.StatusBadRequest {
		t.Fatalf("empty prompt: %d", rec.Code)
	}
}

func TestLLMProviderKey(t *testing.T) {
	gw := &fakeGateway{keys: map[string]string{}}
	h := llmHandler(t, gw, allowAll{})
	if rec := do(h, llmRequest(t, http.MethodPut, "/api/v1/admin/llm/providers/gemini/key", `{"apiKey":"AIza-test"}`)); rec.Code != http.StatusNoContent || gw.keys["gemini"] != "AIza-test" {
		t.Fatalf("put: %d %s", rec.Code, rec.Body)
	}
	if rec := do(h, llmRequest(t, http.MethodDelete, "/api/v1/admin/llm/providers/gemini/key", "")); rec.Code != http.StatusNoContent || gw.keys["gemini"] != "" {
		t.Fatalf("delete: %d", rec.Code)
	}
	rec := do(h, llmRequest(t, http.MethodPut, "/api/v1/admin/llm/providers/nope/key", `{"apiKey":"x"}`))
	if rec.Code != http.StatusNotFound || decodeProblem(t, rec).Code != CodeNotFound {
		t.Fatalf("unknown provider: %d", rec.Code)
	}
}

func TestLLMWithoutGateway(t *testing.T) {
	h := llmHandler(t, nil, allowAll{})
	rec := do(h, llmRequest(t, http.MethodGet, "/api/v1/admin/llm/status", ""))
	if rec.Code != http.StatusServiceUnavailable || decodeProblem(t, rec).Code != CodeLLMUnavailable {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}

func TestLLMProblem(t *testing.T) {
	for _, err := range []error{llm.ErrUnavailable, fmt.Errorf("x: %w", llm.ErrBadOutput)} {
		if p, ok := LLMProblem(t.Context(), err); !ok || p.Status != http.StatusServiceUnavailable || p.Code != CodeLLMUnavailable {
			t.Errorf("%v -> %+v %v", err, p, ok)
		}
	}
	if _, ok := LLMProblem(t.Context(), llm.ErrInvalidRequest); ok {
		t.Error("invalid request mapped to llm_unavailable")
	}
}
