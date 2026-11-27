package backup

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"filippo.io/age"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/a-jay98/stockpot/internal/settings"
	"github.com/a-jay98/stockpot/internal/telegram"
	"github.com/a-jay98/stockpot/internal/telegram/telegramtest"
	"github.com/a-jay98/stockpot/internal/testdb"
)

func TestMain(m *testing.M) { os.Exit(testdb.Main(m)) }

type env struct {
	pool     *pgxpool.Pool
	dbURL    string
	store    *settings.Store
	tg       *telegramtest.Server
	client   *telegram.Client
	svc      *Service
	identity *age.X25519Identity
	blobDir  string
	sleeps   []time.Duration
}

func connURL(p *pgxpool.Pool, db string) string {
	c := p.Config().ConnConfig
	u := url.URL{Scheme: "postgres", User: url.User(c.User), Host: fmt.Sprintf("%s:%d", c.Host, c.Port), Path: "/" + db, RawQuery: "sslmode=disable"}
	return u.String()
}

func newEnv(t *testing.T, partSize int) *env {
	t.Helper()
	pool := testdb.New(t)
	store, err := settings.NewStore(pool, []byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	for _, s := range []struct {
		set func() error
	}{
		{func() error { return settings.SetValue(ctx, store, SettingChatID, "-1001234567890", nil) }},
		{func() error { return settings.SetValue(ctx, store, SettingAgeRecipient, id.Recipient().String(), nil) }},
		{func() error { return settings.SetValue(ctx, store, telegram.SettingBotToken, telegramtest.Token, nil) }},
	} {
		if err := s.set(); err != nil {
			t.Fatal(err)
		}
	}
	tg := telegramtest.New(t)
	client := telegram.New(func() string { return telegram.SettingBotToken.Get(store) }, telegram.WithBaseURL(tg.URL))
	var dbname string
	if err := pool.QueryRow(ctx, "SELECT current_database()").Scan(&dbname); err != nil {
		t.Fatal(err)
	}
	e := &env{pool: pool, dbURL: connURL(pool, dbname), store: store, tg: tg, client: client, identity: id, blobDir: t.TempDir()}
	e.svc = NewService(Deps{
		Pool: pool, Settings: store, Telegram: client, BlobDir: e.blobDir, DatabaseURL: e.dbURL,
		SchemaVersion: func(context.Context) (int64, error) { return 4, nil },
		PartSize:      partSize,
		Sleep:         func(_ context.Context, d time.Duration) error { e.sleeps = append(e.sleeps, d); return nil },
	})
	return e
}

// seed adds rows and blobs; returns the blob hashes.
func (e *env) seed(t *testing.T, rows, blobs int) map[string]bool {
	t.Helper()
	ctx := t.Context()
	if _, err := e.pool.Exec(ctx, `CREATE TABLE demo (id int PRIMARY KEY, v text NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < rows; i++ {
		b := make([]byte, 64)
		_, _ = rand.Read(b)
		if _, err := e.pool.Exec(ctx, `INSERT INTO demo VALUES ($1, $2)`, i, hex.EncodeToString(b)); err != nil {
			t.Fatal(err)
		}
	}
	hashes := map[string]bool{}
	for i := 0; i < blobs; i++ {
		b := make([]byte, 1000+i*500)
		_, _ = rand.Read(b)
		sum := sha256.Sum256(b)
		h := hex.EncodeToString(sum[:])
		p := filepath.Join(e.blobDir, h[:2], h[2:4], h)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, b, 0o600); err != nil {
			t.Fatal(err)
		}
		hashes[h] = true
	}
	// An in-flight upload must not be archived.
	if err := os.WriteFile(filepath.Join(e.blobDir, ".upload-123"), []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	return hashes
}

func (e *env) lastRun(t *testing.T) Run {
	t.Helper()
	rs, err := recentRuns(t.Context(), e.pool, 1)
	if err != nil || len(rs) != 1 {
		t.Fatalf("runs = %v, %v", rs, err)
	}
	return rs[0]
}

func TestBackupRestoreEndToEnd(t *testing.T) {
	const partSize = 16 << 10
	e := newEnv(t, partSize)
	hashes := e.seed(t, 400, 25)

	if err := e.svc.Run(t.Context(), TriggerManual, nil); err != nil {
		t.Fatal(err)
	}
	run := e.lastRun(t)
	if run.Status != "ok" || run.Parts < 4 || run.SizeBytes == 0 || len(run.MessageIDs) != run.Parts+1 {
		t.Fatalf("run = %+v", run)
	}
	msgs := e.tg.Messages()
	if len(msgs) != run.Parts+1 {
		t.Fatalf("%d messages for %d parts", len(msgs), run.Parts)
	}
	for _, m := range msgs[:len(msgs)-1] {
		if len(m.Data) > partSize || m.ChatID != "-1001234567890" || !strings.Contains(m.Caption, "sha256") {
			t.Fatalf("bad part %q: %d bytes chat %s caption %q", m.Filename, len(m.Data), m.ChatID, m.Caption)
		}
		if bytes.Contains(m.Data, []byte("PGDMP")) {
			t.Fatalf("part %s is not encrypted", m.Filename)
		}
	}

	// The manifest is the last message; restore from it into an empty database.
	manifestFile := filepath.Join(t.TempDir(), "manifest.json")
	last := msgs[len(msgs)-1]
	if err := os.WriteFile(manifestFile, last.Data, 0o600); err != nil {
		t.Fatal(err)
	}
	m, err := LoadManifest(t.Context(), manifestFile, nil)
	if err != nil {
		t.Fatal(err)
	}
	if m.SchemaVersion != 4 || len(m.Artifacts) != 2 {
		t.Fatalf("manifest = %+v", m)
	}

	if _, err := e.pool.Exec(t.Context(), `CREATE DATABASE restored`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = e.pool.Exec(context.Background(), `DROP DATABASE IF EXISTS restored WITH (FORCE)`) })
	restoredURL := connURL(e.pool, "restored")
	restoredBlobs := t.TempDir()
	err = Restore(t.Context(), RestoreOptions{
		Manifest: m, Identities: []age.Identity{e.identity}, Source: TelegramSource{e.client},
		DatabaseURL: restoredURL, BlobDir: restoredBlobs,
	})
	if err != nil {
		t.Fatal(err)
	}

	rp, err := pgxpool.New(t.Context(), restoredURL)
	if err != nil {
		t.Fatal(err)
	}
	defer rp.Close()
	var n int
	if err := rp.QueryRow(t.Context(), `SELECT count(*) FROM demo`).Scan(&n); err != nil || n != 400 {
		t.Fatalf("restored rows = %d, %v", n, err)
	}
	var orig, got string
	q := `SELECT md5(string_agg(id || v, ',' ORDER BY id)) FROM demo`
	_ = e.pool.QueryRow(t.Context(), q).Scan(&orig)
	_ = rp.QueryRow(t.Context(), q).Scan(&got)
	if orig != got {
		t.Fatalf("data differs: %s vs %s", orig, got)
	}
	// Settings (and their encrypted secret) came along too.
	if err := rp.QueryRow(t.Context(), `SELECT count(*) FROM backup_run WHERE status = 'ok'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	err = filepath.WalkDir(restoredBlobs, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(b)
		h := hex.EncodeToString(sum[:])
		if filepath.Base(p) != h {
			t.Errorf("blob %s has hash %s", p, h)
		}
		seen[h] = true
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(seen) != len(hashes) {
		t.Fatalf("restored %d blobs, want %d", len(seen), len(hashes))
	}
	for h := range hashes {
		if !seen[h] {
			t.Fatalf("blob %s missing", h)
		}
	}

	// A second restore into the now non-empty database changes nothing and fails.
	err = Restore(t.Context(), RestoreOptions{
		Manifest: m, Identities: []age.Identity{e.identity}, Source: TelegramSource{e.client},
		DatabaseURL: restoredURL, BlobDir: t.TempDir(),
	})
	if err == nil {
		t.Fatal("restore into a non-empty database succeeded")
	}
}

func TestRestoreRejectsBadInput(t *testing.T) {
	e := newEnv(t, 16<<10)
	e.seed(t, 20, 3)
	if err := e.svc.Run(t.Context(), TriggerManual, nil); err != nil {
		t.Fatal(err)
	}
	msgs := e.tg.Messages()
	m, err := ParseManifest(msgs[len(msgs)-1].Data)
	if err != nil {
		t.Fatal(err)
	}
	opts := RestoreOptions{Manifest: m, Identities: []age.Identity{e.identity}, Source: TelegramSource{e.client}, DatabaseURL: "postgres://x", BlobDir: t.TempDir()}

	other, _ := age.GenerateX25519Identity()
	wrong := opts
	wrong.Identities = []age.Identity{other}
	wrong.DatabaseURL = connURL(e.pool, "nonexistent") // never reached: decrypt fails first
	if err := Restore(t.Context(), wrong); err == nil || !strings.Contains(err.Error(), "decrypt") {
		t.Fatalf("wrong identity: %v", err)
	}

	// A corrupted part is caught by its hash before anything is restored.
	dir := t.TempDir()
	for _, a := range m.Artifacts {
		for _, p := range a.Parts {
			for _, sm := range msgs {
				if sm.Filename == p.Filename {
					data := append([]byte(nil), sm.Data...)
					if p.Filename == m.Artifacts[0].Parts[0].Filename {
						data[len(data)/2] ^= 0xff
					}
					if err := os.WriteFile(filepath.Join(dir, p.Filename), data, 0o600); err != nil {
						t.Fatal(err)
					}
				}
			}
		}
	}
	bad := opts
	bad.Source = DirSource(dir)
	if err := Restore(t.Context(), bad); err == nil || !strings.Contains(err.Error(), "sha256 mismatch") {
		t.Fatalf("corrupt part: %v", err)
	}
}

func TestRateLimitIsRetried(t *testing.T) {
	e := newEnv(t, 16<<10)
	e.seed(t, 10, 2)
	e.tg.Fail = func(method string, n int) (int, int) {
		if method == "sendDocument" && n == 2 {
			return 429, 7
		}
		return 0, 0
	}
	if err := e.svc.Run(t.Context(), TriggerManual, nil); err != nil {
		t.Fatal(err)
	}
	if len(e.sleeps) != 1 || e.sleeps[0] < 7*time.Second {
		t.Fatalf("sleeps = %v, want one wait honouring retry_after", e.sleeps)
	}
	if r := e.lastRun(t); r.Status != "ok" {
		t.Fatalf("run = %+v", r)
	}
}

func TestFailureMarksRunFailedAndCleansUp(t *testing.T) {
	e := newEnv(t, 4<<10)
	e.seed(t, 200, 3)
	e.tg.Fail = func(method string, n int) (int, int) {
		if method == "sendDocument" && n == 3 {
			return 400, 0 // permanent: e.g. the bot was removed from the channel
		}
		return 0, 0
	}
	err := e.svc.Run(t.Context(), TriggerManual, nil)
	if err == nil {
		t.Fatal("want an error")
	}
	r := e.lastRun(t)
	if r.Status != "failed" || r.Error == nil || !strings.Contains(*r.Error, "injected failure") || r.FinishedAt == nil {
		t.Fatalf("run = %+v", r)
	}
	for _, m := range e.tg.Messages() {
		if !m.Deleted {
			t.Fatalf("partial upload %s left in the channel", m.Filename)
		}
	}
	// A failed run does not block the next one.
	e.tg.Fail = nil
	if err := e.svc.Run(t.Context(), TriggerManual, nil); err != nil {
		t.Fatal(err)
	}
}

func TestRunLockAndStaleRecovery(t *testing.T) {
	e := newEnv(t, 16<<10)
	ctx := t.Context()
	// A running row whose job is gone is stale and is failed by the next run.
	if _, err := startRun(ctx, e.pool, TriggerManual, nil, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := startRun(ctx, e.pool, TriggerManual, nil, time.Now()); !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("second start = %v", err)
	}
	if err := e.svc.Run(ctx, TriggerManual, nil); err != nil {
		t.Fatal(err)
	}
	rs, _ := recentRuns(ctx, e.pool, 10)
	if len(rs) != 2 || rs[1].Status != "failed" || !strings.Contains(*rs[1].Error, "interrupted") {
		t.Fatalf("runs = %+v", rs)
	}
}

func TestNotConfigured(t *testing.T) {
	e := newEnv(t, 16<<10)
	if err := e.store.Reset(t.Context(), SettingChatID.Key()); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.Run(t.Context(), TriggerManual, nil); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("err = %v", err)
	}
	if rs, _ := recentRuns(t.Context(), e.pool, 10); len(rs) != 0 {
		t.Fatal("an unconfigured run left a record")
	}
}

func TestRetention(t *testing.T) {
	e := newEnv(t, 16<<10)
	if err := settings.SetValue(t.Context(), e.store, SettingKeep, 2, nil); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		if err := e.svc.Run(t.Context(), TriggerManual, nil); err != nil {
			t.Fatal(err)
		}
		time.Sleep(5 * time.Millisecond) // distinct started_at
	}
	rs, _ := recentRuns(t.Context(), e.pool, 10)
	if len(rs) != 4 {
		t.Fatalf("%d runs", len(rs))
	}
	del := map[int64]bool{}
	for _, m := range e.tg.Messages() {
		del[m.MessageID] = m.Deleted
	}
	for i, r := range rs { // newest first
		for _, id := range r.MessageIDs {
			if want := i >= 2; del[id] != want {
				t.Errorf("run %d message %d deleted=%v, want %v", i, id, del[id], want)
			}
		}
		if (r.PrunedAt != nil) != (i >= 2) {
			t.Errorf("run %d pruned_at = %v", i, r.PrunedAt)
		}
	}

	// Messages Telegram refuses to delete are recorded, not retried forever.
	e.tg.Fail = func(method string, _ int) (int, int) {
		if method == "deleteMessage" {
			return 400, 0
		}
		return 0, 0
	}
	if err := e.svc.Run(t.Context(), TriggerManual, nil); err != nil {
		t.Fatal(err)
	}
	rs, _ = recentRuns(t.Context(), e.pool, 10)
	if rs[2].PrunedAt == nil || rs[2].DeleteNote == nil || !strings.Contains(*rs[2].DeleteNote, "could not be deleted") {
		t.Fatalf("run 2 = %+v", rs[2])
	}
	calls := e.tg.Calls("deleteMessage")
	if err := e.svc.Prune(t.Context()); err != nil {
		t.Fatal(err)
	}
	if e.tg.Calls("deleteMessage") != calls {
		t.Fatal("pruned runs were retried")
	}
}

func TestSplitWriter(t *testing.T) {
	for _, tc := range []struct {
		name string
		size int
		in   int
		want []int
	}{
		{"empty", 10, 0, []int{0}},
		{"smaller than a part", 10, 4, []int{4}},
		{"exactly one part", 10, 10, []int{10}},
		{"one over", 10, 11, []int{10, 1}},
		{"several", 10, 35, []int{10, 10, 10, 5}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var parts [][]byte
			w := newSplitWriter(tc.size, func(p []byte) error { parts = append(parts, append([]byte(nil), p...)); return nil })
			data := make([]byte, tc.in)
			_, _ = rand.Read(data)
			// Write in awkward chunk sizes.
			for off := 0; off < len(data); {
				n := min(7, len(data)-off)
				if _, err := w.Write(data[off : off+n]); err != nil {
					t.Fatal(err)
				}
				off += n
			}
			if err := w.Close(); err != nil {
				t.Fatal(err)
			}
			var got []int
			var joined []byte
			for _, p := range parts {
				got = append(got, len(p))
				joined = append(joined, p...)
			}
			if fmt.Sprint(got) != fmt.Sprint(tc.want) || !bytes.Equal(joined, data) {
				t.Fatalf("parts %v, want %v (data equal: %v)", got, tc.want, bytes.Equal(joined, data))
			}
		})
	}
	// An emit error stops the stream.
	w := newSplitWriter(4, func([]byte) error { return io.ErrClosedPipe })
	if _, err := w.Write(make([]byte, 9)); err == nil {
		t.Fatal("emit error swallowed")
	}
}

