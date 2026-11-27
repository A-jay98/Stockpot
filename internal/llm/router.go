package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/a-jay98/stockpot/internal/blob"
	"github.com/a-jay98/stockpot/internal/db/dbq"
	"github.com/a-jay98/stockpot/internal/reqctx"
	"github.com/a-jay98/stockpot/internal/settings"
)

// Factory builds a Provider of one kind from its settings and API key.
type Factory func(p ProviderConfig, apiKey string) (Provider, error)

// Deps are what the Router needs.
type Deps struct {
	Pool     *pgxpool.Pool
	Settings *settings.Store
	// Blobs resolves image refs; nil rejects requests with images.
	Blobs blob.Store
	// Factories maps a provider kind to its adapter.
	Factories map[string]Factory
	// Now defaults to time.Now.
	Now func() time.Time
	// WriteSetting stores (value != nil) or resets a setting on behalf of
	// SetAPIKey, so the app can route it through the audited admin path.
	// Default: Settings.Set / Settings.Reset, unaudited.
	WriteSetting func(ctx context.Context, key string, value json.RawMessage) error
}

// Back-off after failures that aren't quota (which has its own hints).
const (
	minuteQuotaBackoff = time.Minute      // a 429 with no hint and no daily marker
	upstreamBackoff    = 30 * time.Second // 5xx, timeout, network
	authBackoff        = 5 * time.Minute  // 401/403: the key is wrong or revoked
	maxBlock           = 26 * time.Hour   // cap on any provider hint
	maxImageBytes      = 10 << 20         // per image
	allowedImageMIMEs  = "image/jpeg image/png image/webp"
	statusOK           = "ok"
	statusQuota        = "quota"
	statusBadOutput    = "bad_output"
	statusRejected     = "rejected"
	statusError        = "error"
	blockReasonQuota   = "exhausted"
	blockReasonFailing = "cooling_down"
)

// Router chooses a provider per request. Safe for concurrent use.
//
// Caps are checked against llm_call before each call, so concurrent calls
// can overshoot a cap by the number in flight; provider-side limits still
// apply and trigger failover.
type Router struct {
	q         *dbq.Queries
	settings  *settings.Store
	blobs     blob.Store
	factories map[string]Factory
	now       func() time.Time
	write     func(ctx context.Context, key string, value json.RawMessage) error

	mu      sync.Mutex
	blocked map[string]block // by provider id
}

type block struct {
	// key is the API key in use when the block began: a new key (pasted in
	// any process) lifts the block.
	key    string
	until  time.Time
	reason string // blockReasonQuota or blockReasonFailing
	detail string
}

// NewRouter returns a router over d.
func NewRouter(d Deps) *Router {
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.WriteSetting == nil {
		d.WriteSetting = func(ctx context.Context, key string, value json.RawMessage) error {
			if value == nil {
				return d.Settings.Reset(ctx, key)
			}
			return d.Settings.Set(ctx, key, value, nil)
		}
	}
	return &Router{
		write:     d.WriteSetting,
		q:         dbq.New(d.Pool),
		settings:  d.Settings,
		blobs:     d.Blobs,
		factories: d.Factories,
		now:       d.Now,
		blocked:   map[string]block{},
	}
}

