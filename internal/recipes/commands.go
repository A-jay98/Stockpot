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
	"github.com/a-jay98/stockpot/internal/identity"
	"github.com/a-jay98/stockpot/internal/recipes/lines"
)

// Opt re-exports commands.Opt for payload structs.
type Opt[T any] = commands.Opt[T]

// Content is a version's content in a payload.
type Content struct {
	Title        string   `json:"title"`
	Servings     *int     `json:"servings"`
	TotalMinutes *int     `json:"totalMinutes"`
	Ingredients  []string `json:"ingredients"`
	Steps        []Step   `json:"steps"`
	Notes        *string  `json:"notes"`
}

// Create is recipe.create: the recipe and its version 1.
type Create struct {
	ID         uuid.UUID `json:"id"`
	VersionID  uuid.UUID `json:"versionId"`
	Subtitle   *string   `json:"subtitle"`
	SourceKind *string   `json:"sourceKind"`
	SourceURL  *string   `json:"sourceUrl"`
	SourceLang *string   `json:"sourceLang"`
	Tags       []string  `json:"tags"`
	Content
}

// VersionCreate is recipe.version.create: a new version from edited or
// chat-proposed content.
type VersionCreate struct {
	RecipeID        uuid.UUID  `json:"recipeId"`
	VersionID       uuid.UUID  `json:"versionId"`
	Origin          string     `json:"origin"`
	ParentVersionID *uuid.UUID `json:"parentVersionId"`
	FromMessageID   *uuid.UUID `json:"fromMessageId"`
	ChangeSummary   *string    `json:"changeSummary"`
	Content
}

// VersionRestore is recipe.version.restore: a new version copying an old one.
type VersionRestore struct {
	RecipeID      uuid.UUID `json:"recipeId"`
	VersionID     uuid.UUID `json:"versionId"`
	FromVersionID uuid.UUID `json:"fromVersionId"`
}

// Update is recipe.update (last writer wins per field).
type Update struct {
	ID        uuid.UUID     `json:"id"`
	Tags      Opt[[]string] `json:"tags"`
	Saved     *bool         `json:"saved"`
	CoverBlob Opt[string]   `json:"coverBlob"`
	Subtitle  Opt[string]   `json:"subtitle"`
}

// Delete is recipe.delete.
type Delete struct {
	ID uuid.UUID `json:"id"`
}

// LineSetIngredient is recipe.line.set_ingredient: the user's match pick.
type LineSetIngredient struct {
	VersionID    uuid.UUID  `json:"versionId"`
	LineID       uuid.UUID  `json:"lineId"`
	IngredientID *uuid.UUID `json:"ingredientId"`
}

// VersionSavedHook runs inside recipe.version.create when the version came
// from a chat proposal (m2-7 marks the message saved).
type VersionSavedHook func(ctx context.Context, tx pgx.Tx, household, messageID, versionID uuid.UUID) error

// Handlers are the recipe commands.
type Handlers struct {
	W Writer
	// OnChatVersion, when set, is called for recipe.version.create with a fromMessageId.
	OnChatVersion VersionSavedHook
}

// Register adds the recipe commands to r and returns the handlers, so
// later features can set their hooks.
func Register(r *commands.Registry, res catalog.Resolver) *Handlers {
	h := &Handlers{W: Writer{Res: res}}
	commands.Register(r, "recipe.create", h.create)
	commands.Register(r, "recipe.version.create", h.versionCreate)
	commands.Register(r, "recipe.version.restore", h.versionRestore)
	commands.Register(r, "recipe.update", h.update)
	commands.Register(r, "recipe.delete", h.delete)
	commands.Register(r, "recipe.line.set_ingredient", h.lineSetIngredient)
	commands.Register(r, "recipe.photo.add", h.photoAdd)
	commands.Register(r, "recipe.photo.remove", h.photoRemove)
	return h
}

func user(ctx context.Context) *uuid.UUID {
	if p := identity.PrincipalFrom(ctx); p != nil {
		id := p.UserID
		return &id
	}
	return nil
}

func cleanTags(tags []string) ([]string, error) {
	if len(tags) > 30 {
		return nil, commands.Invalid("at most 30 tags")
	}
	out := []string{}
	seen := map[string]bool{}
	for _, t := range tags {
		t = strings.ToLower(strings.TrimSpace(t))
		if t == "" || seen[t] {
			continue
		}
		if len([]rune(t)) > 40 {
			return nil, commands.Invalid("a tag is longer than 40 characters")
		}
		seen[t] = true
		out = append(out, t)
	}
	return out, nil
}

func optText(field string, s *string, limit int) (*string, error) {
	if s == nil {
		return nil, nil
	}
	t := strings.TrimSpace(*s)
	if t == "" {
		return nil, nil
	}
	if len([]rune(t)) > limit {
		return nil, commands.Invalid("%s is longer than %d characters", field, limit)
	}
	return &t, nil
}

