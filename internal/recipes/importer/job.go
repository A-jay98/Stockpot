package importer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/a-jay98/stockpot/internal/audit"
	"github.com/a-jay98/stockpot/internal/blob"
	"github.com/a-jay98/stockpot/internal/commands"
	"github.com/a-jay98/stockpot/internal/llm"
	"github.com/a-jay98/stockpot/internal/recipes"
	"github.com/a-jay98/stockpot/internal/reqctx"
	"github.com/a-jay98/stockpot/internal/syncdb"
)

// Actor is recorded in the audit trail for what the import job writes.
const Actor = "system:recipe_import"

// maxAttempts is how many times a failing import is tried before it is
// marked failed (snoozes for quota or a busy household don't count).
const maxAttempts = 3

// Args is the recipe.import job: one per recipe_import row.
type Args struct {
	ImportID uuid.UUID `json:"import_id" river:"unique"`
	// Background: a bulk import's fourth row onwards, so it never starves
	// interactive LLM features.
	Background bool `json:"background,omitempty"`
}

// Kind names the job in River's tables.
func (Args) Kind() string { return "recipe.import" }

// InsertOpts keeps one live job per import, on the import queue.
func (Args) InsertOpts() river.InsertOpts {
	return river.InsertOpts{
		Queue:       QueueName,
		MaxAttempts: maxAttempts,
		UniqueOpts: river.UniqueOpts{ByArgs: true, ByState: []rivertype.JobState{
			rivertype.JobStateAvailable, rivertype.JobStatePending, rivertype.JobStateRetryable,
			rivertype.JobStateRunning, rivertype.JobStateScheduled,
		}},
	}
}

// Worker runs imports.
type Worker struct {
	river.WorkerDefaults[Args]
	s *Service
}

// Timeout bounds one attempt: a page, a reel download and transcription, and two LLM calls.
func (*Worker) Timeout(*river.Job[Args]) time.Duration { return 8 * time.Minute }

// NextRetry backs off 30 s, then 2 min.
func (*Worker) NextRetry(job *river.Job[Args]) time.Time {
	return time.Now().Add(time.Duration(job.Attempt*job.Attempt) * 30 * time.Second)
}

// Registrar adds the worker to a jobs.Deps.
func (s *Service) Registrar() func(*river.Workers) error {
	return func(ws *river.Workers) error { return river.AddWorkerSafely(ws, &Worker{s: s}) }
}

// Queues is the import queue for jobs.Deps.
func Queues() map[string]river.QueueConfig {
	return map[string]river.QueueConfig{QueueName: {MaxWorkers: maxGlobal}}
}

// problem is recipe_import.error, as the client shows it.
type problem struct {
	Code   string `json:"code"`
	Detail string `json:"detail"`
}

// permanent wraps an error that retrying can't fix.
type permanent struct {
	code string
	err  error
}

func (p *permanent) Error() string { return p.err.Error() }
func (p *permanent) Unwrap() error { return p.err }

func fail(code string, err error) error { return &permanent{code: code, err: err} }

// needsInput stops the import until the user pastes text.
type needsInput struct{ why string }

func (n *needsInput) Error() string { return "needs input: " + n.why }

// row is what the job reads of recipe_import.
type row struct {
	ID        uuid.UUID
	Household uuid.UUID
	Kind      string
	Input     *string
	Refs      []string
	Status    string
	Deleted   bool
	Provided  *string
	Caption   *string
	Script    *string // transcript
}

