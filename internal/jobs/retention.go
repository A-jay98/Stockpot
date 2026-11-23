package jobs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/a-jay98/stockpot/internal/db/dbq"
	"github.com/a-jay98/stockpot/internal/settings"
)

// Sweeper deletes one kind of row older than a retention setting.
type Sweeper struct {
	Name string
	Days *settings.Setting[int]
	// Sweep deletes rows older than before and returns how many it removed.
	Sweep func(ctx context.Context, before time.Time) (int64, error)
}

// RetentionArgs is the daily retention job.
type RetentionArgs struct{}

// Kind names the job in River's tables.
func (RetentionArgs) Kind() string { return "retention" }

// InsertOpts keeps at most one pending retention job.
func (RetentionArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{UniqueOpts: river.UniqueOpts{ByPeriod: time.Hour}}
}

// RetentionWorker runs every sweeper with its configured age.
type RetentionWorker struct {
	river.WorkerDefaults[RetentionArgs]
	settings *settings.Store
	sweepers []Sweeper
	now      func() time.Time
}

// Timeout allows for a large first sweep.
func (*RetentionWorker) Timeout(*river.Job[RetentionArgs]) time.Duration { return 15 * time.Minute }

// Work sweeps each target. One failing target doesn't stop the others; the
// job fails (and River retries it) if any did.
func (w *RetentionWorker) Work(ctx context.Context, _ *river.Job[RetentionArgs]) error {
	now := time.Now
	if w.now != nil {
		now = w.now
	}
	var errs []error
	for _, s := range w.sweepers {
		days := s.Days.Get(w.settings)
		before := now().AddDate(0, 0, -days)
		n, err := s.Sweep(ctx, before)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", s.Name, err))
			continue
		}
		slog.InfoContext(ctx, "retention sweep", "target", s.Name, "days", days, "deleted", n)
	}
	return errors.Join(errs...)
}

// sweepBatch is how many rows one DELETE removes.
const sweepBatch = 5000

// AppLogSweeper prunes app_log per retention.app_log_days.
func AppLogSweeper(pool *pgxpool.Pool) Sweeper {
	q := dbq.New(pool)
	return Sweeper{
		Name: "app_log",
		Days: settings.RetentionAppLogDays,
		Sweep: func(ctx context.Context, before time.Time) (int64, error) {
			return SweepInBatches(ctx, sweepBatch, func(ctx context.Context) (int64, error) {
				return q.DeleteAppLogBefore(ctx, dbq.DeleteAppLogBeforeParams{Before: before, BatchSize: sweepBatch})
			})
		},
	}
}

// SweepInBatches calls deleteBatch, which deletes at most batch rows, until
// it removes fewer than a full batch.
func SweepInBatches(ctx context.Context, batch int64, deleteBatch func(context.Context) (int64, error)) (int64, error) {
	var total int64
	for {
		n, err := deleteBatch(ctx)
		total += n
		if err != nil || n < batch {
			return total, err
		}
	}
}
