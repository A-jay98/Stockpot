package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/a-jay98/stockpot/internal/db/dbq"
)

// ProviderStatus is one provider as the admin panel shows it.
type ProviderStatus struct {
	Config ProviderConfig
	HasKey bool
	// State is ready, disabled, no_key, unknown_kind, capped (our daily
	// cap), exhausted (provider quota) or cooling_down (failing).
	State  string
	Detail string
	// Until is when a capped, exhausted or cooling_down provider returns.
	Until *time.Time
	// RequestsToday and TokensToday count since ResetAt's previous midnight.
	RequestsToday     int64
	TokensToday       int64
	RemainingRequests *int64 // nil without a cap
	RemainingTokens   *int64
	ResetAt           time.Time
}

// Status is the gateway's state now.
type Status struct {
	Enabled   bool
	Available bool // a request without images could be served now
	Vision    bool // a request with images could be served now
	Providers []ProviderStatus
	// Global usage today in TZ, against the global request cap (0 = none).
	TZ               string
	RequestsToday    int64
	TokensToday      int64
	GlobalRequestCap int
	GlobalRemaining  *int64
	ResetAt          time.Time
}

// Status reports configuration, quota and usage per provider and overall.
func (r *Router) Status(ctx context.Context) (Status, error) {
	now := r.now()
	loc := usageLocation(r.settings)
	out := Status{
		Enabled:          FlagEnabled.Get(r.settings),
		TZ:               loc.String(),
		GlobalRequestCap: SettingGlobalDailyRequestCap.Get(r.settings),
		ResetAt:          nextReset(now, loc),
	}
	u, err := r.q.LLMUsageSince(ctx, startOfDay(now, loc))
	if err != nil {
		return Status{}, fmt.Errorf("llm: read usage: %w", err)
	}
	out.RequestsToday, out.TokensToday = u.Requests, u.Tokens
	globalOK := true
	if c := int64(out.GlobalRequestCap); c > 0 {
		rem := max(c-u.Requests, 0)
		out.GlobalRemaining = &rem
		globalOK = rem > 0
	}
	for _, pc := range SettingProviders.Get(r.settings) {
		st, err := r.providerState(ctx, pc, now)
		if err != nil {
			return Status{}, err
		}
		ps := ProviderStatus{
			Config:        pc,
			HasKey:        st.key != "",
			State:         st.state,
			RequestsToday: st.usage.Requests,
			TokensToday:   st.usage.Tokens,
			ResetAt:       nextReset(now, pc.Location()),
		}
		if st.state != stateReady {
			ps.Detail = st.detail
		}
		if !st.until.IsZero() {
			t := st.until
			ps.Until = &t
		}
		if pc.DailyRequestCap > 0 {
			rem := max(int64(pc.DailyRequestCap)-st.usage.Requests, 0)
			ps.RemainingRequests = &rem
		}
		if pc.DailyTokenCap > 0 {
			rem := max(int64(pc.DailyTokenCap)-st.usage.Tokens, 0)
			ps.RemainingTokens = &rem
		}
		if st.state == stateReady && out.Enabled && globalOK {
			out.Available = true
			out.Vision = out.Vision || pc.Vision
		}
		out.Providers = append(out.Providers, ps)
	}
	return out, nil
}

// UsageRow is usage for one day, provider and feature.
type UsageRow struct {
	Day          time.Time // local midnight in the usage zone
	Provider     string
	Feature      string
	Requests     int64
	CacheHits    int64
	Failures     int64
	TokensIn     int64
	TokensOut    int64
	AvgLatencyMs int
}

// Usage returns per-day usage for the last days days (today included), in
// the usage zone, newest first.
func (r *Router) Usage(ctx context.Context, days int) ([]UsageRow, string, error) {
	loc := usageLocation(r.settings)
	today := startOfDay(r.now(), loc)
	since := time.Date(today.Year(), today.Month(), today.Day()-(days-1), 0, 0, 0, 0, loc)
	rows, err := r.q.LLMUsageByDay(ctx, dbq.LLMUsageByDayParams{Tz: loc.String(), Since: since})
	if err != nil {
		return nil, "", fmt.Errorf("llm: usage: %w", err)
	}
	out := make([]UsageRow, 0, len(rows))
	for _, row := range rows {
		out = append(out, UsageRow{
			Day:          time.Date(row.Day.Time.Year(), row.Day.Time.Month(), row.Day.Time.Day(), 0, 0, 0, 0, loc),
			Provider:     row.Provider,
			Feature:      row.Feature,
			Requests:     row.Requests,
			CacheHits:    row.CacheHits,
			Failures:     row.Failures,
			TokensIn:     row.TokensIn,
			TokensOut:    row.TokensOut,
			AvgLatencyMs: int(row.AvgLatencyMs),
		})
	}
	return out, loc.String(), nil
}

// TestPrompt sends prompt to one provider (or the normal routing when
// provider is empty), uncached, and returns the reply.
func (r *Router) TestPrompt(ctx context.Context, provider, prompt string) (Response, error) {
	return r.Complete(ctx, Request{
		Feature:   FeatureAdminTest,
		Provider:  provider,
		Messages:  []Message{{Role: RoleUser, Text: prompt}},
		MaxTokens: 512,
	})
}

// ErrUnknownProvider means no provider in llm.providers has that id.
var ErrUnknownProvider = errors.New("unknown llm provider")

// SetAPIKey stores (key != "") or removes (key == "") a provider's API key.
// A new key lifts any quota or failure block on the provider.
func (r *Router) SetAPIKey(ctx context.Context, id, key string) error {
	var pc *ProviderConfig
	for _, p := range SettingProviders.Get(r.settings) {
		if p.ID == id {
			pc = &p
		}
	}
	s, ok := SettingAPIKey.Member(id)
	h, _ := SettingAPIKeyHost.Member(id)
	if pc == nil || !ok {
		return fmt.Errorf("%w: %q", ErrUnknownProvider, id)
	}
	if key == "" {
		if err := r.write(ctx, s.Key(), nil); err != nil {
			return err
		}
		return r.write(ctx, h.Key(), nil)
	}
	// Bind the key to the provider's current host first, so a key is never
	// stored without one.
	host, err := json.Marshal(keyHost(pc.BaseURL))
	if err != nil {
		return err
	}
	if err := r.write(ctx, h.Key(), host); err != nil {
		return err
	}
	raw, err := json.Marshal(key)
	if err != nil {
		return err
	}
	return r.write(ctx, s.Key(), raw)
}
