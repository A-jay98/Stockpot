package llm_test

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/a-jay98/stockpot/internal/llm"
)

// whisper is a fake /audio/transcriptions endpoint.
type whisper struct {
	srv    *httptest.Server
	hits   atomic.Int32
	status int
	body   string
	model  atomic.Value
	file   atomic.Value
}

func newWhisper(t *testing.T, status int, body string) *whisper {
	t.Helper()
	w := &whisper{status: status, body: body}
	w.srv = httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		w.hits.Add(1)
		if r.URL.Path != "/audio/transcriptions" || r.Header.Get("Authorization") == "" {
			rw.WriteHeader(http.StatusNotFound)
			return
		}
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			rw.WriteHeader(http.StatusBadRequest)
			return
		}
		w.model.Store(r.FormValue("model"))
		if f, _, err := r.FormFile("file"); err == nil {
			b, _ := io.ReadAll(f)
			w.file.Store(string(b))
		}
		rw.WriteHeader(w.status)
		_, _ = io.WriteString(rw, w.body)
	}))
	t.Cleanup(w.srv.Close)
	return w
}

func audioProvider(id string, w *whisper) llm.ProviderConfig {
	return llm.ProviderConfig{
		ID: id, Kind: llm.KindOpenAICompat, BaseURL: w.srv.URL, Model: id + "-chat", Enabled: true,
		ResetTZ: "UTC", TranscriptionModel: id + "-whisper",
	}
}

func clip() llm.TranscribeRequest {
	return llm.TranscribeRequest{
		Feature: llm.Feature{Name: "recipes_transcribe", CacheTTL: time.Hour},
		Audio:   []byte("fake m4a bytes"), Filename: "audio.m4a", MIME: "audio/mp4",
	}
}

func TestTranscribeMetersAndCaches(t *testing.T) {
	w := newWhisper(t, 200, `{"text":" Fry the onions. ","language":"persian","duration":12.5}`)
	e := setup(t, audioProvider("groq", w))

	got, err := e.router.Transcribe(t.Context(), clip())
	if err != nil {
		t.Fatal(err)
	}
	if got.Text != "Fry the onions." || got.Language != "persian" || got.AudioSeconds != 12.5 || got.Model != "groq-whisper" || got.Cached {
		t.Fatalf("got %+v", got)
	}
	if m, _ := w.model.Load().(string); m != "groq-whisper" {
		t.Fatalf("model sent = %q", m)
	}
	if f, _ := w.file.Load().(string); f != "fake m4a bytes" {
		t.Fatalf("file sent = %q", f)
	}
	again, err := e.router.Transcribe(t.Context(), clip())
	if err != nil {
		t.Fatal(err)
	}
	if !again.Cached || again.Text != "Fry the onions." || w.hits.Load() != 1 {
		t.Fatalf("second call: %+v, hits %d", again, w.hits.Load())
	}
	c := calls(t, e)
	if len(c) != 2 || c[0].Status != "ok" || c[0].CacheHit || !c[1].CacheHit {
		t.Fatalf("llm_call = %+v", c)
	}
}

func TestTranscribeSkipsProvidersWithoutModel(t *testing.T) {
	w := newWhisper(t, 200, `{"text":"x"}`)
	p := audioProvider("gemini", w)
	p.TranscriptionModel = ""
	e := setup(t, p)
	_, err := e.router.Transcribe(t.Context(), clip())
	if !errors.Is(err, llm.ErrUnavailable) || w.hits.Load() != 0 {
		t.Fatalf("err = %v, hits %d", err, w.hits.Load())
	}
}

func TestTranscribeFailsOverOnQuota(t *testing.T) {
	a := newWhisper(t, 429, `{"error":{"message":"Rate limit reached on audio seconds per day (ASPD)"}}`)
	b := newWhisper(t, 200, `{"text":"stir"}`)
	e := setup(t, audioProvider("a", a), audioProvider("b", b))
	got, err := e.router.Transcribe(t.Context(), clip())
	if err != nil || got.Provider != "b" {
		t.Fatalf("got %+v, %v", got, err)
	}
	c := calls(t, e)
	if len(c) != 2 || c[0].Status != "quota" || c[1].Status != "ok" {
		t.Fatalf("llm_call = %+v", c)
	}
	// a is now out of rotation: the next call goes straight to b.
	req := clip()
	req.Audio = []byte("other clip")
	if _, err := e.router.Transcribe(t.Context(), req); err != nil || a.hits.Load() != 1 {
		t.Fatalf("err %v, a hits %d", err, a.hits.Load())
	}
}

func TestTranscribeRespectsGlobalCapAndFlag(t *testing.T) {
	w := newWhisper(t, 200, `{"text":"x"}`)
	e := setup(t, audioProvider("groq", w))
	if err := e.store.Set(t.Context(), llm.SettingGlobalDailyRequestCap.Key(), json.RawMessage(`1`), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := e.router.Transcribe(t.Context(), clip()); err != nil {
		t.Fatal(err)
	}
	req := clip()
	req.Audio = []byte("second")
	if _, err := e.router.Transcribe(t.Context(), req); !errors.Is(err, llm.ErrUnavailable) {
		t.Fatalf("over the cap: %v", err)
	}
	if err := e.store.Set(t.Context(), llm.FlagEnabled.Key(), json.RawMessage(`false`), nil); err != nil {
		t.Fatal(err)
	}
	if e.router.TranscriptionAvailable(t.Context()) {
		t.Fatal("available with LLM features off")
	}
}

func TestTranscribeRejectsBadRequests(t *testing.T) {
	w := newWhisper(t, 200, `{"text":"x"}`)
	e := setup(t, audioProvider("groq", w))
	for name, mut := range map[string]func(*llm.TranscribeRequest){
		"no audio":  func(r *llm.TranscribeRequest) { r.Audio = nil },
		"bad mime":  func(r *llm.TranscribeRequest) { r.MIME = "video/mp4" },
		"bad name":  func(r *llm.TranscribeRequest) { r.Filename = "../x.m4a" },
		"bad feat.": func(r *llm.TranscribeRequest) { r.Feature.Name = "Recipes.Parse" },
	} {
		req := clip()
		mut(&req)
		if _, err := e.router.Transcribe(t.Context(), req); !errors.Is(err, llm.ErrInvalidRequest) {
			t.Errorf("%s: %v", name, err)
		}
	}
}
