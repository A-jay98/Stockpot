// Package config reads the bootstrap settings: the few things the process
// needs before it can reach its database. Everything else is a runtime
// setting stored in the database and edited in the admin panel.
package config

import (
	"fmt"
	"net/url"
	"os"
)

// Config is the bootstrap configuration, from STOCKPOT_* environment variables.
type Config struct {
	// DatabaseURL is a libpq connection string or URL. Empty: the PG*
	// variables decide (as in development).
	DatabaseURL string
	// Listen is the TCP address used when systemd passes no socket (dev).
	// Empty: a socket must be passed, so a broken unit fails loudly instead
	// of quietly binding a port.
	Listen string
	// PublicURL is where users reach the app, e.g. https://stockpot.jamadi.me.
	// Passkeys are bound to its host.
	PublicURL string
	// SecretKeyFile and AdminSetupTokenFile point at systemd credentials
	// (%d/secret-key, %d/admin-setup-token). Optional until the features
	// that read them land; empty in development.
	SecretKeyFile       string
	AdminSetupTokenFile string
}

// Load reads the configuration from the environment.
func Load() (Config, error) {
	cfg := Config{
		DatabaseURL: os.Getenv("STOCKPOT_DATABASE_URL"),
		Listen:      os.Getenv("STOCKPOT_LISTEN"),
		PublicURL:   getenv("STOCKPOT_PUBLIC_URL", "http://localhost:5173"),

		SecretKeyFile:       os.Getenv("STOCKPOT_SECRET_KEY_FILE"),
		AdminSetupTokenFile: os.Getenv("STOCKPOT_ADMIN_SETUP_TOKEN_FILE"),
	}
	u, err := url.Parse(cfg.PublicURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return Config{}, fmt.Errorf("STOCKPOT_PUBLIC_URL %q is not an absolute URL", cfg.PublicURL)
	}
	return cfg, nil
}

func getenv(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return fallback
}
