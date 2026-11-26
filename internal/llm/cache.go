package llm

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/a-jay98/stockpot/internal/db/dbq"
	"github.com/a-jay98/stockpot/internal/jobs"
)

// cacheKeyVersion changes whenever the normalization below does, so old
// entries stop matching instead of answering a differently-shaped request.
const cacheKeyVersion = 1

// cacheKey is the SHA-256 of the provider-agnostic, normalized request:
// whitespace-trimmed text with LF line endings, images by content hash,
// and the schema re-encoded with sorted keys.
func cacheKey(req Request) []byte {
	type img struct {
		Ref  string `json:"ref"`
		MIME string `json:"mime"`
	}
	type msg struct {
		Role   Role   `json:"role"`
		Text   string `json:"text"`
		Images []img  `json:"images,omitempty"`
	}
	k := struct {
		V           int      `json:"v"`
		Feature     string   `json:"feature"`
		Messages    []msg    `json:"messages"`
		Schema      any      `json:"schema,omitempty"`
		SchemaName  string   `json:"schema_name,omitempty"`
		MaxTokens   int      `json:"max_tokens,omitempty"`
		Temperature *float64 `json:"temperature,omitempty"`
	}{V: cacheKeyVersion, Feature: req.Feature.Name, SchemaName: req.SchemaName, MaxTokens: req.MaxTokens, Temperature: req.Temperature}
	for _, m := range req.Messages {
		cm := msg{Role: m.Role, Text: strings.TrimSpace(strings.ReplaceAll(m.Text, "\r\n", "\n"))}
		for _, i := range m.Images {
			cm.Images = append(cm.Images, img{Ref: string(i.Ref), MIME: i.MIME})
		}
		k.Messages = append(k.Messages, cm)
	}
	if req.Schema != nil {
		_ = json.Unmarshal(req.Schema, &k.Schema) // compiled already, so valid
	}
	b, _ := json.Marshal(k) // plain data: cannot fail
	sum := sha256.Sum256(b)
	return sum[:]
}

func (r *Router) cacheGet(ctx context.Context, key []byte, structured bool) (Response, bool) {
	row, err := r.q.GetLLMCache(ctx, dbq.GetLLMCacheParams{Key: key, Now: r.now()})
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			slog.WarnContext(ctx, "llm: cache read failed", "err", err)
		}
		return Response{}, false
	}
	resp := Response{Text: row.Response, Provider: row.Provider, Model: row.Model, Cached: true}
	if structured {
		resp.JSON = json.RawMessage(row.Response)
	}
	return resp, true
}

func (r *Router) cachePut(ctx context.Context, key []byte, f Feature, resp Response) {
	text := resp.Text
	if resp.JSON != nil {
		text = string(resp.JSON)
	}
	now := r.now()
	err := r.q.PutLLMCache(ctx, dbq.PutLLMCacheParams{
		Key: key, Feature: f.Name, Provider: resp.Provider, Model: resp.Model,
		Response: text, CreatedAt: now, ExpiresAt: now.Add(f.CacheTTL),
	})
	if err != nil {
		slog.WarnContext(ctx, "llm: cache write failed", "err", err)
	}
}

// compiledSchema validates replies against a request's JSON Schema.
type compiledSchema struct{ s *jsonschema.Schema }

func compileSchema(raw json.RawMessage) (*compiledSchema, error) {
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	c := jsonschema.NewCompiler()
	if err := c.AddResource("request.json", doc); err != nil {
		return nil, err
	}
	s, err := c.Compile("request.json")
	if err != nil {
		return nil, err
	}
	return &compiledSchema{s: s}, nil
}

// validate parses a reply as JSON (tolerating a Markdown code fence) and
// checks it against the schema, returning the compacted JSON.
func (c *compiledSchema) validate(text string) (json.RawMessage, error) {
	t := strings.TrimSpace(text)
	if strings.HasPrefix(t, "```") {
		t = strings.TrimPrefix(t, "```json")
		t = strings.TrimPrefix(t, "```")
		t = strings.TrimSpace(strings.TrimSuffix(t, "```"))
	}
	inst, err := jsonschema.UnmarshalJSON(strings.NewReader(t))
	if err != nil {
		return nil, &ProviderError{Err: ErrBadOutput, Message: "reply is not JSON"}
	}
	if err := c.s.Validate(inst); err != nil {
		msg := err.Error()
		if len(msg) > 300 {
			msg = strings.ToValidUTF8(msg[:300], "") + "…"
		}
		return nil, &ProviderError{Err: ErrBadOutput, Message: msg}
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, []byte(t)); err != nil {
		return nil, &ProviderError{Err: ErrBadOutput, Message: fmt.Sprintf("reply is not JSON: %v", err)}
	}
	return buf.Bytes(), nil
}

// sweepBatch is how many rows one retention DELETE removes.
const sweepBatch = 5000

// Sweepers prune llm_call (per retention.llm_call_days) and expired
// llm_cache entries in the daily retention job.
func Sweepers(pool *pgxpool.Pool) []jobs.Sweeper {
	q := dbq.New(pool)
	return []jobs.Sweeper{
		{
			Name: "llm_call",
			Days: SettingRetentionDays,
			Sweep: func(ctx context.Context, before time.Time) (int64, error) {
				return jobs.SweepInBatches(ctx, sweepBatch, func(ctx context.Context) (int64, error) {
					return q.DeleteLLMCallsBefore(ctx, dbq.DeleteLLMCallsBeforeParams{Before: before, BatchSize: sweepBatch})
				})
			},
		},
		{
			Name: "llm_cache",
			// No Days: entries carry their own expiry; before is now.
			Sweep: func(ctx context.Context, before time.Time) (int64, error) {
				return jobs.SweepInBatches(ctx, sweepBatch, func(ctx context.Context) (int64, error) {
					return q.DeleteLLMCacheExpired(ctx, dbq.DeleteLLMCacheExpiredParams{Before: before, BatchSize: sweepBatch})
				})
			},
		},
	}
}
