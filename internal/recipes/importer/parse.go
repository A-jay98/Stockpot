package importer

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/a-jay98/stockpot/internal/blob"
	"github.com/a-jay98/stockpot/internal/llm"
	"github.com/a-jay98/stockpot/internal/recipes"
)

// LLM features (architecture-m2 §12). Names are lower_snake like every
// metered feature.
var (
	FeatureParse      = llm.Feature{Name: "recipes_parse", CacheTTL: 90 * 24 * time.Hour}
	FeatureTranslate  = llm.Feature{Name: "recipes_translate", CacheTTL: 90 * 24 * time.Hour}
	FeaturePhoto      = llm.Feature{Name: "recipes_photo"}
	FeatureTranscribe = llm.Feature{Name: "recipes_transcribe", CacheTTL: 90 * 24 * time.Hour}
)

// LLM is the part of llm.Router the importer uses.
type LLM interface {
	Complete(ctx context.Context, req llm.Request) (llm.Response, error)
	Transcribe(ctx context.Context, req llm.TranscribeRequest) (llm.Transcription, error)
}

var _ LLM = (*llm.Router)(nil)

// parsed is the LLM's recipe: the strict schema below. Unknown values are
// empty strings, empty arrays and zeros, which every OpenAI-compatible
// endpoint accepts.
type parsed struct {
	IsRecipe     bool     `json:"is_recipe"`
	Lang         string   `json:"lang"`
	Title        string   `json:"title"`
	Subtitle     string   `json:"subtitle"`
	Servings     int      `json:"servings"`
	TotalMinutes int      `json:"total_minutes"`
	Ingredients  []group  `json:"ingredients"`
	Steps        []step   `json:"steps"`
	Notes        string   `json:"notes"`
	Tags         []string `json:"tags"`
}

type group struct {
	Group string   `json:"group"`
	Items []string `json:"items"`
}

type step struct {
	Text    string `json:"text"`
	Minutes int    `json:"minutes"`
}

const recipeProps = `
 "lang":{"type":"string"},
 "title":{"type":"string"},
 "subtitle":{"type":"string"},
 "servings":{"type":"integer"},
 "total_minutes":{"type":"integer"},
 "ingredients":{"type":"array","items":{"type":"object","additionalProperties":false,"required":["group","items"],
   "properties":{"group":{"type":"string"},"items":{"type":"array","items":{"type":"string"}}}}},
 "steps":{"type":"array","items":{"type":"object","additionalProperties":false,"required":["text","minutes"],
   "properties":{"text":{"type":"string"},"minutes":{"type":"integer"}}}},
 "notes":{"type":"string"}`

var parseSchema = json.RawMessage(`{"type":"object","additionalProperties":false,
 "required":["is_recipe","lang","title","subtitle","servings","total_minutes","ingredients","steps","notes","tags"],
 "properties":{"is_recipe":{"type":"boolean"},` + recipeProps + `,
 "tags":{"type":"array","items":{"type":"string"}}}}`)

var translateSchema = json.RawMessage(`{"type":"object","additionalProperties":false,
 "required":["lang","title","subtitle","servings","total_minutes","ingredients","steps","notes"],
 "properties":{` + recipeProps + `}}`)

const parseRules = `Rules:
- is_recipe: false when the content is not a recipe (then leave everything else empty).
- lang: the ISO 639-1 code of the recipe's language as written (fa for Persian, en for English).
- Keep the recipe in its original language and wording; do not translate, invent or "improve" anything.
- ingredients: one string per ingredient line with its quantity and unit, as written ("2 cups basmati rice"). Use groups only when the recipe has sections ("For the sauce"); otherwise one group with an empty name.
- steps: one entry per step, without numbering. minutes: a timer the step states (simmer 20 minutes → 20), else 0.
- servings and total_minutes: as stated, else 0. subtitle: a one-line description if given, else empty.
- notes: tips or variations the author gives, else empty. tags: up to 5 short lower-case tags (cuisine, course, main ingredient).
Reply with JSON only.`

