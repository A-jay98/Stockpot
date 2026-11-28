// Package chat is the per-recipe chat (architecture-m2 §10): the household
// asks about a recipe or says what they changed, and the assistant answers
// and may propose a new version. Messages are plain REST (not synced). A
// proposal is saved only by the client sending recipe.version.create with
// fromMessageId, whose hook (MarkSaved) marks the message saved.
package chat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/a-jay98/stockpot/internal/audit"
	"github.com/a-jay98/stockpot/internal/commands"
	"github.com/a-jay98/stockpot/internal/llm"
	"github.com/a-jay98/stockpot/internal/recipes"
	"github.com/a-jay98/stockpot/internal/recipes/lines"
)

// Feature meters chat calls. Answers depend on the conversation, so they are
// never cached.
var Feature = llm.Feature{Name: "recipes_chat"}

// Roles of a message.
const (
	RoleUser      = "user"
	RoleAssistant = "assistant"
)

// Proposal states.
const (
	StateNone      = "none"
	StatePending   = "pending"
	StateSaved     = "saved"
	StateDismissed = "dismissed"
)

// Limits.
const (
	MaxText      = 2000 // characters in a user message
	historyLimit = 20   // earlier messages sent as context
	listLimit    = 200  // messages GET returns (the newest)
	cookNotes    = 10   // newest cook notes sent as context
	maxTokens    = 4096
)

// Errors the API maps to problems.
var (
	ErrNotFound = errors.New("recipe or message not found")
	ErrInvalid  = errors.New("invalid chat message")
	// ErrSaved means a saved proposal can no longer be dismissed.
	ErrSaved = errors.New("the proposal is already saved")
)

// Proposal is what the assistant would change: only the fields set change.
// It is applied to the recipe's current version when saved (Apply), so a
// proposal made on an older version keeps every later change it doesn't
// touch. Ingredients and Steps, when set, are complete lists. Notes "" clears
// the notes.
type Proposal struct {
	ChangeSummary string          `json:"changeSummary"`
	Title         *string         `json:"title,omitempty"`
	Servings      *int            `json:"servings,omitempty"`
	TotalMinutes  *int            `json:"totalMinutes,omitempty"`
	Ingredients   *[]string       `json:"ingredients,omitempty"`
	Steps         *[]recipes.Step `json:"steps,omitempty"`
	Notes         *string         `json:"notes,omitempty"`
}

// Apply returns c with the proposed changes.
func (p Proposal) Apply(c recipes.Content) recipes.Content {
	if p.Title != nil {
		c.Title = *p.Title
	}
	if p.Servings != nil {
		c.Servings = p.Servings
	}
	if p.TotalMinutes != nil {
		c.TotalMinutes = p.TotalMinutes
	}
	if p.Ingredients != nil {
		c.Ingredients = *p.Ingredients
	}
	if p.Steps != nil {
		c.Steps = *p.Steps
	}
	if p.Notes != nil {
		c.Notes = p.Notes
		if *p.Notes == "" {
			c.Notes = nil
		}
	}
	return c
}

// Message is one chat message.
type Message struct {
	ID        uuid.UUID
	RecipeID  uuid.UUID
	VersionID uuid.UUID // the recipe's current version when it was written
	VersionN  int
	Role      string
	Text      string
	Proposal  *Proposal
	State     string
	// ProposalError says why a change the assistant described could not be
	// offered as a version (it broke a version rule).
	ProposalError *string
	// SavedVersionID is the version a saved proposal became (nil if that
	// version was since deleted).
	SavedVersionID *uuid.UUID
	SavedVersionN  *int
	UserID         *uuid.UUID
	CreatedAt      time.Time
}

// Completer is the part of llm.Router chat uses.
type Completer interface {
	Complete(ctx context.Context, req llm.Request) (llm.Response, error)
}

var _ Completer = (*llm.Router)(nil)

// Service serves the chat endpoints.
type Service struct {
	Pool *pgxpool.Pool
	LLM  Completer
}

const selectMessage = `SELECT m.id, m.recipe_id, m.version_id, v.n, m.role, m.text, m.proposal, m.proposal_state, m.proposal_error,
		m.saved_version_id, sv.n, m.user_id, m.created_at
	FROM recipe_chat_message m
	JOIN recipe_version v ON v.id = m.version_id
	LEFT JOIN recipe_version sv ON sv.id = m.saved_version_id AND sv.deleted_at IS NULL`