// Complete answers req from the cache or the first provider that can take
// it, failing over on quota, outages and bad output. It returns an error
// matching ErrUnavailable when no provider could answer, ErrBadOutput when
// every provider tried replied with invalid output, ErrInvalidRequest for a
// malformed request, or the context's error.
func (r *Router) Complete(ctx context.Context, req Request) (Response, error) {
	if err := validateRequest(&req); err != nil {
		return Response{}, err
	}
	var schema *compiledSchema
	if req.Schema != nil {
		var err error
		if schema, err = compileSchema(req.Schema); err != nil {
			return Response{}, fmt.Errorf("%w: schema: %w", ErrInvalidRequest, err)
		}
	}
	if !FlagEnabled.Get(r.settings) {
		return Response{}, fmt.Errorf("%w: disabled by %s", ErrUnavailable, FlagEnabled.Key())
	}

	var ckey []byte
	if req.Feature.CacheTTL > 0 && req.Provider == "" {
		ckey = cacheKey(req)
		if resp, ok := r.cacheGet(ctx, ckey, req.Schema != nil); ok {
			r.record(ctx, req.Feature, resp.Provider, resp.Model, resp, nil, true)
			return resp, nil
		}
	}

	now := r.now()
	if capReq := SettingGlobalDailyRequestCap.Get(r.settings); capReq > 0 {
		u, err := r.q.LLMUsageSince(ctx, startOfDay(now, usageLocation(r.settings)))
		if err != nil {
			return Response{}, fmt.Errorf("llm: read usage: %w", err)
		}
		if u.Requests >= int64(capReq) {
			return Response{}, fmt.Errorf("%w: global daily cap of %d requests reached", ErrUnavailable, capReq)
		}
		if req.Priority == Background && !aboveReserve(int64(capReq)-u.Requests, int64(capReq), reservePct(r.settings)) {
			return Response{}, fmt.Errorf("%w: global daily cap has %d of %d requests left", ErrDeferred, int64(capReq)-u.Requests, capReq)
		}
	}

	var (
		reasons   []string
		attempted bool
		allBad    = true
		loaded    bool
		deferred  bool
	)
	for _, pc := range SettingProviders.Get(r.settings) {
		if req.Provider != "" && pc.ID != req.Provider {
			continue
		}
		p, key, why, err := r.candidate(ctx, pc, req, now)
		if err != nil {
			return Response{}, err
		}
		if p == nil {
			reasons = append(reasons, pc.ID+": "+why)
			deferred = deferred || strings.HasPrefix(why, reserveWhy)
			continue
		}
		if !loaded && req.NeedsVision() {
			if req.Messages, err = r.loadImages(ctx, req.Messages); err != nil {
				return Response{}, err
			}
			loaded = true
		}

		attempted = true
		resp, err := p.Complete(ctx, req)
		if ctx.Err() != nil {
			// The provider may still bill it: meter it anyway.
			r.record(ctx, req.Feature, pc.ID, p.Model(), resp, ctx.Err(), false)
			return Response{}, ctx.Err()
		}
		if err == nil && schema != nil {
			resp.JSON, err = schema.validate(resp.Text)
		}
		r.record(ctx, req.Feature, pc.ID, p.Model(), resp, err, false)
		if err == nil {
			if ckey != nil {
				r.cachePut(ctx, ckey, req.Feature, resp)
			}
			return resp, nil
		}
		slog.WarnContext(ctx, "llm call failed", "provider", pc.ID, "feature", req.Feature.Name, "err", err)
		reasons = append(reasons, pc.ID+": "+err.Error())
		allBad = allBad && errors.Is(err, ErrBadOutput)
		r.penalize(pc, key, err, r.now())
	}
	if len(reasons) == 0 {
		reasons = append(reasons, "no provider configured")
		if req.Provider != "" {
			reasons = []string{"unknown provider " + req.Provider}
		}
	}
	if attempted && allBad {
		return Response{}, fmt.Errorf("%w: %s", ErrBadOutput, strings.Join(reasons, "; "))
	}
	if !attempted && deferred {
		return Response{}, fmt.Errorf("%w: %s", ErrDeferred, strings.Join(reasons, "; "))
	}
	return Response{}, fmt.Errorf("%w: %s", ErrUnavailable, strings.Join(reasons, "; "))
}

// candidate returns pc's provider and the key it uses when it can take req
// now, else why not. err is set only when usage can't be read.
func (r *Router) candidate(ctx context.Context, pc ProviderConfig, req Request, now time.Time) (Provider, string, string, error) {
	st, err := r.providerState(ctx, pc, now)
	if err != nil {
		return nil, "", "", err
	}
	if st.state != stateReady {
		return nil, "", st.detail, nil
	}
	if req.NeedsVision() && !pc.Vision {
		return nil, "", "no image support", nil
	}
	if req.Priority == Background {
		if why := reserveShortfall(pc, st.usage, reservePct(r.settings), 1); why != "" {
			return nil, "", reserveWhy + why, nil
		}
	}
	p, err := r.factories[pc.Kind](pc, st.key)
	if err != nil {
		return nil, "", "misconfigured: " + err.Error(), nil
	}
	return p, st.key, "", nil
}