// Work runs one attempt of the stage machine.
func (w *Worker) Work(ctx context.Context, job *river.Job[Args]) error {
	s := w.s
	r, err := s.load(ctx, job.Args.ImportID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if r.Deleted || r.Status == StatusDone {
		return nil
	}
	if !s.acquire(r.Household) {
		return river.JobSnooze(10 * time.Second)
	}
	defer s.release(r.Household)

	ctx = reqctx.With(ctx, &reqctx.Info{HouseholdID: r.Household, RequestID: fmt.Sprintf("job-%d", job.ID)})
	prio := llm.Interactive
	if job.Args.Background {
		prio = llm.Background
	}
	s.setAttempts(ctx, r.ID, job.Attempt)
	recipeID, err := s.run(ctx, r, prio)
	var (
		perm *permanent
		need *needsInput
	)
	switch {
	case err == nil:
		slog.InfoContext(ctx, "recipe imported", "import", r.ID, "recipe", recipeID)
		return nil
	case errors.Is(err, errCancelled):
		return nil
	case errors.As(err, &need):
		s.setStatus(ctx, r.ID, StatusNeedsInput, &problem{Code: "needs_input", Detail: need.why})
		return nil
	case errors.Is(err, llm.ErrUnavailable):
		s.setStatus(ctx, r.ID, StatusWaitingQuota, &problem{Code: "llm_unavailable", Detail: "Waiting for AI quota. It continues by itself."})
		return river.JobSnooze(s.quotaWait(ctx, r.Kind == KindPhoto))
	case errors.As(err, &perm):
		s.setStatus(ctx, r.ID, StatusFailed, &problem{Code: perm.code, Detail: userMessage(perm.err)})
		return nil
	case errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded):
		if ctx.Err() != nil && job.Attempt < job.MaxAttempts {
			s.setStatus(ctx, r.ID, StatusQueued, nil)
			return err
		}
	}
	slog.WarnContext(ctx, "recipe import attempt failed", "import", r.ID, "attempt", job.Attempt, "err", err)
	if job.Attempt >= job.MaxAttempts {
		code := "failed"
		if errors.Is(err, llm.ErrBadOutput) {
			code = "llm_bad_output"
		} else if Retryable(err) {
			code = "fetch_failed"
		}
		s.setStatus(ctx, r.ID, StatusFailed, &problem{Code: code, Detail: userMessage(err)})
		return nil
	}
	s.setStatus(ctx, r.ID, StatusQueued, nil)
	return err
}

var errCancelled = errors.New("import cancelled")

// quotaWait is how long to snooze while the LLM has no quota: until the
// router expects a provider back, between one minute and six hours.
func (s *Service) quotaWait(ctx context.Context, vision bool) time.Duration {
	wait := 15 * time.Minute
	if h, ok := s.d.LLM.(interface {
		Headroom(context.Context, llm.Needs) (bool, time.Time)
	}); ok {
		if _, at := h.Headroom(ctx, llm.Needs{Vision: vision}); !at.IsZero() {
			wait = time.Until(at) + time.Minute
		}
	}
	return min(max(wait, time.Minute), 6*time.Hour)
}

func userMessage(err error) string {
	var se *StatusError
	switch {
	case errors.As(err, &se):
		return se.Error() + "."
	case errors.Is(err, ErrBlockedAddress):
		return "That address can't be fetched from the server."
	case errors.Is(err, ErrNotHTML):
		return "That link isn't a web page."
	case errors.Is(err, ErrTooLarge):
		return "That page is too large."
	case errors.Is(err, ErrTooManyHops):
		return "That link redirects too many times."
	case errors.Is(err, ErrFetchNetwork):
		return "Couldn't reach the site."
	case errors.Is(err, ErrNotARecipe):
		return "No recipe found there. Paste the text instead."
	case errors.Is(err, llm.ErrBadOutput):
		return "The AI couldn't read this recipe. Try again or paste the text."
	}
	var ce *commands.Error
	if errors.As(err, &ce) {
		return ce.Detail
	}
	return "Something went wrong importing this recipe."
}

func (s *Service) load(ctx context.Context, id uuid.UUID) (row, error) {
	var r row
	err := s.d.Pool.QueryRow(ctx, `SELECT i.id, i.household_id, i.kind, i.input, i.blob_refs, i.status, i.deleted_at IS NOT NULL,
			d.provided_text, d.caption, d.transcript
		FROM recipe_import i LEFT JOIN recipe_import_detail d ON d.import_id = i.id WHERE i.id = $1`, id).
		Scan(&r.ID, &r.Household, &r.Kind, &r.Input, &r.Refs, &r.Status, &r.Deleted, &r.Provided, &r.Caption, &r.Script)
	return r, err
}

