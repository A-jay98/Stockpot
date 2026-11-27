// Package recipes holds saved recipes: immutable versions written only by
// WriteVersion, and the recipe sync commands (architecture-m2 §3–§5).
package recipes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/a-jay98/stockpot/internal/catalog"
	"github.com/a-jay98/stockpot/internal/commands"
	"github.com/a-jay98/stockpot/internal/recipes/lines"
	"github.com/a-jay98/stockpot/internal/settings"
)

// Version origins.
const (
	OriginImport  = "import"
	OriginManual  = "manual"
	OriginChat    = "chat"
	OriginRestore = "restore"
	OriginIdea    = "idea"
)

// Limits on version content.
const (
	maxTitle       = 200
	maxLines       = 200
	maxLineLen     = 500
	maxSteps       = 100
	maxStepLen     = 4000
	maxNotes       = 10000
	maxServings    = 100
	maxMinutes     = 10000
	maxSummary     = 500
	maxStepMins    = 10000
	defaultStaples = "water,salt,black-pepper,ice"
)

// AssumedStaples are ingredient slugs every kitchen is assumed to have: they
// never count as missing and are never deducted. Descendants count too.
var AssumedStaples = settings.DefineJSON(settings.JSONDef[[]string]{
	Key:         "recipes.assumed_staples",
	Default:     strings.Split(defaultStaples, ","),
	Description: "Ingredient slugs assumed to be in every kitchen (never missing, never deducted).",
	Scope:       settings.ScopeHousehold,
	Validate: func(v []string) error {
		if len(v) > 100 {
			return errors.New("at most 100 staples")
		}
		for _, s := range v {
			if s == "" || len(s) > 100 || strings.ToLower(s) != s || strings.ContainsAny(s, " \t") {
				return fmt.Errorf("invalid ingredient slug %q", s)
			}
		}
		return nil
	},
})

// Step is one method step.
type Step struct {
	Text    string `json:"text"`
	Minutes *int   `json:"minutes,omitempty"`
}

// VersionInput is a new version's content. Ingredients are raw lines
// (section headers included) unless Lines is set, which restore uses to copy
// an old version's lines with their ids and matches.
type VersionInput struct {
	ID                uuid.UUID
	RecipeID          uuid.UUID
	Household         uuid.UUID
	Origin            string
	ParentVersionID   *uuid.UUID // nil: the recipe's current version
	ChangeSummary     *string
	Title             string
	Servings          *int
	TotalMinutes      *int
	Ingredients       []string
	Lines             []lines.Line
	Steps             []Step
	Notes             *string
	Original          json.RawMessage // {lang, title, ingredients, steps, notes} for translated imports
	OriginalVersionID *uuid.UUID
	CreatedBy         *uuid.UUID
}

// Version is what WriteVersion wrote.
type Version struct {
	ID    uuid.UUID
	N     int
	Lines []lines.Line
}

// Writer writes versions; its resolver links line names to ingredients.
type Writer struct{ Res catalog.Resolver }

// WriteVersion is the only writer of recipe_version: it validates, parses
// and resolves the lines, assigns n, inserts the snapshot and moves the
// recipe's current version and denormalized fields. tx must hold the
// household lock (syncdb.LockHousehold); the recipe must exist. Content
// problems are commands.Error rejections.
func (w Writer) WriteVersion(ctx context.Context, tx pgx.Tx, in VersionInput) (Version, error) {
	if err := validate(&in); err != nil {
		return Version{}, err
	}
	var current *uuid.UUID
	err := tx.QueryRow(ctx, `SELECT current_version_id FROM recipe WHERE id = $1 AND household_id = $2 AND deleted_at IS NULL FOR UPDATE`,
		in.RecipeID, in.Household).Scan(&current)
	if errors.Is(err, pgx.ErrNoRows) {
		return Version{}, commands.Gone("recipe no longer exists")
	}
	if err != nil {
		return Version{}, err
	}
	parent := current
	if in.ParentVersionID != nil {
		parent = in.ParentVersionID
	}
	var prev []lines.Line
	if parent != nil {
		var raw []byte
		err := tx.QueryRow(ctx, `SELECT ingredients FROM recipe_version WHERE id = $1 AND recipe_id = $2`, *parent, in.RecipeID).Scan(&raw)
		if errors.Is(err, pgx.ErrNoRows) {
			return Version{}, commands.Invalid("parent version is not a version of this recipe")
		}
		if err != nil {
			return Version{}, err
		}
		if err := json.Unmarshal(raw, &prev); err != nil {
			return Version{}, fmt.Errorf("recipe_version %s ingredients: %w", *parent, err)
		}
	}

	ls := in.Lines
	if ls == nil {
		ls = carryOver(lines.All(in.Ingredients), prev)
	}
	if err := w.resolve(ctx, tx, in.Household, ls); err != nil {
		return Version{}, err
	}
	if err := markStaples(ctx, tx, in.Household, ls); err != nil {
		return Version{}, err
	}
	if ls == nil {
		ls = []lines.Line{}
	}
	steps := in.Steps
	if steps == nil {
		steps = []Step{}
	}
	ingJSON, err := json.Marshal(ls)
	if err != nil {
		return Version{}, err
	}
	stepJSON, err := json.Marshal(steps)
	if err != nil {
		return Version{}, err
	}
	var original any
	if len(in.Original) > 0 && string(in.Original) != "null" {
		original = in.Original
	}

	v := Version{ID: in.ID, Lines: ls}
	err = tx.QueryRow(ctx, `INSERT INTO recipe_version (id, household_id, recipe_id, n, parent_version_id, origin, change_summary,
			title, servings, total_minutes, ingredients, steps, notes, original, original_version_id, created_by)
		VALUES ($1, $2, $3, (SELECT coalesce(max(n), 0) + 1 FROM recipe_version WHERE recipe_id = $3), $4, $5, $6,
			$7, $8, $9, $10, $11, $12, $13, $14, $15)
		RETURNING n`,
		in.ID, in.Household, in.RecipeID, parent, in.Origin, in.ChangeSummary,
		in.Title, in.Servings, in.TotalMinutes, ingJSON, stepJSON, in.Notes, original, in.OriginalVersionID, in.CreatedBy).Scan(&v.N)
	if err != nil {
		return Version{}, err
	}
	_, err = tx.Exec(ctx, `UPDATE recipe SET current_version_id = $2, title = $3, servings = $4, total_minutes = $5 WHERE id = $1`,
		in.RecipeID, in.ID, in.Title, in.Servings, in.TotalMinutes)
	return v, err
}

