// Package lines parses recipe ingredient lines ("1½ cups basmati rice,
// rinsed") into quantity, unit, name and note. It is pure and deterministic;
// matching the name to an ingredient is the caller's job (recipes.WriteVersion).
package lines

import (
	"math/big"
	"regexp"
	"strings"
	"unicode"

	"github.com/google/uuid"
	"golang.org/x/text/unicode/norm"
)

// Line is one structured ingredient line, as stored in recipe_version.ingredients.
type Line struct {
	LineID          uuid.UUID  `json:"line_id"`
	Group           string     `json:"group,omitempty"`
	Raw             string     `json:"raw"`
	Qty             *string    `json:"qty,omitempty"`
	QtyMax          *string    `json:"qty_max,omitempty"`
	Unit            *string    `json:"unit,omitempty"`
	Name            string     `json:"name"`
	Note            *string    `json:"note,omitempty"`
	Optional        bool       `json:"optional"`
	Staple          bool       `json:"staple"`
	IngredientID    *uuid.UUID `json:"ingredient_id,omitempty"`
	MatchConfidence *float32   `json:"match_confidence,omitempty"`
}

// Parsed is the deterministic part of a line, before matching.
type Parsed struct {
	Qty, QtyMax, Unit *string
	Name              string
	Note              *string
	Optional          bool
}

// Header reports whether raw is a section header ("For the rice:") and
// returns the group name without the trailing colon.
func Header(raw string) (string, bool) {
	s := strings.TrimSpace(normalize(raw))
	if !strings.HasSuffix(s, ":") || len(s) < 2 || len(s) > 80 {
		return "", false
	}
	s = strings.TrimSpace(strings.TrimSuffix(s, ":"))
	if s == "" || qtyStart.MatchString(s) {
		return "", false
	}
	return s, true
}

// All parses a recipe's raw lines in order: headers become the group of the
// lines after them, blank lines are dropped. Line ids are left zero.
func All(raws []string) []Line {
	var out []Line
	group := ""
	for _, raw := range raws {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		if g, ok := Header(raw); ok {
			group = g
			continue
		}
		p := Parse(raw)
		out = append(out, Line{
			Group: group, Raw: raw, Qty: p.Qty, QtyMax: p.QtyMax, Unit: p.Unit,
			Name: p.Name, Note: p.Note, Optional: p.Optional,
		})
	}
	return out
}