// Provider states as the admin panel shows them.
const (
	stateReady       = "ready"
	stateDisabled    = "disabled"
	stateNoKey       = "no_key"
	stateUnknownKind = "unknown_kind"
	stateCapped      = "capped"
)

type providerState struct {
	state, detail string
	key           string
	until         time.Time
	usage         dbq.LLMProviderUsageSinceRow
}

func (r *Router) providerState(ctx context.Context, pc ProviderConfig, now time.Time) (providerState, error) {
	var st providerState
	// ids are validated with the setting, so Member succeeds.
	keySetting, _ := SettingAPIKey.Member(pc.ID)
	hostSetting, _ := SettingAPIKeyHost.Member(pc.ID)
	var keyForOtherHost bool
	if keySetting != nil && hostSetting != nil {
		st.key = keySetting.Get(r.settings)
		if st.key != "" && hostSetting.Get(r.settings) != keyHost(pc.BaseURL) {
			st.key, keyForOtherHost = "", true
		}
	}
	u, err := r.q.LLMProviderUsageSince(ctx, dbq.LLMProviderUsageSinceParams{Provider: pc.ID, Since: startOfDay(now, pc.Location())})
	if err != nil {
		return st, fmt.Errorf("llm: read usage of %s: %w", pc.ID, err)
	}
	st.usage = u
	r.mu.Lock()
	b, blocked := r.blocked[pc.ID]
	r.mu.Unlock()
	switch {
	case !pc.Enabled:
		st.state, st.detail = stateDisabled, "disabled"
	case r.factories[pc.Kind] == nil:
		st.state, st.detail = stateUnknownKind, "unknown kind "+pc.Kind
	case keyForOtherHost:
		st.state, st.detail = stateNoKey, "base_url changed since the key was entered; enter the key again"
	case st.key == "":
		st.state, st.detail = stateNoKey, "no API key"
	case blocked && b.key == st.key && now.Before(b.until):
		st.state, st.until = b.reason, b.until
		st.detail = fmt.Sprintf("%s until %s (%s)", b.reason, b.until.UTC().Format(time.RFC3339), b.detail)
	case pc.DailyRequestCap > 0 && u.Requests >= int64(pc.DailyRequestCap):
		st.state, st.until = stateCapped, nextReset(now, pc.Location())
		st.detail = fmt.Sprintf("daily cap of %d requests reached", pc.DailyRequestCap)
	case pc.DailyTokenCap > 0 && u.Tokens >= int64(pc.DailyTokenCap):
		st.state, st.until = stateCapped, nextReset(now, pc.Location())
		st.detail = fmt.Sprintf("daily cap of %d tokens reached", pc.DailyTokenCap)
	default:
		st.state = stateReady
	}
	return st, nil
}

// penalize takes a failing provider out of rotation for a while.
func (r *Router) penalize(pc ProviderConfig, key string, err error, now time.Time) {
	var pe *ProviderError
	if !errors.As(err, &pe) {
		pe = &ProviderError{Err: ErrUpstream}
	}
	var b block
	switch {
	case errors.Is(pe.Err, ErrQuota):
		b.reason, b.detail = blockReasonQuota, "provider quota"
		switch {
		case pe.Daily:
			b.until = laterOf(nextReset(now, pc.Location()), now.Add(pe.RetryAfter))
		case pe.RetryAfter > 0:
			b.until = now.Add(pe.RetryAfter)
		default:
			b.until = now.Add(minuteQuotaBackoff)
		}
	case errors.Is(pe.Err, ErrUpstream):
		b.reason, b.detail, b.until = blockReasonFailing, "provider failing", now.Add(upstreamBackoff)
	case errors.Is(pe.Err, ErrRejected) && (pe.Status == 401 || pe.Status == 403):
		b.reason, b.detail, b.until = blockReasonFailing, "API key rejected", now.Add(authBackoff)
	case errors.Is(pe.Err, ErrRejected) && pe.Status == 404:
		b.reason, b.detail, b.until = blockReasonFailing, "endpoint or model not found", now.Add(authBackoff)
	default:
		return // bad output or a bad request: the next request may be fine
	}
	if limit := now.Add(maxBlock); b.until.After(limit) {
		b.until = limit
	}
	b.key = key
	r.mu.Lock()
	defer r.mu.Unlock()
	// Concurrent failures: a short block (a 503) must not cut a long one (a
	// daily quota) short.
	if old, ok := r.blocked[pc.ID]; ok && old.key == key && old.until.After(b.until) {
		return
	}
	r.blocked[pc.ID] = b
}

