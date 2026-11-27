// Package resolve links free text ("good mandarins", "Kirkland avocado 2 lb")
// to canonical ingredients. The text analysis is pure Go; the matching needs
// one query for exact names and one trigram query for misspellings. The same
// code serves products now and recipe lines in M4.
package resolve

import (
	"regexp"
	"strings"
	"unicode"
)

// Normalize lowercases, strips accents (common Latin ones; SQL does the
// rest), turns punctuation into spaces and collapses whitespace.
func Normalize(s string) string {
	var b strings.Builder
	space := true
	for _, r := range strings.ToLower(s) {
		switch {
		case r == '‌' || r == '‍': // zero-width (non-)joiners inside Persian words
			continue
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(foldAccent(r))
			space = false
		case !space:
			b.WriteByte(' ')
			space = true
		}
	}
	return strings.TrimSpace(b.String())
}

var accentFold = func() map[rune]rune {
	from, to := []rune("àáâãäåçèéêëìíîïñòóôõöùúûüýÿ"), []rune("aaaaaaceeeeiiiinooooouuuuyy")
	m := make(map[rune]rune, len(from))
	for i, r := range from {
		m[r] = to[i]
	}
	return m
}()

func foldAccent(r rune) rune {
	if f, ok := accentFold[r]; ok {
		return f
	}
	return r
}

// sqlNorm is Normalize in SQL, for columns stored as typed (aliases are stored normalized).
func sqlNorm(expr string) string {
	return "btrim(regexp_replace(immutable_unaccent(lower(" + expr + ")), '[^[:alnum:]]+', ' ', 'g'))"
}

// Quantity, size and package expressions, removed before normalizing (which
// would split "1.5kg" and "2%").
var quantityPatterns = func() []*regexp.Regexp {
	const num = `\d+(?:[.,]\d+)?`
	const unit = `(?:kg|g|gr|lbs?|oz|ml|l|cl|dl|litres?|liters?|fl\.?\s?oz|ct|pk|pack|packs|count|ea|each|dozen|doz|pc|pcs|piece|pieces)`
	exprs := []string{
		`\bpack\s+of\s+` + num + `\b`,
		`\b` + num + `\s*(?:x|×)\s*` + num + `\s*` + unit + `?\b`,
		`\b(?:x|×)\s*` + num + `\b`,
		`\b` + num + `\s*(?:x|×)(?:\s|$)`,
		`\b` + num + `\s*` + unit + `\b`,
		num + `\s*%`,
		`\b(?:dozen|doz)\b`,
	}
	out := make([]*regexp.Regexp, len(exprs))
	for i, e := range exprs {
		out[i] = regexp.MustCompile(`(?i)` + e)
	}
	return out
}()

// descriptors carry no ingredient identity: quality, size, freshness, package
// and marketing words. Frozen and friends are descriptors too, but also set a
// location hint (see locationHints).
var descriptors = setOf(
	"good", "great", "best", "fresh", "organic", "natural", "large", "small", "medium", "jumbo", "mini", "extra",
	"ripe", "local", "premium", "seedless", "family", "size", "value", "brand", "bag", "bags", "pack", "packs",
	"package", "packaged", "box", "boxes", "bunch", "bunches", "can", "cans", "jar", "jars", "bottle", "bottles",
	"carton", "tub", "tray", "container", "the", "of", "and", "or", "with", "for", "in", "from", "a", "an",
	"classic", "original", "pure", "selected", "select", "choice", "quality", "farm", "farms", "grade", "store",
	"bulk", "club", "new", "sweet", "juicy", "whole", "tasty", "delicious", "assorted", "mixed",
	"frozen", "canned", "tinned", "refrigerated", "chilled", "fridge", "freezer",
)

// Descriptors that mean something for where the item lives.
var locationHints = map[string]string{
	"frozen": "freezer", "freezer": "freezer",
	"canned": "pantry", "tinned": "pantry",
	"refrigerated": "fridge", "chilled": "fridge", "fridge": "fridge",
}

// Private labels and Canadian grocers, removed like a store name.
var knownBrands = []string{
	"kirkland signature", "kirkland", "president s choice", "pc", "no name", "great value", "compliments",
	"life brand", "irresistibles", "selection", "our compliments", "farm boy", "no frills", "t t", "freshco",
	"loblaws", "metro", "sobeys", "walmart", "costco", "superstore", "real canadian superstore", "food basics",
	"longos", "whole foods", "trader joe s", "lidl", "aldi", "safeway", "iga", "maxi",
}

func setOf(words ...string) map[string]bool {
	m := make(map[string]bool, len(words))
	for _, w := range words {
		m[w] = true
	}
	return m
}

// Token is one word of an analysed name.
type Token struct {
	Raw  string // normalized word as written
	Sing string // singular form
	// Descriptor: carries no identity (good, fresh, frozen, bag, …).
	Descriptor bool
}

// Parsed is a name taken apart.
type Parsed struct {
	// Whole is the plain normalized name (quantities included).
	Whole string
	// Tokens are the words left after removing quantities, brands and stores.
	Tokens []Token
	// LocationHint is freezer, fridge or pantry when the name says so.
	LocationHint string
	// Brand is a brand found in the name ("X brand avocado" gives "X"), or "".
	Brand string
}

