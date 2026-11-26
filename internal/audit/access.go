package audit

import (
	"context"
	"log/slog"
	"os"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/a-jay98/stockpot/internal/db/dbq"
	"github.com/a-jay98/stockpot/internal/jobs"
	"github.com/a-jay98/stockpot/internal/reqctx"
	"github.com/a-jay98/stockpot/internal/settings"
)

// Access is one served request.
type Access struct {
	Time     time.Time
	Duration time.Duration
	Method   string
	Route    string // matched pattern; empty when nothing matched
	Path     string
	Status   int
	Request  reqctx.Info
}

// AccessWriter batches access records into access_log off the request path.
// Its queue is bounded: if the database falls behind, records are dropped
// and counted rather than slowing requests.
type AccessWriter struct {
	pool    *pgxpool.Pool
	queue   chan dbq.InsertAccessLogParams
	dropped atomic.Uint64
	written atomic.Uint64
	report  *slog.Logger
}

// Access writer tuning.
const (
	DefaultAccessQueue = 4096
	accessBatch        = 500
	accessFlushEvery   = time.Second
	accessWriteTimeout = 5 * time.Second
)

// NewAccessWriter returns a writer on pool. Start it with Run.
func NewAccessWriter(pool *pgxpool.Pool, queueSize int) *AccessWriter {
	return &AccessWriter{
		pool:  pool,
		queue: make(chan dbq.InsertAccessLogParams, queueSize),
		// Not slog.Default: that may feed app_log, which shares the database
		// that is failing.
		report: slog.New(slog.NewJSONHandler(os.Stdout, nil)).With("component", "access-log"),
	}
}

// Dropped counts records lost to a full queue or a failed write.
func (w *AccessWriter) Dropped() uint64 { return w.dropped.Load() }

// Written counts records that reached the database.
func (w *AccessWriter) Written() uint64 { return w.written.Load() }

// Log queues a record without blocking.
func (w *AccessWriter) Log(a Access) {
	p := dbq.InsertAccessLogParams{
		Time:        a.Time,
		RequestID:   a.Request.RequestID,
		UserID:      nonNil(a.Request.UserID),
		SessionID:   nonNil(a.Request.SessionID),
		HouseholdID: nonNil(a.Request.HouseholdID),
		Method:      a.Method,
		Route:       a.Route,
		Path:        truncate(a.Path, 512),
		Status:      int16(min(max(a.Status, 0), 999)),                                           //nolint:gosec // clamped
		DurationMs:  int32(min(a.Duration.Milliseconds(), int64(24*time.Hour/time.Millisecond))), //nolint:gosec // clamped
		Ip:          parseIP(a.Request.ClientIP),
		UserAgent:   truncate(a.Request.UserAgent, 512),
	}
	select {
	case w.queue <- p:
	default:
		w.dropped.Add(1)
	}
}

// Run writes batches until ctx ends, then flushes what is queued.
func (w *AccessWriter) Run(ctx context.Context) {
	ticker := time.NewTicker(accessFlushEvery)
	defer ticker.Stop()
	batch := make([]dbq.InsertAccessLogParams, 0, accessBatch)
	var reported uint64
	flush := func(ctx context.Context) {
		if len(batch) > 0 {
			w.write(ctx, batch)
			batch = batch[:0]
		}
		if d := w.dropped.Load(); d != reported {
			w.report.Warn("access log records dropped", "total", d, "since_last", d-reported)
			reported = d
		}
	}
	for {
		select {
		case p := <-w.queue:
			batch = append(batch, p)
			if len(batch) == accessBatch {
				flush(ctx)
			}
		case <-ticker.C:
			flush(ctx)
		case <-ctx.Done():
			fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), accessWriteTimeout)
			defer cancel()
			for {
				select {
				case p := <-w.queue:
					batch = append(batch, p)
					if len(batch) == accessBatch {
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

func (w *AccessWriter) write(ctx context.Context, batch []dbq.InsertAccessLogParams) {
	ctx, cancel := context.WithTimeout(ctx, accessWriteTimeout)
	defer cancel()
	n, err := dbq.New(w.pool).InsertAccessLog(ctx, batch)
	if err != nil {
		w.dropped.Add(uint64(len(batch)))
		w.report.Error("writing access_log batch failed", "records", len(batch), "err", err)
		return
	}
	w.written.Add(uint64(n)) //nolint:gosec // row count
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

const sweepBatch = 5000

// Sweepers prune access_log and audit_event per their retention settings.
func Sweepers(pool *pgxpool.Pool) []jobs.Sweeper {
	q := dbq.New(pool)
	return []jobs.Sweeper{
		{
			Name: "access_log",
			Days: settings.RetentionAccessLogDays,
			Sweep: func(ctx context.Context, before time.Time) (int64, error) {
				return jobs.SweepInBatches(ctx, sweepBatch, func(ctx context.Context) (int64, error) {
					return q.DeleteAccessLogBefore(ctx, dbq.DeleteAccessLogBeforeParams{Before: before, BatchSize: sweepBatch})
				})
			},
		},
		{
			Name: "audit_event",
			Days: settings.RetentionAuditDays,
			Sweep: func(ctx context.Context, before time.Time) (int64, error) {
				return jobs.SweepInBatches(ctx, sweepBatch, func(ctx context.Context) (int64, error) {
					return q.DeleteAuditEventsBefore(ctx, dbq.DeleteAuditEventsBeforeParams{Before: before, BatchSize: sweepBatch})
				})
			},
		},
	}
}