func (h *Handlers) create(ctx context.Context, tx pgx.Tx, p Create) error {
	hh, err := identity.HouseholdID(ctx)
	if err != nil {
		return err
	}
	kind := "manual"
	if p.SourceKind != nil {
		kind = *p.SourceKind
	}
	switch kind {
	case "manual", "url", "text":
	default:
		return commands.Invalid("source kind must be manual, url or text")
	}
	subtitle, err := optText("subtitle", p.Subtitle, 300)
	if err != nil {
		return err
	}
	srcURL, err := optText("source URL", p.SourceURL, 2000)
	if err != nil {
		return err
	}
	if srcURL != nil && !strings.HasPrefix(*srcURL, "https://") && !strings.HasPrefix(*srcURL, "http://") {
		return commands.Invalid("source URL must be http(s)")
	}
	lang, err := optText("source language", p.SourceLang, 35)
	if err != nil {
		return err
	}
	tags, err := cleanTags(p.Tags)
	if err != nil {
		return err
	}
	title := strings.TrimSpace(p.Title)
	if title == "" {
		return commands.Invalid("title must be 1-%d characters", maxTitle)
	}
	tag, err := tx.Exec(ctx, `INSERT INTO recipe (id, household_id, title, subtitle, source_kind, source_url, source_lang, tags)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8) ON CONFLICT (id) DO NOTHING`,
		p.ID, hh, title, subtitle, kind, srcURL, lang, tags)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 { // created by an earlier command: nothing more to do
		return commands.CheckOwned(ctx, tx, "recipe", p.ID, hh)
	}
	_, err = h.W.WriteVersion(ctx, tx, VersionInput{
		ID: p.VersionID, RecipeID: p.ID, Household: hh, Origin: OriginManual,
		Title: p.Title, Servings: p.Servings, TotalMinutes: p.TotalMinutes,
		Ingredients: p.Ingredients, Steps: p.Steps, Notes: p.Notes, CreatedBy: user(ctx),
	})
	return err
}

func (h *Handlers) versionCreate(ctx context.Context, tx pgx.Tx, p VersionCreate) error {
	hh, err := identity.HouseholdID(ctx)
	if err != nil {
		return err
	}
	if p.Origin != OriginManual && p.Origin != OriginChat {
		return commands.Invalid("origin must be manual or chat")
	}
	if p.FromMessageID != nil && p.Origin != OriginChat {
		return commands.Invalid("fromMessageId needs origin chat")
	}
	summary, err := optText("change summary", p.ChangeSummary, maxSummary)
	if err != nil {
		return err
	}
	v, err := h.W.WriteVersion(ctx, tx, VersionInput{
		ID: p.VersionID, RecipeID: p.RecipeID, Household: hh, Origin: p.Origin,
		ParentVersionID: p.ParentVersionID, ChangeSummary: summary,
		Title: p.Title, Servings: p.Servings, TotalMinutes: p.TotalMinutes,
		Ingredients: p.Ingredients, Steps: p.Steps, Notes: p.Notes,
		OriginalVersionID: originalOf(ctx, tx, p.RecipeID), CreatedBy: user(ctx),
	})
	if err != nil {
		return err
	}
	if p.FromMessageID != nil && h.OnChatVersion != nil {
		return h.OnChatVersion(ctx, tx, hh, *p.FromMessageID, v.ID)
	}
	return nil
}

// originalOf is the version holding the recipe's untranslated original, if
// any: later versions point at it so the language toggle keeps working.
func originalOf(ctx context.Context, tx pgx.Tx, recipe uuid.UUID) *uuid.UUID {
	var id *uuid.UUID
	err := tx.QueryRow(ctx, `SELECT coalesce(v.original_version_id, CASE WHEN v.original IS NOT NULL THEN v.id END)
		FROM recipe r JOIN recipe_version v ON v.id = r.current_version_id WHERE r.id = $1`, recipe).Scan(&id)
	if err != nil {
		return nil // no current version yet, or the recipe is gone (WriteVersion reports that)
	}
	return id
}

func (h *Handlers) versionRestore(ctx context.Context, tx pgx.Tx, p VersionRestore) error {
	hh, err := identity.HouseholdID(ctx)
	if err != nil {
		return err
	}
	var (
		n                 int
		title             string
		servings, minutes *int
		ingRaw, stepRaw   []byte
		notes             *string
		original          []byte
		originalVersionID *uuid.UUID
		fromID            = p.FromVersionID
	)
	err = tx.QueryRow(ctx, `SELECT n, title, servings, total_minutes, ingredients, steps, notes, original, original_version_id
		FROM recipe_version WHERE id = $1 AND recipe_id = $2 AND household_id = $3 AND deleted_at IS NULL`,
		p.FromVersionID, p.RecipeID, hh).Scan(&n, &title, &servings, &minutes, &ingRaw, &stepRaw, &notes, &original, &originalVersionID)
	if errors.Is(err, pgx.ErrNoRows) {
		return commands.Gone("that version no longer exists")
	}
	if err != nil {
		return err
	}
	var ls []lines.Line
	var steps []Step
	if err := json.Unmarshal(ingRaw, &ls); err != nil {
		return fmt.Errorf("version %s ingredients: %w", fromID, err)
	}
	if err := json.Unmarshal(stepRaw, &steps); err != nil {
		return fmt.Errorf("version %s steps: %w", fromID, err)
	}
	if ls == nil {
		ls = []lines.Line{}
	}
	if original != nil { // the restored version holds the original itself

		originalVersionID = &fromID
	}
	summary := fmt.Sprintf("Restored v%d", n)
	_, err = h.W.WriteVersion(ctx, tx, VersionInput{
		ID: p.VersionID, RecipeID: p.RecipeID, Household: hh, Origin: OriginRestore,
		ChangeSummary: &summary, Title: title, Servings: servings, TotalMinutes: minutes,
		Lines: ls, Steps: steps, Notes: notes, OriginalVersionID: originalVersionID, CreatedBy: user(ctx),
	})
	return err
}

