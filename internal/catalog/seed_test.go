package catalog_test

import (
	"regexp"
	"testing"

	"github.com/a-jay98/stockpot/internal/catalog"
)

var seedCategories = map[string]bool{
	"produce-fruit": true, "produce-veg": true, "herbs": true, "dairy-eggs": true, "meat": true, "seafood": true,
	"deli": true, "bakery": true, "pantry": true, "baking": true, "spices-condiments": true, "oils-sauces": true,
	"frozen": true, "snacks-sweets": true, "drinks": true, "household": true, "personal-care": true, "other": true,
}

// The seed file is data; these checks catch typos before the migrate step does.
func TestSeedFileIsConsistent(t *testing.T) {
	items, err := catalog.SeedIngredients()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) < 400 {
		t.Fatalf("only %d ingredients", len(items))
	}
	slug := regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)
	bySlug := map[string]catalog.SeedIngredient{}
	names := map[string]string{}
	for _, it := range items {
		if !slug.MatchString(it.Slug) || it.Name == "" || it.Plural == "" {
			t.Errorf("bad entry %+v", it)
		}
		if _, dup := bySlug[it.Slug]; dup {
			t.Errorf("duplicate slug %s", it.Slug)
		}
		bySlug[it.Slug] = it
		if !seedCategories[it.Category] {
			t.Errorf("%s: unknown category %q", it.Slug, it.Category)
		}
		// A name or alias must point at one ingredient, or the exact resolver is ambiguous.
		for _, n := range append([]string{catalog.Normalize(it.Name), catalog.Normalize(it.Plural)}, aliasesNormalized(it.Aliases)...) {
			if other, ok := names[n]; ok && other != it.Slug {
				t.Errorf("%q names both %s and %s", n, other, it.Slug)
			}
			names[n] = it.Slug
		}
	}
	for _, it := range items {
		seen := map[string]bool{}
		for p := it.Parent; p != nil; p = bySlug[*p].Parent {
			if _, ok := bySlug[*p]; !ok {
				t.Errorf("%s: unknown parent %s", it.Slug, *p)
				break
			}
			if seen[*p] || *p == it.Slug {
				t.Errorf("%s: parent cycle", it.Slug)
				break
			}
			seen[*p] = true
		}
	}
	for _, want := range []string{"mandarin", "avocado", "citrus", "berries", "leafy-greens", "poultry", "chicken-thigh", "greek-yogurt"} {
		if _, ok := bySlug[want]; !ok {
			t.Errorf("seed lacks %s", want)
		}
	}
}

func aliasesNormalized(in []string) []string {
	out := make([]string, len(in))
	for i, a := range in {
		out[i] = catalog.Normalize(a)
	}
	return out
}
