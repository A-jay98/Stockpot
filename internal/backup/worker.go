package backup

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/a-jay98/stockpot/internal/audit"
	"github.com/a-jay98/stockpot/internal/jobs"
	"github.com/a-jay98/stockpot/internal/settings"
)

// Run triggers.
const (
	TriggerScheduled = "scheduled"
	TriggerManual    = "manual"
)

// Args is the backup job.
type Args struct {
	Trigger string `json:"trigger"`
}

// Kind names the job in River's tables.
func (Args) Kind() string { return "backup.run" }

// InsertOpts retries a failed backup once and never queues two at once.
func (Args) InsertOpts() river.InsertOpts {
	return river.InsertOpts{
		MaxAttempts: 2,
		UniqueOpts:  river.UniqueOpts{ByArgs: false, ByState: []rivertype.JobState{rivertype.JobStateAvailable, rivertype.JobStateRunning, rivertype.JobStateRetryable, rivertype.JobStateScheduled, rivertype.JobStatePending}},
	}
}

// Worker runs the backup job.
type Worker struct {
	river.WorkerDefaults[Args]
	svc *Service
}

// Timeout bounds a whole backup.
func (*Worker) Timeout(*river.Job[Args]) time.Duration { return 3 * time.Hour }

// Work runs one backup. Scheduled runs honour flag.backups; unconfigured or
// already-running conditions are not retried.
func (w *Worker) Work(ctx context.Context, job *river.Job[Args]) error {
	if job.Args.Trigger == TriggerScheduled && !FlagEnabled.Get(w.svc.d.Settings) {
		return nil
	}
	id := job.ID
	err := w.svc.Run(ctx, job.Args.Trigger, &id)
	if errors.Is(err, ErrNotConfigured) || errors.Is(err, ErrAlreadyRunning) {
		slog.WarnContext(ctx, "backup skipped", "reason", err)
		return river.JobCancel(err)
	}
	return err
}

// Registrar adds the worker to a jobs.Deps.
func (s *Service) Registrar() func(*river.Workers) error {
	return func(ws *river.Workers) error {
		return river.AddWorkerSafely(ws, &Worker{svc: s})
	}
}

// schedule is the nightly time read live from settings.
type schedule struct{ store *settings.Store }

func (s schedule) Next(t time.Time) time.Time { return NextRun(SettingTime.Get(s.store), t) }

const periodicID = "backup"

// Attach installs the nightly periodic job on client and keeps it in step
// with backup.time. Call once, after the client is built.
func (s *Service) Attach(client *jobs.Client) {
	s.client = client
	add := func() {
		client.PeriodicJobs().RemoveByID(periodicID)
		client.PeriodicJobs().Add(river.NewPeriodicJob(
			schedule{s.d.Settings},
			func() (river.JobArgs, *river.InsertOpts) { return Args{Trigger: TriggerScheduled}, nil },
			&river.PeriodicJobOpts{ID: periodicID},
		))
	}
	add()
	s.d.Settings.OnChange(SettingTime.Key(), add)
}

// RunNow queues a manual backup ("Back up now"), audited. It reports
// ErrNotConfigured or ErrAlreadyRunning up front.
func (s *Service) RunNow(ctx context.Context) error {
	if s.client == nil {
		return errors.New("backup: not attached to the job client")
	}
	if !s.Configured() {
		return ErrNotConfigured
	}
	var running bool
	if err := s.d.Pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM backup_run WHERE status = 'running'
		AND EXISTS (SELECT 1 FROM river_job j WHERE j.id = backup_run.job_id AND j.state = 'running'))`).Scan(&running); err != nil {
		return err
	}
	if running {
		return ErrAlreadyRunning
	}
	tx, err := s.d.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := s.client.InsertTx(ctx, tx, Args{Trigger: TriggerManual}, nil); err != nil {
		return fmt.Errorf("queue backup: %w", err)
	}
	if err := audit.Record(ctx, tx, "backup.run", audit.Target{Type: "backup", ID: "now"}, nil, nil); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
