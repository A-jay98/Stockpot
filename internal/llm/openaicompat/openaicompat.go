// Package openaicompat is an llm.Provider for OpenAI-compatible chat
// completions APIs: Google Gemini's OpenAI endpoint, Groq, OpenRouter,
// Cerebras, GitHub Models and local servers.
package openaicompat

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/a-jay98/stockpot/internal/llm"
)

// maxResponseBytes caps a provider's response body.
const maxResponseBytes = 4 << 20

// Config configures one endpoint.
type Config struct {
	ID      string
	BaseURL string // e.g. https://api.groq.com/openai/v1
	APIKey  string
	Model   string
	Caps    llm.Capabilities
	Timeout time.Duration // per call; 0 means none beyond ctx
	// HTTPClient defaults to http.DefaultClient.
	HTTPClient *http.Client
}

// Client implements llm.Provider.
type Client struct {
	cfg      Config
	endpoint string
}

var _ llm.Provider = (*Client)(nil)

// New returns a client for cfg.
func New(cfg Config) (*Client, error) {
	if cfg.BaseURL == "" || cfg.Model == "" {
		return nil, errors.New("openaicompat: base URL and model are required")
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = http.DefaultClient
	}
	return &Client{cfg: cfg, endpoint: strings.TrimRight(cfg.BaseURL, "/") + "/chat/completions"}, nil
}

// Factory builds a Client from a provider's settings, for llm.Deps.
func Factory(hc *http.Client) llm.Factory {
	return func(p llm.ProviderConfig, apiKey string) (llm.Provider, error) {
		return New(Config{
			ID:      p.ID,
			BaseURL: p.BaseURL,
			APIKey:  apiKey,
			Model:   p.Model,
			Caps:    llm.Capabilities{Vision: p.Vision, JSONSchema: p.JSONSchema},
			Timeout: p.Timeout(),

			HTTPClient: hc,
		})
	}
}

// ID implements llm.Provider.
func (c *Client) ID() string { return c.cfg.ID }

// Model implements llm.Provider.
func (c *Client) Model() string { return c.cfg.Model }

// Capabilities implements llm.Provider.
func (c *Client) Capabilities() llm.Capabilities { return c.cfg.Caps }

