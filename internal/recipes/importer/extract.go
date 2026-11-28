package importer

import (
	"bytes"
	"encoding/json"
	"html"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	xhtml "golang.org/x/net/html"
	"golang.org/x/net/html/atom"

	"github.com/a-jay98/stockpot/internal/recipes"
)

// Draft is a recipe as an import stage produced it, before it is written.
type Draft struct {
	// Lang is the content's language (BCP 47 primary subtag, e.g. "fa"); ""
	// means unknown.
	Lang         string
	Title        string
	Subtitle     string
	Servings     *int
	TotalMinutes *int
	// Ingredients are raw lines, group headers ("For the sauce:") included.
	Ingredients []string
	Steps       []recipes.Step
	Notes       string
	Tags        []string
	// ImageURL is the cover to download (absolute), if any.
	ImageURL string
	// Via says how the draft was made: jsonld, microdata, llm, text or photo.
	Via string
}

// usable reports whether the draft has enough to be a recipe.
func (d *Draft) usable() bool {
	return strings.TrimSpace(d.Title) != "" && (len(d.Ingredients) > 0 || len(d.Steps) > 0)
}

// Document is a parsed web page.
type Document struct {
	root *xhtml.Node
	base *url.URL
	// Lang is the page's <html lang>, normalized.
	Lang string
}

// ParseDocument parses an HTML page fetched from base.
func ParseDocument(body []byte, base *url.URL) (*Document, error) {
	root, err := xhtml.Parse(bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	d := &Document{root: root, base: base}
	walk(root, func(n *xhtml.Node) bool {
		if n.Type == xhtml.ElementNode && n.DataAtom == atom.Html {
			d.Lang = normLang(attr(n, "lang"))
			return false
		}
		return true
	})
	return d, nil
}

// Recipe returns the page's schema.org Recipe from JSON-LD, else from
// microdata, else nil.
func (d *Document) Recipe() *Draft {
	if r := d.jsonLD(); r != nil {
		return r
	}
	return d.microdata()
}

// walk visits n and its descendants depth-first while fn returns true for
// the node (false skips its children).
func walk(n *xhtml.Node, fn func(*xhtml.Node) bool) {
	if !fn(n) {
		return
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		walk(c, fn)
	}
}

func attr(n *xhtml.Node, key string) string {
	for _, a := range n.Attr {
		if strings.EqualFold(a.Key, key) {
			return a.Val
		}
	}
	return ""
}

// --- JSON-LD ---------------------------------------------------------------

func (d *Document) jsonLD() *Draft {
	var found *Draft
	walk(d.root, func(n *xhtml.Node) bool {
		if found != nil {
			return false
		}
		if n.Type != xhtml.ElementNode || n.DataAtom != atom.Script {
			return true
		}
		if t := strings.ToLower(strings.TrimSpace(strings.SplitN(attr(n, "type"), ";", 2)[0])); t != "application/ld+json" {
			return false
		}
		var buf strings.Builder
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if c.Type == xhtml.TextNode {
				buf.WriteString(c.Data)
			}
		}
		v, ok := decodeLD(buf.String())
		if !ok {
			return false
		}
		if obj := findRecipe(v, 0); obj != nil {
			found = d.fromLD(obj)
		}
		return false
	})
	return found
}

// decodeLD parses a JSON-LD block, tolerating CDATA and comment wrappers.
func decodeLD(s string) (any, bool) {
	s = strings.TrimSpace(s)
	for _, cut := range [][2]string{{"<![CDATA[", "]]>"}, {"//<![CDATA[", "//]]>"}, {"<!--", "-->"}} {
		s = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(s, cut[0]), cut[1]))
	}
	var v any
	dec := json.NewDecoder(strings.NewReader(s))
	dec.UseNumber()
	if err := dec.Decode(&v); err != nil {
		return nil, false
	}
	return v, true
}

// findRecipe finds the first object typed Recipe in a JSON-LD value: the
// value itself, an array, @graph, or a nested mainEntity.
func findRecipe(v any, depth int) map[string]any {
	if depth > 6 {
		return nil
	}
	switch t := v.(type) {
	case []any:
		for _, e := range t {
			if r := findRecipe(e, depth+1); r != nil {
				return r
			}
		}
	case map[string]any:
		if isType(t["@type"], "Recipe") {
			return t
		}
		for _, k := range []string{"@graph", "mainEntity", "mainEntityOfPage", "itemListElement", "item"} {
			if r := findRecipe(t[k], depth+1); r != nil {
				return r
			}
		}
	}
	return nil
}