// Parse analyses name. brand is the product's own brand and strip any extra
// words to ignore (the household's store names); both may be empty.
func Parse(name string, brand *string, strip []string) Parsed {
	p := Parsed{Whole: Normalize(name)}
	s := name
	for _, re := range quantityPatterns {
		s = re.ReplaceAllString(s, " ")
	}
	words := strings.Fields(Normalize(s))

	// "<brand> brand <thing>": the word before "brand" names the brand.
	for i, w := range words {
		if w == "brand" && i > 0 {
			from := max(0, i-2)
			var found []string
			for _, b := range words[from:i] {
				if !descriptors[b] && !isNumber(b) && len(b) > 0 {
					found = append(found, b)
				}
			}
			if len(found) > 0 {
				p.Brand = titleCase(strings.Join(found, " "))
				words = append(append([]string{}, words[:from]...), words[i+1:]...)
			}
			break
		}
	}

	phrases := append([]string{}, knownBrands...)
	phrases = append(phrases, strip...)
	if brand != nil {
		phrases = append(phrases, *brand)
	}
	words = removePhrases(words, phrases)

	for _, w := range words {
		if isNumber(w) || len([]rune(w)) < 2 {
			continue
		}
		if h, ok := locationHints[w]; ok && p.LocationHint == "" {
			p.LocationHint = h
		}
		p.Tokens = append(p.Tokens, Token{Raw: w, Sing: Singular(w), Descriptor: descriptors[w]})
	}
	return p
}

func isNumber(w string) bool {
	for _, r := range w {
		if !unicode.IsDigit(r) {
			return false
		}
	}
	return w != ""
}

func titleCase(s string) string {
	parts := strings.Fields(s)
	for i, w := range parts {
		r := []rune(w)
		r[0] = unicode.ToUpper(r[0])
		parts[i] = string(r)
	}
	return strings.Join(parts, " ")
}

// removePhrases deletes each phrase's word sequence from words, longest
// phrase first.
func removePhrases(words, phrases []string) []string {
	var seqs [][]string
	for _, ph := range phrases {
		if f := strings.Fields(Normalize(ph)); len(f) > 0 {
			seqs = append(seqs, f)
		}
	}
	// Longest first so "kirkland signature" wins over "kirkland".
	for i := 1; i < len(seqs); i++ {
		for j := i; j > 0 && len(seqs[j]) > len(seqs[j-1]); j-- {
			seqs[j], seqs[j-1] = seqs[j-1], seqs[j]
		}
	}
	for _, seq := range seqs {
		var out []string
		for i := 0; i < len(words); {
			if hasPrefix(words[i:], seq) {
				i += len(seq)
				continue
			}
			out = append(out, words[i])
			i++
		}
		words = out
	}
	return words
}

func hasPrefix(words, seq []string) bool {
	if len(words) < len(seq) {
		return false
	}
	for i, w := range seq {
		if words[i] != w {
			return false
		}
	}
	return true
}

// Words that end in s but are not plurals (or whose singular is irregular).
var singularExceptions = map[string]string{
	"hummus": "hummus", "couscous": "couscous", "asparagus": "asparagus", "molasses": "molasses", "swiss": "swiss",
	"citrus": "citrus", "lemongrass": "lemongrass", "watercress": "watercress", "cress": "cress", "grass": "grass",
	"bass": "bass", "gas": "gas", "chickpeas": "chickpea", "leaves": "leaf", "loaves": "loaf", "knives": "knife",
	"halves": "half", "calves": "calf", "olives": "olive", "cloves": "clove", "chives": "chive", "gloves": "glove",
	"cookies": "cookie", "brownies": "brownie", "smoothies": "smoothie", "pies": "pie", "ties": "tie",
	"oats": "oats", "greens": "green", "peas": "pea", "beans": "bean", "noodles": "noodle", "dates": "date",
	"limes": "lime", "mangoes": "mango", "mangos": "mango", "avocados": "avocado", "bananas": "banana",
	"molasse": "molasses", "bagels": "bagel", "pickles": "pickle", "tortillas": "tortilla",
}

// Singular returns the singular form of a lowercase word. It is a heuristic:
// callers also try the word as written, so a wrong guess only costs a miss.
func Singular(w string) string {
	if s, ok := singularExceptions[w]; ok {
		return s
	}
	n := len(w)
	switch {
	case n <= 3:
		return w
	case strings.HasSuffix(w, "ies") && n > 4:
		return w[:n-3] + "y"
	case strings.HasSuffix(w, "oes"):
		return w[:n-2]
	case strings.HasSuffix(w, "ves") && n > 4:
		return w[:n-3] + "f"
	case strings.HasSuffix(w, "ches"), strings.HasSuffix(w, "shes"), strings.HasSuffix(w, "xes"), strings.HasSuffix(w, "sses"):
		return w[:n-2]
	case strings.HasSuffix(w, "ss"), strings.HasSuffix(w, "us"), strings.HasSuffix(w, "is"):
		return w
	case strings.HasSuffix(w, "s"):
		return w[:n-1]
	}
	return w
}
