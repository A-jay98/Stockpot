// Package db owns the Postgres schema and connection pool. Queries live in
// queries/*.sql and are compiled by sqlc into package dbq.
package db

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivermigrate"
)

//go:generate sqlc generate -f ../../sqlc.yaml

//go:embed migrations/*.sql
var migrations embed.FS

// migrateLockID is the pg_advisory_lock key that serializes `stockpot
// migrate` runs (two units, or a deploy racing a manual run).
const migrateLockID int64 = 0x73746f636b706f74 // "stockpot"

// Open connects a pool and checks the database answers.
func Open(ctx context.Context, url string) (*pgxpool.Pool, error) {
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("database config: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("database: %w", err)
	}
	return pool, nil
}

// Migrate applies the app's goose migrations, then River's job-queue
// migrations, holding an advisory lock throughout. The binary carries both,
// so a deployed server needs neither the goose CLI nor a checkout.
func Migrate(ctx context.Context, pool *pgxpool.Pool) (err error) {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", migrateLockID); err != nil {
		return fmt.Errorf("migrate: take lock: %w", err)
	}
	defer func() {
		// A fresh context: the lock must go even if ctx was cancelled.
		if _, uerr := conn.Exec(context.WithoutCancel(ctx), "SELECT pg_advisory_unlock($1)", migrateLockID); uerr != nil {
			err = errors.Join(err, fmt.Errorf("migrate: release lock: %w", uerr))
		}
	}()

	sqlDB := stdlib.OpenDBFromPool(pool)
	defer func() { _ = sqlDB.Close() }() // leaves the pool open
	provider, err := newGoose(sqlDB)
	if err != nil {
		return err
	}
	results, err := provider.Up(ctx)
	if err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	for _, r := range results {
		slog.InfoContext(ctx, "migrated", "version", r.Source.Version, "file", r.Source.Path, "duration", r.Duration)
	}

	river, err := newRiverMigrator(pool)
	if err != nil {
		return err
	}
	rres, err := river.Migrate(ctx, rivermigrate.DirectionUp, nil)
	if err != nil {
		return fmt.Errorf("migrate river: %w", err)
	}
	for _, v := range rres.Versions {
		slog.InfoContext(ctx, "migrated river", "version", v.Version, "name", v.Name, "duration", v.Duration)
	}
	if len(results) == 0 && len(rres.Versions) == 0 {
		slog.InfoContext(ctx, "database is up to date")
	}
	return nil
}

// SchemaCheck reports whether the database has every migration this binary
// carries. `serve` refuses to start without it, and /api/health fails, so a
// release whose migrations didn't apply rolls back.
type SchemaCheck struct {
	sqlDB *sql.DB
	goose *goose.Provider
	river *rivermigrate.Migrator[pgx.Tx]
}

// NewSchemaCheck builds a check against the pool's database.
func NewSchemaCheck(pool *pgxpool.Pool) (*SchemaCheck, error) {
	sqlDB := stdlib.OpenDBFromPool(pool)
	g, err := newGoose(sqlDB)
	if err != nil {
		_ = sqlDB.Close()
		return nil, err
	}
	r, err := newRiverMigrator(pool)
	if err != nil {
		_ = sqlDB.Close()
		return nil, err
	}
	return &SchemaCheck{sqlDB: sqlDB, goose: g, river: r}, nil
}

// Close releases the check's database/sql handle (not the pool).
func (c *SchemaCheck) Close() error { return c.sqlDB.Close() }

// ErrSchemaBehind means migrations are pending: run `stockpot migrate`.
var ErrSchemaBehind = errors.New("database schema is behind this binary: run `stockpot migrate`")

// Ping fails with ErrSchemaBehind while any migration is pending.
func (c *SchemaCheck) Ping(ctx context.Context) error {
	pending, err := c.goose.HasPending(ctx)
	if err != nil {
		return fmt.Errorf("schema version: %w", err)
	}
	if pending {
		return ErrSchemaBehind
	}
	res, err := c.river.Validate(ctx, nil)
	if err != nil {
		return fmt.Errorf("river schema version: %w", err)
	}
	if !res.OK {
		return fmt.Errorf("%w (river: %v)", ErrSchemaBehind, res.Messages)
	}
	return nil
}

// Versions are the schema versions the database has and this binary carries.
type Versions struct {
	App, AppTarget     int64 // goose
	River, RiverTarget int   // River's job-queue schema
}

// Versions reads the applied migration versions.
func (c *SchemaCheck) Versions(ctx context.Context) (Versions, error) {
	var v Versions
	cur, target, err := c.goose.GetVersions(ctx)
	if err != nil {
		return v, fmt.Errorf("schema version: %w", err)
	}
	v.App, v.AppTarget = cur, target
	existing, err := c.river.ExistingVersions(ctx)
	if err != nil {
		return v, fmt.Errorf("river schema version: %w", err)
	}
	if n := len(existing); n > 0 {
		v.River = existing[n-1].Version
	}
	if all := c.river.AllVersions(); len(all) > 0 {
		v.RiverTarget = all[len(all)-1].Version
	}
	return v, nil
}

func newGoose(sqlDB *sql.DB) (*goose.Provider, error) {
	dir, err := fs.Sub(migrations, "migrations")
	if err != nil {
		return nil, err
	}
	provider, err := goose.NewProvider(goose.DialectPostgres, sqlDB, dir)
	if err != nil {
		return nil, fmt.Errorf("migrations: %w", err)
	}
	return provider, nil
}

func newRiverMigrator(pool *pgxpool.Pool) (*rivermigrate.Migrator[pgx.Tx], error) {
	m, err := rivermigrate.New(riverpgxv5.New(pool), &rivermigrate.Config{Logger: slog.Default()})
	if err != nil {
		return nil, fmt.Errorf("river migrations: %w", err)
	}
	return m, nil
}