func validate(in *VersionInput) error {
	switch in.Origin {
	case OriginImport, OriginManual, OriginChat, OriginRestore, OriginIdea:
	default:
		return fmt.Errorf("recipes: unknown origin %q", in.Origin)
	}
	in.Title = strings.TrimSpace(in.Title)
	if in.Title == "" || len([]rune(in.Title)) > maxTitle {
		return commands.Invalid("title must be 1-%d characters", maxTitle)
	}
	if in.Servings != nil && (*in.Servings < 1 || *in.Servings > maxServings) {
		return commands.Invalid("servings must be 1-%d", maxServings)
	}
	if in.TotalMinutes != nil && (*in.TotalMinutes < 0 || *in.TotalMinutes > maxMinutes) {
		return commands.Invalid("total minutes must be 0-%d", maxMinutes)
	}
	if len(in.Ingredients) > maxLines || len(in.Lines) > maxLines {
		return commands.Invalid("at most %d ingredient lines", maxLines)
	}
	for _, l := range in.Ingredients {
		if len([]rune(l)) > maxLineLen {
			return commands.Invalid("an ingredient line is longer than %d characters", maxLineLen)
		}
	}
	if len(in.Steps) > maxSteps {
		return commands.Invalid("at most %d steps", maxSteps)
	}
	kept := in.Steps[:0]
	for _, s := range in.Steps {
		s.Text = strings.TrimSpace(s.Text)
		if s.Text == "" {
			continue
		}
		if len([]rune(s.Text)) > maxStepLen {
			return commands.Invalid("a step is longer than %d characters", maxStepLen)
		}
		if s.Minutes != nil && (*s.Minutes < 0 || *s.Minutes > maxStepMins) {
			return commands.Invalid("step minutes must be 0-%d", maxStepMins)
		}
		kept = append(kept, s)
	}
	in.Steps = kept
	if in.Notes != nil && len([]rune(*in.Notes)) > maxNotes {
		return commands.Invalid("notes are longer than %d characters", maxNotes)
	}
	if in.ChangeSummary != nil && len([]rune(*in.ChangeSummary)) > maxSummary {
		return commands.Invalid("change summary is longer than %d characters", maxSummary)
	}
	return nil
}

// carryOver keeps the id and match of every line whose text (and group) is
// unchanged from the parent version, so a user's match pick survives edits.
// Other lines get new ids.
func carryOver(ls, prev []lines.Line) []lines.Line {
	type key struct{ group, raw string }
	old := map[key][]lines.Line{}
	for _, p := range prev {
		k := key{p.Group, p.Raw}
		old[k] = append(old[k], p)
	}
	for i := range ls {
		k := key{ls[i].Group, ls[i].Raw}
		if ps := old[k]; len(ps) > 0 {
			ls[i].LineID, ls[i].IngredientID, ls[i].MatchConfidence = ps[0].LineID, ps[0].IngredientID, ps[0].MatchConfidence
			old[k] = ps[1:]
			continue
		}
		ls[i].LineID = uuid.Must(uuid.NewV7())
	}
	return ls
}

func (w Writer) resolve(ctx context.Context, tx pgx.Tx, hh uuid.UUID, ls []lines.Line) error {
	for i := range ls {
		if ls[i].IngredientID != nil || ls[i].Name == "" {
			continue
		}
		m, err := w.Res.Resolve(ctx, tx, hh, ls[i].Name, nil)
		if err != nil {
			return fmt.Errorf("resolve %q: %w", ls[i].Name, err)
		}
		if m != nil {
			id, c := m.IngredientID, m.Confidence
			ls[i].IngredientID, ls[i].MatchConfidence = &id, &c
		}
	}
	return nil
}

// markStaples sets Staple on lines matched to an assumed staple or a kind of one.
func markStaples(ctx context.Context, tx pgx.Tx, hh uuid.UUID, ls []lines.Line) error {
	staples, err := stapleIngredients(ctx, tx, hh)
	if err != nil {
		return err
	}
	for i := range ls {
		ls[i].Staple = ls[i].IngredientID != nil && staples[*ls[i].IngredientID]
	}
	return nil
}

func stapleIngredients(ctx context.Context, tx pgx.Tx, hh uuid.UUID) (map[uuid.UUID]bool, error) {
	slugs, err := AssumedStaples.ForHousehold(ctx, tx, hh)
	if err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `WITH RECURSIVE s AS (
			SELECT id FROM ingredient WHERE slug = ANY($1) AND deleted_at IS NULL AND (household_id IS NULL OR household_id = $2)
			UNION
			SELECT i.id FROM ingredient i JOIN s ON i.parent_id = s.id WHERE i.deleted_at IS NULL AND (i.household_id IS NULL OR i.household_id = $2)
		) SELECT id FROM s`, slugs, hh)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[uuid.UUID]bool{}
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}