const parseSystem = `You turn recipe text (a web page's main text, a social media caption and transcript, or pasted notes) into structured data for a household cooking app.
` + parseRules

const photoSystem = `You read photos of a recipe (a cookbook page, a handwritten card or a screenshot) and turn them into structured data for a household cooking app. The photos are pages of the same recipe, in order.
` + parseRules

const translateSystem = `You translate a recipe into natural English for a household in Canada.
- Translate title, subtitle, every ingredient line, every step and the notes. Keep group names, translated.
- Keep every quantity, number and unit exactly as given (do not convert units). Keep the same number of ingredient lines and steps, in the same order.
- For dishes and ingredients known by their original name (khoresh gheymeh, sabzi, sumac, tahdig), keep that name and add a short English gloss in parentheses the first time, e.g. "dried limes (limoo amani)".
- lang must be "en"; servings, total_minutes and step minutes are unchanged.
Reply with JSON only.`

// toDraft maps an LLM reply onto a draft.
func (p *parsed) toDraft(via string) *Draft {
	d := &Draft{
		Via: via, Lang: normLang(p.Lang), Title: truncate(clean(p.Title), 200), Subtitle: truncate(clean(p.Subtitle), 300),
		Notes: truncate(strings.TrimSpace(p.Notes), 10000), Tags: cleanTags(p.Tags),
	}
	if p.Servings >= 1 && p.Servings <= 100 {
		n := p.Servings
		d.Servings = &n
	}
	if p.TotalMinutes > 0 && p.TotalMinutes <= 10000 {
		n := p.TotalMinutes
		d.TotalMinutes = &n
	}
	d.Ingredients = flattenGroups(p.Ingredients)
	for _, s := range p.Steps {
		t := strings.TrimSpace(s.Text)
		if t == "" {
			continue
		}
		st := recipes.Step{Text: truncate(t, 4000)}
		if s.Minutes > 0 && s.Minutes <= 10000 {
			m := s.Minutes
			st.Minutes = &m
		}
		d.Steps = append(d.Steps, st)
	}
	return d
}

func flattenGroups(gs []group) []string {
	var out []string
	named := 0
	for _, g := range gs {
		if strings.TrimSpace(g.Group) != "" {
			named++
		}
	}
	for _, g := range gs {
		if name := strings.TrimRight(strings.TrimSpace(g.Group), ":"); name != "" && (named > 1 || len(gs) > 1) {
			out = append(out, truncate(name, 200)+":")
		}
		for _, it := range g.Items {
			if it = strings.TrimSpace(it); it != "" {
				out = append(out, truncate(it, 500))
			}
		}
	}
	return out
}

func cleanTags(in []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, t := range in {
		t = strings.ToLower(strings.TrimSpace(t))
		if t == "" || len([]rune(t)) > 40 || seen[t] {
			continue
		}
		seen[t] = true
		out = append(out, t)
		if len(out) == 8 {
			break
		}
	}
	return out
}

// toGroups is the inverse of flattenGroups, for the translation request.
func toGroups(lines []string) []group {
	gs := []group{{Items: []string{}}}
	for _, l := range lines {
		if strings.HasSuffix(l, ":") && len(l) > 1 {
			gs = append(gs, group{Group: strings.TrimSuffix(l, ":"), Items: []string{}})
			continue
		}
		gs[len(gs)-1].Items = append(gs[len(gs)-1].Items, l)
	}
	if len(gs) > 1 && len(gs[0].Items) == 0 {
		gs = gs[1:]
	}
	return gs
}

// ErrNotARecipe means the LLM read the content and found no recipe.
var ErrNotARecipe = fmt.Errorf("no recipe found")

