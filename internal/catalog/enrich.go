package catalog

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/a-jay98/stockpot/internal/audit"
	"github.com/a-jay98/stockpot/internal/catalog/resolve"
	"github.com/a-jay98/stockpot/internal/jobs"
	"github.com/a-jay98/stockpot/internal/llm"
	"github.com/a-jay98/stockpot/internal/reqctx"
	"github.com/a-jay98/stockpot/internal/settings"
	"github.com/a-jay98/stockpot/internal/syncdb"
)

// EnrichActor is the actor recorded in the audit trail for enrichment.
const EnrichActor = "system:enricher"

// Run triggers.
const (
	TriggerScheduled = "scheduled"
	TriggerManual    = "manual"
)

// Run outcomes.
const (
	OutcomeRunning  = "running"
	OutcomeOK       = "ok"
	OutcomePartial  = "partial"
	OutcomeDeferred = "deferred"
	OutcomeError    = "error"
)

// ErrEnrichNotAttached is returned by RunNow before Attach.
var ErrEnrichNotAttached = errors.New("catalog enrich: not attached to the job client")

// Completer is the part of llm.Router the enricher uses.
type Completer interface {
	Complete(ctx context.Context, req llm.Request) (llm.Response, error)
	Headroom(ctx context.Context, needs llm.Needs) (bool, time.Time)
}

var _ Completer = (*llm.Router)(nil)

// EnrichDeps are what the enricher needs.
type EnrichDeps struct {
	Pool     *pgxpool.Pool
	Settings *settings.Store
	LLM      Completer
	// Now defaults to time.Now.
	Now func() time.Time
}

// Enricher fills in what the rule resolver could not: it asks an LLM, with
// spare quota only, to match pending products to ingredients (creating
// household ingredients when none fits), categories, icons and shelf life.
// It never touches a choice the user made.
type Enricher struct {
	d      EnrichDeps
	client *jobs.Client
}

// NewEnricher returns an enricher over d.
func NewEnricher(d EnrichDeps) *Enricher {
	if d.Now == nil {
		d.Now = time.Now
	}
	return &Enricher{d: d}
}

// pending is a product waiting for enrichment.
type pending struct {
	id        uuid.UUID
	household uuid.UUID
	name      string
	brand     *string
	store     *string
	key       string
	ref       string
}

// RunStats summarises one run.
type runStats struct {
	items, applied, failed, cached, calls int
	provider                              string
}

// Run does one enrichment pass. It records an enrich_run row unless there
// was nothing to do on a scheduled run.
func (e *Enricher) Run(ctx context.Context, trigger string, jobID *int64) error {
	if trigger == TriggerScheduled && !FlagEnrich.Get(e.d.Settings) {
		return nil
	}
	n, err := e.pendingCount(ctx)
	if err != nil {
		return err
	}
	if n == 0 && trigger == TriggerScheduled {
		return nil
	}
	var runID uuid.UUID
	if err := e.d.Pool.QueryRow(ctx, `INSERT INTO enrich_run (trigger, job_id) VALUES ($1, $2) RETURNING id`, trigger, jobID).Scan(&runID); err != nil {
		return err
	}
	var st runStats
	outcome, reason, runErr := e.run(ctx, &st)
	fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if _, err := e.d.Pool.Exec(fctx, `UPDATE enrich_run SET finished_at = now(), outcome = $2, reason = $3, items = $4, applied = $5,
		failed = $6, cached = $7, calls = $8, provider = $9 WHERE id = $1`,
		runID, outcome, nullIfEmpty(reason), st.items, st.applied, st.failed, st.cached, st.calls, nullIfEmpty(st.provider)); err != nil {
		slog.ErrorContext(ctx, "catalog enrich: record run", "err", err)
	}
	return runErr
}

func nullIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func (e *Enricher) pendingCount(ctx context.Context) (int, error) {
	var n int
	err := e.d.Pool.QueryRow(ctx, `SELECT count(*) FROM product WHERE deleted_at IS NULL AND enrich_state = 'pending'
		AND match_source <> 'user' AND enrich_attempts < $1`, maxEnrichAttempts).Scan(&n)
	return n, err
}

