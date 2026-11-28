package recipes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/a-jay98/stockpot/internal/audit"
	"github.com/a-jay98/stockpot/internal/jobs"
	"github.com/a-jay98/stockpot/internal/llm"
	"github.com/a-jay98/stockpot/internal/recipes/lines"
	"github.com/a-jay98/stockpot/internal/syncdb"
)

// LLM features (architecture-m2 §12). Both are Interactive: a person taps and waits.
var (
	FeatureSubstitute = llm.Feature{Name: "recipe_substitute", CacheTTL: 7 * 24 * time.Hour}
	FeatureIdeas      = llm.Feature{Name: "recipe_ideas", CacheTTL: 24 * time.Hour}
)

// Limits on what the assistant sends and keeps.
const (
	maxSwaps         = 2
	maxSwapNote      = 300
	maxPromptStock   = 120
	maxIdeas         = 3
	maxIdeaAvoid     = 60
	maxIdeaNote      = 200
	ideaLifetime     = 7 * 24 * time.Hour
	maxSwapRatio     = 20
	maxIdeaLinesSent = 40
)

// Assistant errors the API maps to problems.
var (
	ErrRecipeNotFound = errors.New("recipe not found")
	ErrLineNotFound   = errors.New("ingredient line not found")
	ErrLineUnmatched  = errors.New("the line has no ingredient match yet")
)

// Completer is the part of llm.Router the assistant uses.
type Completer interface {
	Complete(ctx context.Context, req llm.Request) (llm.Response, error)
}

var _ Completer = (*llm.Router)(nil)

// Assistant serves the LLM helpers of "From your kitchen": swaps for a
// missing line and new recipe ideas from what is in stock. Only ingredient
// names and recipe text go to the model.
type Assistant struct {
	Pool *pgxpool.Pool
	LLM  Completer
	W    Writer
	// Now is the clock (tests pin it); nil means time.Now.
	Now func() time.Time
}

func (a *Assistant) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now()
}

// stocked is an ingredient the household has in stock.
type stocked struct {
	ID   uuid.UUID
	Slug string
	Name string
	// Expires is the soonest date among its live stock items, if any.
	Expires *time.Time
}

