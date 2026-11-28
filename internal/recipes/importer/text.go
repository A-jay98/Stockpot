package importer

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	xhtml "golang.org/x/net/html"
	"golang.org/x/net/html/atom"

	"github.com/a-jay98/stockpot/internal/recipes"
)

// maxLLMText bounds the page or pasted text sent to the LLM parser.
const maxLLMText = 12000

func skipElement(n *xhtml.Node) bool {
	switch n.DataAtom {
	case atom.Script, atom.Style, atom.Noscript, atom.Nav, atom.Header, atom.Footer, atom.Aside,
		atom.Form, atom.Iframe, atom.Svg, atom.Button, atom.Select, atom.Template, atom.Dialog:
		return true
	}
	role := attr(n, "role")
	if role == "navigation" || role == "banner" || role == "contentinfo" || role == "complementary" {
		return true
	}
	cls := strings.ToLower(attr(n, "class") + " " + attr(n, "id"))
	for _, k := range []string{"comment", "newsletter", "share", "related", "advert", "cookie", "subscribe", "sidebar"} {
		if strings.Contains(cls, k) {
			return true
		}
	}
	return attr(n, "aria-hidden") == "true" || hasAttr(n, "hidden")
}

func isBlock(n *xhtml.Node) bool {
	switch n.DataAtom {
	case atom.P, atom.Div, atom.Li, atom.Ul, atom.Ol, atom.H1, atom.H2, atom.H3, atom.H4, atom.H5, atom.H6,
		atom.Br, atom.Tr, atom.Section, atom.Article, atom.Main, atom.Blockquote, atom.Pre, atom.Dd, atom.Dt, atom.Figcaption, atom.Table:
		return true
	}
	return false
}

// MainText is a readability-style extraction of the page's main content:
// the <article> (or <main>, else <body>) without navigation, comments and
// widgets, one block per line, capped for the LLM.
func (d *Document) MainText() string {
	var title string
	var article, mainEl, body *xhtml.Node
	walk(d.root, func(n *xhtml.Node) bool {
		if n.Type != xhtml.ElementNode {
			return true
		}
		switch n.DataAtom {
		case atom.Title:
			if title == "" {
				title = clean(textContent(n))
			}
		case atom.Article:
			if article == nil {
				article = n
			}
		case atom.Main:
			if mainEl == nil {
				mainEl = n
			}
		case atom.Body:
			body = n
		}
		return true
	})
	pick := article
	if pick == nil {
		pick = mainEl
	}
	if pick == nil {
		pick = body
	}
	var lines []string
	if title != "" {
		lines = append(lines, title)
	}
	if pick != nil {
		lines = append(lines, splitLines(blockText(pick))...)
	}
	return capText(strings.Join(dedupe(lines), "\n"), maxLLMText)
}

func dedupe(lines []string) []string {
	out := lines[:0:0]
	for i, l := range lines {
		if i > 0 && l == lines[i-1] {
			continue
		}
		out = append(out, l)
	}
	return out
}

