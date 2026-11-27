package resolve

import (
	"context"
	"errors"
	"sort"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/a-jay98/stockpot/internal/db/dbq"
)

// Confidences of the deterministic matches.
const (
	// ConfWhole is a match of everything that identifies the item.
	ConfWhole = 1.0
	// ConfPhrase is a match of a part of the name (other words were left over).
	ConfPhrase = 0.9
	// ConfBroad is a part-of-name match of a broad ingredient (one with kinds
	// under it, like fruit or milk): "zorbo fruit" is probably not fruit
	// itself, so the enricher gets to check it.
	ConfBroad = 0.8
	// MinSimilarity is the lowest trigram similarity accepted as a (low
	// confidence) guess.
	MinSimilarity = 0.45
	// AutoAccept is the confidence at which a rule match needs no LLM check.
	AutoAccept   = 0.85
	maxPhraseLen = 4
)

// Match is a resolver result.
type Match struct {
	IngredientID uuid.UUID
	Slug         string
	Name         string
	// CategorySlug is the ingredient's category (household for paper towels).
	CategorySlug string
	Confidence   float32 // 1.0 = whole name matched exactly
	// LocationHint is freezer, fridge or pantry when the name said "frozen",
	// "canned", …; empty otherwise.
	LocationHint string
	// ExtractedBrand is a brand the name carried ("X brand avocado"), or "".
	ExtractedBrand string
}

// Resolver is the deterministic resolver. The zero value is ready to use.
type Resolver struct{}

// Resolve returns nil, nil when nothing matches.
func (Resolver) Resolve(ctx context.Context, q dbq.DBTX, household uuid.UUID, name string, brand *string) (*Match, error) {
	p := Parse(name, brand, nil)
	if stores, err := storeNames(ctx, q, household); err == nil && len(stores) > 0 {
		p = Parse(name, brand, stores)
	}
	if p.Whole == "" {
		return nil, nil
	}
	m, err := exact(ctx, q, household, p)
	if err != nil || m == nil {
		if err != nil {
			return nil, err
		}
		if m, err = fuzzy(ctx, q, household, p); err != nil || m == nil {
			return nil, err
		}
	}
	m.LocationHint, m.ExtractedBrand = p.LocationHint, p.Brand
	return m, nil
}

