// Package reports takes problem reports from the app ("the default unit is
// wrong", "wrong match", …), has an LLM review them in the background with
// spare quota only, applies approved default-unit corrections to the catalog
// and gives the admin the rest.
package reports

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/a-jay98/stockpot/internal/identity"
	"github.com/a-jay98/stockpot/internal/jobs"
	"github.com/a-jay98/stockpot/internal/llm"
	"github.com/a-jay98/stockpot/internal/settings"
	"github.com/a-jay98/stockpot/internal/units"
)

// Kinds of report.
const (
	KindWrongUnit  = "wrong_unit"
	KindWrongMatch = "wrong_match"
	KindWrongIcon  = "wrong_icon"
	KindOther      = "other"
)

// Sources a report can be made from; each names a household row.
const (
	SourceShoppingItem = "shopping_item"
	SourceProduct      = "product"
	SourceStockItem    = "stock_item"
)

// Statuses.
const (
	StatusOpen       = "open"
	StatusNeedsAdmin = "needs_admin"
	StatusApplied    = "applied"
	StatusRejected   = "rejected"
	StatusDismissed  = "dismissed"
)

// Actor recorded for the review job.
const Actor = "system:report_review"

// Errors.
var (
	ErrInvalid    = errors.New("invalid report")
	ErrNoSubject  = errors.New("the reported item was not found")
	ErrTooMany    = errors.New("too many reports today; try again tomorrow")
	ErrNotFound   = errors.New("report not found")
	ErrBadAction  = errors.New("this action does not fit the report")
	ErrNoTarget   = errors.New("the report names no product or ingredient to change")
	ErrBadUnit    = errors.New("unknown unit")
	ErrNotAttached = errors.New("reports: not attached to the job client")
)

// Completer is the part of llm.Router the review job uses.
type Completer interface {
	Complete(ctx context.Context, req llm.Request) (llm.Response, error)
	Headroom(ctx context.Context, needs llm.Needs) (bool, time.Time)
}

var _ Completer = (*llm.Router)(nil)

// Deps are what the service needs.
type Deps struct {
	Pool     *pgxpool.Pool
	Settings *settings.Store
	LLM      Completer
	Now      func() time.Time
}

// Service handles reports.
type Service struct {
	d      Deps
	client *jobs.Client
}

// New returns a service over d.
func New(d Deps) *Service {
	if d.Now == nil {
		d.Now = time.Now
	}
	return &Service{d: d}
}

// Input is a report as the app sends it.
type Input struct {
	Kind      string
	Source    string
	SubjectID uuid.UUID
	// Suggested is what it should be: a unit code for wrong_unit, free text otherwise.
	Suggested string
	Note      string
}

// Limits.
const (
	maxNote          = 500
	maxSuggested     = 80
	maxPerDayPerHome = 50
)

func clean(s string, limit int) (string, bool) {
	s = strings.TrimSpace(s)
	return s, utf8.RuneCountInString(s) <= limit
}