func (s *Service) parseText(ctx context.Context, text, hint string, prio llm.Priority) (*Draft, error) {
	user := text
	if hint != "" {
		user = hint + "\n\n" + text
	}
	resp, err := s.d.LLM.Complete(ctx, llm.Request{
		Feature: FeatureParse, Schema: parseSchema, SchemaName: "recipe", Priority: prio, MaxTokens: 4096,
		Messages: []llm.Message{{Role: llm.RoleSystem, Text: parseSystem}, {Role: llm.RoleUser, Text: capText(user, maxLLMText)}},
	})
	if err != nil {
		return nil, err
	}
	return decodeParsed(resp.JSON, "llm")
}

func decodeParsed(raw json.RawMessage, via string) (*Draft, error) {
	var p parsed
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("%w: %w", llm.ErrBadOutput, err)
	}
	if !p.IsRecipe {
		return nil, ErrNotARecipe
	}
	d := p.toDraft(via)
	if !d.usable() {
		return nil, ErrNotARecipe
	}
	return d, nil
}

// Photo is one image of a photo import.
type Photo struct {
	Ref  blob.Ref
	MIME string
}

func (s *Service) parsePhotos(ctx context.Context, photos []Photo, prio llm.Priority) (*Draft, error) {
	msg := llm.Message{Role: llm.RoleUser, Text: "The recipe photos:"}
	for _, p := range photos {
		msg.Images = append(msg.Images, llm.Image{Ref: p.Ref, MIME: p.MIME})
	}
	resp, err := s.d.LLM.Complete(ctx, llm.Request{
		Feature: FeaturePhoto, Schema: parseSchema, SchemaName: "recipe", Priority: prio, MaxTokens: 4096,
		Messages: []llm.Message{{Role: llm.RoleSystem, Text: photoSystem}, msg},
	})
	if err != nil {
		return nil, err
	}
	return decodeParsed(resp.JSON, "photo")
}

// Original is version.original: the untranslated recipe (architecture-m2 §3).
type Original struct {
	Lang        string         `json:"lang"`
	Title       string         `json:"title"`
	Subtitle    string         `json:"subtitle,omitempty"`
	Ingredients []string       `json:"ingredients"`
	Steps       []recipes.Step `json:"steps"`
	Notes       string         `json:"notes,omitempty"`
}

// translate returns d in English; d itself is the original.
func (s *Service) translate(ctx context.Context, d *Draft, prio llm.Priority) (*Draft, error) {
	in := map[string]any{
		"lang": d.Lang, "title": d.Title, "subtitle": d.Subtitle, "ingredients": toGroups(d.Ingredients),
		"steps": stepsIn(d.Steps), "notes": d.Notes, "servings": deref(d.Servings), "total_minutes": deref(d.TotalMinutes),
	}
	body, err := json.Marshal(in)
	if err != nil {
		return nil, err
	}
	resp, err := s.d.LLM.Complete(ctx, llm.Request{
		Feature: FeatureTranslate, Schema: translateSchema, SchemaName: "recipe_en", Priority: prio, MaxTokens: 6144,
		Messages: []llm.Message{{Role: llm.RoleSystem, Text: translateSystem}, {Role: llm.RoleUser, Text: string(body)}},
	})
	if err != nil {
		return nil, err
	}
	var p parsed
	if err := json.Unmarshal(resp.JSON, &p); err != nil {
		return nil, fmt.Errorf("%w: %w", llm.ErrBadOutput, err)
	}
	en := p.toDraft(d.Via)
	if en.Title == "" || len(en.Ingredients)+len(en.Steps) == 0 {
		return nil, fmt.Errorf("%w: empty translation", llm.ErrBadOutput)
	}
	// What the translation must not change.
	en.Lang, en.Servings, en.TotalMinutes, en.ImageURL = "en", d.Servings, d.TotalMinutes, d.ImageURL
	if len(en.Tags) == 0 {
		en.Tags = d.Tags
	}
	return en, nil
}

func stepsIn(ss []recipes.Step) []step {
	out := make([]step, 0, len(ss))
	for _, s := range ss {
		out = append(out, step{Text: s.Text, Minutes: deref(s.Minutes)})
	}
	return out
}

func deref(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}