func TestSettingsValidation(t *testing.T) {
	id, _ := age.GenerateX25519Identity()
	for _, tc := range []struct {
		v  string
		ok bool
	}{{id.Recipient().String(), true}, {"", true}, {"age1notakey", false}, {id.String(), false}} {
		if err := validate(SettingAgeRecipient.Key(), tc.v); (err == nil) != tc.ok {
			t.Errorf("recipient %.12q: err = %v", tc.v, err)
		}
	}
	for v, ok := range map[string]bool{"-1001234567890": true, "@my_channel": true, "": true, "abc": false, "12": false} {
		if err := validate(SettingChatID.Key(), v); (err == nil) != ok {
			t.Errorf("chat %q: err = %v", v, err)
		}
	}
	for v, ok := range map[string]bool{"03:30": true, "23:59": true, "24:00": false, "3:30": false, "0330": false} {
		if err := validate(SettingTime.Key(), v); (err == nil) != ok {
			t.Errorf("time %q: err = %v", v, err)
		}
	}
}

func TestNextRun(t *testing.T) {
	loc, _ := time.LoadLocation(Location)
	at := func(y int, mo time.Month, d, h, m int) time.Time { return time.Date(y, mo, d, h, m, 0, 0, loc) }
	if got, want := NextRun("03:30", at(2026, 11, 27, 1, 0)), at(2026, 11, 27, 3, 30); !got.Equal(want) {
		t.Errorf("before: %v, want %v", got, want)
	}
	if got, want := NextRun("03:30", at(2026, 11, 27, 3, 30)), at(2026, 11, 28, 3, 30); !got.Equal(want) {
		t.Errorf("at: %v, want %v", got, want)
	}
	// Across the spring DST change the wall-clock time holds.
	if got, want := NextRun("03:30", at(2027, 3, 13, 12, 0)), at(2027, 3, 14, 3, 30); !got.Equal(want) {
		t.Errorf("dst: %v, want %v", got, want)
	}
}

func TestParseMessageLink(t *testing.T) {
	chat, id, err := ParseMessageLink("https://t.me/c/1234567890/42")
	if err != nil || chat != "-1001234567890" || id != 42 {
		t.Fatalf("%v %d %v", chat, id, err)
	}
	if _, _, err := ParseMessageLink("https://example.com/x"); err == nil {
		t.Fatal("accepted a foreign link")
	}
}

func TestLoadManifestFromLink(t *testing.T) {
	e := newEnv(t, 16<<10)
	if err := e.svc.Run(t.Context(), TriggerManual, nil); err != nil {
		t.Fatal(err)
	}
	msgs := e.tg.Messages()
	link := fmt.Sprintf("https://t.me/c/1234567890/%d", msgs[len(msgs)-1].MessageID)
	m, err := LoadManifest(t.Context(), link, e.client)
	if err != nil || m.PartCount() == 0 {
		t.Fatalf("%+v %v", m, err)
	}
}

func validate(key, v string) error {
	def, ok := settings.Lookup(key)
	if !ok {
		return fmt.Errorf("unknown setting %s", key)
	}
	raw, _ := json.Marshal(v)
	_, err := def.Decode(raw)
	return err
}
