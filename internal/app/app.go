// Package app wires the server together: configuration, database, settings,
// logging, jobs and HTTP, started and stopped in the right order.
package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"golang.org/x/sync/errgroup"

	"github.com/a-jay98/stockpot/internal/admin"
	"github.com/a-jay98/stockpot/internal/alerts"
	"github.com/a-jay98/stockpot/internal/audit"
	"github.com/a-jay98/stockpot/internal/backup"
	"github.com/a-jay98/stockpot/internal/blob"
	"github.com/a-jay98/stockpot/internal/buildinfo"
	"github.com/a-jay98/stockpot/internal/catalog"
	"github.com/a-jay98/stockpot/internal/catalog/resolve"
	"github.com/a-jay98/stockpot/internal/commands"
	"github.com/a-jay98/stockpot/internal/config"
	"github.com/a-jay98/stockpot/internal/cook"
	"github.com/a-jay98/stockpot/internal/db"
	"github.com/a-jay98/stockpot/internal/httpapi"
	"github.com/a-jay98/stockpot/internal/identity"
	"github.com/a-jay98/stockpot/internal/identity/passkey"
	"github.com/a-jay98/stockpot/internal/inventory"
	"github.com/a-jay98/stockpot/internal/jobs"
	"github.com/a-jay98/stockpot/internal/listener"
	"github.com/a-jay98/stockpot/internal/llm"
	"github.com/a-jay98/stockpot/internal/llm/openaicompat"
	"github.com/a-jay98/stockpot/internal/logging"
	"github.com/a-jay98/stockpot/internal/media"
	"github.com/a-jay98/stockpot/internal/recipes"
	"github.com/a-jay98/stockpot/internal/recipes/chat"
	"github.com/a-jay98/stockpot/internal/reports"
	"github.com/a-jay98/stockpot/internal/settings"
	"github.com/a-jay98/stockpot/internal/shopping"
	"github.com/a-jay98/stockpot/internal/syncdb"
	"github.com/a-jay98/stockpot/internal/telegram"
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
	if err := db.Migrate(ctx, pool); err != nil {
		return err
	}
	// Reference data that ships with the binary; idempotent. Swaps name
	// ingredients, so they go second.
	if err := catalog.Seed(ctx, pool); err != nil {
		return err
	}
	return recipes.SeedSubstitutionRows(ctx, pool)
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

	var adminSvc *admin.Service // set below; the gateway's key changes go through it (audited)
	gateway := llm.NewRouter(llm.Deps{
		Pool:      pool,
		Settings:  store,
		Blobs:     blobs,
		Factories: map[string]llm.Factory{llm.KindOpenAICompat: openaicompat.Factory(llmHTTPClient())},
		WriteSetting: func(ctx context.Context, key string, value json.RawMessage) error {
			var err error
			if value == nil {
				_, err = adminSvc.ResetSetting(ctx, key)
			} else {
				_, err = adminSvc.SetSetting(ctx, key, value)
			}
			return err
		},
	})

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

	// Offline sync: commands are the only write path; each feature registers
	// its handlers here (one line per area).
	cmdRegistry := commands.NewRegistry()
	catalog.Register(cmdRegistry, resolve.Resolver{})
	inventory.Register(cmdRegistry, resolve.Resolver{}, func(ctx context.Context, tx pgx.Tx, hh, product uuid.UUID, store *uuid.UUID, at time.Time, qty, unit *string) error {
		return shopping.RecordPurchase(ctx, tx, hh, product, store, uuid.Nil, at, qty, unit)
	})
	shopping.Register(cmdRegistry, resolve.Resolver{})
	recipeCmds := recipes.Register(cmdRegistry, resolve.Resolver{})
	recipeCmds.OnChatVersion = chat.MarkSaved
	cook.Register(cmdRegistry)
	syncStore, err := syncdb.New(ctx, pool)
	if err != nil {
		return err
	}

	// Every request is recorded in access_log, off the request path.
	access := audit.NewAccessWriter(pool, audit.DefaultAccessQueue)
	accessCtx, stopAccess := context.WithCancel(context.WithoutCancel(ctx))
	accessDone := make(chan struct{})
	go func() { access.Run(accessCtx); close(accessDone) }()
	defer func() { stopAccess(); <-accessDone }()

	// Telegram: one bot for backups (and alerts); the token is a live setting.
	tg := telegram.New(func() string { return telegram.SettingBotToken.Get(store) },
		telegram.WithHTTPClient(&http.Client{Transport: llmHTTPClient().Transport}))
	backups := backup.NewService(backup.Deps{
		Pool: pool, Settings: store, Telegram: tg, BlobDir: cfg.BlobDir, DatabaseURL: cfg.DatabaseURL,
		SchemaVersion: func(ctx context.Context) (int64, error) {
			v, err := schema.Versions(ctx)
			return v.App, err
		},
	})

	alertSvc := alerts.NewService(alerts.Deps{Pool: pool, Settings: store, Telegram: tg, PublicURL: publicURL(cfg)})

	sweepers := []jobs.Sweeper{jobs.AppLogSweeper(pool)}
	sweepers = append(sweepers, audit.Sweepers(pool)...)
	sweepers = append(sweepers, identity.Sweepers(pool)...)
	sweepers = append(sweepers, llm.Sweepers(pool)...)
	sweepers = append(sweepers, backup.Sweepers(pool)...)
	enricher := catalog.NewEnricher(catalog.EnrichDeps{Pool: pool, Settings: store, LLM: gateway})
	sweepers = append(sweepers, enricher.Sweepers()...)
	sweepers = append(sweepers, alerts.Sweepers(alertSvc)...)
	sweepers = append(sweepers, recipes.Sweepers(pool)...)
	reportSvc := reports.New(reports.Deps{Pool: pool, Settings: store, LLM: gateway})
	sweepers = append(sweepers, reportSvc.Sweepers()...)
	jobClient, err := jobs.New(jobs.Deps{
		Pool:     pool,
		Settings: store,
		Sweepers: sweepers,
		Workers:  []func(*river.Workers) error{backups.Registrar(), enricher.Registrar(), alertSvc.Registrar(), reportSvc.Registrar()},
		Periodic: []*river.PeriodicJob{alerts.PeriodicJob()},
		Logger:   slog.Default(),
	})
	if err != nil {
		return err
	}

	backups.Attach(jobClient)
	enricher.Attach(jobClient)
	reportSvc.Attach(jobClient)

	adminSvc = admin.New(admin.Deps{
		Pool:      pool,
		Settings:  store,
		Jobs:      jobClient,
		Schema:    schema,
		LogSink:   sink,
		StartedAt: startedAt,
		LLMToday:  llmToday(gateway),
		Backups:   backupsToday(backups),
		Audit: func(ctx context.Context, tx pgx.Tx, action string, t admin.Target, before, after any) error {
			return audit.Record(ctx, tx, action, audit.Target{Type: t.Type, ID: t.ID}, before, after)
		},
	})

	handler, err := httpapi.NewHandler(
		httpapi.NewServer(httpapi.Deps{
			Checks:    map[string]httpapi.Pinger{"database": pool, "schema": schema},
			Admin:     adminSvc,
			Identity:  ident,
			LLM:       gateway,
			Commands:  commands.NewService(pool, cmdRegistry),
			Sync:      syncStore,
			Backups:   backups,
			Enrich:    enricher,
			Matcher:   catalog.Matcher{Pool: pool, Res: resolve.Resolver{}},
			Alerts:    alertSvc,
			FoodRules: inventory.NewRuleService(pool),
			Recipes:   &recipes.Assistant{Pool: pool, LLM: gateway, W: recipeCmds.W},
			Reports:   reportSvc,
			Chat:      &chat.Service{Pool: pool, LLM: gateway},
			Blobs:     media.New(pool, blobs),
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
	g.Go(func() error { return alertSvc.RunBot(gctx) })
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

// llmHTTPClient is shared by every LLM provider: outbound only, bounded
// connection setup; each call's deadline comes from its provider's timeout.
func llmHTTPClient() *http.Client {
	t := http.DefaultTransport.(*http.Transport).Clone() //nolint:forcetypeassert // stdlib default
	t.TLSHandshakeTimeout = 10 * time.Second
	t.ResponseHeaderTimeout = 2 * time.Minute
	t.MaxIdleConnsPerHost = 4
	return &http.Client{Transport: t}
}

// llmToday adapts the gateway's status to the admin overview tile.
func llmToday(g *llm.Router) func(context.Context) (admin.LLMToday, error) {
	return func(ctx context.Context) (admin.LLMToday, error) {
		st, err := g.Status(ctx)
		if err != nil {
			return admin.LLMToday{}, err
		}
		t := admin.LLMToday{Requests: st.RequestsToday, Tokens: st.TokensToday, Available: st.Available}
		if st.GlobalRequestCap > 0 {
			c := int64(st.GlobalRequestCap)
			t.RequestCap = &c
		}
		return t, nil
	}
}

// backupsToday adapts backup health to the admin overview tile.
func backupsToday(b *backup.Service) func(context.Context) (admin.BackupsToday, error) {
	return func(ctx context.Context) (admin.BackupsToday, error) {
		h, err := b.Health(ctx)
		if err != nil {
			return admin.BackupsToday{}, err
		}
		return admin.BackupsToday{Enabled: h.Enabled, LastOK: h.LastOK, LastStatus: h.LastStatus, LastError: h.LastError, Stale: h.Stale}, nil
	}
}

// publicURL is the app's origin as a string ("" when unset).
func publicURL(cfg config.Config) string {
	if cfg.PublicURL == nil {
		return ""
	}
	return cfg.PublicURL.String()
}