// record writes one llm_call row. Metering failures are logged, not
// returned: the answer is already paid for.
func (r *Router) record(ctx context.Context, f Feature, provider, model string, resp Response, callErr error, cacheHit bool) {
	p := dbq.InsertLLMCallParams{
		Time:      r.now(),
		Feature:   f.Name,
		Provider:  provider,
		Model:     model,
		TokensIn:  clampInt32(resp.Usage.InputTokens),
		TokensOut: clampInt32(resp.Usage.OutputTokens),
		LatencyMs: clampInt32(int(resp.Latency.Milliseconds())),
		Status:    statusOf(callErr),
		CacheHit:  cacheHit,
	}
	if cacheHit {
		p.TokensIn, p.TokensOut, p.LatencyMs = 0, 0, 0
	}
	if callErr != nil {
		msg := callErr.Error()
		if len(msg) > 500 {
			msg = strings.ToValidUTF8(msg[:500], "")
		}
		p.Error = &msg
	}
	if info := reqctx.From(ctx); info != nil {
		p.UserID = nonNil(info.UserID)
		p.HouseholdID = nonNil(info.HouseholdID)
	}
	// Record even when the caller's context ended mid-flight.
	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := r.q.InsertLLMCall(wctx, p); err != nil {
		slog.ErrorContext(ctx, "llm: metering write failed", "err", err)
	}
}

func statusOf(err error) string {
	switch {
	case err == nil:
		return statusOK
	case errors.Is(err, ErrQuota):
		return statusQuota
	case errors.Is(err, ErrBadOutput):
		return statusBadOutput
	case errors.Is(err, ErrRejected):
		return statusRejected
	default:
		return statusError
	}
}

// loadImages returns a copy of msgs with every image's Data read from the
// blob store.
func (r *Router) loadImages(ctx context.Context, msgs []Message) ([]Message, error) {
	if r.blobs == nil {
		return nil, fmt.Errorf("%w: no blob store for images", ErrInvalidRequest)
	}
	out := slices.Clone(msgs)
	for i := range out {
		if len(out[i].Images) == 0 {
			continue
		}
		out[i].Images = slices.Clone(out[i].Images)
		for j := range out[i].Images {
			img := &out[i].Images[j]
			data, err := r.readBlob(ctx, img.Ref)
			if err != nil {
				return nil, err
			}
			img.Data = data
		}
	}
	return out, nil
}

func (r *Router) readBlob(ctx context.Context, ref blob.Ref) ([]byte, error) {
	rc, err := r.blobs.Open(ctx, ref)
	if errors.Is(err, blob.ErrNotFound) || errors.Is(err, blob.ErrInvalidRef) {
		return nil, fmt.Errorf("%w: image %s: %w", ErrInvalidRequest, ref, err)
	}
	if err != nil {
		return nil, fmt.Errorf("llm: open image %s: %w", ref, err)
	}
	defer func() { _ = rc.Close() }()
	data, err := io.ReadAll(io.LimitReader(rc, maxImageBytes+1))
	if err != nil {
		return nil, fmt.Errorf("llm: read image %s: %w", ref, err)
	}
	if len(data) > maxImageBytes {
		return nil, fmt.Errorf("%w: image %s is over %d bytes", ErrInvalidRequest, ref, maxImageBytes)
	}
	return data, nil
}