// Parse parses one ingredient line.
func Parse(raw string) Parsed {
	s := strings.Join(strings.Fields(normalize(raw)), " ")
	var p Parsed
	var notes []string

	// Bullets and list markers.
	s = strings.TrimLeft(s, "-*•·– ")

	// Parentheses holding an optional marker ("(or to taste)") go as a whole.
	s = markerParen.ReplaceAllStringFunc(s, func(g string) string {
		for _, m := range optionalMarkers {
			if m.re.MatchString(g) {
				p.Optional = true
				if m.note != "" {
					notes = append(notes, m.note)
				}
				return " "
			}
		}
		return g
	})
	s = strings.Join(strings.Fields(s), " ")

	// Optional markers anywhere in the line.
	for _, m := range optionalMarkers {
		if loc := m.re.FindStringIndex(s); loc != nil {
			p.Optional = true
			s = strings.TrimSpace(s[:loc[0]] + " " + s[loc[1]:])
			if m.note != "" {
				notes = append(notes, m.note)
			}
		}
	}

	// "Juice of 1 lime", "zest of 2 oranges": the part used is a note.
	if m := partOf.FindStringSubmatch(s); m != nil {
		notes = append([]string{strings.ToLower(m[1])}, notes...)
		s = m[2]
	}

	// Leading quantity or range.
	if m := qtyStart.FindStringSubmatch(s); m != nil && !digitNext(s[len(m[0]):]) {
		if q, ok := parseNumber(m[1]); ok {
			p.Qty = &q
			if m[2] != "" {
				if hi, ok := parseNumber(m[2]); ok {
					p.QtyMax = &hi
				}
			}
			s = strings.TrimSpace(s[len(m[0]):])
		}
	} else if m := article.FindStringSubmatch(s); m != nil {
		// "a pinch of salt", "an onion"
		one := "1"
		p.Qty = &one
		s = strings.TrimSpace(s[len(m[0]):])
	}

	// Parenthetical size right after the quantity: "1 (400 mL) can", or a
	// multiplied one: "2 x 400g cans".
	if p.Qty != nil {
		if m := timesSize.FindStringSubmatch(s); m != nil {
			notes = append(notes, m[1])
			s = strings.TrimSpace(s[len(m[0]):])
		} else if m := leadingParen.FindStringSubmatch(s); m != nil {
			notes = append(notes, strings.TrimSpace(m[1]))
			s = strings.TrimSpace(s[len(m[0]):])
		}
	}

	// Unit word.
	if p.Qty != nil {
		word, rest := firstWord(s)
		if code, ok := unitOf(word); ok {
			s = rest
			switch code {
			case "":
				// Unitless measure (pinch, dash): keep it as a note.
				notes = append([]string{strings.ToLower(word)}, notes...)
				p.Qty, p.QtyMax = nil, nil
			default:
				p.Unit = &code
			}
		} else if s != "" {
			ea := "ea"
			p.Unit = &ea
		}
		s = strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(s, "of "), "Of "))
	}

	// Trailing note after a comma, and any parentheses left.
	if i := strings.Index(s, ","); i >= 0 {
		if n := strings.TrimSpace(s[i+1:]); n != "" {
			notes = append(notes, n)
		}
		s = s[:i]
	}
	for {
		loc := anyParen.FindStringSubmatchIndex(s)
		if loc == nil {
			break
		}
		if n := strings.TrimSpace(s[loc[2]:loc[3]]); n != "" {
			notes = append(notes, n)
		}
		s = s[:loc[0]] + " " + s[loc[1]:]
	}
	p.Name = strings.Trim(strings.Join(strings.Fields(s), " "), " .;:-")
	if len(notes) > 0 {
		n := strings.Join(notes, ", ")
		p.Note = &n
	}
	return p
}

type marker struct {
	re   *regexp.Regexp
	note string
}

var optionalMarkers = []marker{
	{regexp.MustCompile(`(?i)[,(]?\s*\(?\boptional\b\)?`), ""},
	{regexp.MustCompile(`(?i)[,(]?\s*\(?\bto taste\b\)?`), "to taste"},
	{regexp.MustCompile(`(?i)[,(]?\s*\(?\bfor garnish(?:ing)?\b\)?`), "for garnish"},
	{regexp.MustCompile(`(?i)[,(]?\s*\(?\bfor serving\b\)?`), "for serving"},
}

// number is a fraction, a decimal, or a whole number and a fraction
// ("1 1/2"); fractions come first because alternation is leftmost-first.
// A comma with one or two digits after it is a decimal comma ("1,5 kg").
const number = `\d+/\d+|\d+(?:[.,]\d{1,2}\b|\.\d+)?(?: \d+/\d+)?`

var (
	qtyStart     = regexp.MustCompile(`^(` + number + `)(?:\s*(?:-|to)\s*(` + number + `))?`)
	article      = regexp.MustCompile(`(?i)^an?\s+`)
	partOf       = regexp.MustCompile(`(?i)^(juice|zest|zest and juice|juice and zest)\s+of\s+(.+)$`)
	leadingParen = regexp.MustCompile(`^\(([^)]*)\)\s*`)
	timesSize    = regexp.MustCompile(`^[x×]\s*(\d+(?:\.\d+)?\s*[a-zA-Z]+)\s+`)
	markerParen  = regexp.MustCompile(`\([^)]*\)`)
	anyParen     = regexp.MustCompile(`\(([^)]*)\)`)
)

// firstWord splits off the first word, keeping "800g" style attached units:
// qtyStart leaves the unit letters at the start of s. A word not followed by
// whitespace or the end ("t-bone") is no unit, so it comes back empty.
func firstWord(s string) (word, rest string) {
	i := strings.IndexFunc(s, func(r rune) bool { return !unicode.IsLetter(r) && r != '.' })
	if i < 0 {
		return s, ""
	}
	if !unicode.IsSpace(rune(s[i])) {
		return "", s
	}
	return s[:i], strings.TrimSpace(s[i:])
}

