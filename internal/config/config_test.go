package config

import "testing"

func TestLoad(t *testing.T) {
	t.Setenv("STOCKPOT_DATABASE_URL", "host=/run/postgresql user=stockpot dbname=stockpot")
	t.Setenv("STOCKPOT_LISTEN", "")
	t.Setenv("STOCKPOT_PUBLIC_URL", "https://stockpot.jamadi.me")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Listen != "" {
		t.Errorf("Listen = %q, want empty", cfg.Listen)
	}
	if cfg.PublicURL != "https://stockpot.jamadi.me" {
		t.Errorf("PublicURL = %q", cfg.PublicURL)
	}
}

func TestLoadRejectsRelativePublicURL(t *testing.T) {
	t.Setenv("STOCKPOT_PUBLIC_URL", "stockpot.jamadi.me")
	if _, err := Load(); err == nil {
		t.Fatal("want an error for a URL without scheme")
	}
}