// setStatus moves a live import to status. Status changes are synced rows,
// so the client's progress list updates through SSE.
func (s *Service) setStatus(ctx context.Context, id uuid.UUID, status string, p *problem) {
	var raw []byte
	if p != nil {
		raw, _ = json.Marshal(p)
	}
	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if _, err := s.d.Pool.Exec(wctx, `UPDATE recipe_import SET status = $2, error = $3
		WHERE id = $1 AND deleted_at IS NULL AND status <> 'done' AND (status <> $2 OR error IS DISTINCT FROM $3)`, id, status, raw); err != nil {
		slog.ErrorContext(ctx, "recipe import: set status", "import", id, "err", err)
	}
}

func (s *Service) setAttempts(ctx context.Context, id uuid.UUID, n int) {
	if _, err := s.d.Pool.Exec(ctx, `UPDATE recipe_import SET attempts = $2 WHERE id = $1 AND attempts <> $2`, id, n); err != nil {
		slog.WarnContext(ctx, "recipe import: set attempts", "import", id, "err", err)
	}
}

// stage moves to the next status unless the import was cancelled meanwhile.
func (s *Service) stage(ctx context.Context, id uuid.UUID, status string) error {
	tag, err := s.d.Pool.Exec(ctx, `UPDATE recipe_import SET status = $2, error = NULL WHERE id = $1 AND deleted_at IS NULL AND status <> 'done'`, id, status)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return errCancelled
	}
	return nil
}

// run is the stage machine; it returns the new recipe's id.
func (s *Service) run(ctx context.Context, r row, prio llm.Priority) (uuid.UUID, error) {
	var (
		d   *Draft
		err error
		src *string // source URL
	)
	switch r.Kind {
	case KindURL:
		src = r.Input
		d, err = s.fromURL(ctx, r, prio)
	case KindInstagram:
		src = r.Input
		d, err = s.fromReel(ctx, r, prio)
	case KindText:
		d, err = s.fromText(ctx, r, deref0(r.Input), prio)
	case KindPhoto:
		d, err = s.fromPhotos(ctx, r, prio)
	default:
		err = fail("bad_kind", fmt.Errorf("unknown import kind %q", r.Kind))
	}
	if err != nil {
		return uuid.Nil, err
	}

	var original *Draft
	if d.Lang != "" && d.Lang != "en" {
		if err := s.stage(ctx, r.ID, StatusTranslating); err != nil {
			return uuid.Nil, err
		}
		en, err := s.translate(ctx, d, prio)
		if err != nil {
			return uuid.Nil, err
		}
		original, d = d, en
	}
	cover := s.cover(ctx, r, d.ImageURL)
	if len(r.Refs) > 0 && cover == "" && r.Kind == KindPhoto {
		cover = r.Refs[0]
	}
	return s.write(ctx, r, d, original, src, cover)
}

