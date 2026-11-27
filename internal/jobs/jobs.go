// Package jobs runs background work on River (Postgres-backed queue) inside
// the server process: workers, periodic jobs, retries. The admin panel reads
// and controls jobs through the same River client.
package jobs

import (
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"

	"github.com/a-jay98/stockpot/internal/settings"
)

// Client is the River client type the app uses.
type Client = river.Client[pgx.Tx]

// Deps are what workers need.
type Deps struct {
	Pool     *pgxpool.Pool
	Settings *settings.Store
	// Sweepers prune expired rows in the daily retention job. Feature
	// packages contribute theirs (access log, audit, sessions, …).
	Sweepers []Sweeper
	Logger   *slog.Logger
	// Workers registers feature workers (backups, alerts, …) before the
	// client is built.
	Workers []func(*river.Workers) error
}

// New builds a client with every worker and periodic job registered. Call
// Start to begin working and Stop to drain on shutdown.
func New(d Deps) (*Client, error) {
	workers := river.NewWorkers()
	if err := river.AddWorkerSafely(workers, &RetentionWorker{settings: d.Settings, sweepers: d.Sweepers}); err != nil {
		return nil, err
	}
	for _, add := range d.Workers {
		if err := add(workers); err != nil {
			return nil, fmt.Errorf("register worker: %w", err)
		}
	}
	client, err := river.NewClient(riverpgxv5.New(d.Pool), &river.Config{
		Queues: map[string]river.QueueConfig{
			river.QueueDefault: {MaxWorkers: 4},
		},
		Workers: workers,
		PeriodicJobs: []*river.PeriodicJob{
			river.NewPeriodicJob(
				river.PeriodicInterval(24*time.Hour),
				func() (river.JobArgs, *river.InsertOpts) { return RetentionArgs{}, nil },
				&river.PeriodicJobOpts{ID: "retention", RunOnStart: true},
			),
		},
		Logger: d.Logger,
	})
	if err != nil {
		return nil, fmt.Errorf("river client: %w", err)
	}
	return client, nil
}
