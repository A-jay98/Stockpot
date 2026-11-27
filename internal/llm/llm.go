// Package llm is Stockpot's provider-agnostic gateway to large language
// models. Features build a Request and call Router.Complete; the router
// picks a configured provider (settings "llm.providers", keys
// "llm.api_key.<id>") that has the needed capability and quota left, fails
// over on quota and outages, meters every call (table llm_call) and caches
// responses (table llm_cache).
//
// Every LLM feature must have a manual path: when Complete returns
// ErrUnavailable (or ErrBadOutput), the API answers 503 llm_unavailable and
// the client falls back to manual entry.
package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/a-jay98/stockpot/internal/blob"
)

// Errors callers branch on (with errors.Is).
var (
	// ErrUnavailable means no provider can serve the request right now:
	// LLM features are off, nothing is configured, or every candidate is out
	// of quota or failing. Callers degrade to the manual path.
	ErrUnavailable = errors.New("llm unavailable")
	// ErrDeferred means a Background request was not tried because the quota
	// left is within the reserve kept for interactive features. It is an
	// ErrUnavailable: callers retry later.
	ErrDeferred = fmt.Errorf("%w: background request deferred to keep the reserve for interactive use", ErrUnavailable)
	// ErrQuota means a provider refused the call for rate or quota limits.
	ErrQuota = errors.New("llm quota exhausted")
	// ErrBadOutput means the model's reply didn't match the requested JSON
	// schema (or was empty or truncated).
	ErrBadOutput = errors.New("llm output did not match the schema")
	// ErrUpstream means the provider failed (5xx, timeout, network): retry
	// elsewhere.
	ErrUpstream = errors.New("llm provider failed")
	// ErrRejected means the provider refused the request itself (4xx other
	// than 429: bad key, unsupported input, too large).
	ErrRejected = errors.New("llm provider rejected the request")
	// ErrInvalidRequest means the Request is malformed (a programming error).
	ErrInvalidRequest = errors.New("invalid llm request")
)

// ProviderError is what a Provider returns on failure. Err is one of
// ErrQuota, ErrUpstream, ErrRejected or ErrBadOutput.
type ProviderError struct {
	Err error
	// Status is the HTTP status, 0 when none was received.
	Status int
	// Message is the provider's explanation, safe to log (never the key).
	Message string
	// RetryAfter is the provider's hint for when to retry (quota), 0 if none.
	RetryAfter time.Duration
	// Daily marks a quota error for a per-day limit (as opposed to per
	// minute), so the provider stays exhausted until its daily reset.
	Daily bool
}

func (e *ProviderError) Error() string {
	s := e.Err.Error()
	if e.Status != 0 {
		s = fmt.Sprintf("%s (HTTP %d)", s, e.Status)
	}
	if e.Message != "" {
		s += ": " + e.Message
	}
	return s
}

func (e *ProviderError) Unwrap() error { return e.Err }

// Priority says whether a person is waiting for the answer.
type Priority int

// Request priorities.
const (
	// Interactive is the default: a person waits, so the request may use all
	// the quota there is.
	Interactive Priority = iota
	// Background work (enrichment, backfills) only runs while the quota left
	// is above the reserve (llm.background_reserve_pct), so it never starves
	// interactive features. Otherwise the router answers ErrDeferred.
	Background
)

// Feature tags a request with the product feature it serves, for metering,
// and sets how long its responses may be reused.
type Feature struct {
	// Name is a stable lower_snake identifier, e.g. "recipe_parse".
	Name string
	// CacheTTL is how long an identical request is answered from the cache;
	// 0 disables caching.
	CacheTTL time.Duration
}

// FeatureAdminTest is the admin panel's "test prompt" (never cached).
var FeatureAdminTest = Feature{Name: "admin_test"}

// Role is who wrote a message.
type Role string

// Message roles.
const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
)

// Message is one turn of the conversation.
type Message struct {
	Role   Role
	Text   string
	Images []Image // only on user messages; needs a vision provider
}

// Image is a picture from the blob store attached to a message.
type Image struct {
	Ref  blob.Ref
	MIME string // image/jpeg, image/png or image/webp
	// Data is the image content. The Router fills it from Ref before calling
	// a provider; callers leave it empty.
	Data []byte
}

// Request is one completion request.
type Request struct {
	Feature  Feature
	Messages []Message
	// Schema, when set, is a JSON Schema the reply must satisfy; the reply
	// is then a JSON document (Response.JSON).
	Schema json.RawMessage
	// SchemaName names the schema for providers that want one (default
	// "result").
	SchemaName string
	// MaxTokens caps the reply length; 0 leaves the provider default.
	MaxTokens int
	// Temperature, when set, overrides the provider default.
	Temperature *float64
	// Priority defaults to Interactive.
	Priority Priority
	// Provider pins the request to one provider id (admin test prompt).
	Provider string
}

// NeedsVision reports whether any message carries an image.
func (r *Request) NeedsVision() bool {
	for _, m := range r.Messages {
		if len(m.Images) > 0 {
			return true
		}
	}
	return false
}

// Usage is the tokens a call consumed, as the provider reported them.
type Usage struct {
	InputTokens  int
	OutputTokens int
}

// Response is a completion.
type Response struct {
	Text string
	// JSON is the validated reply when the request had a Schema.
	JSON     json.RawMessage
	Provider string
	Model    string
	Usage    Usage
	Latency  time.Duration
	// Cached is true when the response came from llm_cache.
	Cached bool
}

// Capabilities describe what a provider (model) can take.
type Capabilities struct {
	Vision bool
	// JSONSchema means the provider constrains output to a JSON schema
	// (response_format json_schema). Without it, structured requests use
	// plain JSON mode and the router validates the reply.
	JSONSchema bool
	// MaxContext is the context window in tokens, 0 when unknown.
	MaxContext int
}

// Provider is one configured model endpoint.
type Provider interface {
	ID() string
	Model() string
	Capabilities() Capabilities
	// Complete runs req. Failures are *ProviderError. Image Data is filled.
	Complete(ctx context.Context, req Request) (Response, error)
}
