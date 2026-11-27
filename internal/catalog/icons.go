// Package catalog holds the shared grocery catalog: ingredients, categories and the food icon registry.
package catalog

import (
	"encoding/json"
	"fmt"
	"sync"

	_ "embed"
)

// icons.json is a copy of the web registry (web/src/ui/food-icons/registry.json), refreshed
// by `go generate` and checked by CI via `make gen`. Seeds and the LLM enricher may only use
// its keys.
//
//go:generate cp ../../web/src/ui/food-icons/registry.json icons.json
//go:embed icons.json
var iconsJSON []byte

// IconEntry is one registry glyph: set is "fe" (Fluent Emoji) or "tb" (Tabler).
type IconEntry struct {
	Set   string   `json:"set"`
	Name  string   `json:"name"`
	Label string   `json:"label"`
	Tags  []string `json:"tags"`
}

// FallbackIcon is used when nothing in product → ingredient → ancestors → category resolves.
const FallbackIcon = "basket"

var (
	iconsOnce sync.Once
	icons     map[string]IconEntry
	iconsErr  error
)

func loadIcons() {
	iconsOnce.Do(func() {
		if err := json.Unmarshal(iconsJSON, &icons); err != nil {
			iconsErr = fmt.Errorf("parse icons.json: %w", err)
		}
	})
}

// IconExists reports whether key is a registry icon key.
func IconExists(key string) bool {
	loadIcons()
	_, ok := icons[key]
	return ok
}

// IconKeys returns the registry keys (unordered) for prompts and validation.
func IconKeys() []string {
	loadIcons()
	keys := make([]string, 0, len(icons))
	for k := range icons {
		keys = append(keys, k)
	}
	return keys
}

// CategoryIconKey is the registry key of a category's icon.
func CategoryIconKey(slug string) string { return "cat-" + slug }
