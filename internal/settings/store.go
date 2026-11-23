package settings

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/a-jay98/stockpot/internal/db/dbq"
	"github.com/a-jay98/stockpot/internal/secretbox"
)

// notifyChannel is raised by the setting table's trigger with the key.
const notifyChannel = "setting_changed"

// ErrUnknownKey means no setting with that key is declared.
var ErrUnknownKey = errors.New("unknown setting")

// Store caches setting values and keeps them current. Safe for concurrent use.
type Store struct {
	pool *pgxpool.Pool
	q    *dbq.Queries
	box  *secretbox.Box

	// loadMu serializes Load and reload, so a full reload can't overwrite
	// a newer single-key reload with its older snapshot.
	loadMu sync.Mutex

	mu     sync.RWMutex
	values map[string]any
	meta   map[string]stored
	subs   map[string][]func()
}

type stored struct {
	UpdatedAt time.Time
	UpdatedBy *uuid.UUID
}

// NewStore returns a store over pool. secretKey is the process's master key;
// secret settings are sealed under a key derived from it.
func NewStore(pool *pgxpool.Pool, secretKey []byte) (*Store, error) {
	box, err := secretbox.New(secretKey, "settings")
	if err != nil {
		return nil, err
	}
	return &Store{
		pool:   pool,
		q:      dbq.New(pool),
		box:    box,
		values: map[string]any{},
		meta:   map[string]stored{},
		subs:   map[string][]func(){},
	}, nil
}

// Load reads every stored value into the cache.
func (s *Store) Load(ctx context.Context) error {
	s.loadMu.Lock()
	defer s.loadMu.Unlock()
	rows, err := s.q.ListSystemSettings(ctx)
	if err != nil {
		return fmt.Errorf("load settings: %w", err)
	}
	values := make(map[string]any, len(rows))
	meta := make(map[string]stored, len(rows))
	for _, r := range rows {
		v, ok := s.decodeRow(ctx, r.Key, r.Value, r.Secret)
		if !ok {
			continue
		}
		values[r.Key] = v
		meta[r.Key] = stored{UpdatedAt: r.UpdatedAt, UpdatedBy: r.UpdatedBy}
	}

	s.mu.Lock()
	changed := changedKeys(s.values, values)
	s.values, s.meta = values, meta
	s.mu.Unlock()
	s.notify(changed)
	return nil
}

// Run keeps the cache current until ctx ends: it LISTENs for changes on a
// dedicated connection, reloads the changed key, and after any reconnect
// reloads everything (notifications sent while disconnected are lost).
func (s *Store) Run(ctx context.Context) error {
	const initialBackoff = time.Second
	backoff := initialBackoff
	for {
		start := time.Now()
		err := s.listen(ctx)
		if ctx.Err() != nil {
			return nil //nolint:nilerr // shutting down
		}
		slog.WarnContext(ctx, "settings listener lost, retrying", "err", err, "in", backoff)
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(backoff):
		}
		if time.Since(start) > time.Minute {
			backoff = initialBackoff // it was healthy for a while: retry fast
		}
		backoff = min(backoff*2, 30*time.Second)
	}
}

func (s *Store) listen(ctx context.Context) error {
	pc, err := s.pool.Acquire(ctx)
	if err != nil {
		return err
	}
	// A LISTENing connection must never go back to the pool: take it over.
	conn := pc.Hijack()
	defer func() {
		cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = conn.Close(cctx)
	}()
	if _, err := conn.Exec(ctx, "LISTEN "+notifyChannel); err != nil {
		return err
	}
	if err := s.Load(ctx); err != nil {
		return err
	}
	for {
		n, err := conn.WaitForNotification(ctx)
		if err != nil {
			return err
		}
		if err := s.reload(ctx, n.Payload); err != nil {
			slog.WarnContext(ctx, "settings reload failed", "key", n.Payload, "err", err)
		}
	}
}

// reload refreshes one key from the database.
func (s *Store) reload(ctx context.Context, key string) error {
	s.loadMu.Lock()
	defer s.loadMu.Unlock()
	if _, ok := Lookup(key); !ok {
		return nil
	}
	r, err := s.q.GetSystemSetting(ctx, key)
	if errors.Is(err, pgx.ErrNoRows) {
		s.apply(key, nil, false, stored{})
		return nil
	}
	if err != nil {
		return err
	}
	v, ok := s.decodeRow(ctx, r.Key, r.Value, r.Secret)
	s.apply(key, v, ok, stored{UpdatedAt: r.UpdatedAt, UpdatedBy: r.UpdatedBy})
	return nil
}

func (s *Store) apply(key string, v any, present bool, m stored) {
	s.mu.Lock()
	old, had := s.values[key]
	if present {
		s.values[key] = v
		s.meta[key] = m
	} else {
		delete(s.values, key)
		delete(s.meta, key)
	}
	s.mu.Unlock()
	if had != present || old != v {
		s.notify([]string{key})
	}
}

