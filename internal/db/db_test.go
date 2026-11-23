package db

import (
	"os"
	"testing"
)

// Needs a scratch database: `make test` creates and passes one.
func testURL(t *testing.T) string {
	t.Helper()
	url := os.Getenv("STOCKPOT_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("STOCKPOT_TEST_DATABASE_URL not set")
	}
	return url
}

func TestMigrateThenSchemaIsCurrent(t *testing.T) {
	ctx := t.Context()
	url := testURL(t)
	// Twice: a second run must be a no-op.
	for range 2 {
		if err := Migrate(ctx, url); err != nil {
			t.Fatal(err)
		}
	}
	pool, err := Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	check, err := NewSchemaCheck(pool)
	if err != nil {
		t.Fatal(err)
	}
	if err := check.Ping(ctx); err != nil {
		t.Fatalf("schema check after migrate: %v", err)
	}
}
