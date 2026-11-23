package db_test

import (
	"errors"
	"os"
	"testing"

	"github.com/a-jay98/stockpot/internal/db"
	"github.com/a-jay98/stockpot/internal/testdb"
)

func TestMain(m *testing.M) { os.Exit(testdb.Main(m)) }

func TestMigrateIsIdempotentAndSchemaCurrent(t *testing.T) {
	ctx := t.Context()
	pool := testdb.New(t) // already migrated once
	if err := db.Migrate(ctx, pool); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
	check, err := db.NewSchemaCheck(pool)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = check.Close() })
	if err := check.Ping(ctx); err != nil {
		t.Fatalf("schema check after migrate: %v", err)
	}
	var riverTables int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM pg_tables WHERE tablename = 'river_job'").Scan(&riverTables); err != nil || riverTables != 1 {
		t.Fatalf("river_job table: %d, %v", riverTables, err)
	}
}

func TestSchemaCheckDetectsPendingMigration(t *testing.T) {
	ctx := t.Context()
	pool := testdb.New(t)
	if _, err := pool.Exec(ctx, "DELETE FROM goose_db_version WHERE version_id = (SELECT max(version_id) FROM goose_db_version)"); err != nil {
		t.Fatal(err)
	}
	check, err := db.NewSchemaCheck(pool)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = check.Close() })
	if err := check.Ping(ctx); !errors.Is(err, db.ErrSchemaBehind) {
		t.Fatalf("Ping = %v, want ErrSchemaBehind", err)
	}
}

func TestSchemaCheckDetectsPendingRiverMigration(t *testing.T) {
	ctx := t.Context()
	pool := testdb.New(t)
	if _, err := pool.Exec(ctx, "DELETE FROM river_migration WHERE version = (SELECT max(version) FROM river_migration)"); err != nil {
		t.Fatal(err)
	}
	check, err := db.NewSchemaCheck(pool)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = check.Close() })
	if err := check.Ping(ctx); !errors.Is(err, db.ErrSchemaBehind) {
		t.Fatalf("Ping = %v, want ErrSchemaBehind", err)
	}
}

func TestRevTrigger(t *testing.T) {
	ctx := t.Context()
	pool := testdb.New(t)
	_, err := pool.Exec(ctx, `
		CREATE TABLE thing (
			id int PRIMARY KEY,
			created_at timestamptz NOT NULL DEFAULT now(),
			updated_at timestamptz NOT NULL DEFAULT now(),
			rev bigint NOT NULL DEFAULT 0
		);
		CREATE TRIGGER t_rev BEFORE INSERT OR UPDATE ON thing
			FOR EACH ROW EXECUTE FUNCTION set_rev_and_updated_at();
		INSERT INTO thing (id, updated_at) VALUES (1, '2000-01-01'), (2, now());`)
	if err != nil {
		t.Fatal(err)
	}
	var rev1, rev2 int64
	if err := pool.QueryRow(ctx, "SELECT rev FROM thing WHERE id = 1").Scan(&rev1); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, "SELECT rev FROM thing WHERE id = 2").Scan(&rev2); err != nil {
		t.Fatal(err)
	}
	if rev1 == 0 || rev2 <= rev1 {
		t.Fatalf("insert revs = %d, %d; want increasing and non-zero", rev1, rev2)
	}
	var rev int64
	var bumped bool
	if err := pool.QueryRow(ctx, "UPDATE thing SET rev = 0 WHERE id = 1 RETURNING rev, updated_at > '2000-01-02'").Scan(&rev, &bumped); err != nil {
		t.Fatal(err)
	}
	if rev <= rev2 || !bumped {
		t.Fatalf("after update rev = %d (want > %d), updated_at bumped = %v", rev, rev2, bumped)
	}
}