func scanMessage(row pgx.Row) (Message, error) {
	var (
		m   Message
		raw []byte
	)
	err := row.Scan(&m.ID, &m.RecipeID, &m.VersionID, &m.VersionN, &m.Role, &m.Text, &raw, &m.State, &m.ProposalError,
		&m.SavedVersionID, &m.SavedVersionN, &m.UserID, &m.CreatedAt)
	if err != nil {
		return m, err
	}
	if m.SavedVersionN == nil {
		m.SavedVersionID = nil
	}
	if raw != nil {
		m.Proposal = &Proposal{}
		if err := json.Unmarshal(raw, m.Proposal); err != nil {
			return m, fmt.Errorf("recipe_chat_message %s proposal: %w", m.ID, err)
		}
	}
	return m, nil
}

// recipeExists reports whether the household has the (live) recipe.
func recipeExists(ctx context.Context, q pgx.Tx, hh, recipe uuid.UUID) error {
	var ok bool
	err := q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM recipe WHERE id = $1 AND household_id = $2 AND deleted_at IS NULL)`,
		recipe, hh).Scan(&ok)
	if err != nil {
		return err
	}
	if !ok {
		return ErrNotFound
	}
	return nil
}

// List returns the recipe's messages, oldest first (the newest listLimit).
func (s *Service) List(ctx context.Context, hh, recipe uuid.UUID) ([]Message, error) {
	var out []Message
	err := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		if err := recipeExists(ctx, tx, hh, recipe); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT * FROM (`+selectMessage+`
			WHERE m.recipe_id = $1 AND m.household_id = $2
			ORDER BY m.created_at DESC, m.id DESC LIMIT $3) t ORDER BY created_at, id`, recipe, hh, listLimit)
		if err != nil {
			return err
		}
		out, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (Message, error) { return scanMessage(r) })
		return err
	})
	if out == nil {
		out = []Message{}
	}
	return out, err
}

// version is the recipe content the assistant sees.
type version struct {
	ID       uuid.UUID
	N        int
	Title    string
	Servings *int
	Minutes  *int
	Lines    []lines.Line
	Steps    []recipes.Step
	Notes    *string
}

func (v version) content() recipes.Content {
	return recipes.Content{
		Title: v.Title, Servings: v.Servings, TotalMinutes: v.Minutes,
		Ingredients: lines.Raws(v.Lines), Steps: v.Steps, Notes: v.Notes,
	}
}

func currentVersion(ctx context.Context, tx pgx.Tx, hh, recipe uuid.UUID) (version, error) {
	var (
		v               version
		ingRaw, stepRaw []byte
	)
	err := tx.QueryRow(ctx, `SELECT v.id, v.n, v.title, v.servings, v.total_minutes, v.ingredients, v.steps, v.notes
		FROM recipe r JOIN recipe_version v ON v.id = r.current_version_id
		WHERE r.id = $1 AND r.household_id = $2 AND r.deleted_at IS NULL`, recipe, hh).
		Scan(&v.ID, &v.N, &v.Title, &v.Servings, &v.Minutes, &ingRaw, &stepRaw, &v.Notes)
	if errors.Is(err, pgx.ErrNoRows) {
		return v, ErrNotFound
	}
	if err != nil {
		return v, err
	}
	if err := json.Unmarshal(ingRaw, &v.Lines); err != nil {
		return v, fmt.Errorf("recipe_version %s ingredients: %w", v.ID, err)
	}
	if err := json.Unmarshal(stepRaw, &v.Steps); err != nil {
		return v, fmt.Errorf("recipe_version %s steps: %w", v.ID, err)
	}
	return v, nil
}

// turn is an earlier message as context.
type turn struct {
	role, text string
	proposal   *Proposal
	state      string
}

func history(ctx context.Context, tx pgx.Tx, hh, recipe uuid.UUID) ([]turn, error) {
	rows, err := tx.Query(ctx, `SELECT role, text, proposal, proposal_state FROM (
			SELECT role, text, proposal, proposal_state, created_at, id FROM recipe_chat_message
			WHERE recipe_id = $1 AND household_id = $2 ORDER BY created_at DESC, id DESC LIMIT $3
		) t ORDER BY created_at, id`, recipe, hh, historyLimit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (turn, error) {
		var (
			t   turn
			raw []byte
		)
		if err := r.Scan(&t.role, &t.text, &raw, &t.state); err != nil {
			return t, err
		}
		if raw != nil {
			t.proposal = &Proposal{}
			if err := json.Unmarshal(raw, t.proposal); err != nil {
				return t, err
			}
		}
		return t, nil
	})
}

type cookNote struct {
	On   string `json:"cooked_on"`
	Note string `json:"note"`
}

func notes(ctx context.Context, tx pgx.Tx, hh, recipe uuid.UUID) ([]cookNote, error) {
	rows, err := tx.Query(ctx, `SELECT to_char(cooked_on, 'YYYY-MM-DD'), note FROM cook_session
		WHERE recipe_id = $1 AND household_id = $2 AND deleted_at IS NULL AND undone_at IS NULL AND coalesce(btrim(note), '') <> ''
		ORDER BY cooked_on DESC, created_at DESC LIMIT $3`, recipe, hh, cookNotes)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (cookNote, error) {
		var n cookNote
		return n, r.Scan(&n.On, &n.Note)
	})
}

