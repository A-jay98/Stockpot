// Package db owns the Postgres schema and connection pool.
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
)

// The whole directory, so it embeds even before the first .sql file exists;
// goose only reads *.sql.
//
//go:embed migrations
var migrations embed.FS

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

// Migrate applies pending migrations. The binary carries them, so a deployed
// server needs neither the goose CLI nor a source checkout.
func Migrate(ctx context.Context, url string) error {
	cfg, err := pgx.ParseConfig(url)
	if err != nil {
		return fmt.Errorf("database config: %w", err)
	}
	sqlDB := stdlib.OpenDB(*cfg)
	defer func() { _ = sqlDB.Close() }()

	provider, err := newProvider(sqlDB)
	if errors.Is(err, goose.ErrNoMigrations) {
		slog.InfoContext(ctx, "no migrations to apply")
		return nil
	}
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
	if len(results) == 0 {
		slog.InfoContext(ctx, "database is up to date")
	}
	return nil
}

// SchemaCheck reports whether the database has every migration this binary
// carries. Health uses it so a release whose migrations didn't apply rolls back.
type SchemaCheck struct {
	provider *goose.Provider // nil: the binary has no migrations
}

// NewSchemaCheck builds a check against the pool's database.
func NewSchemaCheck(pool *pgxpool.Pool) (*SchemaCheck, error) {
	provider, err := newProvider(stdlib.OpenDBFromPool(pool))
	if errors.Is(err, goose.ErrNoMigrations) {
		return &SchemaCheck{}, nil
	}
	if err != nil {
		return nil, err
	}
	return &SchemaCheck{provider: provider}, nil
}

// Ping fails while migrations are pending.
func (c *SchemaCheck) Ping(ctx context.Context) error {
	if c.provider == nil {
		return nil
	}
	pending, err := c.provider.HasPending(ctx)
	if err != nil {
		return fmt.Errorf("schema version: %w", err)
	}
	if pending {
		return errors.New("migrations pending")
	}
	return nil
}

func newProvider(sqlDB *sql.DB) (*goose.Provider, error) {
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
