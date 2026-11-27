package llm

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"
	_ "time/tzdata" // reset_tz must resolve inside the confined (chroot) service

	"github.com/a-jay98/stockpot/internal/settings"
)

// Provider kinds. A kind names the adapter that talks to the endpoint.
const (
	// KindOpenAICompat is an OpenAI-compatible chat completions API
	// (Gemini's OpenAI endpoint, Groq, OpenRouter, Cerebras, GitHub Models).
	KindOpenAICompat = "openai_compat"
)

// Kinds lists the provider kinds the server knows.
var Kinds = []string{KindOpenAICompat}

// ProviderConfig is one entry of llm.providers, edited in the admin panel.
type ProviderConfig struct {
	// ID names the provider in metering and in its key setting
	// llm.api_key.<id>.
	ID      string `json:"id"`
	Kind    string `json:"kind"`
	BaseURL string `json:"base_url"`
	Model   string `json:"model"`
	// Vision: the model accepts images.
	Vision bool `json:"vision"`
	// JSONSchema: the endpoint supports response_format json_schema.
	JSONSchema bool `json:"json_schema"`
	Enabled    bool `json:"enabled"`
	// Daily caps, enforced before calling; 0 means no cap of our own.
	DailyRequestCap int `json:"daily_request_cap"`
	DailyTokenCap   int `json:"daily_token_cap"`
	// ResetTZ is the IANA zone whose midnight resets the provider's daily
	// quota (Google: America/Los_Angeles).
	ResetTZ string `json:"reset_tz"`
	// TimeoutSeconds bounds one call; 0 means the default (60s).
	TimeoutSeconds int `json:"timeout_seconds,omitempty"`
}

// Timeout is the per-call deadline.
func (c ProviderConfig) Timeout() time.Duration {
	if c.TimeoutSeconds <= 0 {
		return defaultTimeout
	}
	return time.Duration(c.TimeoutSeconds) * time.Second
}

const defaultTimeout = 60 * time.Second

// Location is ResetTZ resolved (validated when the setting is stored).
func (c ProviderConfig) Location() *time.Location {
	loc, err := time.LoadLocation(c.ResetTZ)
	if err != nil {
		return time.UTC
	}
	return loc
}

func (c ProviderConfig) validate() error {
	var errs []error
	if !settings.ValidID(c.ID) {
		errs = append(errs, errors.New("id must be lower_snake (a-z, 0-9, _), starting with a letter"))
	}
	known := false
	for _, k := range Kinds {
		known = known || c.Kind == k
	}
	if !known {
		errs = append(errs, fmt.Errorf("kind must be one of %v", Kinds))
	}
	if err := validateBaseURL(c.BaseURL); err != nil {
		errs = append(errs, err)
	}
	if c.Model == "" {
		errs = append(errs, errors.New("model is required"))
	}
	if c.DailyRequestCap < 0 || c.DailyTokenCap < 0 {
		errs = append(errs, errors.New("daily caps must be >= 0 (0 = no cap)"))
	}
	if _, err := time.LoadLocation(c.ResetTZ); err != nil || c.ResetTZ == "" {
		errs = append(errs, fmt.Errorf("reset_tz %q is not an IANA time zone", c.ResetTZ))
	}
	if c.TimeoutSeconds < 0 || c.TimeoutSeconds > 300 {
		errs = append(errs, errors.New("timeout_seconds must be between 0 and 300"))
	}
	if err := errors.Join(errs...); err != nil {
		return fmt.Errorf("provider %q: %w", c.ID, err)
	}
	return nil
}

// validateBaseURL requires https, except plain http to a loopback host (a
// local model server or a test double).
func validateBaseURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("base_url %q must be an absolute URL without credentials, query or fragment", raw)
	}
	switch u.Scheme {
	case "https":
		return nil
	case "http":
		host := u.Hostname()
		if ip := net.ParseIP(host); host == "localhost" || (ip != nil && ip.IsLoopback()) {
			return nil
		}
	}
	return fmt.Errorf("base_url %q must use https", raw)
}

func validateProviders(ps []ProviderConfig) error {
	seen := map[string]bool{}
	var errs []error
	for _, p := range ps {
		if err := p.validate(); err != nil {
			errs = append(errs, err)
		}
		if seen[p.ID] {
			errs = append(errs, fmt.Errorf("provider id %q is used twice", p.ID))
		}
		seen[p.ID] = true
	}
	return errors.Join(errs...)
}