// decodeRow turns a stored row into a typed value. A row that no longer
// decodes (unknown key, tightened validation, wrong secret key) is logged and
// skipped, so the default applies instead of the process failing.
func (s *Store) decodeRow(ctx context.Context, key string, value, secret []byte) (any, bool) {
	def, ok := Lookup(key)
	if !ok {
		return nil, false
	}
	raw := json.RawMessage(value)
	if secret != nil {
		pt, err := s.box.Open(secret, []byte(key))
		if err != nil {
			slog.ErrorContext(ctx, "secret setting cannot be decrypted; using default (was the secret key rotated?)", "key", key)
			return nil, false
		}
		raw = pt
	}
	v, err := def.decode(raw)
	if err != nil {
		slog.ErrorContext(ctx, "stored setting is invalid; using default", "key", key, "err", err)
		return nil, false
	}
	return v, true
}

// Set validates and stores a JSON value for key, then re-reads the row into
// this process's cache (others follow via NOTIFY), so a concurrent newer
// write is never masked by this one. actor is the admin making the change.
func (s *Store) Set(ctx context.Context, key string, raw json.RawMessage, actor *uuid.UUID) error {
	def, ok := Lookup(key)
	if !ok {
		return fmt.Errorf("%w: %s", ErrUnknownKey, key)
	}
	v, err := def.decode(raw)
	if err != nil {
		return &ValidationError{Key: key, Err: err}
	}
	canonical, err := json.Marshal(v)
	if err != nil {
		return err
	}
	p := dbq.UpsertSystemSettingParams{Key: key, UpdatedBy: actor}
	if def.Secret {
		p.Secret = s.box.Seal(canonical, []byte(key))
	} else {
		p.Value = canonical
	}
	if err := s.q.UpsertSystemSetting(ctx, p); err != nil {
		return fmt.Errorf("store setting %s: %w", key, err)
	}
	return s.reload(ctx, key)
}

// Reset deletes the stored value, so the default applies again.
func (s *Store) Reset(ctx context.Context, key string) error {
	if _, ok := Lookup(key); !ok {
		return fmt.Errorf("%w: %s", ErrUnknownKey, key)
	}
	if err := s.q.DeleteSystemSetting(ctx, key); err != nil {
		return fmt.Errorf("reset setting %s: %w", key, err)
	}
	return s.reload(ctx, key)
}

// SetValue stores a typed value for setting (for code and tests; the admin
// API goes through Set with JSON).
func SetValue[T Value](ctx context.Context, s *Store, setting *Setting[T], v T, actor *uuid.UUID) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return s.Set(ctx, setting.Key(), raw, actor)
}

// OnChange registers fn to run whenever key's effective value changes in
// this process: on the listener goroutine, or on the caller of Set, Reset or
// Load. fn must not block or call back into the store's write methods.
func (s *Store) OnChange(key string, fn func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.subs[key] = append(s.subs[key], fn)
}

func (s *Store) notify(keys []string) {
	for _, k := range keys {
		s.mu.RLock()
		fns := append([]func(){}, s.subs[k]...)
		s.mu.RUnlock()
		for _, fn := range fns {
			fn()
		}
	}
}

func (s *Store) cached(key string) (any, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v, ok := s.values[key]
	return v, ok
}

// View is one setting as the admin panel lists it.
type View struct {
	*Definition
	// Value is the effective value; nil for secrets.
	Value any
	// IsSet reports a stored value (otherwise the default applies).
	IsSet     bool
	UpdatedAt time.Time
	UpdatedBy *uuid.UUID
}

// List returns every declared setting with its effective value.
func (s *Store) List() []View {
	s.mu.RLock()
	defer s.mu.RUnlock()
	defs := Definitions()
	out := make([]View, 0, len(defs))
	for _, d := range defs {
		v, set := s.values[d.Key]
		if !set {
			v = d.Default
		}
		if d.Secret {
			v = nil
		}
		m := s.meta[d.Key]
		out = append(out, View{Definition: d, Value: v, IsSet: set, UpdatedAt: m.UpdatedAt, UpdatedBy: m.UpdatedBy})
	}
	return out
}

// ValidationError is a value the setting rejects; the API maps it to 422.
type ValidationError struct {
	Key string
	Err error
}

func (e *ValidationError) Error() string { return e.Err.Error() }
func (e *ValidationError) Unwrap() error { return e.Err }

func changedKeys(old, cur map[string]any) []string {
	var out []string
	for k, v := range cur {
		if ov, ok := old[k]; !ok || ov != v {
			out = append(out, k)
		}
	}
	for k := range old {
		if _, ok := cur[k]; !ok {
			out = append(out, k)
		}
	}
	return out
}
