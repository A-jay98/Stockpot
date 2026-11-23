package settings_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/a-jay98/stockpot/internal/settings"
	"github.com/a-jay98/stockpot/internal/testdb"
)

func TestMain(m *testing.M) { os.Exit(testdb.Main(m)) }

var testKey = []byte("0123456789abcdef0123456789abcdef")

var (
	testFlag = settings.Define(settings.Def[bool]{
		Key: "flag.test_feature", Default: false, Description: "test",
	})
	testSecret = settings.Define(settings.Def[string]{
		Key: "test.api_key", Secret: true, Description: "test",
	})
)

func TestDefineValidatesDefaults(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("want a panic for an invalid default")
		}
	}()
	settings.Define(settings.Def[int]{Key: "test.bad_default", Default: 0, Validate: settings.Between(1, 5)})
}

func TestDefineRejectsDuplicates(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("want a panic for a duplicate key")
		}
	}()
	settings.Define(settings.Def[string]{Key: "log.level", Default: "info"})
}

func TestBuiltinSettings(t *testing.T) {
	want := map[string]any{
		"log.level":                 "info",
		"retention.app_log_days":    14,
		"retention.access_log_days": 90,
		"retention.audit_days":      730,
	}
	for key, def := range want {
		d, ok := settings.Lookup(key)
		if !ok || d.Default != def {
			t.Errorf("%s: %+v", key, d)
		}
	}
}

func newStore(t *testing.T, pool *pgxpool.Pool) *settings.Store {
	t.Helper()
	s, err := settings.NewStore(pool, testKey)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Load(t.Context()); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestSetGetReset(t *testing.T) {
	ctx := t.Context()
	pool := testdb.New(t)
	s := newStore(t, pool)
	if got := settings.RetentionAppLogDays.Get(s); got != 14 {
		t.Fatalf("default = %d", got)
	}
	if err := settings.SetValue(ctx, s, settings.RetentionAppLogDays, 30, nil); err != nil {
		t.Fatal(err)
	}
	if got := settings.RetentionAppLogDays.Get(s); got != 30 {
		t.Fatalf("after set = %d", got)
	}
	if got := settings.RetentionAppLogDays.Get(newStore(t, pool)); got != 30 {
		t.Fatalf("fresh store reads %d from the database", got)
	}
	if err := s.Reset(ctx, settings.RetentionAppLogDays.Key()); err != nil {
		t.Fatal(err)
	}
	if got := settings.RetentionAppLogDays.Get(s); got != 14 {
		t.Fatalf("after reset = %d", got)
	}
}

func TestSetValidates(t *testing.T) {
	ctx := t.Context()
	s := newStore(t, testdb.New(t))
	cases := map[string]string{
		"retention.app_log_days": `0`,      // out of range
		"log.level":              `"loud"`, // not an option
		"flag.test_feature":      `"yes"`,  // wrong type
	}
	for key, raw := range cases {
		var ve *settings.ValidationError
		if err := s.Set(ctx, key, json.RawMessage(raw), nil); !errors.As(err, &ve) {
			t.Errorf("Set(%s, %s) = %v, want ValidationError", key, raw, err)
		}
	}
	if err := s.Set(ctx, "no.such_key", json.RawMessage(`1`), nil); !errors.Is(err, settings.ErrUnknownKey) {
		t.Errorf("unknown key: %v", err)
	}
}

func TestSecretIsEncryptedAtRest(t *testing.T) {
	ctx := t.Context()
	pool := testdb.New(t)
	s := newStore(t, pool)
	if err := settings.SetValue(ctx, s, testSecret, "sk-very-secret", nil); err != nil {
		t.Fatal(err)
	}
	var value, secret []byte
	if err := pool.QueryRow(ctx, "SELECT value, secret FROM setting WHERE key = $1", testSecret.Key()).Scan(&value, &secret); err != nil {
		t.Fatal(err)
	}
	if value != nil || len(secret) == 0 || bytes.Contains(secret, []byte("sk-very-secret")) {
		t.Fatalf("stored value=%q secret=%x", value, secret)
	}
	if got := testSecret.Get(newStore(t, pool)); got != "sk-very-secret" {
		t.Fatalf("decrypted = %q", got)
	}
	for _, v := range s.List() {
		if v.Key == testSecret.Key() && (v.Value != nil || !v.IsSet) {
			t.Fatalf("List exposes secret: %+v", v)
		}
	}

	// Under another master key the value can't be opened: default applies.
	other, err := settings.NewStore(pool, []byte("a different key of thirty-two bytes"))
	if err != nil {
		t.Fatal(err)
	}
	if err := other.Load(ctx); err != nil {
		t.Fatal(err)
	}
	if got := testSecret.Get(other); got != "" {
		t.Fatalf("wrong key decrypted %q", got)
	}
}

func TestInvalidStoredValueFallsBackToDefault(t *testing.T) {
	ctx := t.Context()
	pool := testdb.New(t)
	if _, err := pool.Exec(ctx, `INSERT INTO setting (key, value) VALUES ('retention.app_log_days', '"many"')`); err != nil {
		t.Fatal(err)
	}
	if got := settings.RetentionAppLogDays.Get(newStore(t, pool)); got != 14 {
		t.Fatalf("got %d, want default 14", got)
	}
}

func TestChangesPropagateLive(t *testing.T) {
	ctx := t.Context()
	pool := testdb.New(t)
	writer := newStore(t, pool)
	reader := newStore(t, pool)

	changed := make(chan struct{}, 10)
	reader.OnChange(testFlag.Key(), func() { changed <- struct{}{} })
	go func() { _ = reader.Run(ctx) }()

	// Run reloads everything once it is listening; wait for LISTEN to be up
	// by retrying the write until the reader sees one.
	deadline := time.After(10 * time.Second)
	for want := true; ; want = !want {
		if err := settings.SetValue(ctx, writer, testFlag, want, nil); err != nil {
			t.Fatal(err)
		}
		select {
		case <-changed:
			if got := testFlag.Get(reader); got != want {
				// A notification for an earlier write; keep going.
				continue
			}
			return
		case <-time.After(200 * time.Millisecond):
		case <-deadline:
			t.Fatal("reader never saw the change")
		}
	}
}
