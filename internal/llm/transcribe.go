package llm

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/a-jay98/stockpot/internal/settings"
)

// MaxAudioBytes caps one transcription upload (Groq's free tier takes 25 MB).
const MaxAudioBytes = 25 << 20

// allowedAudioMIMEs are the formats Whisper-style endpoints take.
const allowedAudioMIMEs = "audio/mp4 audio/m4a audio/x-m4a audio/mpeg audio/ogg audio/opus audio/webm audio/wav audio/flac"

// TranscribeRequest is one speech-to-text request.
type TranscribeRequest struct {
	Feature Feature
	// Audio is the recording; Filename's extension tells the provider its format.
	Audio    []byte
	Filename string
	MIME     string
	// Language is an optional ISO 639-1 hint ("fa"); empty lets the model detect it.
	Language string
	// Prompt is optional context that steers spelling (e.g. dish names).
	Prompt   string
	Priority Priority
	// Provider pins the request to one provider id.
	Provider string
}

// Transcription is a transcript.
type Transcription struct {
	Text string
	// Language is what the model detected, when it says.
	Language string
	// AudioSeconds is the recording's length as the provider reported it.
	AudioSeconds float64
	Provider     string
	Model        string
	Latency      time.Duration
	Cached       bool
}

// Transcriber is implemented by providers that can transcribe audio. The
// router only offers it providers whose config names a transcription model.
type Transcriber interface {
	// Transcribe runs req with model. Failures are *ProviderError.
	Transcribe(ctx context.Context, model string, req TranscribeRequest) (Transcription, error)
}

// CanTranscribe reports whether a provider config offers transcription.
func (c ProviderConfig) CanTranscribe() bool { return c.TranscriptionModel != "" }

// Transcribe answers req from the cache or the first provider with a
// transcription model and quota left, failing over like Complete. Calls are
// metered in llm_call (tokens 0) and count against the same daily request
// caps as chat.
func (r *Router) Transcribe(ctx context.Context, req TranscribeRequest) (Transcription, error) {
	if err := validateTranscribe(req); err != nil {
		return Transcription{}, err
	}
	if !FlagEnabled.Get(r.settings) {
		return Transcription{}, fmt.Errorf("%w: disabled by %s", ErrUnavailable, FlagEnabled.Key())
	}
	var ckey []byte
	if req.Feature.CacheTTL > 0 && req.Provider == "" {
		ckey = transcribeCacheKey(req)
		if resp, ok := r.cacheGet(ctx, ckey, true); ok {
			var t Transcription
			if err := json.Unmarshal(resp.JSON, &t); err == nil {
				t.Provider, t.Model, t.Cached = resp.Provider, resp.Model, true
				r.record(ctx, req.Feature, resp.Provider, resp.Model, Response{}, nil, true)
				return t, nil
			}
		}
	}
	now := r.now()
	if err := r.globalCapCheck(ctx, req.Priority, now); err != nil {
		return Transcription{}, err
	}

	var (
		reasons   []string
		attempted bool
		deferred  bool
	)
	for _, pc := range SettingProviders.Get(r.settings) {
		if req.Provider != "" && pc.ID != req.Provider {
			continue
		}
		if !pc.CanTranscribe() {
			continue
		}
		st, err := r.providerState(ctx, pc, now)
		if err != nil {
			return Transcription{}, err
		}
		if st.state != stateReady {
			reasons = append(reasons, pc.ID+": "+st.detail)
			continue
		}
		if req.Priority == Background {
			if why := reserveShortfall(pc, st.usage, reservePct(r.settings), 1); why != "" {
				reasons = append(reasons, pc.ID+": "+reserveWhy+why)
				deferred = true
				continue
			}
		}
		p, err := r.factories[pc.Kind](pc, st.key)
		if err != nil {
			reasons = append(reasons, pc.ID+": misconfigured: "+err.Error())
			continue
		}
		tr, ok := p.(Transcriber)
		if !ok {
			reasons = append(reasons, pc.ID+": adapter cannot transcribe")
			continue
		}
		attempted = true
		start := r.now()
		t, err := tr.Transcribe(ctx, pc.TranscriptionModel, req)
		meter := Response{Latency: t.Latency}
		if meter.Latency == 0 {
			meter.Latency = r.now().Sub(start)
		}
		if ctx.Err() != nil {
			r.record(ctx, req.Feature, pc.ID, pc.TranscriptionModel, meter, ctx.Err(), false)
			return Transcription{}, ctx.Err()
		}
		if err == nil && strings.TrimSpace(t.Text) == "" {
			err = &ProviderError{Err: ErrBadOutput, Message: "empty transcript"}
		}
		r.record(ctx, req.Feature, pc.ID, pc.TranscriptionModel, meter, err, false)
		if err == nil {
			t.Provider, t.Model = pc.ID, pc.TranscriptionModel
			if ckey != nil {
				if b, merr := json.Marshal(t); merr == nil {
					r.cachePut(ctx, ckey, req.Feature, Response{JSON: b, Provider: pc.ID, Model: pc.TranscriptionModel})
				}
			}
			return t, nil
		}
		slog.WarnContext(ctx, "llm transcription failed", "provider", pc.ID, "feature", req.Feature.Name, "err", err)
		reasons = append(reasons, pc.ID+": "+err.Error())
		r.penalize(pc, st.key, err, r.now())
	}
	if len(reasons) == 0 {
		reasons = append(reasons, "no provider with a transcription model")
	}
	if !attempted && deferred {
		return Transcription{}, fmt.Errorf("%w: %s", ErrDeferred, strings.Join(reasons, "; "))
	}
	return Transcription{}, fmt.Errorf("%w: %s", ErrUnavailable, strings.Join(reasons, "; "))
}