// units maps spelled units to unit-table codes ("" = a measure with no unit
// code, kept as a note). Keys are lowercase except T/t, which differ by case.
var units = func() map[string]string {
	m := map[string]string{"T": "tbsp", "t": "tsp"}
	add := func(code string, words ...string) {
		for _, w := range words {
			m[w] = code
		}
	}
	add("tbsp", "tbsp", "tbsp.", "tbs", "tbl", "tablespoon", "tablespoons")
	add("tsp", "tsp", "tsp.", "teaspoon", "teaspoons")
	add("cup", "cup", "cups", "c", "c.")
	add("g", "g", "gr", "gram", "grams", "gramme", "grammes")
	add("kg", "kg", "kilo", "kilos", "kilogram", "kilograms")
	add("ml", "ml", "millilitre", "millilitres", "milliliter", "milliliters")
	add("l", "l", "litre", "litres", "liter", "liters")
	add("oz", "oz", "oz.", "ounce", "ounces")
	add("fl_oz", "floz")
	add("lb", "lb", "lbs", "lb.", "pound", "pounds")
	add("clove", "clove", "cloves")
	add("can", "can", "cans", "tin", "tins")
	add("bunch", "bunch", "bunches")
	add("slice", "slice", "slices")
	add("pack", "package", "packages", "pkg", "packet", "packets", "pack", "packs")
	add("jar", "jar", "jars")
	add("bottle", "bottle", "bottles")
	add("box", "box", "boxes")
	add("bag", "bag", "bags")
	add("head", "head", "heads")
	add("", "pinch", "pinches", "dash", "dashes", "handful", "handfuls", "sprig", "sprigs")
	return m
}()

func unitOf(word string) (string, bool) {
	if word == "" {
		return "", false
	}
	if c, ok := units[word]; ok {
		return c, true
	}
	c, ok := units[strings.ToLower(word)]
	return c, ok
}

// digitNext reports whether a quantity match was cut short ("12x", "1/").
func digitNext(rest string) bool {
	return rest != "" && (rest[0] >= '0' && rest[0] <= '9' || rest[0] == '/' || rest[0] == '.')
}

// fractions are replaced before NFKC, which would glue "1½" into "11⁄2".
var fractions = strings.NewReplacer(
	"½", " 1/2", "¼", " 1/4", "¾", " 3/4", "⅓", " 1/3", "⅔", " 2/3",
	"⅛", " 1/8", "⅜", " 3/8", "⅝", " 5/8", "⅞", " 7/8", "⅕", " 1/5", "⅙", " 1/6",
	"–", "-", "—", "-",
)

// normalize maps fractions, NFKC-folds, and turns Persian/Arabic digits and
// decimal separators into ASCII.
func normalize(s string) string {
	s = norm.NFKC.String(fractions.Replace(s))
	s = strings.ReplaceAll(s, "⁄", "/")
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= '۰' && r <= '۹':
			b.WriteRune('0' + (r - '۰'))
		case r >= '٠' && r <= '٩':
			b.WriteRune('0' + (r - '٠'))
		case r == '٫':
			b.WriteRune('.')
		default:
			b.WriteRune(r)
		}
	}
	return strings.TrimSpace(b.String())
}

// parseNumber turns "1 1/2", "1/2", "0.5" into a decimal string ("1.5"),
// rounded to 3 places for thirds.
func parseNumber(s string) (string, bool) {
	total := new(big.Rat)
	for _, part := range strings.Fields(strings.ReplaceAll(s, ",", ".")) {
		r, ok := new(big.Rat).SetString(part)
		if !ok {
			return "", false
		}
		total.Add(total, r)
	}
	if total.Sign() <= 0 {
		return "", false
	}
	out := total.FloatString(3)
	out = strings.TrimRight(strings.TrimRight(out, "0"), ".")
	return out, true
}

// Raws is the inverse of All: the raw lines in order, with a section header
// ("For the rice:") before each change of group, so All(Raws(ls)) keeps
// every line's group and text.
func Raws(ls []Line) []string {
	out := make([]string, 0, len(ls))
	group := ""
	for _, l := range ls {
		if l.Group != group {
			group = l.Group
			if group != "" {
				out = append(out, group+":")
			}
		}
		out = append(out, l.Raw)
	}
	return out
}