func storeNames(ctx context.Context, q dbq.DBTX, household uuid.UUID) ([]string, error) {
	rows, err := q.Query(ctx, `SELECT name FROM store WHERE household_id = $1 AND deleted_at IS NULL`, household)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

type span struct {
	from, to int // tokens [from, to)
	forms    []string
}

// spans lists every phrase of up to maxPhraseLen tokens that contains a
// content word, longest first (then leftmost), each in the forms to try.
func spans(toks []Token) []span {
	var out []span
	for n := min(len(toks), maxPhraseLen); n >= 1; n-- {
		for i := 0; i+n <= len(toks); i++ {
			part := toks[i : i+n]
			content := false
			raw, sing, lastSing := make([]string, n), make([]string, n), make([]string, n)
			for j, t := range part {
				content = content || !t.Descriptor
				raw[j], sing[j], lastSing[j] = t.Raw, t.Sing, t.Raw
			}
			if !content {
				continue
			}
			lastSing[n-1] = sing[n-1]
			out = append(out, span{from: i, to: i + n, forms: uniq(strings.Join(raw, " "), strings.Join(sing, " "), strings.Join(lastSing, " "))})
		}
	}
	return out
}

// wholeForms is the whole name as written and with every word singular.
func wholeForms(whole string) []string {
	words := strings.Fields(whole)
	for i, w := range words {
		words[i] = Singular(w)
	}
	return uniq(whole, strings.Join(words, " "))
}

func uniq(in ...string) []string {
	var out []string
	seen := map[string]bool{}
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

type hit struct {
	phrase    string
	m         Match
	household bool
	broad     bool
}

// exact matches phrases of the name against ingredient names, plurals, slugs
// and aliases, longest phrase first. Household rows win over global ones.
func exact(ctx context.Context, q dbq.DBTX, household uuid.UUID, p Parsed) (*Match, error) {
	sp := spans(p.Tokens)
	wholeForms := wholeForms(p.Whole)
	phrases := append([]string{}, wholeForms...)
	for _, s := range sp {
		phrases = append(phrases, s.forms...)
	}
	phrases = uniq(phrases...)
	rows, err := q.Query(ctx, `
		SELECT ph, i.id, i.slug, i.name, coalesce(c.slug, ''), i.household_id IS NOT NULL,
		       EXISTS (SELECT 1 FROM ingredient k WHERE k.parent_id = i.id AND k.deleted_at IS NULL)
		FROM unnest($2::text[]) AS ph
		JOIN ingredient i ON i.deleted_at IS NULL AND (i.household_id = $1 OR i.household_id IS NULL)
		 AND (`+sqlNorm("i.name")+` = ph OR `+sqlNorm("coalesce(i.plural, '')")+` = ph
		      OR replace(i.slug, '-', ' ') = ph OR ph = ANY(i.aliases))
		LEFT JOIN category c ON c.id = i.category_id`, household, phrases)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byPhrase := map[string][]hit{}
	for rows.Next() {
		var h hit
		if err := rows.Scan(&h.phrase, &h.m.IngredientID, &h.m.Slug, &h.m.Name, &h.m.CategorySlug, &h.household, &h.broad); err != nil {
			return nil, err
		}
		byPhrase[h.phrase] = append(byPhrase[h.phrase], h)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// pick takes the best hit among the forms of one phrase: household rows
	// before global ones, then the earlier form.
	pick := func(forms []string, conf float32) *Match {
		var best *hit
		for _, f := range forms {
			for _, h := range byPhrase[f] {
				if best == nil || (h.household && !best.household) {
					best = &h
				}
			}
		}
		if best == nil {
			return nil
		}
		m := best.m
		m.Confidence = conf
		if conf < ConfWhole && best.broad {
			m.Confidence = ConfBroad
		}
		return &m
	}
	if m := pick(wholeForms, ConfWhole); m != nil {
		return m, nil
	}
	for _, s := range sp {
		conf := float32(ConfPhrase)
		if restIsDescriptors(p.Tokens, s) {
			conf = ConfWhole
		}
		if m := pick(s.forms, conf); m != nil {
			return m, nil
		}
	}
	return nil, nil
}

// restIsDescriptors reports whether every token outside s is a descriptor.
func restIsDescriptors(toks []Token, s span) bool {
	for i, t := range toks {
		if (i < s.from || i >= s.to) && !t.Descriptor {
			return false
		}
	}
	return true
}

// contentPhrases are the strings the trigram fallback compares: all content
// words together, then each word of 4+ letters, longest first.
func contentPhrases(p Parsed) []string {
	var words []string
	for _, t := range p.Tokens {
		if !t.Descriptor {
			words = append(words, t.Sing)
		}
	}
	if len(words) == 0 {
		return nil
	}
	out := []string{strings.Join(words, " ")}
	long := make([]string, 0, len(words))
	for _, w := range words {
		if len([]rune(w)) >= 4 {
			long = append(long, w)
		}
	}
	sort.SliceStable(long, func(i, j int) bool { return len(long[i]) > len(long[j]) })
	return uniq(append(out, long...)...)
}

const similarityScore = `GREATEST(
	similarity(immutable_unaccent(lower(i.name)), $1),
	similarity(immutable_unaccent(lower(coalesce(i.plural, ''))), $1),
	coalesce((SELECT max(similarity(a, $1)) FROM unnest(i.aliases) a), 0))`

// fuzzy finds the ingredient whose name or alias is most similar (trigrams)
// to the content words; the similarity is the confidence.
func fuzzy(ctx context.Context, q dbq.DBTX, household uuid.UUID, p Parsed) (*Match, error) {
	var best *Match
	for _, ph := range contentPhrases(p) {
		var m Match
		err := q.QueryRow(ctx, `
			SELECT * FROM (
				SELECT i.id, i.slug, i.name, coalesce(c.slug, ''), `+similarityScore+` AS sim, i.household_id IS NULL AS global
				FROM ingredient i LEFT JOIN category c ON c.id = i.category_id
				WHERE i.deleted_at IS NULL AND (i.household_id = $2 OR i.household_id IS NULL)
			) t WHERE sim >= $3 ORDER BY sim DESC, global, length(name) LIMIT 1`, ph, household, MinSimilarity,
		).Scan(&m.IngredientID, &m.Slug, &m.Name, &m.CategorySlug, &m.Confidence, new(bool))
		if err != nil {
			if isNoRows(err) {
				continue
			}
			return nil, err
		}
		if best == nil || m.Confidence > best.Confidence {
			best = &m
		}
	}
	return best, nil
}

// Candidate is an ingredient that might fit a name.
type Candidate struct {
	Slug, Name string
	Score      float32
}

// Candidates lists the n ingredients most similar to name, best first, for
// the LLM to choose from.
func Candidates(ctx context.Context, q dbq.DBTX, household uuid.UUID, name string, brand *string, n int) ([]Candidate, error) {
	p := Parse(name, brand, nil)
	phrase := ""
	if ps := contentPhrases(p); len(ps) > 0 {
		phrase = ps[0]
	}
	if phrase == "" {
		phrase = p.Whole
	}
	rows, err := q.Query(ctx, `
		SELECT slug, name, score FROM (
			SELECT i.slug, i.name, `+strings.ReplaceAll(similarityScore, "similarity(", "word_similarity(")+` AS score, i.household_id
			FROM ingredient i
			WHERE i.deleted_at IS NULL AND (i.household_id = $2 OR i.household_id IS NULL)
		) t WHERE score > 0.3 ORDER BY score DESC, household_id IS NULL, length(name) LIMIT $3`, phrase, household, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Candidate
	for rows.Next() {
		var c Candidate
		if err := rows.Scan(&c.Slug, &c.Name, &c.Score); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func isNoRows(err error) bool { return errors.Is(err, pgx.ErrNoRows) }
