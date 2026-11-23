// Package testdb gives tests their own migrated Postgres database.
//
// With STOCKPOT_TEST_DATABASE_URL unset, the first test in a package starts a
// throwaway cluster (initdb in a temp dir, unix socket only, fsync off) that
// Main stops when the package's tests end. With it set, that server is used
// instead; the URL must allow CREATE DATABASE. Either way the schema is
// migrated once into a template database and each test gets a fresh copy.
//
// Packages using it need:
//
//	func TestMain(m *testing.M) { os.Exit(testdb.Main(m)) }
package testdb

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/a-jay98/stockpot/internal/db"
)

// EnvURL names an existing server to use instead of a throwaway cluster.
const EnvURL = "STOCKPOT_TEST_DATABASE_URL"

var (
	once     sync.Once
	setupErr error
	admin    *pgxpool.Pool // on the maintenance database
	adminCfg *pgxpool.Config
	template string
	cluster  string // data dir of the cluster we started, if any
)

// Main runs the package's tests and then tears down what they set up.
func Main(m *testing.M) int {
	code := m.Run()
	teardown()
	return code
}

// New returns a pool on a fresh, fully migrated database that is dropped
// when the test ends.
func New(t testing.TB) *pgxpool.Pool {
	t.Helper()
	cfg := Config(t)
	pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// Config returns the connection config of a fresh, fully migrated database
// that is dropped when the test ends.
func Config(t testing.TB) *pgxpool.Config {
	t.Helper()
	once.Do(func() { setupErr = setup() })
	if setupErr != nil {
		t.Fatalf("testdb: %v", setupErr)
	}
	name := "t_" + randHex()
	ctx := context.Background()
	if _, err := admin.Exec(ctx, fmt.Sprintf("CREATE DATABASE %s TEMPLATE %s", name, template)); err != nil {
		t.Fatalf("testdb: create database: %v", err)
	}
	// Registered first, so it runs after the caller's pool.Close cleanup.
	t.Cleanup(func() {
		if _, err := admin.Exec(context.Background(), fmt.Sprintf("DROP DATABASE IF EXISTS %s WITH (FORCE)", name)); err != nil {
			t.Errorf("testdb: drop %s: %v", name, err)
		}
	})
	cfg := adminCfg.Copy()
	cfg.ConnConfig.Database = name
	return cfg
}

func setup() error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	url := os.Getenv(EnvURL)
	if url == "" {
		var err error
		if url, err = startCluster(ctx); err != nil {
			return err
		}
	}
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return fmt.Errorf("%s: %w", EnvURL, err)
	}
	cfg.MaxConns = 4
	adminCfg = cfg
	if admin, err = pgxpool.NewWithConfig(ctx, cfg); err != nil {
		return err
	}

	// One template per test process, so parallel packages sharing an
	// external server don't race on it.
	template = "tmpl_" + randHex()
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+template); err != nil {
		return fmt.Errorf("create template: %w", err)
	}
	tcfg := cfg.Copy()
	tcfg.ConnConfig.Database = template
	pool, err := pgxpool.NewWithConfig(ctx, tcfg)
	if err != nil {
		return err
	}
	defer pool.Close() // a template must have no connections when copied
	if err := db.Migrate(ctx, pool); err != nil {
		return fmt.Errorf("migrate template: %w", err)
	}
	return nil
}

// startCluster runs initdb and postgres from PATH (the dev shell has them)
// and returns a URL on the maintenance database.
func startCluster(ctx context.Context) (string, error) {
	if os.Geteuid() == 0 {
		return "", fmt.Errorf("initdb refuses to run as root: set %s to a server you started as another user", EnvURL)
	}
	for _, bin := range []string{"initdb", "pg_ctl"} {
		if _, err := exec.LookPath(bin); err != nil {
			return "", fmt.Errorf("%s not on PATH: run tests in the dev shell (nix develop) or set %s", bin, EnvURL)
		}
	}
	dir, err := os.MkdirTemp("", "stockpot-pg-")
	if err != nil {
		return "", err
	}
	data := filepath.Join(dir, "data")
	initdb := exec.CommandContext(ctx, "initdb", "-D", data, "-U", "postgres", "--auth=trust", //nolint:gosec // fixed tool, our temp dir
		"--encoding=UTF8", "--no-locale", "--no-sync")
	if out, err := initdb.CombinedOutput(); err != nil {
		_ = os.RemoveAll(dir)
		return "", fmt.Errorf("initdb: %w\n%s", err, out)
	}
	opts := fmt.Sprintf("-k %s -c listen_addresses='' -c fsync=off -c synchronous_commit=off -c full_page_writes=off -c max_connections=200", dir)
	if out, err := exec.CommandContext(ctx, "pg_ctl", "-D", data, "-l", filepath.Join(dir, "log"), "-o", opts, "-w", "start").CombinedOutput(); err != nil { //nolint:gosec // fixed tool, our temp dir
		_ = os.RemoveAll(dir)
		return "", fmt.Errorf("pg_ctl start: %w\n%s", err, out)
	}
	cluster = data
	return fmt.Sprintf("host=%s user=postgres dbname=postgres", dir), nil
}

func teardown() {
	if admin != nil {
		if cluster == "" {
			_, _ = admin.Exec(context.Background(), fmt.Sprintf("DROP DATABASE IF EXISTS %s WITH (FORCE)", template))
		}
		admin.Close()
	}
	if cluster != "" {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		out, err := exec.CommandContext(ctx, "pg_ctl", "-D", cluster, "-m", "immediate", "stop").CombinedOutput() //nolint:gosec // our own data dir
		if err != nil {
			fmt.Fprintf(os.Stderr, "testdb: pg_ctl stop: %v\n%s", err, out)
		}
		_ = os.RemoveAll(filepath.Dir(cluster))
	}
}

func randHex() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
