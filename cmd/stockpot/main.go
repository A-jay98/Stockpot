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
	"os"
	"os/signal"
	"syscall"
	_ "time/tzdata" // zone names must resolve inside the sandbox, which has no zoneinfo

	"github.com/a-jay98/stockpot/internal/app"
	"github.com/a-jay98/stockpot/internal/buildinfo"
	"github.com/a-jay98/stockpot/internal/config"
)

const usage = `usage: stockpot <command>

commands:
  serve     run the HTTP API
  migrate   apply pending database migrations
  version   print the build
`

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
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
	case "serve", "migrate":
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		if args[0] == "serve" {
			return app.Serve(ctx, cfg)
		}
		return app.Migrate(ctx, cfg)
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
