// Package backup makes nightly encrypted backups of the database and blobs
// and keeps them in a private Telegram channel: pg_dump (custom format) and a
// tar of the blob directory are each compressed with zstd, encrypted with
// age to a public key whose private half only the owner holds, split into
// parts the Bot API can carry and uploaded, followed by a manifest. Restore
// (Restore, `stockpot restore`) reverses it.
package backup

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"filippo.io/age"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/klauspost/compress/zstd"

	"github.com/a-jay98/stockpot/internal/buildinfo"
	"github.com/a-jay98/stockpot/internal/jobs"
	"github.com/a-jay98/stockpot/internal/settings"
	"github.com/a-jay98/stockpot/internal/telegram"
)

// DefaultPartSize is the size of an uploaded part. Bots can upload 50 MB but
// download only 20 MB through getFile, and restore must be able to fetch
// every part, so parts stay safely under 20 MB.
const DefaultPartSize = 19 << 20

// uploadAttempts bounds retries of one part on rate limits and outages.
const uploadAttempts = 6

// Deps are what the service needs.
type Deps struct {
	Pool     *pgxpool.Pool
	Settings *settings.Store
	Telegram *telegram.Client
	// BlobDir is the directory tarred as the blobs artifact.
	BlobDir string
	// DatabaseURL is passed to pg_dump; empty means libpq's PG* variables.
	DatabaseURL string
	// PGDump is the pg_dump binary ("pg_dump" from PATH when empty).
	PGDump string
	// SchemaVersion reports the applied migration version for the manifest.
	SchemaVersion func(context.Context) (int64, error)
	// PartSize overrides DefaultPartSize (tests).
	PartSize int
	// Now and Sleep are replaceable in tests.
	Now   func() time.Time
	Sleep func(context.Context, time.Duration) error
}

// Service runs backups and applies retention.
type Service struct {
	client *jobs.Client
	d      Deps
}

