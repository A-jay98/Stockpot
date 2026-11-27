package catalog

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

var seededCategories = []string{
	"produce-fruit", "produce-veg", "herbs", "dairy-eggs", "meat", "seafood", "deli", "bakery",
	"pantry", "baking", "spices-condiments", "oils-sauces", "frozen", "snacks-sweets", "drinks",
	"household", "personal-care", "other",
}

func TestIconRegistry(t *testing.T) {
	loadIcons()
	if iconsErr != nil {
		t.Fatal(iconsErr)
	}
	if !IconExists(FallbackIcon) {
		t.Fatalf("fallback icon %q missing", FallbackIcon)
	}
	for _, slug := range seededCategories {
		if !IconExists(CategoryIconKey(slug)) {
			t.Errorf("category %q has no icon %q", slug, CategoryIconKey(slug))
		}
	}
}

// Every icon named by the ingredient seed must be in the registry. Active once m1-1's
// seed (seed/ingredients.json) is merged; skipped before that.
func TestSeedIconsExist(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("seed", "ingredients.json"))
	if os.IsNotExist(err) {
		t.Skip("ingredient seed not merged yet")
	}
	if err != nil {
		t.Fatal(err)
	}
	var list []struct {
		Slug string `json:"slug"`
		Icon string `json:"icon"`
	}
	if err := json.Unmarshal(raw, &list); err != nil {
		var wrapped struct {
			Ingredients []struct {
				Slug string `json:"slug"`
				Icon string `json:"icon"`
			} `json:"ingredients"`
		}
		if err2 := json.Unmarshal(raw, &wrapped); err2 != nil {
			t.Fatalf("parse seed: %v", err)
		}
		list = wrapped.Ingredients
	}
	for _, in := range list {
		if in.Icon != "" && !IconExists(in.Icon) {
			t.Errorf("ingredient %q uses unknown icon %q", in.Slug, in.Icon)
		}
	}
}