// Wire types (the subset of the chat completions API we use).
type (
	chatRequest struct {
		Model          string          `json:"model"`
		Messages       []chatMessage   `json:"messages"`
		MaxTokens      int             `json:"max_tokens,omitempty"`
		Temperature    *float64        `json:"temperature,omitempty"`
		ResponseFormat *responseFormat `json:"response_format,omitempty"`
	}
	chatMessage struct {
		Role    string `json:"role"`
		Content any    `json:"content"` // string, or []contentPart with images
	}
	contentPart struct {
		Type     string    `json:"type"`
		Text     string    `json:"text,omitempty"`
		ImageURL *imageURL `json:"image_url,omitempty"`
	}
	imageURL struct {
		URL string `json:"url"`
	}
	responseFormat struct {
		Type       string      `json:"type"`
		JSONSchema *jsonSchema `json:"json_schema,omitempty"`
	}
	jsonSchema struct {
		Name   string          `json:"name"`
		Schema json.RawMessage `json:"schema"`
	}
	chatResponse struct {
		Model   string `json:"model"`
		Choices []struct {
			Message struct {
				Content *string `json:"content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage *struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
	}
)

// Complete implements llm.Provider.
func (c *Client) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	body, err := json.Marshal(c.buildRequest(req))
	if err != nil {
		return llm.Response{}, fmt.Errorf("openaicompat: encode request: %w", err)
	}
	if c.cfg.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.cfg.Timeout)
		defer cancel()
	}
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return llm.Response{}, fmt.Errorf("openaicompat: %w", err)
	}
	hreq.Header.Set("Content-Type", "application/json")
	hreq.Header.Set("Accept", "application/json")
	if c.cfg.APIKey != "" {
		hreq.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)
	}

	start := time.Now()
	resp, err := c.cfg.HTTPClient.Do(hreq)
	if err != nil {
		if ctx.Err() != nil && !errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return llm.Response{}, ctx.Err() // the caller gave up
		}
		return llm.Response{}, &llm.ProviderError{Err: llm.ErrUpstream, Message: redact(err.Error(), c.cfg.APIKey)}
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	latency := time.Since(start)
	if err != nil {
		return llm.Response{}, &llm.ProviderError{Err: llm.ErrUpstream, Status: resp.StatusCode, Message: "reading response: " + err.Error()}
	}
	if len(raw) > maxResponseBytes {
		return llm.Response{}, &llm.ProviderError{Err: llm.ErrUpstream, Status: resp.StatusCode, Message: "response too large"}
	}
	if resp.StatusCode/100 != 2 {
		return llm.Response{}, classify(resp, raw, c.cfg.APIKey)
	}

	var cr chatResponse
	if err := json.Unmarshal(raw, &cr); err != nil {
		return llm.Response{}, &llm.ProviderError{Err: llm.ErrUpstream, Status: resp.StatusCode, Message: "undecodable response"}
	}
	out := llm.Response{Provider: c.cfg.ID, Model: c.cfg.Model, Latency: latency}
	if cr.Usage != nil {
		out.Usage = llm.Usage{InputTokens: cr.Usage.PromptTokens, OutputTokens: cr.Usage.CompletionTokens}
	}
	if len(cr.Choices) == 0 || cr.Choices[0].Message.Content == nil || *cr.Choices[0].Message.Content == "" {
		return out, &llm.ProviderError{Err: llm.ErrBadOutput, Status: resp.StatusCode, Message: "empty reply"}
	}
	out.Text = *cr.Choices[0].Message.Content
	if req.Schema != nil && cr.Choices[0].FinishReason == "length" {
		return out, &llm.ProviderError{Err: llm.ErrBadOutput, Status: resp.StatusCode, Message: "reply truncated at max tokens"}
	}
	return out, nil
}

func (c *Client) buildRequest(req llm.Request) chatRequest {
	cr := chatRequest{Model: c.cfg.Model, MaxTokens: req.MaxTokens, Temperature: req.Temperature}
	msgs := req.Messages
	if req.Schema != nil {
		if c.cfg.Caps.JSONSchema {
			name := req.SchemaName
			if name == "" {
				name = "result"
			}
			cr.ResponseFormat = &responseFormat{Type: "json_schema", JSONSchema: &jsonSchema{Name: name, Schema: req.Schema}}
		} else {
			// JSON mode only guarantees some JSON object: put the schema in
			// the prompt and let the router validate the reply.
			cr.ResponseFormat = &responseFormat{Type: "json_object"}
			msgs = append([]llm.Message{{
				Role: llm.RoleSystem,
				Text: "Reply with only a JSON object that validates against this JSON Schema:\n" + string(req.Schema),
			}}, msgs...)
		}
	}
	for _, m := range msgs {
		cm := chatMessage{Role: string(m.Role), Content: m.Text}
		if len(m.Images) > 0 {
			parts := make([]contentPart, 0, len(m.Images)+1)
			if m.Text != "" {
				parts = append(parts, contentPart{Type: "text", Text: m.Text})
			}
			for _, img := range m.Images {
				url := "data:" + img.MIME + ";base64," + base64.StdEncoding.EncodeToString(img.Data)
				parts = append(parts, contentPart{Type: "image_url", ImageURL: &imageURL{URL: url}})
			}
			cm.Content = parts
		}
		cr.Messages = append(cr.Messages, cm)
	}
	return cr
}

var (
	// Gemini: "retryDelay": "41s" in the error details.
	retryDelayPattern = regexp.MustCompile(`"retryDelay"\s*:\s*"([0-9.]+)s"`)
	// Per-day limits: Gemini quota ids ("…PerDay…"), Groq messages
	// ("requests per day (RPD)", "tokens per day (TPD)").
	dailyPattern = regexp.MustCompile(`(?i)per ?day|\bRPD\b|\bTPD\b`)
)

// classify turns a non-2xx response into a *ProviderError.
func classify(resp *http.Response, body []byte, apiKey string) error {
	msg := redact(errorMessage(body), apiKey)
	e := &llm.ProviderError{Status: resp.StatusCode, Message: msg}
	switch {
	case resp.StatusCode == http.StatusTooManyRequests:
		e.Err = llm.ErrQuota
		e.RetryAfter = retryAfter(resp.Header, body)
		e.Daily = dailyPattern.Match(body)
	case resp.StatusCode >= 500, resp.StatusCode == http.StatusRequestTimeout:
		e.Err = llm.ErrUpstream
	default:
		e.Err = llm.ErrRejected
	}
	return e
}

// retryAfter reads the provider's retry hint: Retry-After (seconds or a
// date), Groq's x-ratelimit-reset-* headers, or Gemini's retryDelay.
func retryAfter(h http.Header, body []byte) time.Duration {
	if v := h.Get("Retry-After"); v != "" {
		if s, err := strconv.Atoi(v); err == nil && s >= 0 {
			return time.Duration(s) * time.Second
		}
		if t, err := http.ParseTime(v); err == nil {
			return max(time.Until(t), 0)
		}
	}
	var best time.Duration
	for _, k := range []string{"X-Ratelimit-Reset-Requests", "X-Ratelimit-Reset-Tokens"} {
		if d, err := time.ParseDuration(h.Get(k)); err == nil && d > best {
			best = d
		}
	}
	if best > 0 {
		return best
	}
	if m := retryDelayPattern.FindSubmatch(body); m != nil {
		if s, err := strconv.ParseFloat(string(m[1]), 64); err == nil {
			return time.Duration(s * float64(time.Second))
		}
	}
	return 0
}

// errorMessage extracts the message from OpenAI-style ({"error":{…}}) or
// Google-style ([{"error":{…}}]) error bodies, else a prefix of the body.
func errorMessage(body []byte) string {
	type apiErr struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	var one apiErr
	if json.Unmarshal(body, &one) == nil && one.Error.Message != "" {
		return truncate(one.Error.Message)
	}
	var list []apiErr
	if json.Unmarshal(body, &list) == nil && len(list) > 0 && list[0].Error.Message != "" {
		return truncate(list[0].Error.Message)
	}
	return truncate(strings.TrimSpace(string(body)))
}

func truncate(s string) string {
	const n = 300
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

// redact removes the API key from text that echoes the request.
func redact(s, key string) string {
	if key == "" {
		return s
	}
	return strings.ReplaceAll(s, key, "[redacted]")
}