// globalCapCheck enforces llm.global_daily_request_cap and its reserve.
func (r *Router) globalCapCheck(ctx context.Context, prio Priority, now time.Time) error {
	capReq := SettingGlobalDailyRequestCap.Get(r.settings)
	if capReq <= 0 {
		return nil
	}
	u, err := r.q.LLMUsageSince(ctx, startOfDay(now, usageLocation(r.settings)))
	if err != nil {
		return fmt.Errorf("llm: read usage: %w", err)
	}
	if u.Requests >= int64(capReq) {
		return fmt.Errorf("%w: global daily cap of %d requests reached", ErrUnavailable, capReq)
	}
	if prio == Background && !aboveReserve(int64(capReq)-u.Requests, int64(capReq), reservePct(r.settings)) {
		return fmt.Errorf("%w: global daily cap has %d of %d requests left", ErrDeferred, int64(capReq)-u.Requests, capReq)
	}
	return nil
}

// TranscriptionAvailable reports whether some provider could transcribe now
// (for the admin panel and to skip downloading audio that can't be used).
func (r *Router) TranscriptionAvailable(ctx context.Context) bool {
	if !FlagEnabled.Get(r.settings) {
		return false
	}
	now := r.now()
	for _, pc := range SettingProviders.Get(r.settings) {
		if !pc.CanTranscribe() {
			continue
		}
		if st, err := r.providerState(ctx, pc, now); err == nil && st.state == stateReady {
			return true
		}
	}
	return false
}

func validateTranscribe(req TranscribeRequest) error {
	switch {
	case !settings.ValidID(req.Feature.Name):
		return fmt.Errorf("%w: feature name %q", ErrInvalidRequest, req.Feature.Name)
	case len(req.Audio) == 0:
		return fmt.Errorf("%w: no audio", ErrInvalidRequest)
	case len(req.Audio) > MaxAudioBytes:
		return fmt.Errorf("%w: audio is over %d bytes", ErrInvalidRequest, MaxAudioBytes)
	case req.Filename == "" || strings.ContainsAny(req.Filename, "/\\\"\r\n"):
		return fmt.Errorf("%w: audio filename %q", ErrInvalidRequest, req.Filename)
	case !containsField(allowedAudioMIMEs, req.MIME):
		return fmt.Errorf("%w: audio type %q", ErrInvalidRequest, req.MIME)
	case len(req.Language) > 8:
		return fmt.Errorf("%w: language %q", ErrInvalidRequest, req.Language)
	case len(req.Prompt) > 1000:
		return fmt.Errorf("%w: prompt over 1000 bytes", ErrInvalidRequest)
	}
	return nil
}

func containsField(list, v string) bool {
	for _, f := range strings.Fields(list) {
		if f == v {
			return true
		}
	}
	return false
}

// transcribeCacheKey hashes the audio, not the request bytes, so the same
// recording is transcribed once.
func transcribeCacheKey(req TranscribeRequest) []byte {
	sum := sha256.Sum256(req.Audio)
	k := struct {
		V        int    `json:"v"`
		Kind     string `json:"kind"`
		Feature  string `json:"feature"`
		Audio    string `json:"audio"`
		Language string `json:"language,omitempty"`
		Prompt   string `json:"prompt,omitempty"`
	}{cacheKeyVersion, "transcribe", req.Feature.Name, fmt.Sprintf("%x", sum), req.Language, strings.TrimSpace(req.Prompt)}
	b, _ := json.Marshal(k) // plain data
	key := sha256.Sum256(b)
	return key[:]
}
