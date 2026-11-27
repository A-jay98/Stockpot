package resolve_test

import (
	"os"
	"testing"

	"github.com/google/uuid"

	"github.com/a-jay98/stockpot/internal/catalog"
	"github.com/a-jay98/stockpot/internal/catalog/resolve"
	"github.com/a-jay98/stockpot/internal/testdb"
)

func TestMain(m *testing.M) { os.Exit(testdb.Main(m)) }

func TestResolve(t *testing.T) {
	pool := testdb.New(t)
	ctx := t.Context()
	if err := catalog.Seed(ctx, pool); err != nil {
		t.Fatal(err)
	}
	hh := uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO household (id, name) VALUES ($1, 'Home')`, hh); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO store (id, household_id, name) VALUES (gen_random_uuid(), $1, 'Bulk Barn')`, hh); err != nil {
		t.Fatal(err)
	}

	str := func(s string) *string { return &s }
	tests := []struct {
		name     string
		brand    *string
		slug     string // "" = no match
		minConf  float32
		maxConf  float32
		hint     string
		category string
		xbrand   string
	}{
		{name: "good mandarins", slug: "mandarin", minConf: 1, maxConf: 1},
		{name: "Kirkland mandarins 2lb", slug: "mandarin", minConf: 1, maxConf: 1},
		{name: "mandarins", brand: str("Kirkland"), slug: "mandarin", minConf: 1, maxConf: 1},
		{name: "manderins", slug: "mandarin", minConf: 1, maxConf: 1}, // alias
		{name: "X brand avocado", slug: "avocado", minConf: 1, maxConf: 1, xbrand: "X"},
		{name: "Avocados 4 pack", slug: "avocado", minConf: 1, maxConf: 1},
		{name: "frozen peas", slug: "peas", minConf: 1, maxConf: 1, hint: "freezer"},
		{name: "baby spinach", slug: "spinach", minConf: 1, maxConf: 1},
		{name: "chicken thighs boneless", slug: "chicken-thigh", minConf: 0.9, maxConf: 0.9},
		{name: "2% milk", slug: "milk", minConf: 1, maxConf: 1},
		{name: "President's Choice greek yogurt 750g", slug: "greek-yogurt", minConf: 1, maxConf: 1},
		{name: "greek yogurt", slug: "greek-yogurt", minConf: 1, maxConf: 1},
		{name: "paper towels", slug: "paper-towels", minConf: 1, maxConf: 1, category: "household"},
		{name: "Bulk Barn tomatoes 1.5kg", slug: "tomato", minConf: 1, maxConf: 1},
		{name: "Dempster's bagels x6", slug: "bagel", minConf: 0.9, maxConf: 0.9},
		// Misspelling that is not an alias: trigram guess, below auto-accept.
		{name: "avocadoo", slug: "avocado", minConf: resolve.MinSimilarity, maxConf: resolve.AutoAccept},
		// A broad ingredient with leftover words is only a weak guess.
		{name: "zorbo fruit", slug: "fruit", minConf: resolve.ConfBroad, maxConf: resolve.ConfBroad},
		// Unknown words resolve to nothing: they wait for the enricher.
		{name: "zxqv plugh", slug: ""},
		{name: "   ", slug: ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m, err := resolve.Resolver{}.Resolve(ctx, pool, hh, tc.name, tc.brand)
			if err != nil {
				t.Fatal(err)
			}
			if tc.slug == "" {
				if m != nil {
					t.Fatalf("matched %s (%.2f), want nothing", m.Slug, m.Confidence)
				}
				return
			}
			if m == nil {
				t.Fatalf("no match, want %s", tc.slug)
			}
			if m.Slug != tc.slug {
				t.Errorf("slug = %s, want %s", m.Slug, tc.slug)
			}
			if m.Confidence < tc.minConf || m.Confidence > tc.maxConf {
				t.Errorf("confidence = %.2f, want %.2f..%.2f", m.Confidence, tc.minConf, tc.maxConf)
			}
			if m.LocationHint != tc.hint {
				t.Errorf("hint = %q, want %q", m.LocationHint, tc.hint)
			}
			if tc.category != "" && m.CategorySlug != tc.category {
				t.Errorf("category = %q, want %q", m.CategorySlug, tc.category)
			}
			if m.ExtractedBrand != tc.xbrand {
				t.Errorf("brand = %q, want %q", m.ExtractedBrand, tc.xbrand)
			}
		})
	}
}

func TestHouseholdIngredientWinsOverGlobal(t *testing.T) {
	pool := testdb.New(t)
	ctx := t.Context()
	if err := catalog.Seed(ctx, pool); err != nil {
		t.Fatal(err)
	}
	hh := uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO household (id, name) VALUES ($1, 'Home')`, hh); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO ingredient (household_id, slug, name, aliases, source) VALUES ($1, 'my-mandarin', 'Mandarin', '{}', 'household')`, hh); err != nil {
		t.Fatal(err)
	}
	m, err := resolve.Resolver{}.Resolve(ctx, pool, hh, "mandarins", nil)
	if err != nil || m == nil || m.Slug != "my-mandarin" {
		t.Fatalf("got %+v, %v; want the household ingredient", m, err)
	}
	other := uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO household (id, name) VALUES ($1, 'Other')`, other); err != nil {
		t.Fatal(err)
	}
	if m, err = (resolve.Resolver{}).Resolve(ctx, pool, other, "mandarins", nil); err != nil || m == nil || m.Slug != "mandarin" {
		t.Fatalf("other household got %+v, %v; want the global one", m, err)
	}
}

func TestCandidates(t *testing.T) {
	pool := testdb.New(t)
	ctx := t.Context()
	if err := catalog.Seed(ctx, pool); err != nil {
		t.Fatal(err)
	}
	cs, err := resolve.Candidates(ctx, pool, uuid.New(), "Fresh sweet mandarines 3lb", nil, 8)
	if err != nil || len(cs) == 0 || cs[0].Slug != "mandarin" || len(cs) > 8 {
		t.Fatalf("candidates = %+v, %v", cs, err)
	}
}

func TestParse(t *testing.T) {
	tests := []struct {
		in    string
		brand string
		want  []string // non-descriptor singular tokens
		hint  string
	}{
		{"Good Mandarins", "", []string{"mandarin"}, ""},
		{"Frozen Peas 750 g", "", []string{"pea"}, "freezer"},
		{"Tomatoes, 1.5kg", "", []string{"tomato"}, ""},
		{"Berries pack of 12", "", []string{"berry"}, ""},
		{"Cherries x6", "", []string{"cherry"}, ""},
		{"Leaves", "", []string{"leaf"}, ""},
		{"Hummus", "", []string{"hummus"}, ""},
		{"Acme basil", "Acme", []string{"basil"}, ""},
		{"Canned chickpeas", "", []string{"chickpea"}, "pantry"},
	}
	for _, tc := range tests {
		var b *string
		if tc.brand != "" {
			b = &tc.brand
		}
		p := resolve.Parse(tc.in, b, nil)
		var got []string
		for _, tk := range p.Tokens {
			if !tk.Descriptor {
				got = append(got, tk.Sing)
			}
		}
		if len(got) != len(tc.want) || (len(got) > 0 && got[0] != tc.want[0]) || p.LocationHint != tc.hint {
			t.Errorf("Parse(%q) = %v hint %q, want %v hint %q", tc.in, got, p.LocationHint, tc.want, tc.hint)
		}
	}
}