// inStock lists the ingredients of the household's live stock (each
// product's ingredient and extra ingredients), soonest expiry first.
func inStock(ctx context.Context, q pgx.Tx, hh uuid.UUID) ([]stocked, error) {
	rows, err := q.Query(ctx, `
		SELECT i.id, i.slug, i.name, min(s.expires_on)
		FROM stock_item s
		JOIN product p ON p.id = s.product_id AND p.deleted_at IS NULL
		CROSS JOIN LATERAL unnest(array_prepend(p.ingredient_id, p.extra_ingredient_ids)) AS ing(id)
		JOIN ingredient i ON i.id = ing.id AND i.deleted_at IS NULL
		WHERE s.household_id = $1 AND s.deleted_at IS NULL
		  AND (s.quantity IS NULL OR s.quantity > 0) AND (s.amount_kind <> 'portions' OR s.portions > 0)
		GROUP BY i.id, i.slug, i.name
		ORDER BY min(s.expires_on) NULLS LAST, i.name`, hh)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []stocked
	for rows.Next() {
		var s stocked
		if err := rows.Scan(&s.ID, &s.Slug, &s.Name, &s.Expires); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// kindsOf is the ingredient and every kind of it ("is a kind of" downwards).
func kindsOf(ctx context.Context, q pgx.Tx, hh, id uuid.UUID) (map[uuid.UUID]bool, error) {
	rows, err := q.Query(ctx, `WITH RECURSIVE k AS (
			SELECT id FROM ingredient WHERE id = $2
			UNION
			SELECT i.id FROM ingredient i JOIN k ON i.parent_id = k.id
			WHERE i.deleted_at IS NULL AND (i.household_id IS NULL OR i.household_id = $1)
		) SELECT id FROM k`, hh, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[uuid.UUID]bool{}
	for rows.Next() {
		var k uuid.UUID
		if err := rows.Scan(&k); err != nil {
			return nil, err
		}
		out[k] = true
	}
	return out, rows.Err()
}

// SwapResult is a swap the assistant saved.
type SwapResult struct {
	ID   uuid.UUID
	Note string
}

const substituteSystem = `You help a home cook who is missing one ingredient of a recipe.
Suggest at most 2 substitutions made ONLY from the ingredients in "in_stock" (use their slugs exactly; salt and water may also be used if listed).
For each use, "ratio" is the amount of that ingredient per 1 unit of the missing ingredient's amount (1 = same amount), and "unit" is a unit when the ratio is in a different unit (e.g. "tbsp"), else "".
"note" is one short sentence the cook reads, like "Use 200 g Greek yogurt + 100 mL water, stirred in off the heat." Mention amounts for this recipe.
If nothing in stock works, return an empty "swaps" list. Never invent ingredients.`

var substituteSchema = json.RawMessage(`{
  "type": "object",
  "additionalProperties": false,
  "required": ["swaps"],
  "properties": {
    "swaps": {
      "type": "array",
      "maxItems": 2,
      "items": {
        "type": "object",
        "additionalProperties": false,
        "required": ["uses", "note"],
        "properties": {
          "uses": {
            "type": "array",
            "minItems": 1,
            "maxItems": 4,
            "items": {
              "type": "object",
              "additionalProperties": false,
              "required": ["slug", "ratio", "unit"],
              "properties": {
                "slug": {"type": "string"},
                "ratio": {"type": "number"},
                "unit": {"type": "string"}
              }
            }
          },
          "note": {"type": "string"}
        }
      }
    }
  }
}`)

// Substitute asks the model for swaps for one line of a recipe version (the
// current one when version is nil) using only what is in stock, and saves
// them as household substitution rows for that recipe (replacing earlier
// model answers for the same ingredient). The result can be empty.
func (a *Assistant) Substitute(ctx context.Context, hh, recipe uuid.UUID, version *uuid.UUID, lineID uuid.UUID) ([]SwapResult, error) {
	tx, err := a.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var (
		title string
		vid   uuid.UUID
		raw   []byte
	)
	err = tx.QueryRow(ctx, `SELECT r.title, v.id, v.ingredients FROM recipe r
		JOIN recipe_version v ON v.recipe_id = r.id AND v.id = coalesce($3, r.current_version_id) AND v.deleted_at IS NULL
		WHERE r.id = $1 AND r.household_id = $2 AND r.deleted_at IS NULL`, recipe, hh, version).Scan(&title, &vid, &raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrRecipeNotFound
	}
	if err != nil {
		return nil, err
	}
	var ls []lines.Line
	if err := json.Unmarshal(raw, &ls); err != nil {
		return nil, fmt.Errorf("version %s ingredients: %w", vid, err)
	}
	i := slices.IndexFunc(ls, func(l lines.Line) bool { return l.LineID == lineID })
	if i < 0 {
		return nil, ErrLineNotFound
	}
	line := ls[i]
	if line.IngredientID == nil {
		return nil, ErrLineUnmatched
	}
	kinds, err := kindsOf(ctx, tx, hh, *line.IngredientID)
	if err != nil {
		return nil, err
	}
	stock, err := inStock(ctx, tx, hh)
	if err != nil {
		return nil, err
	}
	staples, err := stapleIngredients(ctx, tx, hh)
	if err != nil {
		return nil, err
	}
	avail := map[string]uuid.UUID{}
	var names []string
	for _, s := range stock {
		if kinds[s.ID] || len(avail) >= maxPromptStock {
			continue
		}
		avail[s.Slug] = s.ID
		names = append(names, s.Slug+" ("+s.Name+")")
	}
	if err := addStaples(ctx, tx, staples, avail, &names); err != nil {
		return nil, err
	}
	if len(avail) == 0 {
		return []SwapResult{}, nil
	}
	slices.Sort(names) // a stable prompt is a cacheable prompt

	raws := make([]string, 0, len(ls))
	for _, l := range ls {
		raws = append(raws, l.Raw)
	}
	prompt, _ := json.Marshal(struct {
		Version     uuid.UUID `json:"version"`
		Recipe      string    `json:"recipe"`
		Ingredients []string  `json:"ingredients"`
		Missing     string    `json:"missing"`
		InStock     []string  `json:"in_stock"`
	}{vid, title, raws, line.Raw, names})
	// Don't hold a connection (or a lock) while the model thinks.
	_ = tx.Rollback(ctx)
	resp, err := a.LLM.Complete(ctx, llm.Request{
		Feature: FeatureSubstitute, Schema: substituteSchema, SchemaName: "recipe_substitute", MaxTokens: 800,
		Messages: []llm.Message{{Role: llm.RoleSystem, Text: substituteSystem}, {Role: llm.RoleUser, Text: string(prompt)}},
	})
	if err != nil {
		return nil, err
	}
	var reply struct {
		Swaps []struct {
			Uses []struct {
				Slug  string  `json:"slug"`
				Ratio float64 `json:"ratio"`
				Unit  string  `json:"unit"`
			} `json:"uses"`
			Note string `json:"note"`
		} `json:"swaps"`
	}
	if err := json.Unmarshal(resp.JSON, &reply); err != nil {
		return nil, fmt.Errorf("%w: %w", llm.ErrBadOutput, err)
	}

	if tx, err = a.Pool.Begin(ctx); err != nil {
		return nil, err
	}
	if err := syncdb.LockHousehold(ctx, tx, hh); err != nil {
		return nil, err
	}
	var live bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM recipe WHERE id = $1 AND household_id = $2 AND deleted_at IS NULL)`, recipe, hh).Scan(&live); err != nil {
		return nil, err
	}
	if !live {
		return nil, ErrRecipeNotFound // deleted while the model was answering
	}
	if _, err := tx.Exec(ctx, `UPDATE substitution SET deleted_at = now()
		WHERE household_id = $1 AND recipe_id = $2 AND ingredient_id = $3 AND source = 'llm' AND deleted_at IS NULL`,
		hh, recipe, *line.IngredientID); err != nil {
		return nil, err
	}
	out := []SwapResult{}
	for _, s := range reply.Swaps {
		if len(out) == maxSwaps {
			break
		}
		note := strings.TrimSpace(s.Note)
		if note == "" || len([]rune(note)) > maxSwapNote {
			continue
		}
		uses := make([]Use, 0, len(s.Uses))
		for _, u := range s.Uses {
			id, ok := avail[u.Slug]
			if !ok || u.Ratio <= 0 || u.Ratio > maxSwapRatio || len(u.Unit) > 20 {
				uses = nil // anything not in stock (or nonsense) voids the swap
				break
			}
			uses = append(uses, Use{IngredientID: id, Ratio: u.Ratio, Unit: strings.TrimSpace(u.Unit)})
		}
		if len(uses) == 0 {
			continue
		}
		usesJSON, _ := json.Marshal(uses)
		id := uuid.Must(uuid.NewV7())
		if _, err := tx.Exec(ctx, `INSERT INTO substitution (id, household_id, ingredient_id, uses, note, recipe_id, source)
			VALUES ($1, $2, $3, $4, $5, $6, 'llm')`, id, hh, *line.IngredientID, usesJSON, note, recipe); err != nil {
			return nil, err
		}
		out = append(out, SwapResult{ID: id, Note: note})
	}
	if err := audit.Record(ctx, tx, "recipe.substitute", audit.Target{Type: "recipe", ID: recipe.String()}, nil,
		map[string]any{"versionId": vid, "lineId": lineID, "swaps": out, "provider": resp.Provider}); err != nil {
		return nil, err
	}
	return out, tx.Commit(ctx)
}

// addStaples makes the assumed staples (water, salt, …) available to swaps.
func addStaples(ctx context.Context, tx pgx.Tx, staples map[uuid.UUID]bool, avail map[string]uuid.UUID, names *[]string) error {
	if len(staples) == 0 {
		return nil
	}
	ids := make([]uuid.UUID, 0, len(staples))
	for id := range staples {
		ids = append(ids, id)
	}
	rows, err := tx.Query(ctx, `SELECT id, slug, name FROM ingredient WHERE id = ANY($1)`, ids)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id uuid.UUID
		var slug, name string
		if err := rows.Scan(&id, &slug, &name); err != nil {
			return err
		}
		if _, ok := avail[slug]; !ok {
			avail[slug] = id
			*names = append(*names, slug+" ("+name+")")
		}
	}
	return rows.Err()
}

const ideasSystem = `You suggest dinner recipes for a home cook from what is in their kitchen.
Give up to 3 different recipes that use mostly ingredients from "in_stock", favouring the ones listed in "use_soon" (they expire first). Common pantry staples (salt, pepper, oil, water) may be assumed.
Do not repeat any recipe in "avoid". Follow "note" if present (diet, cuisine, time).
Each recipe: "title" (short), "subtitle" (one line), "servings", "total_minutes", "ingredients" (one line each with amount and unit, like "2 tbsp olive oil", "800 g chicken thighs, diced"), "steps" (short imperative steps).`

var ideasSchema = json.RawMessage(`{
  "type": "object",
  "additionalProperties": false,
  "required": ["ideas"],
  "properties": {
    "ideas": {
      "type": "array",
      "maxItems": 3,
      "items": {
        "type": "object",
        "additionalProperties": false,
        "required": ["title", "subtitle", "servings", "total_minutes", "ingredients", "steps"],
        "properties": {
          "title": {"type": "string"},
          "subtitle": {"type": "string"},
          "servings": {"type": "integer"},
          "total_minutes": {"type": "integer"},
          "ingredients": {"type": "array", "items": {"type": "string"}},
          "steps": {"type": "array", "items": {"type": "string"}}
        }
      }
    }
  }
}`)

// Ideas asks the model for up to three recipes from what is in stock and
// creates them as unsaved ideas (source idea, expiring in 7 days unless
// saved) through WriteVersion. It returns the new recipes' ids, which can be
// empty when the kitchen is empty or the model had nothing new.
func (a *Assistant) Ideas(ctx context.Context, hh uuid.UUID, user *uuid.UUID, note string) ([]uuid.UUID, error) {
	note = strings.TrimSpace(note)
	if len([]rune(note)) > maxIdeaNote {
		note = string([]rune(note)[:maxIdeaNote])
	}
	tx, err := a.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	stock, err := inStock(ctx, tx, hh)
	if err != nil {
		return nil, err
	}
	if len(stock) == 0 {
		return []uuid.UUID{}, nil
	}
	now := a.now()
	soonBy := now.AddDate(0, 0, 4)
	var have, soon []string
	for _, s := range stock {
		if len(have) == maxPromptStock {
			break
		}
		have = append(have, s.Name)
		if s.Expires != nil && s.Expires.Before(soonBy) {
			soon = append(soon, s.Name)
		}
	}
	slices.Sort(have)
	slices.Sort(soon)
	avoid, err := liveTitles(ctx, tx, hh)
	if err != nil {
		return nil, err
	}
	prompt, _ := json.Marshal(struct {
		InStock []string `json:"in_stock"`
		UseSoon []string `json:"use_soon"`
		Avoid   []string `json:"avoid"`
		Note    string   `json:"note,omitempty"`
	}{have, soon, avoid, note})
	_ = tx.Rollback(ctx)
	resp, err := a.LLM.Complete(ctx, llm.Request{
		Feature: FeatureIdeas, Schema: ideasSchema, SchemaName: "recipe_ideas", MaxTokens: 3000,
		Messages: []llm.Message{{Role: llm.RoleSystem, Text: ideasSystem}, {Role: llm.RoleUser, Text: string(prompt)}},
	})
	if err != nil {
		return nil, err
	}
	var reply struct {
		Ideas []struct {
			Title        string   `json:"title"`
			Subtitle     string   `json:"subtitle"`
			Servings     int      `json:"servings"`
			TotalMinutes int      `json:"total_minutes"`
			Ingredients  []string `json:"ingredients"`
			Steps        []string `json:"steps"`
		} `json:"ideas"`
	}
	if err := json.Unmarshal(resp.JSON, &reply); err != nil {
		return nil, fmt.Errorf("%w: %w", llm.ErrBadOutput, err)
	}

	if tx, err = a.Pool.Begin(ctx); err != nil {
		return nil, err
	}
	if err := syncdb.LockHousehold(ctx, tx, hh); err != nil {
		return nil, err
	}
	if avoid, err = liveTitles(ctx, tx, hh); err != nil { // ideas made meanwhile count too
		return nil, err
	}
	seen := map[string]bool{}
	for _, t := range avoid {
		seen[strings.ToLower(t)] = true
	}
	expires := now.Add(ideaLifetime)
	out := []uuid.UUID{}
	for _, idea := range reply.Ideas {
		if len(out) == maxIdeas {
			break
		}
		in := VersionInput{
			ID: uuid.Must(uuid.NewV7()), RecipeID: uuid.Must(uuid.NewV7()), Household: hh, Origin: OriginIdea,
			Title: idea.Title, Ingredients: clip(idea.Ingredients, maxIdeaLinesSent, maxLineLen), CreatedBy: user,
		}
		if idea.Servings >= 1 && idea.Servings <= maxServings {
			in.Servings = &idea.Servings
		}
		if idea.TotalMinutes > 0 && idea.TotalMinutes <= maxMinutes {
			in.TotalMinutes = &idea.TotalMinutes
		}
		for _, s := range clip(idea.Steps, maxSteps, maxStepLen) {
			in.Steps = append(in.Steps, Step{Text: s})
		}
		if err := validate(&in); err != nil || len(in.Ingredients) == 0 || seen[strings.ToLower(in.Title)] {
			continue // a malformed or repeated idea is dropped, not fatal
		}
		seen[strings.ToLower(in.Title)] = true
		var subtitle *string
		if s := strings.TrimSpace(idea.Subtitle); s != "" {
			s = string([]rune(s)[:min(len([]rune(s)), 300)])
			subtitle = &s
		}
		if _, err := tx.Exec(ctx, `INSERT INTO recipe (id, household_id, title, subtitle, source_kind, saved, idea_expires_at, tags)
			VALUES ($1, $2, $3, $4, 'idea', false, $5, '{}')`, in.RecipeID, hh, in.Title, subtitle, expires); err != nil {
			return nil, err
		}
		if _, err := a.W.WriteVersion(ctx, tx, in); err != nil {
			return nil, err
		}
		out = append(out, in.RecipeID)
	}
	if err := audit.Record(ctx, tx, "recipe.ideas", audit.Target{Type: "household", ID: hh.String()}, nil,
		map[string]any{"recipes": out, "provider": resp.Provider, "cached": resp.Cached}); err != nil {
		return nil, err
	}
	return out, tx.Commit(ctx)
}

// clip keeps at most n non-empty trimmed strings of at most size runes.
func clip(in []string, n, size int) []string {
	out := make([]string, 0, min(len(in), n))
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" || len([]rune(s)) > size {
			continue
		}
		if out = append(out, s); len(out) == n {
			break
		}
	}
	return out
}

// liveTitles are the titles the household already has (saved recipes and
// live ideas), so "More ideas" asks for something new.
func liveTitles(ctx context.Context, tx pgx.Tx, hh uuid.UUID) ([]string, error) {
	rows, err := tx.Query(ctx, `SELECT title FROM recipe WHERE household_id = $1 AND deleted_at IS NULL
		ORDER BY saved, created_at DESC LIMIT $2`, hh, maxIdeaAvoid)
	if err != nil {
		return nil, err
	}
	titles, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, err
	}
	slices.Sort(titles)
	return titles, nil
}

// ExpireIdeas tombstones unsaved ideas past their expiry, with their
// versions and recipe-specific swaps, one household lock at a time.
func ExpireIdeas(ctx context.Context, pool *pgxpool.Pool, now time.Time) (int64, error) {
	rows, err := pool.Query(ctx, `SELECT DISTINCT household_id FROM recipe
		WHERE source_kind = 'idea' AND NOT saved AND idea_expires_at < $1 AND deleted_at IS NULL`, now)
	if err != nil {
		return 0, err
	}
	hhs, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
	if err != nil {
		return 0, err
	}
	var n int64
	for _, hh := range hhs {
		err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
			if err := syncdb.LockHousehold(ctx, tx, hh); err != nil {
				return err
			}
			rows, err := tx.Query(ctx, `UPDATE recipe SET deleted_at = now()
				WHERE household_id = $1 AND source_kind = 'idea' AND NOT saved AND idea_expires_at < $2 AND deleted_at IS NULL
				RETURNING id`, hh, now)
			if err != nil {
				return err
			}
			ids, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
			if err != nil {
				return err
			}
			for _, t := range []string{"recipe_version", "recipe_photo", "substitution"} {
				if _, err := tx.Exec(ctx, `UPDATE `+t+` SET deleted_at = now() WHERE recipe_id = ANY($1) AND household_id = $2 AND deleted_at IS NULL`, ids, hh); err != nil {
					return err
				}
			}
			n += int64(len(ids))
			return nil
		})
		if err != nil {
			return n, err
		}
	}
	return n, nil
}

// Sweepers are the recipe jobs run by the daily retention pass.
func Sweepers(pool *pgxpool.Pool) []jobs.Sweeper {
	return []jobs.Sweeper{{
		Name:  "recipe_ideas",
		Sweep: func(ctx context.Context, now time.Time) (int64, error) { return ExpireIdeas(ctx, pool, now) },
	}}
}
