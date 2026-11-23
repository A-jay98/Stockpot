// Command stockpot is the Stockpot server.
//
//	stockpot serve     run the HTTP API (socket-activated under systemd)
//	stockpot migrate   apply pending database migrations
//	stockpot version   print the build
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
	_ "time/tzdata" // zone names must resolve inside the sandbox, which has no zoneinfo

	"golang.org/x/sync/errgroup"

	"github.com/a-jay98/stockpot/internal/buildinfo"
	"github.com/a-jay98/stockpot/internal/config"
	"github.com/a-jay98/stockpot/internal/db"
	"github.com/a-jay98/stockpot/internal/httpapi"
	"github.com/a-jay98/stockpot/internal/listener"
)

const usage = `usage: stockpot <command>

commands:
  serve     run the HTTP API
  migrate   apply pending database migrations
  version   print the build
`

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, nil)))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := run(ctx, os.Args[1:], os.Stdout)
	stop()
	if err != nil {
		slog.Error("exiting", "err", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, stdout io.Writer) error {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, usage)
		return errors.New("missing command")
	}
	switch args[0] {
	case "serve":
		return serve(ctx)
	case "migrate":
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		return db.Migrate(ctx, cfg.DatabaseURL)
	case "version":
		bi := buildinfo.Get()
		_, err := fmt.Fprintf(stdout, "stockpot %s (%s, built %s)\n", bi.Version, bi.Commit, bi.BuiltAt)
		return err
	case "help", "-h", "--help":
		_, err := fmt.Fprint(stdout, usage)
		return err
	default:
		fmt.Fprint(os.Stderr, usage)
		return fmt.Errorf("unknown command %q", args[0])
	}
}

// shutdownTimeout is how long in-flight requests get after SIGTERM.
const shutdownTimeout = 10 * time.Second

func serve(ctx context.Context) error {
	bi := buildinfo.Get()
	slog.Info("starting", "version", bi.Version, "commit", bi.Commit)

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	pool, err := db.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	schema, err := db.NewSchemaCheck(pool)
	if err != nil {
		return err
	}
	handler, err := httpapi.NewHandler(httpapi.NewServer(map[string]httpapi.Pinger{
		"database": pool,
		"schema":   schema,
	}))
	if err != nil {
		return err
	}
	ln, err := listener.Listen(ctx, cfg.Listen)
	if err != nil {
		return err
	}
	srv := httpapi.NewHTTPServer(handler)

	g, ctx := errgroup.WithContext(ctx)
	g.Go(func() error {
		slog.Info("listening", "addr", ln.Addr().String())
		if err := srv.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	})
	g.Go(func() error {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		return srv.Shutdown(sctx)
	})
	return g.Wait()
}
