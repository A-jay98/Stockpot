// Package logging sets up log/slog: JSON lines to stdout (journald on
// cardea) and, once the database is up, an asynchronous copy into the
// app_log table for the admin panel. Lines carry the request id and user of
// the request they were logged under.
package logging

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/a-jay98/stockpot/internal/reqctx"
	"github.com/a-jay98/stockpot/internal/settings"
)

// New returns a logger writing JSON lines to w at level, and to sink when
// non-nil.
func New(w io.Writer, level slog.Leveler, sink *Sink) *slog.Logger {
	return slog.New(&handler{out: &lockedWriter{w: w}, level: level, sink: sink})
}

type lockedWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

// FollowSetting keeps level equal to the log.level setting, live.
func FollowSetting(store *settings.Store, level *slog.LevelVar) {
	apply := func() {
		var l slog.Level
		if err := l.UnmarshalText([]byte(settings.LogLevel.Get(store))); err == nil {
			level.Set(l)
		}
	}
	apply()
	store.OnChange(settings.LogLevel.Key(), apply)
}

// handler writes each record as one JSON line (time, level, msg, request_id,
// user_id, then attributes) and queues the same record for the DB sink.
type handler struct {
	out   io.Writer
	level slog.Leveler
	sink  *Sink
	goas  []groupOrAttrs // WithGroup/WithAttrs calls, in order, for the DB copy
}

type groupOrAttrs struct {
	group string
	attrs []slog.Attr
}

func (h *handler) Enabled(_ context.Context, l slog.Level) bool { return l >= h.level.Level() }

func (h *handler) Handle(ctx context.Context, r slog.Record) error {
	var requestID string
	var userID uuid.UUID
	if info := reqctx.From(ctx); info != nil {
		requestID, userID = info.RequestID, info.UserID
	}
	attrs := h.attrsJSON(r)
	if r.Time.IsZero() {
		r.Time = time.Now()
	}

	// time, level and msg lead, in that order, as in slog's JSON handler.
	head := make([]byte, 0, 128)
	head = appendField(head, "time", r.Time.UTC().Format(time.RFC3339Nano))
	head = appendField(head, "level", r.Level.String())
	head = appendField(head, "msg", r.Message)
	if requestID != "" {
		head = appendField(head, "request_id", requestID)
	}
	if userID != uuid.Nil {
		head = appendField(head, "user_id", userID.String())
	}
	head[0] = '{'
	head = append(head, '}')
	buf := make([]byte, 0, len(head)+len(attrs)+1)
	buf = append(buf, head[:len(head)-1]...) // drop the closing brace
	if len(attrs) > 2 {                      // a non-empty object: splice its members in
		buf = append(buf, ',')
		buf = append(buf, attrs[1:len(attrs)-1]...)
	}
	buf = append(buf, '}', '\n')
	_, err := h.out.Write(buf)

	if h.sink != nil {
		h.sink.enqueue(Entry{
			Time:      r.Time,
			Level:     r.Level,
			Msg:       r.Message,
			Attrs:     attrs,
			RequestID: requestID,
			UserID:    userID,
		})
	}
	return err
}

func (h *handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	if len(attrs) == 0 {
		return h
	}
	return h.with(groupOrAttrs{attrs: attrs})
}

func (h *handler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	return h.with(groupOrAttrs{group: name})
}

func (h *handler) with(g groupOrAttrs) *handler {
	h2 := *h
	h2.goas = append(h.goas[:len(h.goas):len(h.goas)], g)
	return &h2
}

// attrsJSON renders the record's attributes (with groups) as a JSON object.
func (h *handler) attrsJSON(r slog.Record) []byte {
	root := map[string]any{}
	cur := root
	for _, g := range h.goas {
		if g.group != "" {
			m := map[string]any{}
			cur[g.group] = m
			cur = m
			continue
		}
		for _, a := range g.attrs {
			addAttr(cur, a)
		}
	}
	r.Attrs(func(a slog.Attr) bool {
		addAttr(cur, a)
		return true
	})
	pruneEmpty(root)
	b, err := json.Marshal(root)
	if err != nil {
		b, _ = json.Marshal(map[string]string{"!marshal_error": err.Error()})
	}
	return b
}

func addAttr(m map[string]any, a slog.Attr) {
	a.Value = a.Value.Resolve()
	if a.Equal(slog.Attr{}) {
		return
	}
	switch a.Value.Kind() {
	case slog.KindGroup:
		attrs := a.Value.Group()
		if len(attrs) == 0 {
			return
		}
		target := m
		if a.Key != "" { // an empty key inlines the group
			sub := map[string]any{}
			m[a.Key] = sub
			target = sub
		}
		for _, ga := range attrs {
			addAttr(target, ga)
		}
	case slog.KindTime:
		m[a.Key] = a.Value.Time().UTC().Format(time.RFC3339Nano)
	case slog.KindDuration:
		m[a.Key] = a.Value.Duration().String()
	case slog.KindAny:
		switch v := a.Value.Any().(type) {
		case error:
			m[a.Key] = v.Error()
		case json.Marshaler:
			m[a.Key] = v
		case fmt.Stringer:
			m[a.Key] = v.String()
		default:
			if _, err := json.Marshal(v); err != nil {
				m[a.Key] = fmt.Sprintf("%+v", v)
			} else {
				m[a.Key] = v
			}
		}
	default:
		m[a.Key] = a.Value.Any()
	}
}

// pruneEmpty drops groups left empty (slog omits those too).
func pruneEmpty(m map[string]any) {
	for k, v := range m {
		if sub, ok := v.(map[string]any); ok {
			pruneEmpty(sub)
			if len(sub) == 0 {
				delete(m, k)
			}
		}
	}
}

// LevelName is the lower-case name of l ("debug", "info", "warn", "error").
func LevelName(l slog.Level) string { return strings.ToLower(l.String()) }

// appendField appends `,"key":"value"`; the caller turns the first comma
// into the opening brace.
func appendField(b []byte, key, value string) []byte {
	k, _ := json.Marshal(key)
	v, _ := json.Marshal(value)
	b = append(b, ',')
	b = append(b, k...)
	b = append(b, ':')
	return append(b, v...)
}