// NewService returns a service.
func NewService(d Deps) *Service {
	if d.PGDump == "" {
		d.PGDump = "pg_dump"
	}
	if d.PartSize <= 0 {
		d.PartSize = DefaultPartSize
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Sleep == nil {
		d.Sleep = func(ctx context.Context, dur time.Duration) error {
			t := time.NewTimer(dur)
			defer t.Stop()
			select {
			case <-t.C:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
	return &Service{d: d}
}

// ErrNotConfigured means the bot token, chat or age key is missing.
var ErrNotConfigured = errors.New("backups are not configured: set the Telegram bot token, chat id and age public key")

type target struct {
	chat      telegram.ChatID
	recipient age.Recipient
}

func (s *Service) target() (target, error) {
	chat := SettingChatID.Get(s.d.Settings)
	rec := SettingAgeRecipient.Get(s.d.Settings)
	if chat == "" || rec == "" || telegram.SettingBotToken.Get(s.d.Settings) == "" {
		return target{}, ErrNotConfigured
	}
	r, err := age.ParseX25519Recipient(rec)
	if err != nil {
		return target{}, fmt.Errorf("backup.age_recipient: %w", err)
	}
	return target{chat: telegram.ChatID(chat), recipient: r}, nil
}

// Configured reports whether a run could start.
func (s *Service) Configured() bool { _, err := s.target(); return err == nil }

// Run makes one backup, recording it in backup_run. The returned error is
// also stored on the run. It fails fast with ErrAlreadyRunning.
func (s *Service) Run(ctx context.Context, trigger string, jobID *int64) (err error) {
	tg, err := s.target()
	if err != nil {
		return err
	}
	if err := failStale(ctx, s.d.Pool); err != nil {
		return err
	}
	id, err := startRun(ctx, s.d.Pool, trigger, jobID, s.d.Now())
	if err != nil {
		return err
	}
	slog.InfoContext(ctx, "backup started", "run", id, "trigger", trigger)
	run := &runState{s: s, id: id, tg: tg}
	defer func() {
		// The run's own context may be cancelled (shutdown): record anyway.
		rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		if err != nil {
			slog.ErrorContext(ctx, "backup failed", "run", id, "err", err)
			run.deleteMessages(rctx)
			if ferr := finishFailed(rctx, s.d.Pool, id, truncate(err.Error(), 2000), s.d.Now()); ferr != nil {
				slog.ErrorContext(ctx, "backup: record failure", "run", id, "err", ferr)
			}
			return
		}
		if perr := s.Prune(rctx); perr != nil {
			slog.WarnContext(ctx, "backup retention failed", "err", perr)
		}
	}()

	version := int64(0)
	if s.d.SchemaVersion != nil {
		if version, err = s.d.SchemaVersion(ctx); err != nil {
			return fmt.Errorf("schema version: %w", err)
		}
	}
	m := Manifest{
		Version: ManifestVersion, RunID: id, CreatedAt: s.d.Now().UTC(),
		AppVersion: buildinfo.Get().Version, SchemaVersion: version,
		AgeRecipient: SettingAgeRecipient.Get(s.d.Settings), PartSize: int64(s.d.PartSize),
	}
	for _, art := range []struct {
		name    string
		produce func(context.Context, io.Writer) error
	}{
		{ArtifactDB, s.dumpDatabase},
		{ArtifactBlobs, s.tarBlobs},
	} {
		a, err := run.artifact(ctx, art.name, art.produce)
		if err != nil {
			return fmt.Errorf("%s: %w", art.name, err)
		}
		m.Artifacts = append(m.Artifacts, a)
	}

	mb, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	caption := fmt.Sprintf("Stockpot backup %s · %s · %d parts · %s", short(id), m.CreatedAt.Format("2006-01-02 15:04 MST"), m.PartCount(), humanBytes(m.Size()))
	if _, err := run.upload(ctx, "stockpot-"+short(id)+"-manifest.json", caption, mb); err != nil {
		return fmt.Errorf("manifest: %w", err)
	}
	if err := finishOK(ctx, s.d.Pool, id, m, s.d.Now()); err != nil {
		return err
	}
	slog.InfoContext(ctx, "backup finished", "run", id, "parts", m.PartCount(), "bytes", m.Size())
	return nil
}

type runState struct {
	s   *Service
	id  uuid.UUID
	tg  target
	ids []int64
}

// artifact streams produce → zstd → age → parts → Telegram.
func (r *runState) artifact(ctx context.Context, name string, produce func(context.Context, io.Writer) error) (Artifact, error) {
	a := Artifact{Name: name}
	sw := newSplitWriter(r.s.d.PartSize, func(part []byte) error {
		idx := len(a.Parts) + 1
		filename := fmt.Sprintf("stockpot-%s-%s.zst.age.%03d", short(r.id), name, idx)
		sum := sha256.Sum256(part)
		caption := fmt.Sprintf("Stockpot backup %s · %s part %d · sha256 %s", short(r.id), name, idx, hex.EncodeToString(sum[:])[:16])
		msg, err := r.upload(ctx, filename, caption, part)
		if err != nil {
			return err
		}
		fileID := ""
		if msg.Document != nil {
			fileID = msg.Document.FileID
		}
		a.Parts = append(a.Parts, Part{
			Index: idx, Filename: filename, Size: int64(len(part)),
			SHA256: hex.EncodeToString(sum[:]), FileID: fileID, MessageID: msg.MessageID,
		})
		return nil
	})
	enc, err := age.Encrypt(sw, r.tg.recipient)
	if err != nil {
		return a, err
	}
	zw, err := zstd.NewWriter(enc)
	if err != nil {
		return a, err
	}
	if err := produce(ctx, zw); err != nil {
		return a, err
	}
	for _, c := range []io.Closer{zw, enc, sw} { // flush each layer, innermost first
		if err := c.Close(); err != nil {
			return a, err
		}
	}
	return a, nil
}

// upload sends one file with retries, recording the message for retention.
func (r *runState) upload(ctx context.Context, filename, caption string, data []byte) (telegram.Message, error) {
	var lastErr error
	for attempt := 1; attempt <= uploadAttempts; attempt++ {
		msg, err := r.s.d.Telegram.SendDocument(ctx, r.tg.chat, filename, caption, bytes.NewReader(data))
		if err == nil {
			r.ids = append(r.ids, msg.MessageID)
			if err := addMessage(ctx, r.s.d.Pool, r.id, msg.MessageID); err != nil {
				return msg, err
			}
			return msg, nil
		}
		lastErr = err
		if !telegram.IsTemporary(err) || attempt == uploadAttempts {
			break
		}
		wait := time.Duration(1<<attempt) * time.Second
		if ra, ok := telegram.RetryAfter(err); ok && ra > 0 {
			wait = ra + time.Second
		}
		slog.WarnContext(ctx, "backup upload retry", "file", filename, "attempt", attempt, "wait", wait, "err", err)
		if err := r.s.d.Sleep(ctx, wait); err != nil {
			return telegram.Message{}, err
		}
	}
	return telegram.Message{}, fmt.Errorf("upload %s: %w", filename, lastErr)
}

// deleteMessages removes what a failed run already uploaded, best effort.
func (r *runState) deleteMessages(ctx context.Context) {
	for _, id := range r.ids {
		if err := r.s.d.Telegram.DeleteMessage(ctx, r.tg.chat, id); err != nil {
			slog.WarnContext(ctx, "backup: delete partial upload", "message", id, "err", err)
		}
	}
	_ = markPruned(ctx, r.s.d.Pool, r.id, "", r.s.d.Now())
}

// dumpDatabase streams pg_dump's custom format into w.
func (s *Service) dumpDatabase(ctx context.Context, w io.Writer) error {
	args := []string{"--format=custom", "--no-owner", "--compress=0"}
	if s.d.DatabaseURL != "" {
		args = append(args, "--dbname="+s.d.DatabaseURL)
	}
	cmd := exec.CommandContext(ctx, s.d.PGDump, args...)
	var stderr limitedBuffer
	cmd.Stdout, cmd.Stderr = w, &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("pg_dump: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// tarBlobs streams the blob directory as a tar archive (paths relative to
// the directory). A missing directory is an empty archive. Blobs are
// content-addressed and immutable, so a file vanishing mid-walk (deleted
// by the app) is skipped, not an error.
func (s *Service) tarBlobs(ctx context.Context, w io.Writer) error {
	tw := tar.NewWriter(w)
	root := s.d.BlobDir
	if root != "" {
		err := filepath.WalkDir(root, func(path string, e fs.DirEntry, err error) error {
			if err != nil {
				if errors.Is(err, fs.ErrNotExist) {
					return nil
				}
				return err
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			if !e.Type().IsRegular() || strings.HasPrefix(e.Name(), ".") { // skips in-flight .upload-* temp files
				return nil
			}
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			return addFile(tw, path, filepath.ToSlash(rel))
		})
		if err != nil {
			return fmt.Errorf("tar blobs: %w", err)
		}
	}
	return tw.Close()
}

func addFile(tw *tar.Writer, path, name string) error {
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: st.Size(), ModTime: st.ModTime(), Typeflag: tar.TypeReg}); err != nil {
		return err
	}
	_, err = io.Copy(tw, f)
	return err
}

// Prune applies retention: runs beyond backup.keep lose their Telegram
// messages where the API allows. Failures are recorded, not retried (the
// Bot API refuses to delete messages older than 48 h in most chats).
func (s *Service) Prune(ctx context.Context) error {
	tg, err := s.target()
	if err != nil {
		return nil // nothing to prune with
	}
	runs, err := unprunedOlder(ctx, s.d.Pool, SettingKeep.Get(s.d.Settings))
	if err != nil {
		return err
	}
	for _, r := range runs {
		var left int
		for _, id := range r.MessageIDs {
			if err := s.d.Telegram.DeleteMessage(ctx, tg.chat, id); err != nil {
				left++
			}
		}
		note := ""
		if left > 0 {
			note = fmt.Sprintf("%d of %d messages could not be deleted (Telegram only lets bots delete messages younger than 48 h); delete them by hand", left, len(r.MessageIDs))
		}
		if err := markPruned(ctx, s.d.Pool, r.ID, note, s.d.Now()); err != nil {
			return err
		}
	}
	return nil
}

func short(id uuid.UUID) string { return id.String()[:8] }

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGT"[exp])
}

// limitedBuffer keeps the first 4 KiB written (pg_dump's stderr).
type limitedBuffer struct{ bytes.Buffer }

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if room := 4096 - b.Len(); room > 0 {
		b.Buffer.Write(p[:min(room, len(p))])
	}
	return len(p), nil
}
