// Package app wires the server together: configuration, database, settings,
// logging, jobs and HTTP, started and stopped in the right order.
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/jackc/pgx/v5"
	"golang.org/x/sync/errgroup"

	"github.com/a-jay98/stockpot/internal/admin"
	"github.com/a-jay98/stockpot/internal/audit"
	"github.com/a-jay98/stockpot/internal/blob"
	"github.com/a-jay98/stockpot/internal/buildinfo"
	"github.com/a-jay98/stockpot/internal/config"
	"github.com/a-jay98/stockpot/internal/db"
	"github.com/a-jay98/stockpot/internal/httpapi"
	"github.com/a-jay98/stockpot/internal/identity"
	"github.com/a-jay98/stockpot/internal/identity/passkey"
	"github.com/a-jay98/stockpot/internal/jobs"
	"github.com/a-jay98/stockpot/internal/listener"
	"github.com/a-jay98/stockpot/internal/logging"
	"github.com/a-jay98/stockpot/internal/settings"
)

// shutdownTimeout bounds each shutdown stage (HTTP drain, job drain).
const shutdownTimeout = 10 * time.Second

// Migrate applies every pending migration (the systemd preStart step).
func Migrate(ctx context.Context, cfg config.Config) error {
	pool, err := db.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	return db.Migrate(ctx, pool)
}

// Serve runs the server until ctx ends, then shuts down in order: stop
// accepting requests, drain jobs, flush logs, close the database.
func Serve(ctx context.Context, cfg config.Config) error {
	startedAt := time.Now()
	level := new(slog.LevelVar)
	level.Set(cfg.LogLevel)
	slog.SetDefault(logging.New(os.Stdout, level, nil))

	bi := buildinfo.Get()
	slog.InfoContext(ctx, "starting", "version", bi.Version, "commit", bi.Commit)

	pool, err := db.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	schema, err := db.NewSchemaCheck(pool)
	if err != nil {
		return err
	}
	defer func() { _ = schema.Close() }()
	if err := schema.Ping(ctx); err != nil {
		return fmt.Errorf("refusing to serve: %w", err)
	}

	store, err := settings.NewStore(pool, cfg.SecretKey)
	if err != nil {
		return err
	}
	if err := store.Load(ctx); err != nil {
		return err
	}

	// From here on, log lines also go to app_log for the admin panel.
	sink := logging.NewSink(pool, logging.DefaultQueueSize)
	sinkCtx, stopSink := context.WithCancel(context.WithoutCancel(ctx))
	sinkDone := make(chan struct{})
	go func() { sink.Run(sinkCtx); close(sinkDone) }()
	defer func() { stopSink(); <-sinkDone }()
	slog.SetDefault(logging.New(os.Stdout, level, sink))
	logging.FollowSetting(store, level, cfg.LogLevel)

	blobs, err := blob.NewFS(cfg.BlobDir)
	if err != nil {
		return err
	}
	_ = blobs // first used by capture (M2)

	passkeys, err := passkey.New(pool, cfg.PublicURL)
	if err != nil {
		return err
	}
	ident, err := identity.NewService(identity.Deps{
		Pool: pool, Settings: store, Enroller: passkeys,
		SetupToken: cfg.AdminSetupToken, PublicURL: cfg.PublicURL,
	})
	if err != nil {
		return err
	}

	// Every request is recorded in access_log, off the request path.
	access := audit.NewAccessWriter(pool, audit.DefaultAccessQueue)
	accessCtx, stopAccess := context.WithCancel(context.WithoutCancel(ctx))
	accessDone := make(chan struct{})
	go func() { access.Run(accessCtx); close(accessDone) }()
	defer func() { stopAccess(); <-accessDone }()

	sweepers := []jobs.Sweeper{jobs.AppLogSweeper(pool)}
	sweepers = append(sweepers, audit.Sweepers(pool)...)
	sweepers = append(sweepers, identity.Sweepers(pool)...)
	jobClient, err := jobs.New(jobs.Deps{
		Pool:     pool,
		Settings: store,
		Sweepers: sweepers,
		Logger:   slog.Default(),
	})
	if err != nil {
		return err
	}

	adminSvc := admin.New(admin.Deps{
		Pool:      pool,
		Settings:  store,
		Jobs:      jobClient,
		Schema:    schema,
		LogSink:   sink,
		StartedAt: startedAt,
		Audit: func(ctx context.Context, tx pgx.Tx, action string, t admin.Target, before, after any) error {
			return audit.Record(ctx, tx, action, audit.Target{Type: t.Type, ID: t.ID}, before, after)
		},
	})

	handler, err := httpapi.NewHandler(
		httpapi.NewServer(httpapi.Deps{
			Checks:   map[string]httpapi.Pinger{"database": pool, "schema": schema},
			Admin:    adminSvc,
			Identity: ident,
		}),
		httpapi.Options{
			PublicURL:  cfg.PublicURL,
			AccessLog:  accessLogger{access},
			Session:    httpapi.SessionLoader(ident),
			Authorizer: httpapi.IdentityAuthorizer{},
		},
	)
	if err != nil {
		return err
	}
	ln, err := listener.Listen(ctx, cfg.Listen)
	if err != nil {
		return err
	}
	srv := httpapi.NewHTTPServer(handler)

	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error { return store.Run(gctx) })
	g.Go(func() error {
		// River's own context must outlive gctx so Stop can drain gracefully.
		if err := jobClient.Start(context.WithoutCancel(gctx)); err != nil {
			return fmt.Errorf("start jobs: %w", err)
		}
		<-gctx.Done()
		sctx, cancel := context.WithTimeout(context.WithoutCancel(gctx), shutdownTimeout)
		defer cancel()
		if err := jobClient.Stop(sctx); err != nil {
			// Running jobs outlived the grace period: cancel them, so they
			// release their connections before the pool closes.
			slog.WarnContext(sctx, "jobs did not stop in time; cancelling", "err", err)
			cctx, ccancel := context.WithTimeout(context.WithoutCancel(gctx), shutdownTimeout)
			defer ccancel()
			if err := jobClient.StopAndCancel(cctx); err != nil {
				slog.ErrorContext(cctx, "jobs did not cancel", "err", err)
			}
		}
		return nil
	})
	g.Go(func() error {
		slog.InfoContext(gctx, "listening", "addr", ln.Addr().String())
		if err := srv.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	})
	g.Go(func() error {
		<-gctx.Done()
		sctx, cancel := context.WithTimeout(context.WithoutCancel(gctx), shutdownTimeout)
		defer cancel()
		return srv.Shutdown(sctx)
	})
	err = g.Wait()
	slog.InfoContext(ctx, "stopped", "err", err)
	return err
}

// accessLogger feeds the HTTP pipeline's access records to access_log.
type accessLogger struct{ w *audit.AccessWriter }

func (a accessLogger) LogAccess(_ context.Context, rec httpapi.AccessRecord) {
	a.w.Log(audit.Access{
		Time: rec.Time, Duration: rec.Duration, Method: rec.Method, Route: rec.Route,
		Path: rec.Path, Status: rec.Status, Request: rec.Request,
	})
}
