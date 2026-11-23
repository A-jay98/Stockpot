package jobs

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/riverqueue/river"

	"github.com/a-jay98/stockpot/internal/settings"
	"github.com/a-jay98/stockpot/internal/testdb"
)

func TestMain(m *testing.M) { os.Exit(testdb.Main(m)) }

func TestRetentionPrunesAppLog(t *testing.T) {
	ctx := t.Context()
	pool := testdb.New(t)
	store, err := settings.NewStore(pool, []byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	if err := settings.SetValue(ctx, store, settings.RetentionAppLogDays, 7, nil); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
	if _, err := pool.Exec(ctx, `
		INSERT INTO app_log (time, level, msg)
		SELECT $1::timestamptz - make_interval(days => d), 0, 'x'
		FROM generate_series(0, 10) AS d`, now); err != nil {
		t.Fatal(err)
	}

	w := &RetentionWorker{settings: store, sweepers: []Sweeper{AppLogSweeper(pool)}, now: func() time.Time { return now }}
	if err := w.Work(ctx, &river.Job[RetentionArgs]{}); err != nil {
		t.Fatal(err)
	}
	var left int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM app_log").Scan(&left); err != nil {
		t.Fatal(err)
	}
	if left != 8 { // ages 0..7 days stay
		t.Fatalf("%d rows left, want 8", left)
	}
}

func TestSweepInBatches(t *testing.T) {
	calls := 0
	total, err := SweepInBatches(t.Context(), 10, func(_ context.Context) (int64, error) {
		calls++
		if calls < 3 {
			return 10, nil
		}
		return 4, nil
	})
	if err != nil || total != 24 || calls != 3 {
		t.Fatalf("total %d calls %d err %v", total, calls, err)
	}
}

func TestClientStartsAndRunsRetention(t *testing.T) {
	ctx := t.Context()
	pool := testdb.New(t)
	store, err := settings.NewStore(pool, []byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	client, err := New(Deps{Pool: pool, Settings: store, Sweepers: []Sweeper{AppLogSweeper(pool)}})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Stop(context.Background()) })

	// RunOnStart: the leader enqueues the periodic job and a worker runs it.
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		var state string
		err := pool.QueryRow(ctx, "SELECT state FROM river_job WHERE kind = 'retention' ORDER BY id LIMIT 1").Scan(&state)
		if err == nil && state == "completed" {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("retention job did not complete")
}