func isType(v any, want string) bool {
	match := func(s string) bool {
		s = strings.TrimPrefix(strings.TrimPrefix(s, "http://schema.org/"), "https://schema.org/")
		return strings.EqualFold(s, want)
	}
	switch t := v.(type) {
	case string:
		return match(t)
	case []any:
		for _, e := range t {
			if s, ok := e.(string); ok && match(s) {
				return true
			}
		}
	}
	return false
}

func (d *Document) fromLD(o map[string]any) *Draft {
	r := &Draft{Via: "jsonld"}
	r.Title = clean(str(o["name"]))
	if r.Title == "" {
		r.Title = clean(str(o["headline"]))
	}
	r.Subtitle = truncate(clean(str(o["description"])), 300)
	r.Lang = normLang(str(o["inLanguage"]))
	r.Servings = servings(o["recipeYield"])
	r.TotalMinutes = totalMinutes(o)
	ing := o["recipeIngredient"]
	if ing == nil {
		ing = o["ingredients"]
	}
	for _, s := range strs(ing) {
		if s = clean(s); s != "" {
			r.Ingredients = append(r.Ingredients, s)
		}
	}
	r.Steps = ldSteps(o["recipeInstructions"])
	r.Tags = ldTags(o)
	r.ImageURL = d.abs(ldImage(o["image"]))
	return r
}

// str returns v as a string: a string, a number, or an object's @value/name/text.
func str(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case json.Number:
		return t.String()
	case []any:
		if len(t) > 0 {
			return str(t[0])
		}
	case map[string]any:
		for _, k := range []string{"@value", "name", "text"} {
			if s := str(t[k]); s != "" {
				return s
			}
		}
	}
	return ""
}

