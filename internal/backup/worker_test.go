package backup

import (
	"errors"
	"testing"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/a-jay98/stockpot/internal/jobs"
	"github.com/a-jay98/stockpot/internal/settings"
)

func attach(t *testing.T, e *env) *jobs.Client {
	t.Helper()
	c, err := jobs.New(jobs.Deps{Pool: e.pool, Settings: e.store, Workers: []func(*river.Workers) error{e.svc.Registrar()}})
	if err != nil {
		t.Fatal(err)
	}
	e.svc.Attach(c)
	return c
}

func TestRunNowQueuesAJob(t *testing.T) {
	e := newEnv(t, 16<<10)
	attach(t, e)
	if err := e.svc.RunNow(t.Context()); err != nil {
		t.Fatal(err)
	}
	var kind, trigger string
	if err := e.pool.QueryRow(t.Context(), `SELECT kind, args->>'trigger' FROM river_job WHERE kind = 'backup.run'`).Scan(&kind, &trigger); err != nil {
		t.Fatal(err)
	}
	if trigger != TriggerManual {
		t.Fatalf("trigger %q", trigger)
	}
	var audits int
	if err := e.pool.QueryRow(t.Context(), `SELECT count(*) FROM audit_event WHERE action = 'backup.run'`).Scan(&audits); err != nil || audits != 1 {
		t.Fatalf("audit events = %d, %v", audits, err)
	}

	// Unconfigured: refused up front.
	if err := e.store.Reset(t.Context(), SettingAgeRecipient.Key()); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.RunNow(t.Context()); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("err = %v", err)
	}
}

func TestScheduledRunHonoursFlag(t *testing.T) {
	e := newEnv(t, 16<<10)
	w := &Worker{svc: e.svc}
	job := &river.Job[Args]{JobRow: &rivertype.JobRow{ID: 1}, Args: Args{Trigger: TriggerScheduled}}
	if err := w.Work(t.Context(), job); err != nil { // flag.backups defaults to off
		t.Fatal(err)
	}
	if rs, _ := recentRuns(t.Context(), e.pool, 5); len(rs) != 0 {
		t.Fatal("scheduled run ignored the flag")
	}
	if err := settings.SetValue(t.Context(), e.store, FlagEnabled, true, nil); err != nil {
		t.Fatal(err)
	}
	e.seed(t, 5, 1)
	if err := w.Work(t.Context(), job); err != nil {
		t.Fatal(err)
	}
	if r := e.lastRun(t); r.Status != "ok" || r.Trigger != TriggerScheduled {
		t.Fatalf("run = %+v", r)
	}
}