// run is the body of Run; it returns the outcome and why.
func (e *Enricher) run(ctx context.Context, st *runStats) (outcome, reason string, err error) {
	if ok, retry := e.d.LLM.Headroom(ctx, llm.Needs{}); !ok {
		return OutcomeDeferred, noHeadroomReason(retry), nil
	}
	maxCalls := SettingEnrichMaxCalls.Get(e.d.Settings)
	batch := SettingEnrichBatchSize.Get(e.d.Settings)
	deferred := ""
	seen := []uuid.UUID{} // an item is tried once per run, whatever the answer
	for i := 0; i < maxBatchesPerRun && st.calls < maxCalls; i++ {
		items, err := e.loadPending(ctx, batch, seen)
		if err != nil {
			return OutcomeError, err.Error(), err
		}
		if len(items) == 0 {
			break
		}
		st.items += len(items)
		for _, it := range items {
			seen = append(seen, it.id)
		}
		rest, err := e.applyCached(ctx, items, st)
		if err != nil {
			return OutcomeError, err.Error(), err
		}
		if len(rest) == 0 {
			continue
		}
		if ok, retry := e.d.LLM.Headroom(ctx, llm.Needs{}); !ok {
			deferred = noHeadroomReason(retry)
			break
		}
		st.calls++
		if err := e.callAndApply(ctx, rest, st); err != nil {
			var def bool
			switch {
			case errors.Is(err, llm.ErrUnavailable):
				def = true
			case errors.Is(err, llm.ErrBadOutput):
				// Counted against the products (callAndApply); try the next ones.
				continue
			default:
				return OutcomeError, err.Error(), err
			}
			if def {
				deferred = err.Error()
				break
			}
		}
	}
	switch {
	case deferred != "" && st.applied+st.cached == 0:
		return OutcomeDeferred, deferred, nil
	case deferred != "" || st.failed > 0:
		return OutcomePartial, deferred, nil
	}
	return OutcomeOK, "", nil
}

func noHeadroomReason(retry time.Time) string {
	if retry.IsZero() {
		return "no LLM quota above the reserve for background work (or LLM features are off)"
	}
	return "no LLM quota above the reserve for background work; quota frees up " + retry.UTC().Format(time.RFC3339)
}

// loadPending returns up to n products to enrich, fresh ones first.
func (e *Enricher) loadPending(ctx context.Context, n int, seen []uuid.UUID) ([]pending, error) {
	rows, err := e.d.Pool.Query(ctx, `
		SELECT p.id, p.household_id, p.name, p.brand, s.name
		FROM product p LEFT JOIN store s ON s.id = p.default_store_id
		WHERE p.deleted_at IS NULL AND p.enrich_state = 'pending' AND p.match_source <> 'user' AND p.enrich_attempts < $1 AND p.id <> ALL($3)
		ORDER BY p.enrich_attempts, p.created_at, p.id LIMIT $2`, maxEnrichAttempts, n, seen)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []pending
	for rows.Next() {
		var p pending
		if err := rows.Scan(&p.id, &p.household, &p.name, &p.brand, &p.store); err != nil {
			return nil, err
		}
		p.key = cacheKey(p.name, p.brand)
		out = append(out, p)
	}
	return out, rows.Err()
}

func cacheKey(name string, brand *string) string {
	b := ""
	if brand != nil {
		b = Normalize(*brand)
	}
	sum := sha256.Sum256([]byte(Normalize(name) + "\x00" + b))
	return hex.EncodeToString(sum[:])
}

