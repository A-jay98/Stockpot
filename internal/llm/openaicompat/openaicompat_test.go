package openaicompat

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/a-jay98/stockpot/internal/llm"
)

const testKey = "sk-test-secret"

func newClient(t *testing.T, h http.HandlerFunc, caps llm.Capabilities) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c, err := New(Config{ID: "fake", BaseURL: srv.URL + "/v1/", APIKey: testKey, Model: "m-1", Caps: caps, Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func reply(content, finish string) string {
	b, _ := json.Marshal(map[string]any{
		"choices": []any{map[string]any{"message": map[string]any{"content": content}, "finish_reason": finish}},
		"usage":   map[string]any{"prompt_tokens": 12, "completion_tokens": 5},
	})
	return string(b)
}

func TestCompleteSendsRequestAndParsesUsage(t *testing.T) {
	var got chatRequest
	var raw map[string]any
	c := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer "+testKey {
			t.Errorf("path %s auth %q", r.URL.Path, r.Header.Get("Authorization"))
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &got)
		_ = json.Unmarshal(body, &raw)
		_, _ = io.WriteString(w, reply("hello", "stop"))
	}, llm.Capabilities{Vision: true})

	temp := 0.2
	resp, err := c.Complete(t.Context(), llm.Request{
		Feature:     llm.Feature{Name: "t"},
		Temperature: &temp,
		MaxTokens:   100,
		Messages: []llm.Message{
			{Role: llm.RoleSystem, Text: "be brief"},
			{Role: llm.RoleUser, Text: "what is this?", Images: []llm.Image{{MIME: "image/png", Data: []byte{1, 2, 3}}}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != "hello" || resp.Usage != (llm.Usage{InputTokens: 12, OutputTokens: 5}) || resp.Provider != "fake" || resp.Model != "m-1" {
		t.Fatalf("resp = %+v", resp)
	}
	if got.Model != "m-1" || got.MaxTokens != 100 || got.Temperature == nil || *got.Temperature != 0.2 || got.ResponseFormat != nil {
		t.Fatalf("request = %+v", got)
	}
	msgs := raw["messages"].([]any)
	if msgs[0].(map[string]any)["content"] != "be brief" {
		t.Fatalf("system message = %v", msgs[0])
	}
	parts := msgs[1].(map[string]any)["content"].([]any)
	img := parts[1].(map[string]any)["image_url"].(map[string]any)["url"]
	if len(parts) != 2 || img != "data:image/png;base64,AQID" {
		t.Fatalf("user parts = %v", parts)
	}
}

func TestStructuredOutputFormats(t *testing.T) {
	schema := json.RawMessage(`{"type":"object"}`)
	for _, tc := range []struct {
		caps       llm.Capabilities
		wantType   string
		wantSystem bool
	}{
		{llm.Capabilities{JSONSchema: true}, "json_schema", false},
		{llm.Capabilities{}, "json_object", true},
	} {
		var got chatRequest
		c := newClient(t, func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewDecoder(r.Body).Decode(&got)
			_, _ = io.WriteString(w, reply(`{}`, "stop"))
		}, tc.caps)
		if _, err := c.Complete(t.Context(), llm.Request{Schema: schema, SchemaName: "item", Messages: []llm.Message{{Role: llm.RoleUser, Text: "x"}}}); err != nil {
			t.Fatal(err)
		}
		if got.ResponseFormat == nil || got.ResponseFormat.Type != tc.wantType {
			t.Fatalf("%s: response_format = %+v", tc.wantType, got.ResponseFormat)
		}
		if tc.wantType == "json_schema" && (got.ResponseFormat.JSONSchema.Name != "item" || string(got.ResponseFormat.JSONSchema.Schema) != `{"type":"object"}`) {
			t.Fatalf("json_schema = %+v", got.ResponseFormat.JSONSchema)
		}
		hasSystem := len(got.Messages) == 2 && got.Messages[0].Role == "system" && strings.Contains(got.Messages[0].Content.(string), `{"type":"object"}`)
		if hasSystem != tc.wantSystem {
			t.Fatalf("%s: messages = %+v", tc.wantType, got.Messages)
		}
	}
}

func TestErrorClassification(t *testing.T) {
	tests := []struct {
		name      string
		status    int
		header    map[string]string
		body      string
		want      error
		wantRetry time.Duration
		wantDaily bool
	}{
		{
			name: "groq per minute", status: 429, header: map[string]string{"Retry-After": "7"},
			body: `{"error":{"message":"Rate limit reached on requests per minute (RPM)"}}`, want: llm.ErrQuota, wantRetry: 7 * time.Second,
		},
		{
			name: "groq per day", status: 429, header: map[string]string{"X-Ratelimit-Reset-Requests": "2m30s"},
			body: `{"error":{"message":"Rate limit reached for model on requests per day (RPD): Limit 1000"}}`, want: llm.ErrQuota, wantRetry: 150 * time.Second, wantDaily: true,
		},
		{
			name: "gemini daily", status: 429,
			body: `[{"error":{"code":429,"message":"You exceeded your current quota","details":[{"violations":[{"quotaId":"GenerateRequestsPerDayPerProjectPerModel-FreeTier"}]},{"retryDelay":"41s"}]}}]`,
			want: llm.ErrQuota, wantRetry: 41 * time.Second, wantDaily: true,
		},
		{name: "server error", status: 503, body: `oops`, want: llm.ErrUpstream},
		{name: "bad key", status: 401, body: `{"error":{"message":"Invalid API key sk-test-secret"}}`, want: llm.ErrRejected},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newClient(t, func(w http.ResponseWriter, _ *http.Request) {
				for k, v := range tt.header {
					w.Header().Set(k, v)
				}
				w.WriteHeader(tt.status)
				_, _ = io.WriteString(w, tt.body)
			}, llm.Capabilities{})
			_, err := c.Complete(t.Context(), llm.Request{Messages: []llm.Message{{Role: llm.RoleUser, Text: "x"}}})
			var pe *llm.ProviderError
			if !errors.As(err, &pe) || !errors.Is(err, tt.want) {
				t.Fatalf("err = %v", err)
			}
			if pe.Status != tt.status || pe.RetryAfter != tt.wantRetry || pe.Daily != tt.wantDaily {
				t.Fatalf("got %+v", pe)
			}
			if strings.Contains(err.Error(), testKey) {
				t.Fatalf("key leaked: %v", err)
			}
		})
	}
}

func TestBadReplies(t *testing.T) {
	for name, body := range map[string]string{
		"empty":     reply("", "stop"),
		"truncated": reply(`{"a":`, "length"),
	} {
		t.Run(name, func(t *testing.T) {
			c := newClient(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, body) }, llm.Capabilities{JSONSchema: true})
			_, err := c.Complete(t.Context(), llm.Request{Schema: json.RawMessage(`{}`), Messages: []llm.Message{{Role: llm.RoleUser, Text: "x"}}})
			if !errors.Is(err, llm.ErrBadOutput) {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

func TestTimeoutIsUpstream(t *testing.T) {
	done := make(chan struct{})
	c := newClient(t, func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-done:
		}
	}, llm.Capabilities{})
	t.Cleanup(func() { close(done) }) // runs before the server's Close
	c.cfg.Timeout = 50 * time.Millisecond
	_, err := c.Complete(t.Context(), llm.Request{Messages: []llm.Message{{Role: llm.RoleUser, Text: "x"}}})
	if !errors.Is(err, llm.ErrUpstream) {
		t.Fatalf("err = %v", err)
	}
}