// Submit stores a report from the caller's household and returns its id.
func (s *Service) Submit(ctx context.Context, in Input) (uuid.UUID, error) {
	hh, err := identity.HouseholdID(ctx)
	if err != nil {
		return uuid.Nil, err
	}
	var reporter *uuid.UUID
	if p := identity.PrincipalFrom(ctx); p != nil && p.UserID != uuid.Nil {
		reporter = &p.UserID
	}
	switch in.Kind {
	case KindWrongUnit, KindWrongMatch, KindWrongIcon, KindOther:
	default:
		return uuid.Nil, fmt.Errorf("%w: kind %q", ErrInvalid, in.Kind)
	}
	note, ok := clean(in.Note, maxNote)
	if !ok {
		return uuid.Nil, fmt.Errorf("%w: note is too long", ErrInvalid)
	}
	sugg, ok := clean(in.Suggested, maxSuggested)
	if !ok {
		return uuid.Nil, fmt.Errorf("%w: suggestion is too long", ErrInvalid)
	}
	if in.Kind == KindWrongUnit && sugg != "" && !units.Valid(sugg) {
		return uuid.Nil, ErrBadUnit
	}
	if in.Kind == KindOther && note == "" {
		return uuid.Nil, fmt.Errorf("%w: say what is wrong", ErrInvalid)
	}

	tx, err := s.d.Pool.Begin(ctx)
	if err != nil {
		return uuid.Nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var today int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM problem_report WHERE household_id = $1 AND created_at > now() - interval '1 day'`, hh).Scan(&today); err != nil {
		return uuid.Nil, err
	}
	if today >= maxPerDayPerHome {
		return uuid.Nil, ErrTooMany
	}
	snap, err := snapshot(ctx, tx, hh, in.Source, in.SubjectID)
	if err != nil {
		return uuid.Nil, err
	}
	var id uuid.UUID
	err = tx.QueryRow(ctx, `INSERT INTO problem_report (household_id, reporter_id, kind, source, subject_id, product_id, ingredient_id, context, suggested, note)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10) RETURNING id`,
		hh, reporter, in.Kind, in.Source, in.SubjectID, snap.ProductID, snap.IngredientID, snap, nullIfEmpty(sugg), nullIfEmpty(note)).Scan(&id)
	if err != nil {
		return uuid.Nil, err
	}
	return id, tx.Commit(ctx)
}

func nullIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// Context is what the person saw: stored with the report so the LLM and the
// admin judge the same thing even after the row changes.
type Context struct {
	ItemText string `json:"itemText,omitempty"`
	Qty      string `json:"qty,omitempty"`
	Unit     string `json:"unit,omitempty"`

	ProductID          *uuid.UUID `json:"productId,omitempty"`
	ProductName        string     `json:"productName,omitempty"`
	Brand              string     `json:"brand,omitempty"`
	ProductDefaultUnit string     `json:"productDefaultUnit,omitempty"`

	IngredientID   *uuid.UUID `json:"ingredientId,omitempty"`
	IngredientSlug string     `json:"ingredientSlug,omitempty"`
	IngredientName string     `json:"ingredientName,omitempty"`
	// IngredientUnit is the ingredient's own default_unit; InheritedUnit the
	// first one up its parent chain (what applies when its own is empty).
	IngredientUnit string `json:"ingredientUnit,omitempty"`
	InheritedUnit  string `json:"inheritedUnit,omitempty"`
	Global         bool   `json:"global,omitempty"`
	Category       string `json:"category,omitempty"`
	Icon           string `json:"icon,omitempty"`
}

func snapshot(ctx context.Context, tx pgx.Tx, hh uuid.UUID, source string, subject uuid.UUID) (Context, error) {
	var c Context
	var product *uuid.UUID
	var qty, unit *string
	var err error
	switch source {
	case SourceShoppingItem:
		err = tx.QueryRow(ctx, `SELECT text, qty::text, unit, product_id FROM shopping_item WHERE id = $1 AND household_id = $2 AND deleted_at IS NULL`,
			subject, hh).Scan(&c.ItemText, &qty, &unit, &product)
	case SourceStockItem:
		err = tx.QueryRow(ctx, `SELECT qty::text, unit, product_id FROM stock_item WHERE id = $1 AND household_id = $2 AND deleted_at IS NULL`,
			subject, hh).Scan(&qty, &unit, &product)
	case SourceProduct:
		product = &subject
	default:
		return c, fmt.Errorf("%w: source %q", ErrInvalid, source)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return c, ErrNoSubject
	}
	if err != nil {
		return c, err
	}
	if qty != nil {
		c.Qty = *qty
	}
	if unit != nil {
		c.Unit = *unit
	}
	if product == nil {
		return c, nil
	}
	var brand, pUnit, icon, category *string
	var ingredient *uuid.UUID
	err = tx.QueryRow(ctx, `SELECT p.name, p.brand, p.default_unit, p.ingredient_id, coalesce(p.icon, i.icon), c.slug
		FROM product p LEFT JOIN ingredient i ON i.id = p.ingredient_id LEFT JOIN category c ON c.id = coalesce(p.category_id, i.category_id)
		WHERE p.id = $1 AND p.household_id = $2 AND p.deleted_at IS NULL`, *product, hh).
		Scan(&c.ProductName, &brand, &pUnit, &ingredient, &icon, &category)
	if errors.Is(err, pgx.ErrNoRows) {
		if source == SourceProduct {
			return c, ErrNoSubject
		}
		return c, nil
	}
	if err != nil {
		return c, err
	}
	c.ProductID = product
	c.Brand, c.ProductDefaultUnit, c.Icon, c.Category = deref(brand), deref(pUnit), deref(icon), deref(category)
	if ingredient == nil {
		return c, nil
	}
	var own, inherited *string
	err = tx.QueryRow(ctx, `
		WITH RECURSIVE up AS (
			SELECT id, parent_id, default_unit, 0 AS depth FROM ingredient WHERE id = $1
			UNION ALL SELECT i.id, i.parent_id, i.default_unit, up.depth + 1 FROM ingredient i JOIN up ON i.id = up.parent_id WHERE up.depth < 10)
		SELECT i.slug, i.name, i.default_unit, i.household_id IS NULL,
		       (SELECT default_unit FROM up WHERE default_unit IS NOT NULL ORDER BY depth LIMIT 1)
		FROM ingredient i WHERE i.id = $1`, *ingredient).Scan(&c.IngredientSlug, &c.IngredientName, &own, &c.Global, &inherited)
	if err != nil {
		return c, err
	}
	c.IngredientID = ingredient
	c.IngredientUnit, c.InheritedUnit = deref(own), deref(inherited)
	return c, nil
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