// applyCached applies stored answers and returns the items still needing the LLM.
func (e *Enricher) applyCached(ctx context.Context, items []pending, st *runStats) ([]pending, error) {
	keys := make([]string, 0, len(items))
	for _, it := range items {
		keys = append(keys, it.key)
	}
	rows, err := e.d.Pool.Query(ctx, `SELECT key, result FROM enrich_cache WHERE key = ANY($1) AND created_at > $2`,
		keys, e.d.Now().AddDate(0, 0, -cacheDays))
	if err != nil {
		return nil, err
	}
	cache := map[string]json.RawMessage{}
	for rows.Next() {
		var k string
		var raw json.RawMessage
		if err := rows.Scan(&k, &raw); err != nil {
			rows.Close()
			return nil, err
		}
		cache[k] = raw
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var rest []pending
	for _, it := range items {
		raw, ok := cache[it.key]
		if !ok {
			rest = append(rest, it)
			continue
		}
		var res enrichItem
		if json.Unmarshal(raw, &res) != nil {
			rest = append(rest, it)
			continue
		}
		applied, err := e.applyOne(ctx, it, res)
		switch {
		case err == nil && applied:
			st.cached++
		case err == nil, errors.Is(err, errSkip):
			// Gone or chosen by the user meanwhile.
		case errors.Is(err, errInvalid):
			// The cached answer no longer fits this household (a slug was removed).
			_, _ = e.d.Pool.Exec(ctx, `DELETE FROM enrich_cache WHERE key = $1`, it.key)
			rest = append(rest, it)
		default:
			return nil, err
		}
	}
	return rest, nil
}

// enrichSchema is the strict reply format. Absent values are empty strings,
// empty arrays and zeros, which every OpenAI-compatible endpoint accepts.
var enrichSchema = json.RawMessage(`{
 "type":"object","additionalProperties":false,"required":["items"],
 "properties":{"items":{"type":"array","items":{
  "type":"object","additionalProperties":false,
  "required":["ref","ingredient_slug","new_ingredient","extra_ingredient_slugs","category_slug","icon","brand","shelf_life","confidence"],
  "properties":{
   "ref":{"type":"string"},
   "ingredient_slug":{"type":"string"},
   "new_ingredient":{"type":"object","additionalProperties":false,
     "required":["name","plural","parent_slug","category_slug","aliases"],
     "properties":{"name":{"type":"string"},"plural":{"type":"string"},"parent_slug":{"type":"string"},
                   "category_slug":{"type":"string"},"aliases":{"type":"array","items":{"type":"string"}}}},
   "extra_ingredient_slugs":{"type":"array","items":{"type":"string"}},
   "category_slug":{"type":"string"},
   "icon":{"type":"string"},
   "brand":{"type":"string"},
   "shelf_life":{"type":"object","additionalProperties":false,
     "required":["fridge","freezer","pantry","opened_fridge"],
     "properties":{"fridge":{"type":"integer"},"freezer":{"type":"integer"},"pantry":{"type":"integer"},"opened_fridge":{"type":"integer"}}},
   "confidence":{"type":"number"}}}}}}`)

const enrichSystem = `You match grocery product names, as typed by a household in Canada, to canonical ingredients so recipes can find them.
For each item return exactly one object with the item's "ref".
- ingredient_slug: the slug of the single best ingredient. Choose from the item's candidates. Brands, stores, sizes and adjectives never matter: "Kirkland organic mandarins 2 lb" is mandarin. Prefer the most specific ingredient that is correct (greek-yogurt over yogurt), never a broader one when a specific one fits.
- If no ingredient fits, leave ingredient_slug empty and fill new_ingredient (name, plural, a parent_slug that is an existing ingredient slug, category_slug, aliases). Otherwise leave new_ingredient.name empty.
- extra_ingredient_slugs: further ingredients a mix contains (e.g. stir-fry vegetables); usually empty.
- category_slug and icon must come from the given lists; empty when unsure.
- brand: the brand named in the text, else empty.
- shelf_life: typical days (fridge, freezer, pantry, opened_fridge), 0 when unknown.
- confidence: 0 to 1.
Use only the text of each item. Reply with JSON only.`

// enrichItem is one validated answer.
type enrichItem struct {
	Ref                  string      `json:"ref"`
	IngredientSlug       string      `json:"ingredient_slug"`
	NewIngredient        newIngr     `json:"new_ingredient"`
	ExtraIngredientSlugs []string    `json:"extra_ingredient_slugs"`
	CategorySlug         string      `json:"category_slug"`
	Icon                 string      `json:"icon"`
	Brand                string      `json:"brand"`
	ShelfLife            shelfAnswer `json:"shelf_life"`
	Confidence           float64     `json:"confidence"`
}

type newIngr struct {
	Name         string   `json:"name"`
	Plural       string   `json:"plural"`
	ParentSlug   string   `json:"parent_slug"`
	CategorySlug string   `json:"category_slug"`
	Aliases      []string `json:"aliases"`
}

type shelfAnswer struct {
	Fridge       int `json:"fridge"`
	Freezer      int `json:"freezer"`
	Pantry       int `json:"pantry"`
	OpenedFridge int `json:"opened_fridge"`
}

func (s shelfAnswer) json() []byte {
	m := map[string]int{}
	for k, v := range map[string]int{"fridge": s.Fridge, "freezer": s.Freezer, "pantry": s.Pantry, "opened_fridge": s.OpenedFridge} {
		if v > 0 {
			m[k] = v
		}
	}
	if len(m) == 0 {
		return nil
	}
	b, _ := json.Marshal(m)
	return b
}

// callAndApply sends one LLM call for items (grouped by name) and applies
// the answers. Invalid or missing answers use up an attempt.
func (e *Enricher) callAndApply(ctx context.Context, items []pending, st *runStats) error {
	// One LLM item per distinct (name, brand), however many households have it.
	var groups [][]pending
	byKey := map[string]int{}
	for _, it := range items {
		if i, ok := byKey[it.key]; ok {
			groups[i] = append(groups[i], it)
			continue
		}
		byKey[it.key] = len(groups)
		groups = append(groups, []pending{it})
	}
	cats, err := e.categorySlugs(ctx)
	if err != nil {
		return err
	}
	type promptItem struct {
		Ref        string   `json:"ref"`
		Name       string   `json:"name"`
		Brand      string   `json:"brand,omitempty"`
		Store      string   `json:"store,omitempty"`
		Candidates []string `json:"candidates"`
	}
	prompt := struct {
		Items      []promptItem `json:"items"`
		Categories []string     `json:"allowed_category_slugs"`
		Icons      []string     `json:"allowed_icons"`
	}{Categories: cats, Icons: sortedIcons()}
	for i, g := range groups {
		ref := fmt.Sprintf("i%d", i+1)
		for j := range g {
			g[j].ref = ref
		}
		it := g[0]
		cands, err := resolve.Candidates(ctx, e.d.Pool, it.household, it.name, it.brand, candidatesPerItem)
		if err != nil {
			return err
		}
		pi := promptItem{Ref: ref, Name: it.name, Candidates: make([]string, 0, len(cands))}
		if it.brand != nil {
			pi.Brand = *it.brand
		}
		if it.store != nil {
			pi.Store = *it.store
		}
		for _, c := range cands {
			pi.Candidates = append(pi.Candidates, c.Slug+" ("+c.Name+")")
		}
		prompt.Items = append(prompt.Items, pi)
	}
	body, _ := json.Marshal(prompt)
	resp, err := e.d.LLM.Complete(ctx, llm.Request{
		Feature:    llm.Feature{Name: "catalog_enrich"},
		Priority:   llm.Background,
		Schema:     enrichSchema,
		SchemaName: "catalog_enrich",
		Messages:   []llm.Message{{Role: llm.RoleSystem, Text: enrichSystem}, {Role: llm.RoleUser, Text: string(body)}},
	})
	if err != nil {
		if errors.Is(err, llm.ErrBadOutput) {
			if ferr := e.failGroups(ctx, groups, st); ferr != nil {
				return ferr
			}
		}
		return err
	}
	st.provider = resp.Provider
	var reply struct {
		Items []enrichItem `json:"items"`
	}
	if err := json.Unmarshal(resp.JSON, &reply); err != nil {
		if ferr := e.failGroups(ctx, groups, st); ferr != nil {
			return ferr
		}
		return fmt.Errorf("%w: %w", llm.ErrBadOutput, err)
	}
	answers := map[string]enrichItem{}
	for _, a := range reply.Items {
		answers[a.Ref] = a
	}
	for _, g := range groups {
		ans, ok := answers[g[0].ref]
		cacheable := false
		for _, it := range g {
			applied := false
			var aerr error
			if ok {
				applied, aerr = e.applyOne(ctx, it, ans)
			} else {
				aerr = errInvalid
			}
			switch {
			case aerr == nil && applied:
				st.applied++
				cacheable = true
			case aerr == nil, errors.Is(aerr, errSkip):
			case errors.Is(aerr, errInvalid):
				if ferr := e.failAttempt(ctx, it); ferr != nil {
					return ferr
				}
				st.failed++
			default:
				return aerr
			}
		}
		if cacheable {
			raw, _ := json.Marshal(ans)
			if _, err := e.d.Pool.Exec(ctx, `INSERT INTO enrich_cache (key, result) VALUES ($1, $2)
				ON CONFLICT (key) DO UPDATE SET result = EXCLUDED.result, created_at = now()`, g[0].key, raw); err != nil {
				slog.WarnContext(ctx, "catalog enrich: cache write", "err", err)
			}
		}
	}
	return nil
}

// failGroups counts an unusable reply against every product of the call.
func (e *Enricher) failGroups(ctx context.Context, groups [][]pending, st *runStats) error {
	for _, g := range groups {
		for _, it := range g {
			if err := e.failAttempt(ctx, it); err != nil {
				return err
			}
			st.failed++
		}
	}
	return nil
}

func sortedIcons() []string {
	k := IconKeys()
	slices.Sort(k)
	return k
}

func (e *Enricher) categorySlugs(ctx context.Context) ([]string, error) {
	rows, err := e.d.Pool.Query(ctx, `SELECT slug FROM category WHERE household_id IS NULL AND deleted_at IS NULL ORDER BY sort, slug`)
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

// Sentinels of applyOne.
var (
	// errInvalid: the answer does not fit the closed lists or the household's
	// catalog; the product uses an attempt.
	errInvalid = errors.New("invalid enrichment answer")
	// errSkip: the product is gone, no longer pending or was chosen by the user.
	errSkip = errors.New("product no longer needs enrichment")
)

// failAttempt counts an invalid answer; the third one marks the product
// failed (the rule result stands).
func (e *Enricher) failAttempt(ctx context.Context, p pending) error {
	return e.inHousehold(ctx, p.household, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE product SET enrich_attempts = enrich_attempts + 1,
			enrich_state = CASE WHEN enrich_attempts + 1 >= $2 THEN 'failed' ELSE enrich_state END
			WHERE id = $1 AND deleted_at IS NULL AND enrich_state = 'pending' AND match_source <> 'user'`, p.id, maxEnrichAttempts)
		return err
	})
}

// inHousehold runs fn in a transaction holding the household's sync lock, so
// the rev order of its rows stays in commit order.
func (e *Enricher) inHousehold(ctx context.Context, hh uuid.UUID, fn func(tx pgx.Tx) error) error {
	tx, err := e.d.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := syncdb.LockHousehold(ctx, tx, hh); err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func trimLen(s string, limit int) (string, bool) {
	s = strings.TrimSpace(s)
	return s, utf8.RuneCountInString(s) <= limit && strings.IndexFunc(s, func(r rune) bool { return r < 0x20 }) < 0
}

// applyOne validates ans against the household's catalog and writes it to
// the product in one transaction. It returns applied=false, nil when the
// product no longer needs it.
func (e *Enricher) applyOne(ctx context.Context, p pending, ans enrichItem) (applied bool, err error) {
	err = e.inHousehold(ctx, p.household, func(tx pgx.Tx) error {
		var (
			source, state string
			category      *uuid.UUID
			icon, brand   *string
			shelf         []byte
			oldIngredient *uuid.UUID
			name          string
		)
		err := tx.QueryRow(ctx, `SELECT match_source, enrich_state, category_id, icon, brand, shelf_life, ingredient_id, name
			FROM product WHERE id = $1 AND household_id = $2 AND deleted_at IS NULL FOR UPDATE`, p.id, p.household,
		).Scan(&source, &state, &category, &icon, &brand, &shelf, &oldIngredient, &name)
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && (source == "user" || state != "pending")) {
			return errSkip
		}
		if err != nil {
			return err
		}
		v, err := e.validate(ctx, tx, p.household, ans)
		if err != nil {
			return err
		}
		if v.ingredient == uuid.Nil {
			if v.ingredient, err = e.createIngredient(ctx, tx, p.household, ans.NewIngredient, v); err != nil {
				return err
			}
		}
		// Only fill what is empty: a category, icon or brand already there was
		// chosen by the person (or by an earlier pass) and stays.
		var newBrand *string
		if brand == nil && ans.Brand != "" {
			var taken bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM product WHERE household_id = $1 AND id <> $2 AND deleted_at IS NULL
				AND lower(name) = lower($3) AND lower(coalesce(brand, '')) = lower($4))`, p.household, p.id, name, ans.Brand).Scan(&taken); err != nil {
				return err
			}
			if !taken {
				newBrand = &ans.Brand
			}
		}
		conf := min(max(ans.Confidence, 0), 1)
		if _, err := tx.Exec(ctx, `UPDATE product SET ingredient_id = $2, extra_ingredient_ids = $3, match_source = 'llm', match_confidence = $4,
			enrich_state = 'done', category_id = coalesce(category_id, $5), icon = coalesce(icon, $6), brand = coalesce(brand, $7),
			shelf_life = coalesce(shelf_life, $8::jsonb)
			WHERE id = $1`, p.id, v.ingredient, v.extra, conf, nilUUID(v.category), nullIfEmpty(ans.Icon), newBrand, v.shelf); err != nil {
			return err
		}
		if err := FillBuyUnits(ctx, tx, p.id); err != nil {
			return err
		}
		actx := reqctx.With(ctx, &reqctx.Info{HouseholdID: p.household})
		if err := audit.Record(actx, tx, "catalog.enrich.apply", audit.Target{Type: "product", ID: p.id.String()},
			map[string]any{"ingredient_id": oldIngredient},
			map[string]any{
				"actor": EnrichActor, "ingredient_id": v.ingredient, "extra_ingredient_ids": v.extra, "confidence": conf,
				"new_ingredient": ans.NewIngredient.Name, "category": v.categorySlug, "icon": ans.Icon,
			}); err != nil {
			return err
		}
		applied = true
		return nil
	})
	if errors.Is(err, errSkip) {
		return false, nil
	}
	return applied, err
}

