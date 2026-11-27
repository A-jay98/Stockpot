package backup

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"filippo.io/age"
	"github.com/jackc/pgx/v5"
	"github.com/klauspost/compress/zstd"

	"github.com/a-jay98/stockpot/internal/telegram"
)

// PartSource opens the bytes of a part.
type PartSource interface {
	Open(ctx context.Context, p Part) (io.ReadCloser, error)
}

// DirSource reads parts from files in a directory, named as in the manifest:
// the fallback when the bot or its file ids are gone (download the files
// from the channel by hand).
type DirSource string

// Open implements PartSource.
func (d DirSource) Open(_ context.Context, p Part) (io.ReadCloser, error) {
	return os.Open(filepath.Join(string(d), filepath.Base(p.Filename)))
}

// TelegramSource downloads parts with getFile.
type TelegramSource struct{ Client *telegram.Client }

// Open implements PartSource.
func (t TelegramSource) Open(ctx context.Context, p Part) (io.ReadCloser, error) {
	if p.FileID == "" {
		return nil, errors.New("part has no file id")
	}
	f, err := t.Client.GetFile(ctx, p.FileID)
	if err != nil {
		return nil, err
	}
	return t.Client.Download(ctx, f.FilePath)
}

// RestoreOptions drive Restore.
type RestoreOptions struct {
	Manifest   Manifest
	Identities []age.Identity
	Source     PartSource
	// DatabaseURL is the database to restore into: freshly created and NOT
	// migrated (pg_restore creates the schema; migrate afterwards, or just
	// start the app). Empty means libpq's PG* variables.
	DatabaseURL string
	// BlobDir receives the blobs.
	BlobDir string
	// PGRestore is the pg_restore binary ("pg_restore" when empty).
	PGRestore string
	// WorkDir stages downloaded parts (a temporary directory when empty).
	WorkDir string
	// Logf reports progress; may be nil.
	Logf func(format string, args ...any)
}

// Restore downloads and verifies every part, then restores the database and
// the blobs. Nothing is touched until every part has been verified. The
// database must be empty: pg_restore runs in one transaction without
// --clean, so a non-empty target fails and changes nothing.
func Restore(ctx context.Context, o RestoreOptions) error {
	logf := o.Logf
	if logf == nil {
		logf = func(string, ...any) {}
	}
	work := o.WorkDir
	if work == "" {
		var err error
		if work, err = os.MkdirTemp("", "stockpot-restore-*"); err != nil {
			return err
		}
		defer func() { _ = os.RemoveAll(work) }()
	}
	staged := map[string][]string{}
	for _, name := range []string{ArtifactDB, ArtifactBlobs} {
		a, ok := o.Manifest.Artifact(name)
		if !ok {
			return fmt.Errorf("manifest has no %q artifact", name)
		}
		for _, p := range a.Parts {
			logf("fetching %s", p.Filename)
			path, err := stage(ctx, o.Source, p, work)
			if err != nil {
				return fmt.Errorf("%s: %w", p.Filename, err)
			}
			staged[name] = append(staged[name], path)
		}
	}
	logf("all %d parts verified", o.Manifest.PartCount())

	// Blobs first: writing them is idempotent (atomic, content-addressed), so
	// a failure leaves the database untouched and the command can be re-run.
	logf("restoring blobs")
	if err := o.restoreBlobs(ctx, staged[ArtifactBlobs], logf); err != nil {
		return fmt.Errorf("blobs: %w", err)
	}
	logf("restoring database")
	if err := o.restoreDB(ctx, staged[ArtifactDB]); err != nil {
		return fmt.Errorf("database: %w", err)
	}
	// The dump contains the backup run that made it, still "running". Close
	// it, or the restored instance shows a phantom backup in progress.
	if err := o.closePhantomRun(ctx); err != nil {
		logf("warning: could not clear the in-progress backup recorded in the dump: %v", err)
	}
	return nil
}