func capText(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

// DetectScript guesses a language from the writing system alone (no LLM):
// Arabic script → "fa" (the household's other language), Cyrillic → "ru",
// and so on. Latin script or too little text gives "".
func DetectScript(s string) string {
	counts := map[string]int{}
	letters := 0
	for _, r := range s {
		if !unicode.IsLetter(r) {
			continue
		}
		letters++
		switch {
		case unicode.Is(unicode.Arabic, r):
			counts["fa"]++
		case unicode.Is(unicode.Cyrillic, r):
			counts["ru"]++
		case unicode.Is(unicode.Hebrew, r):
			counts["he"]++
		case unicode.Is(unicode.Greek, r):
			counts["el"]++
		case unicode.Is(unicode.Hangul, r):
			counts["ko"]++
		case unicode.Is(unicode.Hiragana, r), unicode.Is(unicode.Katakana, r):
			counts["ja"]++
		case unicode.Is(unicode.Han, r):
			counts["zh"]++
		case unicode.Is(unicode.Thai, r):
			counts["th"]++
		case unicode.Is(unicode.Devanagari, r):
			counts["hi"]++
		case unicode.Is(unicode.Armenian, r):
			counts["hy"]++
		case unicode.Is(unicode.Georgian, r):
			counts["ka"]++
		}
	}
	if letters < 8 {
		return ""
	}
	if counts["ja"] > 0 { // Japanese mixes kana with Han
		counts["ja"] += counts["zh"]
		counts["zh"] = 0
	}
	best, n := "", 0
	for lang, c := range counts {
		if c > n {
			best, n = lang, c
		}
	}
	if n*10 < letters*3 { // under 30 %: mostly Latin
		return ""
	}
	return best
}

// draftLang settles a deterministic draft's language: what the recipe or
// page declares, unless the script says otherwise; English when nothing says.
func draftLang(d *Draft, pageLang string) string {
	sample := d.Title + "\n" + strings.Join(d.Ingredients, "\n")
	for _, s := range d.Steps {
		sample += "\n" + s.Text
	}
	if s := DetectScript(sample); s != "" {
		return s
	}
	for _, l := range []string{d.Lang, pageLang} {
		if l != "" && l != "fa" && l != "ar" { // Arabic-script tags with Latin text: trust the text
			return l
		}
	}
	return "en"
}

var (
	ingHeading  = regexp.MustCompile(`(?i)^\s*(ingredients?|what you(?:'|’)ll need|you will need|مواد لازم|مواد|ingrédients|zutaten|ingredient(?:es|i))\s*:?\s*$`)
	stepHeading = regexp.MustCompile(`(?i)^\s*(method|instructions?|directions?|steps?|preparation|how to make( it)?|طرز تهیه|دستور پخت|préparation|zubereitung|preparación|procedimento)\s*:?\s*$`)
	notesHead   = regexp.MustCompile(`(?i)^\s*(notes?|tips?|نکته|نکات)\s*:?\s*$`)
	numbered    = regexp.MustCompile(`^\s*(?:step\s*)?(\d{1,2}|[۰-۹]{1,2})[.):\-]\s+(.+)$`)
	bullet      = regexp.MustCompile(`^\s*[-•*–·▢□☐]\s*`)
	quantityish = regexp.MustCompile(`(?i)^\s*(?:[\d½¼¾⅓⅔⅛۰-۹]|a\s|an\s|one\s|two\s|three\s|pinch|handful|dash|salt\b)`)
)

// SplitStructured builds a draft from pasted text without the LLM when the
// text is clearly a recipe: a title line, an ingredients section (or a run
// of quantity lines) and numbered or headed steps. It returns nil when the
// text needs the LLM.
func SplitStructured(text string) *Draft {
	raw := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	var lines []string
	for _, l := range raw {
		if l = strings.TrimSpace(l); l != "" {
			lines = append(lines, l)
		}
	}
	if len(lines) < 4 {
		return nil
	}
	const (
		inNone = iota
		inIng
		inSteps
		inNotes
	)
	d := &Draft{Via: "text"}
	state := inNone
	var notes []string
	headed := false
	for i, l := range lines {
		switch {
		case ingHeading.MatchString(l):
			state, headed = inIng, true
			continue
		case stepHeading.MatchString(l):
			state, headed = inSteps, true
			continue
		case notesHead.MatchString(l):
			state = inNotes
			continue
		}
		if m := numbered.FindStringSubmatch(l); m != nil && state != inIng {
			state = inSteps
			d.Steps = append(d.Steps, recipes.Step{Text: strings.TrimSpace(m[2])})
			continue
		}
		switch state {
		case inNone:
			if d.Title == "" && i == 0 {
				d.Title = truncate(strings.TrimRight(l, ":"), 200)
				continue
			}
			if quantityish.MatchString(bullet.ReplaceAllString(l, "")) {
				state = inIng
				d.Ingredients = append(d.Ingredients, bullet.ReplaceAllString(l, ""))
				continue
			}
			if d.Subtitle == "" {
				d.Subtitle = truncate(l, 300)
			}
		case inIng:
			if m := numbered.FindStringSubmatch(l); m != nil && headed && len(d.Ingredients) > 0 && !quantityish.MatchString(m[2]) {
				state = inSteps
				d.Steps = append(d.Steps, recipes.Step{Text: m[2]})
				continue
			}
			d.Ingredients = append(d.Ingredients, bullet.ReplaceAllString(l, ""))
		case inSteps:
			d.Steps = append(d.Steps, recipes.Step{Text: bullet.ReplaceAllString(l, "")})
		case inNotes:
			notes = append(notes, l)
		}
	}
	d.Notes = strings.Join(notes, "\n")
	if d.Title == "" || len(d.Ingredients) < 2 || len(d.Steps) < 1 {
		return nil
	}
	qty := 0
	for _, l := range d.Ingredients {
		if quantityish.MatchString(l) || strings.HasSuffix(l, ":") {
			qty++
		}
	}
	if qty*2 < len(d.Ingredients) { // mostly prose: let the LLM read it
		return nil
	}
	return d
}