func nilUUID(id uuid.UUID) *uuid.UUID {
	if id == uuid.Nil {
		return nil
	}
	return &id
}

// validated is an answer resolved against the catalog.
type validated struct {
	ingredient   uuid.UUID // Nil: create the answer's new ingredient
	extra        []uuid.UUID
	category     uuid.UUID
	categorySlug string
	parent       uuid.UUID // of the new ingredient
	parentCat    *uuid.UUID
	shelf        []byte
}

// ingredientIDBySlug finds a household or global ingredient (household wins).
func ingredientIDBySlug(ctx context.Context, tx pgx.Tx, hh uuid.UUID, slug string) (id uuid.UUID, cat *uuid.UUID, err error) {
	err = tx.QueryRow(ctx, `SELECT id, category_id FROM ingredient WHERE slug = $1 AND deleted_at IS NULL
		AND (household_id = $2 OR household_id IS NULL) ORDER BY household_id IS NULL LIMIT 1`, slug, hh).Scan(&id, &cat)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, nil, errInvalid
	}
	return id, cat, err
}

// validate checks every field of ans against the closed lists and the
// household's catalog.
func (e *Enricher) validate(ctx context.Context, tx pgx.Tx, hh uuid.UUID, ans enrichItem) (validated, error) {
	var v validated
	if ans.Confidence < 0 || ans.Confidence > 1 {
		return v, errInvalid
	}
	slug, ni := strings.TrimSpace(ans.IngredientSlug), ans.NewIngredient
	switch {
	case slug != "" && strings.TrimSpace(ni.Name) != "", slug == "" && strings.TrimSpace(ni.Name) == "":
		return v, errInvalid // exactly one of the two
	case slug != "":
		id, cat, err := ingredientIDBySlug(ctx, tx, hh, slug)
		if err != nil {
			return v, err
		}
		v.ingredient, v.parentCat = id, cat
	default:
		name, ok := trimLen(ni.Name, 80)
		if !ok || name == "" || utf8.RuneCountInString(ni.Plural) > 80 || len(ni.Aliases) > 10 {
			return v, errInvalid
		}
		if len(ni.ParentSlug) == 0 {
			return v, errInvalid
		}
		parent, pcat, err := ingredientIDBySlug(ctx, tx, hh, strings.TrimSpace(ni.ParentSlug))
		if err != nil {
			return v, err
		}
		v.parent, v.parentCat = parent, pcat
		// An ingredient of that name may already exist (a household's own, or
		// another pass created it): use it instead of a duplicate.
		if id, cat, err := ingredientIDBySlug(ctx, tx, hh, Slugify(name)); err == nil {
			v.ingredient, v.parentCat = id, cat
		} else if !errors.Is(err, errInvalid) {
			return v, err
		}
		if ni.CategorySlug != "" {
			if v.category, err = categoryIDBySlug(ctx, tx, hh, ni.CategorySlug); err != nil {
				return v, err
			}
		}
	}
	for _, s := range ans.ExtraIngredientSlugs {
		if len(v.extra) >= 5 {
			return v, errInvalid
		}
		id, _, err := ingredientIDBySlug(ctx, tx, hh, strings.TrimSpace(s))
		if err != nil {
			return v, err
		}
		if id != v.ingredient && !slices.Contains(v.extra, id) {
			v.extra = append(v.extra, id)
		}
	}
	if v.extra == nil {
		v.extra = []uuid.UUID{}
	}
	if ans.CategorySlug != "" {
		var err error
		if v.category, err = categoryIDBySlug(ctx, tx, hh, ans.CategorySlug); err != nil {
			return v, err
		}
		v.categorySlug = ans.CategorySlug
	} else if v.category == uuid.Nil && v.parentCat != nil {
		v.category = *v.parentCat
	}
	if ans.Icon != "" && !IconExists(ans.Icon) {
		return v, errInvalid
	}
	if _, ok := trimLen(ans.Brand, 100); !ok {
		return v, errInvalid
	}
	for _, d := range []int{ans.ShelfLife.Fridge, ans.ShelfLife.Freezer, ans.ShelfLife.Pantry, ans.ShelfLife.OpenedFridge} {
		if d < 0 || d > 3650 {
			return v, errInvalid
		}
	}
	v.shelf = ans.ShelfLife.json()
	return v, nil
}