// closePhantomRun fails backup runs left "running" by the dump and cancels
// their queued River jobs, so a restored instance doesn't resume them.
func (o RestoreOptions) closePhantomRun(ctx context.Context) error {
	conn, err := pgx.Connect(ctx, o.DatabaseURL)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close(ctx) }()
	tx, err := conn.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `UPDATE backup_run SET status = 'failed', finished_at = now(),
		error = 'restored from a backup taken while this run was in progress' WHERE status = 'running'`); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM river_job WHERE kind = 'backup.run' AND state NOT IN ('completed', 'discarded', 'cancelled')`); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// stage downloads a part into dir and checks its size and SHA-256.
func stage(ctx context.Context, src PartSource, p Part, dir string) (string, error) {
	rc, err := src.Open(ctx, p)
	if err != nil {
		return "", err
	}
	defer func() { _ = rc.Close() }()
	path := filepath.Join(dir, filepath.Base(p.Filename))
	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600) //nolint:gosec // path is inside our own staging dir, basename of a manifest name
	if err != nil {
		return "", err
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), rc)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return "", err
	}
	if n != p.Size {
		return "", fmt.Errorf("size %d, manifest says %d", n, p.Size)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != p.SHA256 {
		return "", fmt.Errorf("sha256 mismatch: got %s, manifest says %s", got, p.SHA256)
	}
	return path, nil
}

// plaintext decrypts and decompresses the concatenation of the staged parts.
func (o RestoreOptions) plaintext(paths []string) (io.Reader, func(), error) {
	var files []*os.File
	closeAll := func() {
		for _, f := range files {
			_ = f.Close()
		}
	}
	var readers []io.Reader
	for _, p := range paths {
		f, err := os.Open(p) //nolint:gosec // staged by stage() inside the work dir
		if err != nil {
			closeAll()
			return nil, nil, err
		}
		files = append(files, f)
		readers = append(readers, f)
	}
	dec, err := age.Decrypt(io.MultiReader(readers...), o.Identities...)
	if err != nil {
		closeAll()
		return nil, nil, fmt.Errorf("decrypt (wrong identity?): %w", err)
	}
	zr, err := zstd.NewReader(dec)
	if err != nil {
		closeAll()
		return nil, nil, err
	}
	return zr, func() { zr.Close(); closeAll() }, nil
}

func (o RestoreOptions) restoreDB(ctx context.Context, paths []string) error {
	r, done, err := o.plaintext(paths)
	if err != nil {
		return err
	}
	defer done()
	bin := o.PGRestore
	if bin == "" {
		bin = "pg_restore"
	}
	args := []string{"--no-owner", "--single-transaction", "--exit-on-error"}
	if o.DatabaseURL != "" {
		args = append(args, "--dbname="+o.DatabaseURL)
	}
	cmd := exec.CommandContext(ctx, bin, args...) //nolint:gosec // operator-supplied binary and target
	var stderr limitedBuffer
	cmd.Stdin, cmd.Stderr = r, &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("pg_restore: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

var blobName = regexp.MustCompile(`^[0-9a-f]{2}/[0-9a-f]{2}/[0-9a-f]{64}$`)

func (o RestoreOptions) restoreBlobs(ctx context.Context, paths []string, logf func(string, ...any)) error {
	r, done, err := o.plaintext(paths)
	if err != nil {
		return err
	}
	defer done()
	if err := os.MkdirAll(o.BlobDir, 0o700); err != nil {
		return err
	}
	tr := tar.NewReader(r)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if h.Typeflag != tar.TypeReg || !blobName.MatchString(h.Name) {
			// Never extract anything but well-formed blob paths (this also
			// rules out "..", absolute paths and links); skip the rest.
			if h.Typeflag != tar.TypeDir {
				logf("skipping unexpected archive entry %q", h.Name)
			}
			continue
		}
		dst := filepath.Join(o.BlobDir, filepath.FromSlash(h.Name))
		if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
			return err
		}
		if err := writeAtomic(dst, tr); err != nil {
			return err
		}
	}
}

func writeAtomic(dst string, r io.Reader) error {
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".restore-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := io.Copy(tmp, r); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), dst)
}

// LoadManifest reads a manifest from a file path or, with a Telegram
// message link (https://t.me/c/<chat>/<message>), from that message in the
// channel via client.
func LoadManifest(ctx context.Context, ref string, client *telegram.Client) (Manifest, error) {
	if !strings.HasPrefix(ref, "https://t.me/") && !strings.HasPrefix(ref, "t.me/") {
		b, err := os.ReadFile(ref) //nolint:gosec // the operator names the manifest file
		if err != nil {
			return Manifest{}, err
		}
		return ParseManifest(b)
	}
	chat, msgID, err := ParseMessageLink(ref)
	if err != nil {
		return Manifest{}, err
	}
	if client == nil {
		return Manifest{}, errors.New("a message link needs --bot-token")
	}
	// The Bot API cannot read a message by id; forward it to the same chat to
	// see its document, then remove the copy.
	fwd, err := client.ForwardMessage(ctx, chat, chat, msgID)
	if err != nil {
		return Manifest{}, fmt.Errorf("fetch message: %w", err)
	}
	defer func() { _ = client.DeleteMessage(ctx, chat, fwd.MessageID) }()
	if fwd.Document == nil {
		return Manifest{}, errors.New("that message has no file; link the manifest.json message")
	}
	f, err := client.GetFile(ctx, fwd.Document.FileID)
	if err != nil {
		return Manifest{}, err
	}
	rc, err := client.Download(ctx, f.FilePath)
	if err != nil {
		return Manifest{}, err
	}
	defer func() { _ = rc.Close() }()
	b, err := io.ReadAll(io.LimitReader(rc, 16<<20))
	if err != nil {
		return Manifest{}, err
	}
	return ParseManifest(b)
}

var linkPattern = regexp.MustCompile(`^(?:https://)?t\.me/c/(\d+)/(?:\d+/)?(\d+)$`)

// ParseMessageLink turns https://t.me/c/1234567890/42 into the chat id
// (-1001234567890) and message id.
func ParseMessageLink(link string) (telegram.ChatID, int64, error) {
	m := linkPattern.FindStringSubmatch(strings.TrimSpace(link))
	if m == nil {
		return "", 0, fmt.Errorf("not a private-channel message link (https://t.me/c/<id>/<message>): %q", link)
	}
	id, err := strconv.ParseInt(m[2], 10, 64)
	if err != nil {
		return "", 0, fmt.Errorf("bad message id in %q", link)
	}
	return telegram.ChatID("-100" + m[1]), id, nil
}