// DefaultProviders is the seeded configuration: Gemini Flash-Lite (free AI
// Studio key, vision) first, Groq (free, text only) as fallback. Both stay
// unused until their key is pasted in the admin panel. Free quotas change
// often and Google shows them per key in AI Studio, so the caps are
// conservative guesses to adjust there.
var DefaultProviders = []ProviderConfig{
	{
		ID:              "gemini",
		Kind:            KindOpenAICompat,
		BaseURL:         "https://generativelanguage.googleapis.com/v1beta/openai/",
		Model:           "gemini-3.5-flash-lite",
		Vision:          true,
		JSONSchema:      true,
		Enabled:         true,
		DailyRequestCap: 450,
		ResetTZ:         "America/Los_Angeles",
	},
	{
		ID:              "groq",
		Kind:            KindOpenAICompat,
		BaseURL:         "https://api.groq.com/openai/v1",
		Model:           "openai/gpt-oss-120b",
		Vision:          false,
		JSONSchema:      true,
		Enabled:         true,
		DailyRequestCap: 900,
		DailyTokenCap:   180000,
		ResetTZ:         "UTC",
	},
}

// Settings.
var (
	// SettingProviders is the ordered provider list; the router tries them
	// in this order.
	SettingProviders = settings.DefineJSON(settings.JSONDef[[]ProviderConfig]{
		Key:         "llm.providers",
		Default:     DefaultProviders,
		Validate:    validateProviders,
		Description: "LLM providers, tried in order. Each needs its key (llm.api_key.<id>). Caps of 0 mean no cap.",
	})
	// SettingAPIKey holds each provider's API key, llm.api_key.<id>.
	SettingAPIKey = settings.DefineFamily(settings.Def[string]{
		Key:         "llm.api_key",
		Secret:      true,
		Description: "API key of an LLM provider. A provider without a key is skipped.",
	})
	// SettingAPIKeyHost records the base_url host each key was entered for,
	// llm.api_key_host.<id>. A key is only sent to that host, so editing a
	// provider's base_url can't redirect its stored key elsewhere.
	SettingAPIKeyHost = settings.DefineFamily(settings.Def[string]{
		Key:         "llm.api_key_host",
		Description: "Host an LLM provider's API key was entered for; the key is only sent there.",
	})
	// SettingGlobalDailyRequestCap caps calls across all providers per day
	// (in llm.usage_tz).
	SettingGlobalDailyRequestCap = settings.Define(settings.Def[int]{
		Key:         "llm.global_daily_request_cap",
		Default:     1000,
		Validate:    settings.Between(0, 1_000_000),
		Description: "Maximum LLM calls per day across all providers (0 = no cap).",
	})
	// SettingBackgroundReservePct is the share of each daily quota kept for
	// interactive requests.
	SettingBackgroundReservePct = settings.Define(settings.Def[int]{
		Key:         "llm.background_reserve_pct",
		Default:     40,
		Validate:    settings.Between(0, 100),
		Description: "Percent of each daily LLM quota (per provider and global) kept for interactive features. Background jobs such as catalog enrichment only run while more than this is left. Only providers with a daily cap (and the global cap) are held back; a provider with no cap of its own is not.",
	})
	// SettingUsageTZ is the zone of the global cap's day and of usage reports.
	SettingUsageTZ = settings.Define(settings.Def[string]{
		Key:     "llm.usage_tz",
		Default: "America/Toronto",
		Validate: func(s string) error {
			if _, err := time.LoadLocation(s); err != nil || s == "" {
				return fmt.Errorf("%q is not an IANA time zone", s)
			}
			return nil
		},
		Description: "Time zone of the global daily cap and of LLM usage reports.",
	})
	// FlagEnabled turns every LLM feature on or off.
	FlagEnabled = settings.Define(settings.Def[bool]{
		Key:         "flag.llm_enabled",
		Default:     true,
		Description: "Use LLM providers. Off: every LLM feature falls back to manual entry.",
	})
	// SettingRetentionDays is how long llm_call rows are kept.
	SettingRetentionDays = settings.Define(settings.Def[int]{
		Key:         "retention.llm_call_days",
		Default:     180,
		Validate:    settings.Between(7, 3650),
		Description: "Days to keep LLM call metering rows.",
	})
)

func usageLocation(s *settings.Store) *time.Location {
	loc, err := time.LoadLocation(SettingUsageTZ.Get(s))
	if err != nil {
		return time.UTC
	}
	return loc
}

// startOfDay is the most recent local midnight in loc at or before t.
func startOfDay(t time.Time, loc *time.Location) time.Time {
	l := t.In(loc)
	return time.Date(l.Year(), l.Month(), l.Day(), 0, 0, 0, 0, loc)
}

// nextReset is the next local midnight in loc after t.
func nextReset(t time.Time, loc *time.Location) time.Time {
	s := startOfDay(t, loc)
	return time.Date(s.Year(), s.Month(), s.Day()+1, 0, 0, 0, 0, loc)
}

// keyHost is the host a provider's key is bound to.
func keyHost(baseURL string) string {
	u, err := url.Parse(baseURL)
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Host)
}