func categoryIDBySlug(ctx context.Context, tx pgx.Tx, hh uuid.UUID, slug string) (uuid.UUID, error) {
	var id uuid.UUID
	err := tx.QueryRow(ctx, `SELECT id FROM category WHERE slug = $1 AND deleted_at IS NULL AND (household_id = $2 OR household_id IS NULL)
		ORDER BY household_id IS NULL LIMIT 1`, slug, hh).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, errInvalid
	}
	return id, err
}

// createIngredient adds a household ingredient with source llm.
func (e *Enricher) createIngredient(ctx context.Context, tx pgx.Tx, hh uuid.UUID, ni newIngr, v validated) (uuid.UUID, error) {
	name, _ := trimLen(ni.Name, 80)
	var plural *string
	if p, ok := trimLen(ni.Plural, 80); ok && p != "" {
		plural = &p
	}
	aliases := normalizedAliases(ni.Aliases)
	base := Slugify(name)
	for i := 1; i <= 20; i++ {
		slug := base
		if i > 1 {
			slug = fmt.Sprintf("%s-%d", base, i)
		}
		taken, err := exists(ctx, tx, `SELECT 1 FROM ingredient WHERE slug = $1 AND (household_id = $2 OR household_id IS NULL)`, slug, hh)
		if err != nil {
			return uuid.Nil, err
		}
		if taken {
			continue
		}
		var id uuid.UUID
		err = tx.QueryRow(ctx, `INSERT INTO ingredient (household_id, slug, name, plural, parent_id, category_id, aliases, source)
			VALUES ($1, $2, $3, $4, $5, $6, $7, 'llm') RETURNING id`, hh, slug, name, plural, v.parent, nilUUID(v.category), aliases).Scan(&id)
		return id, err
	}
	return uuid.Nil, errInvalid
}

