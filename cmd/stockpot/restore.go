package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"filippo.io/age"

	"github.com/a-jay98/stockpot/internal/backup"
	"github.com/a-jay98/stockpot/internal/telegram"
)

const restoreUsage = `usage: stockpot restore --manifest <file|message link> --identity <age key file> [flags]

Restores a backup into an EMPTY database and blob directory. See docs/restore.md.

`

// runRestore implements `stockpot restore`. Secrets come from flags or the
// environment (STOCKPOT_BOT_TOKEN, STOCKPOT_DATABASE_URL).
func runRestore(ctx context.Context, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("restore", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.Usage = func() { fmt.Fprint(os.Stderr, restoreUsage); fs.PrintDefaults() }
	var (
		manifest = fs.String("manifest", "", "manifest.json file, or a t.me/c/<channel>/<message> link to it (needs the bot token)")
		identity = fs.String("identity", "", "age identity (private key) file from age-keygen")
		dbURL    = fs.String("database-url", os.Getenv("STOCKPOT_DATABASE_URL"), "target database (default: libpq PG* variables)")
		blobDir  = fs.String("blob-dir", "", "target blob directory (e.g. /var/lib/stockpot/blobs)")
		partsDir = fs.String("parts-dir", "", "read parts from this directory instead of Telegram (files downloaded by hand)")
		token    = fs.String("bot-token", os.Getenv("STOCKPOT_BOT_TOKEN"), "Telegram bot token")
		baseURL  = fs.String("telegram-url", telegram.DefaultBaseURL, "Bot API base URL")
		workDir  = fs.String("work-dir", "", "staging directory for downloaded parts (default: a temp dir)")
		pgRest   = fs.String("pg-restore", "pg_restore", "pg_restore binary")
	)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *manifest == "" || *identity == "" || *blobDir == "" {
		fs.Usage()
		return errors.New("--manifest, --identity and --blob-dir are required")
	}
	idf, err := os.Open(*identity)
	if err != nil {
		return err
	}
	ids, err := age.ParseIdentities(idf)
	_ = idf.Close()
	if err != nil {
		return fmt.Errorf("identity: %w", err)
	}

	var client *telegram.Client
	if *token != "" {
		client = telegram.New(func() string { return *token }, telegram.WithBaseURL(*baseURL))
	}
	m, err := backup.LoadManifest(ctx, *manifest, client)
	if err != nil {
		return err
	}
	var src backup.PartSource
	switch {
	case *partsDir != "":
		src = backup.DirSource(*partsDir)
	case client != nil:
		src = backup.TelegramSource{Client: client}
	default:
		return errors.New("need --bot-token (download from Telegram) or --parts-dir (files already downloaded)")
	}
	fmt.Fprintf(out, "backup %s from %s (app %s, schema v%d, %d parts)\n",
		m.RunID, m.CreatedAt.Format("2006-01-02 15:04 MST"), m.AppVersion, m.SchemaVersion, m.PartCount())
	return backup.Restore(ctx, backup.RestoreOptions{
		Manifest: m, Identities: ids, Source: src, DatabaseURL: *dbURL, BlobDir: *blobDir,
		PGRestore: *pgRest, WorkDir: *workDir,
		Logf: func(f string, a ...any) { fmt.Fprintf(out, f+"\n", a...) },
	})
}
