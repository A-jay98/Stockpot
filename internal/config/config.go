// Package config reads the bootstrap settings: the few things the process
// needs before it can reach its database, or that must never live in it.
// Everything else is a runtime setting stored in the database and edited in
// the admin panel (see internal/settings).
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// MinSecretKeyBytes is the least key material accepted for the secret key.
// The value is fed through HKDF, so any encoding (raw, base64, hex) works.
const MinSecretKeyBytes = 32

// Config is the bootstrap configuration, from STOCKPOT_* environment variables.
type Config struct {
	// DatabaseURL is a libpq connection string or URL. Empty: the PG*
	// variables decide (as in development).
	DatabaseURL string
	// Listen is the TCP address used when systemd passes no socket (dev).
	// Empty: a socket must be passed, so a broken unit fails loudly instead
	// of quietly binding a port.
	Listen string
	// PublicURL is the origin users reach the app at, e.g.
	// https://stockpot.jamadi.me. CSRF checks and passkeys are bound to it.
	PublicURL *url.URL
	// SecretKey encrypts secret runtime settings. Required.
	SecretKey []byte
	// AdminSetupToken claims the first admin account. Optional: once an admin
	// exists it is never read.
	AdminSetupToken string
	// LogLevel is the level used until the runtime setting log.level loads.
	LogLevel slog.Level
	// BlobDir holds uploaded files (receipt photos, …).
	BlobDir string
}

// Load reads the configuration from the process environment.
func Load() (Config, error) { return LoadFrom(os.LookupEnv) }

// LoadFrom reads the configuration through lookup (os.LookupEnv in
// production). It reports every problem at once, so a misconfigured unit is
// fixed in one round.
func LoadFrom(lookup func(string) (string, bool)) (Config, error) {
	env := func(key string) string {
		v, _ := lookup(key)
		return strings.TrimSpace(v)
	}
	var errs []error
	cfg := Config{
		DatabaseURL: env("STOCKPOT_DATABASE_URL"),
		Listen:      env("STOCKPOT_LISTEN"),
	}

	publicURL := env("STOCKPOT_PUBLIC_URL")
	if publicURL == "" {
		publicURL = "http://localhost:5173" // the Vite dev server, which proxies /api
	}
	u, err := parseOrigin(publicURL)
	if err != nil {
		errs = append(errs, fmt.Errorf("STOCKPOT_PUBLIC_URL: %w", err))
	}
	cfg.PublicURL = u

	key, err := secret(lookup, "STOCKPOT_SECRET_KEY")
	switch {
	case err != nil:
		errs = append(errs, err)
	case len(key) == 0:
		errs = append(errs, errors.New("STOCKPOT_SECRET_KEY_FILE is required (e.g. `head -c 32 /dev/urandom | base64 > key`)"))
	case len(key) < MinSecretKeyBytes:
		errs = append(errs, fmt.Errorf("STOCKPOT_SECRET_KEY_FILE: %d bytes of key material, want at least %d", len(key), MinSecretKeyBytes))
	}
	cfg.SecretKey = key

	token, err := secret(lookup, "STOCKPOT_ADMIN_SETUP_TOKEN")
	if err != nil {
		errs = append(errs, err)
	}
	cfg.AdminSetupToken = string(token)

	if lvl := env("STOCKPOT_LOG_LEVEL"); lvl != "" {
		if err := cfg.LogLevel.UnmarshalText([]byte(lvl)); err != nil {
			errs = append(errs, fmt.Errorf("STOCKPOT_LOG_LEVEL: %q is not debug, info, warn or error", lvl))
		}
	}

	cfg.BlobDir = blobDir(env("STATE_DIRECTORY"))

	if err := errors.Join(errs...); err != nil {
		return Config{}, fmt.Errorf("invalid configuration:\n%w", err)
	}
	return cfg, nil
}

// parseOrigin accepts an absolute http(s) URL with no path beyond "/".
func parseOrigin(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, err
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("%q is not an absolute http(s) URL", raw)
	}
	if (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return nil, fmt.Errorf("%q must be an origin (scheme://host[:port]) without path, query or credentials", raw)
	}
	return &url.URL{Scheme: u.Scheme, Host: u.Host}, nil
}

// secret reads NAME_FILE (a systemd credential path), or NAME itself for
// development. Setting both is an error, so there is never doubt about
// which value is live.
func secret(lookup func(string) (string, bool), name string) ([]byte, error) {
	direct, hasDirect := lookup(name)
	path, hasFile := lookup(name + "_FILE")
	hasDirect = hasDirect && direct != ""
	hasFile = hasFile && path != ""
	switch {
	case hasDirect && hasFile:
		return nil, fmt.Errorf("set %s or %s_FILE, not both", name, name)
	case hasFile:
		b, err := os.ReadFile(filepath.Clean(path))
		if err != nil {
			return nil, fmt.Errorf("%s_FILE: %w", name, err)
		}
		return []byte(strings.TrimSpace(string(b))), nil
	case hasDirect:
		return []byte(strings.TrimSpace(direct)), nil
	default:
		return nil, nil
	}
}

// blobDir picks systemd's StateDirectory (the first, if several) or a dev
// directory under the working tree.
func blobDir(stateDirectory string) string {
	if first, _, _ := strings.Cut(stateDirectory, ":"); first != "" {
		return filepath.Join(first, "blobs")
	}
	return filepath.Join(".data", "blobs")
}