// ---- job wiring -----------------------------------------------------------

// EnrichArgs is the enrichment job.
type EnrichArgs struct {
	Trigger string `json:"trigger"`
}

// Kind names the job in River's tables.
func (EnrichArgs) Kind() string { return "catalog.enrich" }

// InsertOpts never queues a second run while one waits or runs.
func (EnrichArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{
		MaxAttempts: 1,
		UniqueOpts: river.UniqueOpts{ByState: []rivertype.JobState{
			rivertype.JobStateAvailable, rivertype.JobStateRunning, rivertype.JobStateRetryable, rivertype.JobStateScheduled, rivertype.JobStatePending,
		}},
	}
}

// EnrichWorker runs the enrichment job.
type EnrichWorker struct {
	river.WorkerDefaults[EnrichArgs]
	e *Enricher
}

// Timeout bounds one run: a few LLM calls.
func (*EnrichWorker) Timeout(*river.Job[EnrichArgs]) time.Duration { return 10 * time.Minute }

// Work runs one pass. A failed pass is not retried: the next periodic run is.
func (w *EnrichWorker) Work(ctx context.Context, job *river.Job[EnrichArgs]) error {
	id := job.ID
	if err := w.e.Run(ctx, job.Args.Trigger, &id); err != nil {
		slog.ErrorContext(ctx, "catalog enrich failed", "err", err)
		return err
	}
	return nil
}