// strs flattens a string or an array of strings.
func strs(v any) []string {
	switch t := v.(type) {
	case string:
		return []string{t}
	case []any:
		var out []string
		for _, e := range t {
			if s := str(e); s != "" {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

var firstInt = regexp.MustCompile(`\d+`)

func servings(v any) *int {
	for _, s := range append([]string{str(v)}, strs(v)...) {
		if m := firstInt.FindString(s); m != "" {
			if n, err := strconv.Atoi(m); err == nil && n >= 1 && n <= 100 {
				return &n
			}
		}
	}
	return nil
}

var isoDuration = regexp.MustCompile(`(?i)^P(?:(\d+(?:\.\d+)?)D)?(?:T(?:(\d+(?:\.\d+)?)H)?(?:(\d+(?:\.\d+)?)M)?(?:(\d+(?:\.\d+)?)S)?)?$`)

// Minutes parses an ISO 8601 duration (PT1H30M, P0DT0H20M) into minutes.
func Minutes(s string) (int, bool) {
	m := isoDuration.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil || s == "P" || strings.EqualFold(strings.TrimSpace(s), "PT") {
		return 0, false
	}
	f := func(i int) float64 {
		v, _ := strconv.ParseFloat(m[i], 64)
		return v
	}
	total := f(1)*24*60 + f(2)*60 + f(3) + f(4)/60
	return int(total + 0.5), true
}

func totalMinutes(o map[string]any) *int {
	clamp := func(n int) *int {
		if n <= 0 || n > 10000 {
			return nil
		}
		return &n
	}
	if n, ok := Minutes(str(o["totalTime"])); ok && n > 0 {
		return clamp(n)
	}
	sum := 0
	for _, k := range []string{"prepTime", "cookTime", "performTime"} {
		if n, ok := Minutes(str(o[k])); ok {
			sum += n
		}
	}
	return clamp(sum)
}

// ldSteps reads recipeInstructions: text, a list of texts, HowToStep objects
// or HowToSection groups (whose name prefixes their first step).
func ldSteps(v any) []recipes.Step {
	var out []recipes.Step
	add := func(s string) {
		for _, line := range splitLines(s) {
			out = append(out, recipes.Step{Text: line})
		}
	}
	var visit func(v any, section string, depth int)
	visit = func(v any, section string, depth int) {
		if depth > 5 {
			return
		}
		switch t := v.(type) {
		case string:
			lines := splitLines(t)
			for i, l := range lines {
				if i == 0 && section != "" {
					l = section + ": " + l
				}
				out = append(out, recipes.Step{Text: l})
			}
		case []any:
			for i, e := range t {
				s := ""
				if i == 0 {
					s = section
				}
				visit(e, s, depth+1)
			}
		case map[string]any:
			switch {
			case isType(t["@type"], "HowToSection") || (t["itemListElement"] != nil && !isType(t["@type"], "HowToStep")):
				name := strings.TrimRight(clean(str(t["name"])), ": ")
				visit(t["itemListElement"], name, depth+1)
			default:
				text := str(t["text"])
				if text == "" {
					text = str(t["name"])
				}
				if text == "" {
					text = str(t["description"])
				}
				if section != "" && text != "" {
					text = section + ": " + text
				}
				if text != "" {
					add(text)
				}
			}
		}
	}
	visit(v, "", 0)
	return out
}

func ldTags(o map[string]any) []string {
	var raw []string
	for _, k := range []string{"recipeCategory", "recipeCuisine"} {
		raw = append(raw, strs(o[k])...)
	}
	// Keywords are mostly SEO phrases: keep only short ones.
	for _, k := range strs(o["keywords"]) {
		for _, w := range strings.Split(k, ",") {
			if len(strings.Fields(w)) <= 2 {
				raw = append(raw, w)
			}
		}
	}
	seen := map[string]bool{}
	var out []string
	for _, t := range raw {
		t = strings.ToLower(clean(t))
		if t == "" || utf8.RuneCountInString(t) > 40 || seen[t] {
			continue
		}
		seen[t] = true
		out = append(out, t)
		if len(out) == 6 {
			break
		}
	}
	return out
}

func ldImage(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case []any:
		for _, e := range t {
			if s := ldImage(e); s != "" {
				return s
			}
		}
	case map[string]any:
		for _, k := range []string{"url", "contentUrl", "@id"} {
			if s, ok := t[k].(string); ok && s != "" {
				return s
			}
		}
	}
	return ""
}

// abs resolves an image reference against the page; only http(s) results.
func (d *Document) abs(ref string) string {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return ""
	}
	u, err := url.Parse(ref)
	if err != nil {
		return ""
	}
	if d.base != nil {
		u = d.base.ResolveReference(u)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return ""
	}
	return u.String()
}

// --- microdata -------------------------------------------------------------

func (d *Document) microdata() *Draft {
	var scope *xhtml.Node
	walk(d.root, func(n *xhtml.Node) bool {
		if scope != nil {
			return false
		}
		if hasAttr(n, "itemscope") {
			if strings.HasSuffix(strings.TrimRight(attr(n, "itemtype"), "/"), "schema.org/Recipe") {
				scope = n
				return false
			}
		}
		return true
	})
	if scope == nil {
		return nil
	}
	props := map[string][]*xhtml.Node{}
	walk(scope, func(n *xhtml.Node) bool {
		if n.Type != xhtml.ElementNode {
			return true
		}
		if p := attr(n, "itemprop"); p != "" {
			for _, name := range strings.Fields(p) {
				props[name] = append(props[name], n)
			}
		}
		// A nested item's own properties are read through it, not as the recipe's.
		return n == scope || !hasAttr(n, "itemscope")
	})
	r := &Draft{Via: "microdata"}
	first := func(name string) string {
		if ns := props[name]; len(ns) > 0 {
			return d.itemValue(ns[0])
		}
		return ""
	}
	r.Title = clean(first("name"))
	r.Subtitle = truncate(clean(first("description")), 300)
	if y := first("recipeYield"); y != "" {
		r.Servings = servings(y)
	}
	ld := map[string]any{"totalTime": first("totalTime"), "prepTime": first("prepTime"), "cookTime": first("cookTime")}
	r.TotalMinutes = totalMinutes(ld)
	ings := props["recipeIngredient"]
	if len(ings) == 0 {
		ings = props["ingredients"]
	}
	for _, n := range ings {
		if s := clean(d.itemValue(n)); s != "" {
			r.Ingredients = append(r.Ingredients, s)
		}
	}
	for _, n := range props["recipeInstructions"] {
		if hasAttr(n, "itemscope") {
			var text string
			walk(n, func(c *xhtml.Node) bool {
				if c != n && c.Type == xhtml.ElementNode && strings.Contains(" "+attr(c, "itemprop")+" ", " text ") {
					text = d.itemValue(c)
					return false
				}
				return text == ""
			})
			if text == "" {
				text = textContent(n)
			}
			for _, l := range splitLines(text) {
				r.Steps = append(r.Steps, recipes.Step{Text: l})
			}
			continue
		}
		for _, l := range splitLines(blockText(n)) {
			r.Steps = append(r.Steps, recipes.Step{Text: l})
		}
	}
	r.ImageURL = d.abs(first("image"))
	return r
}

func hasAttr(n *xhtml.Node, key string) bool {
	if n.Type != xhtml.ElementNode {
		return false
	}
	for _, a := range n.Attr {
		if strings.EqualFold(a.Key, key) {
			return true
		}
	}
	return false
}

// itemValue is a microdata property's value per the HTML spec.
func (d *Document) itemValue(n *xhtml.Node) string {
	if v := attr(n, "content"); v != "" {
		return v
	}
	switch n.DataAtom {
	case atom.Img, atom.Audio, atom.Video, atom.Source, atom.Embed, atom.Iframe:
		return attr(n, "src")
	case atom.A, atom.Link, atom.Area:
		return attr(n, "href")
	case atom.Time:
		if v := attr(n, "datetime"); v != "" {
			return v
		}
	case atom.Meta:
		return attr(n, "content")
	}
	return textContent(n)
}

// --- text helpers ----------------------------------------------------------

func textContent(n *xhtml.Node) string {
	var b strings.Builder
	walk(n, func(c *xhtml.Node) bool {
		if c.Type == xhtml.ElementNode && (c.DataAtom == atom.Script || c.DataAtom == atom.Style) {
			return false
		}
		if c.Type == xhtml.TextNode {
			b.WriteString(c.Data)
		}
		return true
	})
	return b.String()
}

// blockText is n's text with a newline after each block element.
func blockText(n *xhtml.Node) string {
	var b strings.Builder
	var rec func(*xhtml.Node)
	rec = func(c *xhtml.Node) {
		switch c.Type {
		case xhtml.TextNode:
			b.WriteString(c.Data)
			return
		case xhtml.ElementNode:
			if skipElement(c) {
				return
			}
		}
		for k := c.FirstChild; k != nil; k = k.NextSibling {
			rec(k)
		}
		if c.Type == xhtml.ElementNode && isBlock(c) {
			b.WriteByte('\n')
		}
	}
	rec(n)
	return b.String()
}

var (
	tagPattern = regexp.MustCompile(`<[^>]*>`)
	lineBreaks = regexp.MustCompile(`(?i)<br\s*/?>|</p>|</li>`)
)

var spaces = regexp.MustCompile(`[ \t\f\v\x{00a0}\x{200b}]+`)

// clean decodes entities (twice: some sites double-escape), strips tags and
// collapses whitespace to one line.
func clean(s string) string {
	s = html.UnescapeString(html.UnescapeString(s))
	s = tagPattern.ReplaceAllString(s, " ")
	s = strings.Join(strings.Fields(s), " ")
	return strings.TrimSpace(s)
}

// splitLines splits text into trimmed, non-empty single-line entries.
func splitLines(s string) []string {
	s = html.UnescapeString(html.UnescapeString(s))
	s = lineBreaks.ReplaceAllString(s, "\n")
	s = tagPattern.ReplaceAllString(s, " ")
	var out []string
	for _, l := range strings.Split(s, "\n") {
		l = strings.TrimSpace(spaces.ReplaceAllString(l, " "))
		if l != "" {
			out = append(out, l)
		}
	}
	return out
}

func truncate(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return strings.TrimSpace(string(r[:n-1])) + "…"
}

// normLang lowercases a BCP 47 tag's primary subtag ("fr-FR" → "fr").
func normLang(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if i := strings.IndexAny(s, "-_"); i > 0 {
		s = s[:i]
	}
	if len(s) < 2 || len(s) > 3 {
		return ""
	}
	for _, c := range s {
		if c < 'a' || c > 'z' {
			return ""
		}
	}
	return s
}

// OGImage is the page's og:image (absolute), the cover fallback for pages
// without recipe data.
func (d *Document) OGImage() string {
	var out string
	walk(d.root, func(n *xhtml.Node) bool {
		if out != "" {
			return false
		}
		if n.Type == xhtml.ElementNode && n.DataAtom == atom.Meta {
			if p := strings.ToLower(attr(n, "property") + attr(n, "name")); p == "og:image" || p == "og:image:url" || p == "twitter:image" {
				out = d.abs(attr(n, "content"))
			}
		}
		return n.DataAtom != atom.Body
	})
	return out
}
