package admin

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/a-jay98/stockpot/internal/settings"
	"github.com/a-jay98/stockpot/internal/testdb"
)

func TestMain(m *testing.M) { os.Exit(testdb.Main(m)) }

var testSecret = settings.Define(settings.Def[string]{
	Key: "test.admin_secret", Secret: true, Description: "test",
})

func newService(t *testing.T, audit AuditFunc) (*Service, *settings.Store) {
	t.Helper()
	pool := testdb.New(t)
	store, err := settings.NewStore(pool, []byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Load(t.Context()); err != nil {
		t.Fatal(err)
	}
	return New(Deps{Pool: pool, Settings: store, Audit: audit}), store
}

func TestFailedAuditRollsBackTheChange(t *testing.T) {
	svc, store := newService(t, func(context.Context, pgx.Tx, string, Target, any, any) error {
		return errors.New("audit store down")
	})
	if _, err := svc.SetSetting(t.Context(), "retention.app_log_days", json.RawMessage(`30`)); err == nil {
		t.Fatal("want the audit error")
	}
	if err := store.Load(t.Context()); err != nil {
		t.Fatal(err)
	}
	if v, set := settings.RetentionAppLogDays.Lookup(store); set {
		t.Fatalf("change committed without its audit record: %d", v)
	}
}

func TestSecretsNeverReachTheAuditTrail(t *testing.T) {
	var got []any
	svc, store := newService(t, func(_ context.Context, _ pgx.Tx, _ string, _ Target, before, after any) error {
		got = append(got, before, after)
		return nil
	})
	for _, v := range []string{`"sk-first"`, `"sk-second"`} {
		view, err := svc.SetSetting(t.Context(), testSecret.Key(), json.RawMessage(v))
		if err != nil {
			t.Fatal(err)
		}
		if view.Value != nil || !view.IsSet {
			t.Fatalf("view leaks or misses the secret: %+v", view)
		}
	}
	if testSecret.Get(store) != "sk-second" {
		t.Fatal("secret not stored")
	}
	b, _ := json.Marshal(got)
	if strings.Contains(string(b), "sk-") || string(b) != `[{"set":false},{"set":true},{"set":true},{"set":true}]` {
		t.Fatalf("audit snapshots %s", b)
	}
}

func TestCursor(t *testing.T) {
	id, err := decodeID(encodeID(42))
	if err != nil || id != 42 {
		t.Fatalf("round trip %d %v", id, err)
	}
	for _, bad := range []string{"", "!!", encodeID(0), "LTE"} { // "LTE" = "-1"
		if _, err := decodeID(bad); !errors.Is(err, ErrBadCursor) {
			t.Errorf("%q: %v", bad, err)
		}
	}
}
