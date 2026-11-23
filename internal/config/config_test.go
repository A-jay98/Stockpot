package config

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testKey = "0123456789abcdef0123456789abcdef"

func lookupMap(m map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) { v, ok := m[k]; return v, ok }
}

func TestLoadProduction(t *testing.T) {
	dir := t.TempDir()
	keyFile := filepath.Join(dir, "secret-key")
	tokenFile := filepath.Join(dir, "admin-setup-token")
	if err := os.WriteFile(keyFile, []byte(testKey+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tokenFile, []byte("setup\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadFrom(lookupMap(map[string]string{
		"STOCKPOT_DATABASE_URL":           "host=/run/postgresql user=stockpot dbname=stockpot",
		"STOCKPOT_PUBLIC_URL":             "https://stockpot.jamadi.me/",
		"STOCKPOT_SECRET_KEY_FILE":        keyFile,
		"STOCKPOT_ADMIN_SETUP_TOKEN_FILE": tokenFile,
		"STOCKPOT_LOG_LEVEL":              "warn",
		"STATE_DIRECTORY":                 "/var/lib/stockpot:/var/lib/other",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.PublicURL.String(); got != "https://stockpot.jamadi.me" {
		t.Errorf("PublicURL = %q", got)
	}
	if string(cfg.SecretKey) != testKey {
		t.Errorf("SecretKey = %q (trailing newline must be trimmed)", cfg.SecretKey)
	}
	if cfg.AdminSetupToken != "setup" {
		t.Errorf("AdminSetupToken = %q", cfg.AdminSetupToken)
	}
	if cfg.LogLevel != slog.LevelWarn {
		t.Errorf("LogLevel = %v", cfg.LogLevel)
	}
	if cfg.BlobDir != "/var/lib/stockpot/blobs" {
		t.Errorf("BlobDir = %q", cfg.BlobDir)
	}
	if cfg.Listen != "" {
		t.Errorf("Listen = %q, want empty", cfg.Listen)
	}
}

func TestLoadDevDefaults(t *testing.T) {
	cfg, err := LoadFrom(lookupMap(map[string]string{"STOCKPOT_SECRET_KEY": testKey}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.PublicURL.String() != "http://localhost:5173" || cfg.BlobDir != filepath.Join(".data", "blobs") || cfg.LogLevel != slog.LevelInfo {
		t.Errorf("defaults: %+v", cfg)
	}
}

func TestLoadReportsEveryProblem(t *testing.T) {
	_, err := LoadFrom(lookupMap(map[string]string{
		"STOCKPOT_PUBLIC_URL":             "stockpot.jamadi.me",
		"STOCKPOT_SECRET_KEY":             "short",
		"STOCKPOT_ADMIN_SETUP_TOKEN":      "a",
		"STOCKPOT_ADMIN_SETUP_TOKEN_FILE": "/nonexistent",
		"STOCKPOT_LOG_LEVEL":              "loud",
	}))
	if err == nil {
		t.Fatal("want an error")
	}
	for _, want := range []string{"STOCKPOT_PUBLIC_URL", "at least 32", "not both", "STOCKPOT_LOG_LEVEL"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error lacks %q:\n%v", want, err)
		}
	}
}

func TestLoadRequiresSecretKey(t *testing.T) {
	_, err := LoadFrom(lookupMap(nil))
	if err == nil || !strings.Contains(err.Error(), "STOCKPOT_SECRET_KEY_FILE is required") {
		t.Fatalf("err = %v", err)
	}
}

func TestLoadUnreadableSecretFile(t *testing.T) {
	_, err := LoadFrom(lookupMap(map[string]string{"STOCKPOT_SECRET_KEY_FILE": filepath.Join(t.TempDir(), "missing")}))
	if err == nil || !strings.Contains(err.Error(), "STOCKPOT_SECRET_KEY_FILE") {
		t.Fatalf("err = %v", err)
	}
}

func TestPublicURLMustBeOrigin(t *testing.T) {
	for _, raw := range []string{"https://x.test/app", "ftp://x.test", "https://u:p@x.test", "/relative"} {
		if _, err := parseOrigin(raw); err == nil {
			t.Errorf("parseOrigin(%q): want error", raw)
		}
	}
}