func deref0(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func (s *Service) fromURL(ctx context.Context, r row, prio llm.Priority) (*Draft, error) {
	if r.Provided != nil { // the user pasted the recipe after the page failed
		return s.fromText(ctx, r, *r.Provided, prio)
	}
	if !FlagWebImport.Get(s.d.Settings) {
		return nil, fail("disabled", errors.New("recipe import from links is turned off"))
	}
	if err := s.stage(ctx, r.ID, StatusFetching); err != nil {
		return nil, err
	}
	page, lg, err := s.d.Fetcher.FetchPage(ctx, deref0(r.Input))
	s.logFetch(ctx, r, lg)
	if err != nil {
		if Retryable(err) {
			return nil, err
		}
		return nil, fail(fetchCode(err), err)
	}
	doc, err := ParseDocument(page.Body, page.URL)
	if err != nil {
		return nil, fail("unreadable_page", err)
	}
	if d := doc.Recipe(); d != nil && d.usable() {
		d.Lang = draftLang(d, doc.Lang)
		return d, nil
	}
	text := doc.MainText()
	if len([]rune(text)) < 80 {
		return nil, fail("not_a_recipe", ErrNotARecipe)
	}
	if err := s.stage(ctx, r.ID, StatusParsing); err != nil {
		return nil, err
	}
	d, err := s.parseText(ctx, text, "Main text of the web page "+page.URL.String()+":", prio)
	if err != nil {
		return nil, wrapParse(err)
	}
	d.Lang = llmLang(d.Lang, text)
	if d.ImageURL == "" {
		d.ImageURL = doc.OGImage()
	}
	return d, nil
}

func fetchCode(err error) string {
	var se *StatusError
	switch {
	case errors.As(err, &se):
		return fmt.Sprintf("http_%d", se.Status)
	case errors.Is(err, ErrBlockedAddress):
		return "blocked_address"
	case errors.Is(err, ErrNotHTML):
		return "not_html"
	case errors.Is(err, ErrTooLarge):
		return "too_large"
	case errors.Is(err, ErrTooManyHops):
		return "too_many_redirects"
	case errors.Is(err, ErrBadURL):
		return "bad_url"
	}
	return "fetch_failed"
}

func wrapParse(err error) error {
	if errors.Is(err, ErrNotARecipe) {
		return fail("not_a_recipe", err)
	}
	return err
}

// llmLang settles the language of an LLM draft: the script heuristic wins
// for non-Latin text, else the model's answer, else English.
func llmLang(model, text string) string {
	if s := DetectScript(text); s != "" && (model == "" || model == "en") {
		return s
	}
	if model != "" {
		return model
	}
	return "en"
}

func (s *Service) fromText(ctx context.Context, r row, text string, prio llm.Priority) (*Draft, error) {
	if d := SplitStructured(text); d != nil {
		d.Lang = draftLang(d, "")
		return d, nil
	}
	if err := s.stage(ctx, r.ID, StatusParsing); err != nil {
		return nil, err
	}
	d, err := s.parseText(ctx, text, "Pasted recipe text:", prio)
	if err != nil {
		return nil, wrapParse(err)
	}
	d.Lang = llmLang(d.Lang, text)
	return d, nil
}

func (s *Service) fromReel(ctx context.Context, r row, prio llm.Priority) (*Draft, error) {
	caption, transcript := deref0(r.Caption), deref0(r.Script)
	if r.Provided == nil && caption == "" && transcript == "" {
		if !FlagInstagram.Get(s.d.Settings) {
			return nil, &needsInput{why: "Paste the reel's caption (and what it shows) to import it."}
		}
		if err := s.stage(ctx, r.ID, StatusFetching); err != nil {
			return nil, err
		}
		withAudio := s.d.TranscriptionAvailable == nil || s.d.TranscriptionAvailable(ctx)
		reel, err := s.d.Reels.Read(ctx, deref0(r.Input), withAudio)
		switch {
		case errors.Is(err, ErrReelBlocked), errors.Is(err, ErrToolMissing):
			slog.InfoContext(ctx, "recipe import: reel unreadable", "import", r.ID, "err", err)
			return nil, &needsInput{why: "Instagram didn't let us read this reel. Paste its caption (and what it shows) instead."}
		case errors.Is(err, ErrBadURL):
			return nil, fail("bad_url", err)
		case err != nil:
			return nil, err
		}
		caption = reel.Caption
		if len(reel.Audio) > 0 {
			t, err := s.d.LLM.Transcribe(ctx, llm.TranscribeRequest{
				Feature: FeatureTranscribe, Audio: reel.Audio, Filename: "audio.m4a", MIME: "audio/mp4",
				Prompt: truncate(reel.Title, 200), Priority: prio,
			})
			switch {
			case err == nil:
				transcript = t.Text
			case errors.Is(err, llm.ErrUnavailable) && caption != "":
				// The caption may be enough; the transcript is a bonus.
			default:
				return nil, err
			}
		}
		if err := s.saveDetail(ctx, r.ID, caption, transcript); err != nil {
			return nil, err
		}
		if strings.TrimSpace(caption+transcript) == "" {
			return nil, &needsInput{why: "This reel has no caption or speech to read. Paste the recipe instead."}
		}
	}
	var b strings.Builder
	if r.Provided != nil {
		b.WriteString("What the user pasted about the reel:\n" + *r.Provided + "\n\n")
	}
	if caption != "" {
		b.WriteString("Caption:\n" + caption + "\n\n")
	}
	if transcript != "" {
		b.WriteString("Transcript of the audio:\n" + transcript + "\n")
	}
	if err := s.stage(ctx, r.ID, StatusParsing); err != nil {
		return nil, err
	}
	text := b.String()
	d, err := s.parseText(ctx, text, "An Instagram cooking reel. Use the caption and the transcript together.", prio)
	if err != nil {
		if errors.Is(err, ErrNotARecipe) {
			return nil, &needsInput{why: "We couldn't find a recipe in this reel. Paste it, or describe what it shows."}
		}
		return nil, err
	}
	d.Lang = llmLang(d.Lang, text)
	return d, nil
}

func (s *Service) saveDetail(ctx context.Context, id uuid.UUID, caption, transcript string) error {
	_, err := s.d.Pool.Exec(ctx, `INSERT INTO recipe_import_detail (import_id, caption, transcript) VALUES ($1, $2, $3)
		ON CONFLICT (import_id) DO UPDATE SET caption = excluded.caption, transcript = excluded.transcript, updated_at = now()`,
		id, caption, transcript)
	return err
}

func (s *Service) fromPhotos(ctx context.Context, r row, prio llm.Priority) (*Draft, error) {
	if r.Provided != nil {
		return s.fromText(ctx, r, *r.Provided, prio)
	}
	if err := s.stage(ctx, r.ID, StatusParsing); err != nil {
		return nil, err
	}
	var photos []Photo
	for _, ref := range r.Refs {
		mt, err := sniffImage(ctx, s.d.Blobs, blob.Ref(ref))
		if err != nil {
			if errors.Is(err, blob.ErrNotFound) {
				return nil, fail("photo_missing", errors.New("a photo is missing; add it again"))
			}
			return nil, err
		}
		photos = append(photos, Photo{Ref: blob.Ref(ref), MIME: mt})
	}
	d, err := s.parsePhotos(ctx, photos, prio)
	if err != nil {
		return nil, wrapParse(err)
	}
	d.Lang = llmLang(d.Lang, d.Title+"\n"+strings.Join(d.Ingredients, "\n"))
	if note := deref0(r.Input); note != "" && d.Notes == "" {
		d.Notes = note
	}
	return d, nil
}

// cover downloads and stores the cover image; failures only log.
func (s *Service) cover(ctx context.Context, r row, imageURL string) string {
	if imageURL == "" || s.d.Covers == nil {
		return ""
	}
	page, lg, err := s.d.Fetcher.FetchImage(ctx, imageURL)
	s.logFetch(ctx, r, lg)
	if err != nil {
		slog.InfoContext(ctx, "recipe import: no cover", "import", r.ID, "err", err)
		return ""
	}
	ref, err := s.d.Covers.SaveCover(ctx, r.Household, page.ContentType, page.Body)
	if err != nil {
		slog.InfoContext(ctx, "recipe import: cover not stored", "import", r.ID, "err", err)
		return ""
	}
	return ref
}

func (s *Service) logFetch(ctx context.Context, r row, lg FetchLog) {
	var msg *string
	if lg.Err != nil {
		m := truncate(lg.Err.Error(), 500)
		msg = &m
	}
	var status *int
	if lg.Status != 0 {
		status = &lg.Status
	}
	var final *string
	if lg.FinalURL != "" && lg.FinalURL != lg.URL {
		f := truncate(lg.FinalURL, 2000)
		final = &f
	}
	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if _, err := s.d.Pool.Exec(wctx, `INSERT INTO recipe_fetch (household_id, import_id, purpose, url, final_url, status, bytes, duration_ms, redirects, error)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
		r.Household, r.ID, lg.Purpose, truncate(lg.URL, 2000), final, status, lg.Bytes, lg.Duration.Milliseconds(), lg.Redirects, msg); err != nil {
		slog.WarnContext(ctx, "recipe import: fetch log", "err", err)
	}
}

// write creates the recipe and its version 1, and marks the import done.
func (s *Service) write(ctx context.Context, r row, d *Draft, original *Draft, src *string, cover string) (uuid.UUID, error) {
	recipeID, versionID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	var origJSON json.RawMessage
	lang := "en"
	if original != nil {
		lang = original.Lang
		b, err := json.Marshal(Original{
			Lang: original.Lang, Title: original.Title, Subtitle: original.Subtitle,
			Ingredients: original.Ingredients, Steps: original.Steps, Notes: original.Notes,
		})
		if err != nil {
			return uuid.Nil, err
		}
		origJSON = b
	}
	var srcLang *string
	if lang != "en" {
		srcLang = &lang
	}
	notes := nilIfEmpty(d.Notes)
	tags := d.Tags
	if tags == nil {
		tags = []string{}
	}
	err := pgx.BeginFunc(ctx, s.d.Pool, func(tx pgx.Tx) error {
		if err := syncdb.LockHousehold(ctx, tx, r.Household); err != nil {
			return err
		}
		var status string
		var deleted bool
		if err := tx.QueryRow(ctx, `SELECT status, deleted_at IS NOT NULL FROM recipe_import WHERE id = $1 FOR UPDATE`, r.ID).Scan(&status, &deleted); err != nil {
			return err
		}
		if deleted || status == StatusDone {
			return errCancelled
		}
		var coverRef *string
		if cover != "" {
			coverRef = &cover
		}
		if _, err := tx.Exec(ctx, `INSERT INTO recipe (id, household_id, title, subtitle, source_kind, source_url, source_lang, tags, cover_blob, saved, import_id)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, true, $10)`,
			recipeID, r.Household, d.Title, nilIfEmpty(d.Subtitle), r.Kind, src, srcLang, tags, coverRef, r.ID); err != nil {
			return err
		}
		v, err := s.d.Writer.WriteVersion(ctx, tx, recipes.VersionInput{
			ID: versionID, RecipeID: recipeID, Household: r.Household, Origin: recipes.OriginImport,
			Title: d.Title, Servings: d.Servings, TotalMinutes: d.TotalMinutes, Ingredients: clip(d.Ingredients, 200),
			Steps: clipSteps(d.Steps, 100), Notes: notes, Original: origJSON,
		})
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE recipe_import SET status = 'done', error = NULL, recipe_id = $2, detected_lang = $3 WHERE id = $1`,
			r.ID, recipeID, lang); err != nil {
			return err
		}
		return audit.Record(ctx, tx, "recipe.import.done", audit.Target{Type: "recipe", ID: recipeID.String()}, nil, map[string]any{
			"actor": Actor, "import_id": r.ID, "kind": r.Kind, "via": d.Via, "lang": lang, "version": v.N,
			"lines": len(v.Lines), "steps": len(d.Steps), "cover": cover != "",
		})
	})
	var ce *commands.Error
	if errors.As(err, &ce) {
		return uuid.Nil, fail("invalid_recipe", err)
	}
	return recipeID, err
}

func clip(ls []string, n int) []string {
	if len(ls) > n {
		return ls[:n]
	}
	return ls
}

func clipSteps(ss []recipes.Step, n int) []recipes.Step {
	if len(ss) > n {
		return ss[:n]
	}
	return ss
}

func nilIfEmpty(s string) *string {
	if s = strings.TrimSpace(s); s == "" {
		return nil
	}
	return &s
}

func blobRef(s string) blob.Ref { return blob.Ref(s) }