func (h *Handlers) update(ctx context.Context, tx pgx.Tx, p Update) error {
	hh, err := identity.HouseholdID(ctx)
	if err != nil {
		return err
	}
	var sets []string
	args := []any{p.ID, hh}
	add := func(col string, v any) {
		args = append(args, v)
		sets = append(sets, fmt.Sprintf("%s = $%d", col, len(args)))
	}
	if p.Tags.Set {
		var in []string
		if p.Tags.Value != nil {
			in = *p.Tags.Value
		}
		tags, err := cleanTags(in)
		if err != nil {
			return err
		}
		add("tags", tags)
	}
	if p.Saved != nil {
		add("saved", *p.Saved)
		if *p.Saved {
			sets = append(sets, "idea_expires_at = NULL")
		}
	}
	if p.CoverBlob.Set {
		if v := p.CoverBlob.Value; v != nil {
			if err := requireBlob(ctx, tx, hh, *v); err != nil {
				return err
			}
		}
		add("cover_blob", p.CoverBlob.Value)
	}
	if p.Subtitle.Set {
		s, err := optText("subtitle", p.Subtitle.Value, 300)
		if err != nil {
			return err
		}
		add("subtitle", s)
	}
	q := `UPDATE recipe SET id = id WHERE id = $1 AND household_id = $2 AND deleted_at IS NULL`
	if len(sets) > 0 {
		q = `UPDATE recipe SET ` + strings.Join(sets, ", ") + ` WHERE id = $1 AND household_id = $2 AND deleted_at IS NULL`
	}
	tag, err := tx.Exec(ctx, q, args...)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return commands.Gone("recipe no longer exists")
	}
	return nil
}

// validRef reports whether s looks like a blob ref (hex SHA-256).
func validRef(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

func (h *Handlers) delete(ctx context.Context, tx pgx.Tx, p Delete) error {
	hh, err := identity.HouseholdID(ctx)
	if err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `UPDATE recipe SET deleted_at = now() WHERE id = $1 AND household_id = $2 AND deleted_at IS NULL`, p.ID, hh)
	if err != nil || tag.RowsAffected() == 0 {
		return err // already deleted: nothing to do
	}
	// TODO(m2-4, m2-5, m2-6): also tombstone the recipe's imports, recipe-specific
	// substitutions and cook sessions once those threads write them (and in the client reducer).
	for _, t := range []string{"recipe_version", "recipe_photo"} {
		if _, err := tx.Exec(ctx, `UPDATE `+t+` SET deleted_at = now() WHERE recipe_id = $1 AND household_id = $2 AND deleted_at IS NULL`, p.ID, hh); err != nil {
			return err
		}
	}
	return nil
}

func (h *Handlers) lineSetIngredient(ctx context.Context, tx pgx.Tx, p LineSetIngredient) error {
	hh, err := identity.HouseholdID(ctx)
	if err != nil {
		return err
	}
	var raw []byte
	err = tx.QueryRow(ctx, `SELECT ingredients FROM recipe_version WHERE id = $1 AND household_id = $2 AND deleted_at IS NULL FOR UPDATE`,
		p.VersionID, hh).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return commands.Gone("recipe version no longer exists")
	}
	if err != nil {
		return err
	}
	if p.IngredientID != nil {
		var ok bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM ingredient WHERE id = $1 AND deleted_at IS NULL AND (household_id IS NULL OR household_id = $2))`,
			*p.IngredientID, hh).Scan(&ok); err != nil {
			return err
		}
		if !ok {
			return commands.Invalid("unknown ingredient")
		}
	}
	var ls []lines.Line
	if err := json.Unmarshal(raw, &ls); err != nil {
		return fmt.Errorf("version %s ingredients: %w", p.VersionID, err)
	}
	found := false
	for i := range ls {
		if ls[i].LineID != p.LineID {
			continue
		}
		found = true
		ls[i].IngredientID, ls[i].MatchConfidence = p.IngredientID, nil
		if p.IngredientID != nil {
			one := float32(1)
			ls[i].MatchConfidence = &one
		}
	}
	if !found {
		return commands.Gone("that ingredient line no longer exists")
	}
	if err := markStaples(ctx, tx, hh, ls); err != nil {
		return err
	}
	out, err := json.Marshal(ls)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE recipe_version SET ingredients = $2 WHERE id = $1`, p.VersionID, out)
	return err
}
