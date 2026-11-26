package settings_test

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/a-jay98/stockpot/internal/settings"
	"github.com/a-jay98/stockpot/internal/testdb"
)

type testItem struct {
	Name string `json:"name"`
	N    int    `json:"n"`
}

var (
	testList = settings.DefineJSON(settings.JSONDef[[]testItem]{
		Key:     "test.items",
		Default: []testItem{{Name: "a", N: 1}},
		Validate: func(v []testItem) error {
			for _, it := range v {
				if it.Name == "" {
					return errors.New("name is required")
				}
			}
			return nil
		},
		Description: "test",
	})
	testKeys = settings.DefineFamily(settings.Def[string]{
		Key: "test.key", Secret: true, Description: "test",
	})
)

func TestJSONSetting(t *testing.T) {
	ctx := t.Context()
	pool := testdb.New(t)
	s := newStore(t, pool)
	if got := testList.Get(s); len(got) != 1 || got[0].Name != "a" {
		t.Fatalf("default = %+v", got)
	}
	d, _ := settings.Lookup(testList.Key())
	if d.Kind != settings.KindJSON {
		t.Fatalf("kind = %s", d.Kind)
	}

	changed := 0
	s.OnChange(testList.Key(), func() { changed++ })
	if err := s.Set(ctx, testList.Key(), json.RawMessage(`[{"name":"x","n":2},{"name":"y","n":3}]`), nil); err != nil {
		t.Fatal(err)
	}
	if got := testList.Get(newStore(t, pool)); len(got) != 2 || got[1] != (testItem{Name: "y", N: 3}) {
		t.Fatalf("stored = %+v", got)
	}
	if changed != 1 {
		t.Fatalf("OnChange ran %d times", changed)
	}

	for _, raw := range []string{
		`[{"name":""}]`,         // validation
		`[{"name":"x","zz":1}]`, // unknown field
		`{"name":"x"}`,          // wrong shape
		`[{"name":"x"}] [1]`,    // trailing data
	} {
		var ve *settings.ValidationError
		if err := s.Set(ctx, testList.Key(), json.RawMessage(raw), nil); !errors.As(err, &ve) {
			t.Errorf("Set(%s) = %v, want ValidationError", raw, err)
		}
	}
}

func TestFamily(t *testing.T) {
	ctx := t.Context()
	pool := testdb.New(t)
	s := newStore(t, pool)

	if _, ok := testKeys.Member("Bad-Id"); ok {
		t.Fatal("invalid id accepted")
	}
	if _, ok := settings.Lookup("test.key.Bad"); ok {
		t.Fatal("Lookup accepted an invalid member key")
	}
	m, ok := testKeys.Member("alpha")
	if !ok || m.Key() != "test.key.alpha" {
		t.Fatalf("member = %v %v", m, ok)
	}
	if d, ok := settings.Lookup("test.key.beta"); !ok || !d.Secret || d.Family != "test.key" {
		t.Fatalf("Lookup(member) = %+v %v", d, ok)
	}
	for _, v := range s.List() {
		if v.Family == "test.key" {
			t.Fatalf("unset member listed: %s", v.Key)
		}
	}

	if err := settings.SetValue(ctx, s, m, "sk-alpha", nil); err != nil {
		t.Fatal(err)
	}
	// A fresh handle and a fresh store both see it.
	again, _ := testKeys.Member("alpha")
	if got := again.Get(newStore(t, pool)); got != "sk-alpha" {
		t.Fatalf("member value = %q", got)
	}
	listed := false
	for _, v := range s.List() {
		if v.Key == "test.key.alpha" {
			listed = v.IsSet && v.Value == nil
		}
	}
	if !listed {
		t.Fatal("stored secret member not listed (or its value exposed)")
	}

	var secret []byte
	if err := pool.QueryRow(ctx, "SELECT secret FROM setting WHERE key = 'test.key.alpha'").Scan(&secret); err != nil || len(secret) == 0 {
		t.Fatalf("member not stored encrypted: %v", err)
	}
}

func TestFamilyCollisions(t *testing.T) {
	for name, fn := range map[string]func(){
		"define under family": func() { settings.Define(settings.Def[int]{Key: "test.key.gamma"}) },
		"family over setting": func() { settings.DefineFamily(settings.Def[string]{Key: "retention.app_log_days"}) },
		"family twice":        func() { settings.DefineFamily(settings.Def[string]{Key: "test.key"}) },
	} {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatal("want a panic")
				}
			}()
			fn()
		})
	}
}
