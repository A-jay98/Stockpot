package logging

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/a-jay98/stockpot/internal/reqctx"
	"github.com/a-jay98/stockpot/internal/settings"
	"github.com/a-jay98/stockpot/internal/testdb"
)

func TestMain(m *testing.M) { os.Exit(testdb.Main(m)) }

func TestHandlerAddsRequestContextAndRendersAttrs(t *testing.T) {
	var out bytes.Buffer
	sink := &Sink{queue: make(chan Entry, 4)}
	log := New(&out, slog.LevelInfo, sink)

	user := uuid.Must(uuid.NewV7())
	ctx := reqctx.With(context.Background(), &reqctx.Info{RequestID: "req-1", UserID: user})
	log.With("svc", "api").WithGroup("g").InfoContext(ctx, "hello",
		"n", 3, "err", errors.New("boom"), "d", time.Second, slog.Group("empty"))
	log.DebugContext(ctx, "filtered")

	var line map[string]any
	if err := json.Unmarshal(out.Bytes(), &line); err != nil {
		t.Fatalf("stdout %q: %v", out.String(), err)
	}
	if line["request_id"] != "req-1" || line["user_id"] != user.String() || line["svc"] != "api" {
		t.Fatalf("stdout line: %v", line)
	}

	if len(sink.queue) != 1 {
		t.Fatalf("sink got %d entries, want 1 (debug is below the level)", len(sink.queue))
	}
	e := <-sink.queue
	if e.Msg != "hello" || e.RequestID != "req-1" || e.UserID != user || e.Level != slog.LevelInfo {
		t.Fatalf("entry: %+v", e)
	}
	var attrs map[string]any
	if err := json.Unmarshal(e.Attrs, &attrs); err != nil {
		t.Fatal(err)
	}
	want := `{"g":{"d":"1s","err":"boom","n":3},"svc":"api"}`
	if got, _ := json.Marshal(attrs); string(got) != want {
		t.Fatalf("attrs = %s, want %s", got, want)
	}
}

func TestSinkDropsWhenFull(t *testing.T) {
	sink := &Sink{queue: make(chan Entry, 1)}
	sink.enqueue(Entry{Msg: "a"})
	sink.enqueue(Entry{Msg: "b"})
	if sink.Dropped() != 1 {
		t.Fatalf("dropped = %d, want 1", sink.Dropped())
	}
}

func TestSinkWritesToDatabase(t *testing.T) {
	pool := testdb.New(t)
	sink := NewSink(pool, 16)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { sink.Run(ctx); close(done) }()

	user := uuid.Must(uuid.NewV7())
	log := New(&bytes.Buffer{}, slog.LevelInfo, sink)
	rctx := reqctx.With(context.Background(), &reqctx.Info{RequestID: "req-9", UserID: user})
	log.WarnContext(rctx, "disk low", "free_mb", 12)
	log.Info("plain")
	cancel() // Run drains the queue before returning
	<-done

	if sink.Written() != 2 || sink.Dropped() != 0 {
		t.Fatalf("written %d dropped %d", sink.Written(), sink.Dropped())
	}
	var level int16
	var attrs []byte
	var reqID string
	var uid uuid.UUID
	err := pool.QueryRow(t.Context(),
		"SELECT level, attrs, request_id, user_id FROM app_log WHERE msg = 'disk low'").Scan(&level, &attrs, &reqID, &uid)
	if err != nil {
		t.Fatal(err)
	}
	if level != int16(slog.LevelWarn) || reqID != "req-9" || uid != user || string(attrs) != `{"free_mb": 12}` {
		t.Fatalf("row: level=%d attrs=%s req=%s user=%s", level, attrs, reqID, uid)
	}
}

func TestFollowSetting(t *testing.T) {
	store, err := settings.NewStore(testdb.New(t), []byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	level := new(slog.LevelVar)
	FollowSetting(store, level)
	if level.Level() != slog.LevelInfo {
		t.Fatalf("initial level %v", level.Level())
	}
	if err := settings.SetValue(t.Context(), store, settings.LogLevel, "debug", nil); err != nil {
		t.Fatal(err)
	}
	if level.Level() != slog.LevelDebug {
		t.Fatalf("level after set = %v", level.Level())
	}
}
