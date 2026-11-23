package logging

import (
	"context"
	"log/slog"
	"os"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Entry is one app_log row.
type Entry struct {
	Time      time.Time
	Level     slog.Level
	Msg       string
	Attrs     []byte // JSON object
	RequestID string
	UserID    uuid.UUID // uuid.Nil: none
}

// Sink batches log entries into the app_log table off the request path. Its
// queue is bounded: when the database can't keep up, entries are dropped and
// counted rather than slowing the app or growing memory without limit.
type Sink struct {
	pool    *pgxpool.Pool
	queue   chan Entry
	dropped atomic.Uint64
	written atomic.Uint64
	// report receives the sink's own problems; it must not feed back into
	// the sink.
	report *slog.Logger
}

// Sink tuning.
const (
	DefaultQueueSize = 4096
	batchSize        = 256
	flushInterval    = time.Second
	writeTimeout     = 5 * time.Second
)

// NewSink returns a sink writing to pool. Start it with Run.
func NewSink(pool *pgxpool.Pool, queueSize int) *Sink {
	return &Sink{
		pool:   pool,
		queue:  make(chan Entry, queueSize),
		report: slog.New(slog.NewJSONHandler(os.Stdout, nil)).With("component", "log-sink"),
	}
}

// Dropped is how many entries were discarded because the queue was full or
// a write failed.
func (s *Sink) Dropped() uint64 { return s.dropped.Load() }

// Written is how many entries reached the database.
func (s *Sink) Written() uint64 { return s.written.Load() }

func (s *Sink) enqueue(e Entry) {
	select {
	case s.queue <- e:
	default:
		s.dropped.Add(1)
	}
}

// Run writes batches until ctx ends, then flushes what is queued (bounded by
// writeTimeout) and returns.
func (s *Sink) Run(ctx context.Context) {
	ticker := time.NewTicker(flushInterval)
	defer ticker.Stop()
	batch := make([]Entry, 0, batchSize)
	var reportedDrops uint64
	flush := func(ctx context.Context) {
		if len(batch) > 0 {
			s.write(ctx, batch)
			batch = batch[:0]
		}
		if d := s.dropped.Load(); d != reportedDrops {
			s.report.Warn("app_log entries dropped", "total", d, "since_last", d-reportedDrops)
			reportedDrops = d
		}
	}
	for {
		select {
		case e := <-s.queue:
			batch = append(batch, e)
			if len(batch) == batchSize {
				flush(ctx)
			}
		case <-ticker.C:
			flush(ctx)
		case <-ctx.Done():
			// Drain what is already queued, then stop.
			fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), writeTimeout)
			defer cancel()
			for {
				select {
				case e := <-s.queue:
					batch = append(batch, e)
					if len(batch) == batchSize {
						flush(fctx)
					}
				default:
					flush(fctx)
					return
				}
			}
		}
	}
}

func (s *Sink) write(ctx context.Context, batch []Entry) {
	ctx, cancel := context.WithTimeout(ctx, writeTimeout)
	defer cancel()
	rows := make([][]any, len(batch))
	for i, e := range batch {
		rows[i] = []any{e.Time, int16(e.Level), e.Msg, e.Attrs, nullable(e.RequestID), nullableUUID(e.UserID)} //nolint:gosec // slog levels are small
	}
	n, err := s.pool.CopyFrom(ctx, pgx.Identifier{"app_log"},
		[]string{"time", "level", "msg", "attrs", "request_id", "user_id"},
		pgx.CopyFromRows(rows))
	if err != nil {
		s.dropped.Add(uint64(len(batch)))
		s.report.Error("writing app_log batch failed", "entries", len(batch), "err", err)
		return
	}
	s.written.Add(uint64(n)) //nolint:gosec // n is a row count, never negative
}

func nullableUUID(id uuid.UUID) *uuid.UUID {
	if id == uuid.Nil {
		return nil
	}
	return &id
}

func nullable(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
