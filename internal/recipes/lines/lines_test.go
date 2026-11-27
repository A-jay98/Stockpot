package lines_test

import (
	"testing"

	"github.com/a-jay98/stockpot/internal/recipes/lines"
)

func TestParse(t *testing.T) {
	tests := []struct {
		raw                   string
		qty, qmax, unit, name string
		note                  string
		optional              bool
	}{
		{"2 tbsp tomato paste", "2", "", "tbsp", "tomato paste", "", false},
		{"1½ cups basmati rice, rinsed", "1.5", "", "cup", "basmati rice", "rinsed", false},
		{"1 (400 mL) can coconut milk", "1", "", "can", "coconut milk", "400 mL", false},
		{"2-3 cloves garlic, minced", "2", "3", "clove", "garlic", "minced", false},
		{"2 to 3 cloves garlic", "2", "3", "clove", "garlic", "", false},
		{"salt to taste", "", "", "", "salt", "to taste", true},
		{"800 g boneless chicken thighs", "800", "", "g", "boneless chicken thighs", "", false},
		{"800g chicken", "800", "", "g", "chicken", "", false},
		{"½ bunch cilantro", "0.5", "", "bunch", "cilantro", "", false},
		{"1 tsp ground turmeric", "1", "", "tsp", "ground turmeric", "", false},
		{"Juice of 1 lime", "1", "", "ea", "lime", "juice", false},
		{"1 cup yellow split peas (lappeh)", "1", "", "cup", "yellow split peas", "lappeh", false},
		{"2 onions, finely diced", "2", "", "ea", "onions", "finely diced", false},
		{"1/2 cup milk", "0.5", "", "cup", "milk", "", false},
		{"1 1/2 cups of flour", "1.5", "", "cup", "flour", "", false},
		{"⅓ cup sugar", "0.333", "", "cup", "sugar", "", false},
		{"1 T butter", "1", "", "tbsp", "butter", "", false},
		{"1 t salt", "1", "", "tsp", "salt", "", false},
		{"a pinch of saffron", "", "", "", "saffron", "pinch", false},
		{"fresh mint, for garnish", "", "", "", "fresh mint", "for garnish", true},
		{"1 lemon (optional)", "1", "", "ea", "lemon", "", true},
		{"۲ پیمانه برنج", "2", "", "ea", "پیمانه برنج", "", false},
		{"- 3 eggs", "3", "", "ea", "eggs", "", false},
		{"2 lb ground beef", "2", "", "lb", "ground beef", "", false},
		{"500 ml stock", "500", "", "ml", "stock", "", false},
		{"Rice", "", "", "", "Rice", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.raw, func(t *testing.T) {
			p := lines.Parse(tc.raw)
			if s(p.Qty) != tc.qty || s(p.QtyMax) != tc.qmax || s(p.Unit) != tc.unit || p.Name != tc.name || s(p.Note) != tc.note || p.Optional != tc.optional {
				t.Fatalf("got qty=%q max=%q unit=%q name=%q note=%q optional=%v",
					s(p.Qty), s(p.QtyMax), s(p.Unit), p.Name, s(p.Note), p.Optional)
			}
		})
	}
}

func TestAllGroupsUnderHeaders(t *testing.T) {
	ls := lines.All([]string{"2 cups flour", "", "For the rice:", "1 cup basmati rice", "Sauce:", "2 tbsp butter"})
	if len(ls) != 3 {
		t.Fatalf("got %d lines", len(ls))
	}
	if ls[0].Group != "" || ls[1].Group != "For the rice" || ls[2].Group != "Sauce" {
		t.Fatalf("groups: %q %q %q", ls[0].Group, ls[1].Group, ls[2].Group)
	}
	if ls[1].Raw != "1 cup basmati rice" || ls[1].Name != "basmati rice" {
		t.Fatalf("line: %+v", ls[1])
	}
}

func TestHeader(t *testing.T) {
	for raw, want := range map[string]bool{"For the rice:": true, "2 cups:": false, "salt": false, ":": false} {
		if _, ok := lines.Header(raw); ok != want {
			t.Errorf("Header(%q) = %v", raw, ok)
		}
	}
}

func s(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