func validateRequest(req *Request) error {
	if !settings.ValidID(req.Feature.Name) {
		return fmt.Errorf("%w: feature name %q", ErrInvalidRequest, req.Feature.Name)
	}
	if len(req.Messages) == 0 {
		return fmt.Errorf("%w: no messages", ErrInvalidRequest)
	}
	for i, m := range req.Messages {
		switch m.Role {
		case RoleSystem, RoleUser, RoleAssistant:
		default:
			return fmt.Errorf("%w: message %d: role %q", ErrInvalidRequest, i, m.Role)
		}
		if len(m.Images) > 0 && m.Role != RoleUser {
			return fmt.Errorf("%w: message %d: only user messages carry images", ErrInvalidRequest, i)
		}
		for _, img := range m.Images {
			if !img.Ref.Valid() || !slices.Contains(strings.Fields(allowedImageMIMEs), img.MIME) {
				return fmt.Errorf("%w: message %d: image %q (%s)", ErrInvalidRequest, i, img.Ref, img.MIME)
			}
		}
	}
	if req.MaxTokens < 0 {
		return fmt.Errorf("%w: negative max tokens", ErrInvalidRequest)
	}
	return nil
}

func clampInt32(v int) int32 {
	return int32(min(max(v, 0), 1<<31-1)) //nolint:gosec // clamped
}

func nonNil(id uuid.UUID) *uuid.UUID {
	if id == uuid.Nil {
		return nil
	}
	return &id
}

func laterOf(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}

// reserveWhy starts the reason of a provider skipped to keep the reserve.
const reserveWhy = "reserve: "

func reservePct(s *settings.Store) int { return SettingBackgroundReservePct.Get(s) }

// aboveReserve reports whether more than pct percent of limit is left.
func aboveReserve(remaining, limit int64, pct int) bool {
	if limit <= 0 {
		return true // no cap of our own
	}
	return remaining*100 > limit*int64(pct)
}

// reserveShortfall says why pc cannot take a background call that needs
// requests more requests, or "" when it can: the quota left after the call
// would be within the reserve.
func reserveShortfall(pc ProviderConfig, u dbq.LLMProviderUsageSinceRow, pct int, requests int64) string {
	if c := int64(pc.DailyRequestCap); c > 0 && !aboveReserve(c-u.Requests-requests+1, c, pct) {
		return fmt.Sprintf("%d of %d daily requests left", max(c-u.Requests, 0), c)
	}
	if c := int64(pc.DailyTokenCap); c > 0 && !aboveReserve(c-u.Tokens, c, pct) {
		return fmt.Sprintf("%d of %d daily tokens left", max(c-u.Tokens, 0), c)
	}
	return ""
}

// Needs is what a background job wants to do, for Headroom.
type Needs struct {
	// Requests is how many calls (default 1).
	Requests int
	// Vision: the calls carry images.
	Vision bool
}

// Headroom reports whether a Background request of the given needs would be
// served now, without calling anyone, so jobs can skip cheaply. When not, retryAt
// is when quota resets or a provider recovers (zero when unknown, e.g. LLM
// features are off or nothing is configured).
func (r *Router) Headroom(ctx context.Context, needs Needs) (ok bool, retryAt time.Time) {
	n := int64(max(needs.Requests, 1))
	if !FlagEnabled.Get(r.settings) {
		return false, time.Time{}
	}
	now := r.now()
	pct := reservePct(r.settings)
	if capReq := int64(SettingGlobalDailyRequestCap.Get(r.settings)); capReq > 0 {
		u, err := r.q.LLMUsageSince(ctx, startOfDay(now, usageLocation(r.settings)))
		if err != nil {
			return false, time.Time{}
		}
		if !aboveReserve(capReq-u.Requests-n+1, capReq, pct) || u.Requests+n > capReq {
			return false, nextReset(now, usageLocation(r.settings))
		}
	}
	var soonest time.Time
	note := func(t time.Time) {
		if !t.IsZero() && (soonest.IsZero() || t.Before(soonest)) {
			soonest = t
		}
	}
	for _, pc := range SettingProviders.Get(r.settings) {
		st, err := r.providerState(ctx, pc, now)
		if err != nil {
			return false, time.Time{}
		}
		switch {
		case st.state != stateReady:
			note(st.until)
		case needs.Vision && !pc.Vision:
		case reserveShortfall(pc, st.usage, pct, n) != "":
			note(nextReset(now, pc.Location()))
		default:
			return true, time.Time{}
		}
	}
	return false, soonest
}
