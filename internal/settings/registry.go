// Package settings holds runtime configuration: typed keys declared in Go,
// values stored in Postgres (table setting), edited from the admin panel and
// cached in memory. Every process reloads a key when Postgres announces a
// change (LISTEN setting_changed), so edits apply live without a restart.
//
// Declare a setting once, at package level:
//
//	var RetentionAppLogDays = settings.Define(settings.Def[int]{
//		Key: "retention.app_log_days", Default: 14, Description: "…",
//		Validate: settings.Between(1, 3650),
//	})
//
// and read it with RetentionAppLogDays.Get(store). Boolean feature flags are
// settings under "flag.*".
package settings

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"sync"
)

// Kind is a setting's value type, as the admin editor shows it.
type Kind string

// Supported kinds.
const (
	KindBool   Kind = "bool"
	KindInt    Kind = "int"
	KindString Kind = "string"
)

// Scope says who a value belongs to.
type Scope string

// Scopes. Only ScopeSystem is stored today; household overrides come later.
const (
	ScopeSystem    Scope = "system"
	ScopeHousehold Scope = "household"
)

// Value is the set of Go types a setting can hold.
type Value interface{ ~bool | ~int | ~string }

// Def declares a setting of type T.
type Def[T Value] struct {
	Key         string // dotted lower_snake segments, e.g. "retention.app_log_days"
	Default     T
	Description string
	// Secret values are encrypted at rest and never returned by the API.
	Secret bool
	Scope  Scope // default ScopeSystem
	// Options, when set, are the only accepted values (an enum).
	Options []T
	// Validate rejects bad values. It runs on the default at Define time.
	Validate func(T) error
}

// Definition is the type-erased form of a Def, for listing and editing.
type Definition struct {
	Key         string
	Kind        Kind
	Default     any
	Description string
	Secret      bool
	Scope       Scope
	Options     []any

	// decode parses and validates a JSON value into the setting's Go type.
	decode func(json.RawMessage) (any, error)
}

// Decode parses and validates a JSON-encoded value for this setting.
func (d *Definition) Decode(raw json.RawMessage) (any, error) { return d.decode(raw) }

// Setting is a typed handle to a declared key.
type Setting[T Value] struct {
	def *Definition
}

// Key is the setting's name.
func (s *Setting[T]) Key() string { return s.def.Key }

// Default is the value used while none is stored.
func (s *Setting[T]) Default() T { return s.def.Default.(T) } //nolint:forcetypeassert // set by Define

// Get returns the live value from store's cache.
func (s *Setting[T]) Get(store *Store) T {
	if v, ok := store.cached(s.def.Key); ok {
		return v.(T) //nolint:forcetypeassert // the cache only holds decoded T
	}
	return s.Default()
}

var keyPattern = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)+$`)

var registry = struct {
	sync.Mutex
	defs map[string]*Definition
}{defs: map[string]*Definition{}}

// Define registers a setting. It panics on a duplicate key or an invalid
// definition: both are programming errors caught by any test run.
func Define[T Value](d Def[T]) *Setting[T] {
	if !keyPattern.MatchString(d.Key) {
		panic(fmt.Sprintf("settings: invalid key %q", d.Key))
	}
	if d.Scope == "" {
		d.Scope = ScopeSystem
	}
	check := func(v T) error {
		if len(d.Options) > 0 && !slices.Contains(d.Options, v) {
			return fmt.Errorf("must be one of %v", d.Options)
		}
		if d.Validate != nil {
			return d.Validate(v)
		}
		return nil
	}
	if err := check(d.Default); err != nil {
		panic(fmt.Sprintf("settings: default of %s: %v", d.Key, err))
	}
	def := &Definition{
		Key:         d.Key,
		Kind:        kindOf[T](),
		Default:     d.Default,
		Description: d.Description,
		Secret:      d.Secret,
		Scope:       d.Scope,
		decode: func(raw json.RawMessage) (any, error) {
			var v T
			if err := json.Unmarshal(raw, &v); err != nil {
				return nil, fmt.Errorf("%s: want a %s: %w", d.Key, kindOf[T](), err)
			}
			if err := check(v); err != nil {
				return nil, fmt.Errorf("%s: %w", d.Key, err)
			}
			return v, nil
		},
	}
	for _, o := range d.Options {
		def.Options = append(def.Options, o)
	}

	registry.Lock()
	defer registry.Unlock()
	if _, dup := registry.defs[d.Key]; dup {
		panic(fmt.Sprintf("settings: %s defined twice", d.Key))
	}
	registry.defs[d.Key] = def
	return &Setting[T]{def: def}
}

// Lookup returns the definition of key.
func Lookup(key string) (*Definition, bool) {
	registry.Lock()
	defer registry.Unlock()
	d, ok := registry.defs[key]
	return d, ok
}

// Definitions returns every declared setting, sorted by key.
func Definitions() []*Definition {
	registry.Lock()
	defer registry.Unlock()
	out := make([]*Definition, 0, len(registry.defs))
	for _, d := range registry.defs {
		out = append(out, d)
	}
	slices.SortFunc(out, func(a, b *Definition) int {
		switch {
		case a.Key < b.Key:
			return -1
		case a.Key > b.Key:
			return 1
		}
		return 0
	})
	return out
}

func kindOf[T Value]() Kind {
	var zero T
	switch any(zero).(type) {
	case bool:
		return KindBool
	case int:
		return KindInt
	default:
		return KindString
	}
}

// Between validates an int in [lo, hi].
func Between(lo, hi int) func(int) error {
	return func(v int) error {
		if v < lo || v > hi {
			return fmt.Errorf("must be between %d and %d", lo, hi)
		}
		return nil
	}
}
