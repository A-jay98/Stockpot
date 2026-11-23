package admin

import (
	"context"
	"log/slog"
	"time"

	"github.com/a-jay98/stockpot/internal/buildinfo"
	"github.com/a-jay98/stockpot/internal/db"
)

// Overview is the admin panel's at-a-glance system state. Each part is
// filled best effort: a down database still yields an overview that says so.
type Overview struct {
	Build     buildinfo.Info
	StartedAt time.Time
	Uptime    time.Duration

	Database Database
	// Jobs counts River jobs by state; nil when unavailable.
	Jobs map[string]int64
	// Errors24h and Warnings24h count app_log lines; nil when unavailable.
	Errors24h, Warnings24h *int64
	LogSink                *SinkCounts
	LLM                    *LLMToday
}

// Database is the database part of the overview.
type Database struct {
	Reachable bool
	Latency   time.Duration
	SizeBytes *int64
	// SchemaCurrent is false while migrations are pending.
	SchemaCurrent bool
	Versions      *db.Versions
}

// SinkCounts mirrors the log sink's counters.
type SinkCounts struct{ Written, Dropped uint64 }

// probeTimeout bounds each overview probe.
const probeTimeout = 3 * time.Second

// Overview gathers the system state.
func (s *Service) Overview(ctx context.Context) Overview {
	now := s.now()
	o := Overview{
		Build:     buildinfo.Get(),
		StartedAt: s.d.StartedAt,
		Uptime:    now.Sub(s.d.StartedAt),
	}

	probe := func(name string, fn func(context.Context) error) bool {
		pctx, cancel := context.WithTimeout(ctx, probeTimeout)
		defer cancel()
		if err := fn(pctx); err != nil {
			slog.WarnContext(ctx, "admin overview probe failed", "probe", name, "err", err)
			return false
		}
		return true
	}

	start := time.Now()
	o.Database.Reachable = probe("database", s.d.Pool.Ping)
	o.Database.Latency = time.Since(start)
	if !o.Database.Reachable {
		return o
	}
	probe("database size", func(ctx context.Context) error {
		n, err := s.q.DatabaseSize(ctx)
		if err == nil {
			o.Database.SizeBytes = &n
		}
		return err
	})
	if s.d.Schema != nil {
		o.Database.SchemaCurrent = probe("schema", s.d.Schema.Ping)
		probe("schema versions", func(ctx context.Context) error {
			v, err := s.d.Schema.Versions(ctx)
			if err == nil {
				o.Database.Versions = &v
			}
			return err
		})
	}
	probe("job counts", func(ctx context.Context) error {
		m, err := s.jobCounts(ctx)
		if err == nil {
			o.Jobs = m
		}
		return err
	})
	probe("log counts", func(ctx context.Context) error {
		c, err := s.q.CountAppLogProblemsSince(ctx, now.Add(-24*time.Hour))
		if err == nil {
			o.Errors24h, o.Warnings24h = &c.Errors, &c.Warnings
		}
		return err
	})
	if s.d.LogSink != nil {
		o.LogSink = &SinkCounts{Written: s.d.LogSink.Written(), Dropped: s.d.LogSink.Dropped()}
	}
	if s.d.LLMToday != nil {
		probe("llm usage", func(ctx context.Context) error {
			u, err := s.d.LLMToday(ctx)
			if err == nil {
				o.LLM = &u
			}
			return err
		})
	}
	return o
}

// jobCounts reads River's table directly: River's client has no count API,
// and a grouped count is one cheap query (finished jobs are pruned by River).
func (s *Service) jobCounts(ctx context.Context) (map[string]int64, error) {
	rows, err := s.d.Pool.Query(ctx, "SELECT state::text, count(*) FROM river_job GROUP BY state")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var state string
		var n int64
		if err := rows.Scan(&state, &n); err != nil {
			return nil, err
		}
		out[state] = n
	}
	return out, rows.Err()
}