// Registrar adds the worker to a jobs.Deps.
func (e *Enricher) Registrar() func(*river.Workers) error {
	return func(ws *river.Workers) error { return river.AddWorkerSafely(ws, &EnrichWorker{e: e}) }
}

type intervalSchedule struct{ s *settings.Store }

func (i intervalSchedule) Next(t time.Time) time.Time {
	return t.Add(time.Duration(SettingEnrichIntervalMinutes.Get(i.s)) * time.Minute)
}

const enrichPeriodicID = "catalog_enrich"

// Attach installs the periodic job on client and keeps its interval in step
// with enrich.interval_minutes. Call once, after the client is built.
func (e *Enricher) Attach(client *jobs.Client) {
	e.client = client
	add := func() {
		client.PeriodicJobs().RemoveByID(enrichPeriodicID)
		client.PeriodicJobs().Add(river.NewPeriodicJob(
			intervalSchedule{e.d.Settings},
			func() (river.JobArgs, *river.InsertOpts) { return EnrichArgs{Trigger: TriggerScheduled}, nil },
			&river.PeriodicJobOpts{ID: enrichPeriodicID},
		))
	}
	add()
	e.d.Settings.OnChange(SettingEnrichIntervalMinutes.Key(), add)
}

// RunNow queues a manual run ("Run now"), audited.
func (e *Enricher) RunNow(ctx context.Context) error {
	if e.client == nil {
		return ErrEnrichNotAttached
	}
	tx, err := e.d.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := e.client.InsertTx(ctx, tx, EnrichArgs{Trigger: TriggerManual}, nil); err != nil {
		return fmt.Errorf("queue enrichment: %w", err)
	}
	if err := audit.Record(ctx, tx, "catalog.enrich.run", audit.Target{Type: "catalog", ID: "enrich"}, nil, nil); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// Sweepers prune old run records and cached answers (the retention job).
func (e *Enricher) Sweepers() []jobs.Sweeper {
	return []jobs.Sweeper{
		{
			Name: "enrich_run",
			Days: SettingEnrichRunRetentionDays,
			Sweep: func(ctx context.Context, before time.Time) (int64, error) {
				tag, err := e.d.Pool.Exec(ctx, `DELETE FROM enrich_run WHERE started_at < $1 AND outcome <> 'running'`, before)
				return tag.RowsAffected(), err
			},
		},
		{
			Name: "enrich_cache",
			Sweep: func(ctx context.Context, now time.Time) (int64, error) {
				tag, err := e.d.Pool.Exec(ctx, `DELETE FROM enrich_cache WHERE created_at < $1`, now.AddDate(0, 0, -cacheDays))
				return tag.RowsAffected(), err
			},
		},
	}
}