// Post sends the user's text to the assistant and stores both messages. On
// an LLM failure nothing is stored and the error wraps llm.ErrUnavailable
// (or llm.ErrBadOutput), so the client keeps the text and offers the editor.
func (s *Service) Post(ctx context.Context, hh, user, recipe uuid.UUID, text string) ([]Message, error) {
	text = strings.TrimSpace(text)
	if text == "" || len([]rune(text)) > MaxText {
		return nil, fmt.Errorf("%w: a message must be 1-%d characters", ErrInvalid, MaxText)
	}
	var (
		v     version
		turns []turn
		cooks []cookNote
	)
	err := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		var err error
		if v, err = currentVersion(ctx, tx, hh, recipe); err != nil {
			return err
		}
		if turns, err = history(ctx, tx, hh, recipe); err != nil {
			return err
		}
		cooks, err = notes(ctx, tx, hh, recipe)
		return err
	})
	if err != nil {
		return nil, err
	}
	if s.LLM == nil {
		return nil, llm.ErrUnavailable
	}
	resp, err := s.LLM.Complete(ctx, request(v, turns, cooks, text))
	if err != nil {
		return nil, err
	}
	var r reply
	if err := json.Unmarshal(resp.JSON, &r); err != nil {
		return nil, fmt.Errorf("%w: %w", llm.ErrBadOutput, err)
	}
	r.Reply = strings.TrimSpace(r.Reply)
	if r.Reply == "" {
		return nil, fmt.Errorf("%w: empty reply", llm.ErrBadOutput)
	}
	prop, propErr := merge(v, r.Proposal)

	userMsg := Message{ID: uuid.Must(uuid.NewV7()), Role: RoleUser, Text: text, State: StateNone, UserID: &user}
	aiMsg := Message{ID: uuid.Must(uuid.NewV7()), Role: RoleAssistant, Text: r.Reply, Proposal: prop, State: StateNone, ProposalError: propErr}
	if prop != nil {
		aiMsg.State = StatePending
	}
	var out []Message
	err = pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		if err := recipeExists(ctx, tx, hh, recipe); err != nil {
			return err
		}
		for _, m := range []Message{userMsg, aiMsg} {
			var raw []byte
			if m.Proposal != nil {
				if raw, err = json.Marshal(m.Proposal); err != nil {
					return err
				}
			}
			if _, err := tx.Exec(ctx, `INSERT INTO recipe_chat_message (id, household_id, recipe_id, version_id, role, text, proposal, proposal_state, proposal_error, user_id)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
				m.ID, hh, recipe, v.ID, m.Role, m.Text, raw, m.State, m.ProposalError, m.UserID); err != nil {
				return err
			}
		}
		if err := audit.Record(ctx, tx, "recipe.chat.post", audit.Target{Type: "recipe", ID: recipe.String()}, nil,
			map[string]any{"message_id": userMsg.ID, "reply_id": aiMsg.ID, "proposal": prop != nil, "provider": resp.Provider}); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, selectMessage+` WHERE m.id = ANY($1) ORDER BY m.created_at, m.id`, []uuid.UUID{userMsg.ID, aiMsg.ID})
		if err != nil {
			return err
		}
		out, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (Message, error) { return scanMessage(r) })
		return err
	})
	return out, err
}

// Dismiss sets a pending proposal aside ("Not now"). Dismissing again is a
// no-op; a saved proposal answers ErrSaved.
func (s *Service) Dismiss(ctx context.Context, hh, recipe, id uuid.UUID) (Message, error) {
	var m Message
	err := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		var state string
		err := tx.QueryRow(ctx, `SELECT proposal_state FROM recipe_chat_message m
			WHERE m.id = $1 AND m.recipe_id = $2 AND m.household_id = $3
			AND EXISTS (SELECT 1 FROM recipe r WHERE r.id = m.recipe_id AND r.deleted_at IS NULL) FOR UPDATE`,
			id, recipe, hh).Scan(&state)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		switch state {
		case StateNone:
			return fmt.Errorf("%w: that message has no proposal", ErrInvalid)
		case StateSaved:
			return ErrSaved
		case StatePending:
			if _, err := tx.Exec(ctx, `UPDATE recipe_chat_message SET proposal_state = 'dismissed' WHERE id = $1`, id); err != nil {
				return err
			}
			if err := audit.Record(ctx, tx, "recipe.chat.dismiss", audit.Target{Type: "recipe", ID: recipe.String()},
				map[string]any{"message_id": id, "state": StatePending}, map[string]any{"state": StateDismissed}); err != nil {
				return err
			}
		}
		m, err = scanMessage(tx.QueryRow(ctx, selectMessage+` WHERE m.id = $1`, id))
		return err
	})
	return m, err
}

// MarkSaved is recipes.Handlers.OnChatVersion: inside recipe.version.create
// it records that message's proposal became version. A message of another
// recipe or household is left alone; saving again (a replay, or a second
// save of the same proposal) keeps the first version.
func MarkSaved(ctx context.Context, tx pgx.Tx, hh, message, version uuid.UUID) error {
	tag, err := tx.Exec(ctx, `UPDATE recipe_chat_message m SET proposal_state = 'saved', saved_version_id = $3
		WHERE m.id = $1 AND m.household_id = $2 AND m.proposal_state IN ('pending', 'dismissed')
		AND m.recipe_id = (SELECT recipe_id FROM recipe_version WHERE id = $3)`, message, hh, version)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		slog.DebugContext(ctx, "recipe chat: version saved from a message that is not a pending proposal", "message", message, "version", version)
	}
	return nil
}

var _ recipes.VersionSavedHook = MarkSaved

// reply is the model's answer. Proposal fields are null when unchanged.
type reply struct {
	Reply    string    `json:"reply"`
	Proposal *proposed `json:"proposal"`
}

type proposed struct {
	ChangeSummary string    `json:"change_summary"`
	Title         *string   `json:"title"`
	Servings      *int      `json:"servings"`
	TotalMinutes  *int      `json:"total_minutes"`
	Ingredients   *[]string `json:"ingredients"`
	Steps         *[]string `json:"steps"`
	Notes         *string   `json:"notes"`
}

// merge turns the model's changes into a Proposal (only what differs from
// v) and checks that v with them applied is a valid version. It returns nil
// when nothing changes, and nil plus the reason when the result would be
// rejected, so the reply can say why there is nothing to save.
func merge(v version, p *proposed) (*Proposal, *string) {
	if p == nil {
		return nil, nil
	}
	base := v.content()
	out := Proposal{}
	if p.Title != nil {
		if t := strings.TrimSpace(*p.Title); t != "" && t != base.Title {
			out.Title = &t
		}
	}
	if p.Servings != nil && !eqInt(p.Servings, base.Servings) {
		out.Servings = p.Servings
	}
	if p.TotalMinutes != nil && !eqInt(p.TotalMinutes, base.TotalMinutes) {
		out.TotalMinutes = p.TotalMinutes
	}
	if p.Ingredients != nil {
		if ings := clean(*p.Ingredients); !slices.Equal(ings, base.Ingredients) {
			out.Ingredients = &ings
		}
	}
	if p.Steps != nil {
		if st := steps(*p.Steps, v.Steps); !sameSteps(st, base.Steps) {
			out.Steps = &st
		}
	}
	if p.Notes != nil {
		n := strings.TrimSpace(*p.Notes)
		if old := deref(base.Notes); n != old {
			out.Notes = &n
		}
	}
	if out.Title == nil && out.Servings == nil && out.TotalMinutes == nil && out.Ingredients == nil && out.Steps == nil && out.Notes == nil {
		return nil, nil
	}
	if err := recipes.CheckContent(out.Apply(base)); err != nil {
		var ce *commands.Error
		reason := "the change breaks a recipe rule"
		if errors.As(err, &ce) {
			reason = ce.Detail
		}
		return nil, &reason
	}
	out.ChangeSummary = strings.TrimSpace(p.ChangeSummary)
	if out.ChangeSummary == "" {
		out.ChangeSummary = "Changes from the chat"
	}
	if r := []rune(out.ChangeSummary); len(r) > 200 {
		out.ChangeSummary = string(r[:200])
	}
	return &out, nil
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func eqInt(x, y *int) bool { return (x == nil) == (y == nil) && (x == nil || *x == *y) }

func sameSteps(a, b []recipes.Step) bool {
	return slices.EqualFunc(a, b, func(x, y recipes.Step) bool { return x.Text == y.Text && eqInt(x.Minutes, y.Minutes) })
}

func clean(ss []string) []string {
	out := make([]string, 0, len(ss))
	for _, s := range ss {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// steps turns the proposed step texts into steps, keeping the timer of each
// step whose text is unchanged.
func steps(texts []string, old []recipes.Step) []recipes.Step {
	minutes := map[string]*int{}
	for _, s := range old {
		minutes[s.Text] = s.Minutes
	}
	out := make([]recipes.Step, 0, len(texts))
	for _, t := range clean(texts) {
		out = append(out, recipes.Step{Text: t, Minutes: minutes[t]})
	}
	return out
}
