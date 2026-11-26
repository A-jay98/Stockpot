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
//
// Structured values (lists, objects) use DefineJSON. A family (DefineFamily)
// declares many keys at once under one prefix, one per id, e.g. a secret
// "llm.api_key.<provider>" for every configured provider.
package settings

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"sync"
)

// Kind is a setting's value type, as the admin editor shows it.
type Kind string

// Supported kinds.
const (
	KindBool   Kind = "bool"
	KindInt    Kind = "int"
	KindString Kind = "string"
	KindJSON   Kind = "json" // any JSON document, decoded into a Go struct or slice
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
	// Family is the prefix of the family this key belongs to, or "".
	Family string

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

// Lookup returns the stored value, and false when none is stored (the
// default applies).
func (s *Setting[T]) Lookup(store *Store) (T, bool) {
	if v, ok := store.cached(s.def.Key); ok {
		return v.(T), true //nolint:forcetypeassert // the cache only holds decoded T
	}
	var zero T
	return zero, false
}

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
	defs     map[string]*Definition
	families map[string]func(id string) (*Definition, bool) // by prefix
}{defs: map[string]*Definition{}, families: map[string]func(string) (*Definition, bool){}}

// Define registers a setting. It panics on a duplicate key or an invalid
// definition: both are programming errors caught by any test run.
func Define[T Value](d Def[T]) *Setting[T] {
	if !keyPattern.MatchString(d.Key) {
		panic(fmt.Sprintf("settings: invalid key %q", d.Key))
	}
	def := newDefinition(d)
	register(def)
	return &Setting[T]{def: def}
}

func newDefinition[T Value](d Def[T]) *Definition {
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
	return def
}

func register(def *Definition) {
	registry.Lock()
	defer registry.Unlock()
	if _, dup := registry.defs[def.Key]; dup {
		panic(fmt.Sprintf("settings: %s defined twice", def.Key))
	}
	if _, dup := registry.families[def.Key]; dup {
		panic(fmt.Sprintf("settings: %s is already a family prefix", def.Key))
	}
	if i := strings.LastIndexByte(def.Key, '.'); i > 0 {
		if _, clash := registry.families[def.Key[:i]]; clash {
			panic(fmt.Sprintf("settings: %s collides with family %s", def.Key, def.Key[:i]))
		}
	}
	registry.defs[def.Key] = def
}

// JSONDef declares a setting holding a structured value of type T (a struct
// or slice), stored as JSON. Unknown fields are rejected.
type JSONDef[T any] struct {
	Key         string
	Default     T
	Description string
	Scope       Scope
	// Validate rejects bad values. It runs on the default at Define time.
	Validate func(T) error
}

// JSON is a typed handle to a structured setting.
type JSON[T any] struct {
	def *Definition
}

// DefineJSON registers a structured setting; it panics like Define.
func DefineJSON[T any](d JSONDef[T]) *JSON[T] {
	if !keyPattern.MatchString(d.Key) {
		panic(fmt.Sprintf("settings: invalid key %q", d.Key))
	}
	if d.Scope == "" {
		d.Scope = ScopeSystem
	}
	if d.Validate != nil {
		if err := d.Validate(d.Default); err != nil {
			panic(fmt.Sprintf("settings: default of %s: %v", d.Key, err))
		}
	}
	def := &Definition{
		Key:         d.Key,
		Kind:        KindJSON,
		Default:     &d.Default,
		Description: d.Description,
		Scope:       d.Scope,
		// The cache holds a *T: pointers compare cheaply, so change
		// detection works for values (slices, maps) that aren't comparable.
		decode: func(raw json.RawMessage) (any, error) {
			dec := json.NewDecoder(bytes.NewReader(raw))
			dec.DisallowUnknownFields()
			v := new(T)
			if err := dec.Decode(v); err != nil {
				return nil, fmt.Errorf("%s: %w", d.Key, err)
			}
			if dec.More() {
				return nil, fmt.Errorf("%s: trailing data after the JSON value", d.Key)
			}
			if d.Validate != nil {
				if err := d.Validate(*v); err != nil {
					return nil, fmt.Errorf("%s: %w", d.Key, err)
				}
			}
			return v, nil
		},
	}
	register(def)
	return &JSON[T]{def: def}
}

// Key is the setting's name.
func (s *JSON[T]) Key() string { return s.def.Key }

// Get returns the live value. It is shared with other readers: treat it as
// read-only (copy a slice before changing it).
func (s *JSON[T]) Get(store *Store) T {
	if v, ok := store.cached(s.def.Key); ok {
		return *v.(*T) //nolint:forcetypeassert // the cache only holds decoded *T
	}
	return *s.def.Default.(*T) //nolint:forcetypeassert // set by DefineJSON
}

// Family is a set of settings sharing a prefix and a definition, one per id:
// prefix "llm.api_key" declares "llm.api_key.gemini", "llm.api_key.groq", …
type Family[T Value] struct {
	tmpl Def[T]
	mu   sync.Mutex
	defs map[string]*Definition
}

var idPattern = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// ValidID reports whether id can name a family member (one key segment).
func ValidID(id string) bool { return idPattern.MatchString(id) }

// DefineFamily registers a family whose Key is the prefix. It panics like
// Define. Members' defaults and validation come from d.
func DefineFamily[T Value](d Def[T]) *Family[T] {
	if !keyPattern.MatchString(d.Key) {
		panic(fmt.Sprintf("settings: invalid family prefix %q", d.Key))
	}
	newDefinition(d) // validates the default
	f := &Family[T]{tmpl: d, defs: map[string]*Definition{}}
	registry.Lock()
	defer registry.Unlock()
	if _, dup := registry.families[d.Key]; dup {
		panic(fmt.Sprintf("settings: family %s defined twice", d.Key))
	}
	for key := range registry.defs {
		if key == d.Key || strings.HasPrefix(key, d.Key+".") {
			panic(fmt.Sprintf("settings: family %s collides with %s", d.Key, key))
		}
	}
	registry.families[d.Key] = f.member
	return f
}

// Prefix is the family's key prefix.
func (f *Family[T]) Prefix() string { return f.tmpl.Key }

// Member returns the setting for id; ok is false when id isn't a valid key
// segment.
func (f *Family[T]) Member(id string) (*Setting[T], bool) {
	def, ok := f.member(id)
	if !ok {
		return nil, false
	}
	return &Setting[T]{def: def}, true
}

func (f *Family[T]) member(id string) (*Definition, bool) {
	if !ValidID(id) {
		return nil, false
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if def, ok := f.defs[id]; ok {
		return def, true
	}
	d := f.tmpl
	d.Key = f.tmpl.Key + "." + id
	def := newDefinition(d)
	def.Family = f.tmpl.Key
	f.defs[id] = def
	return def, true
}

// Lookup returns the definition of key, which is a declared setting or a
// member of a declared family.
func Lookup(key string) (*Definition, bool) {
	registry.Lock()
	d, ok := registry.defs[key]
	var member func(string) (*Definition, bool)
	if i := strings.LastIndexByte(key, '.'); !ok && i > 0 {
		member = registry.families[key[:i]]
		key = key[i+1:]
	}
	registry.Unlock()
	if member != nil {
		return member(key)
	}
	return d, ok
}

// Definitions returns every declared setting (not family members), sorted
// by key.
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
