package importer

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/a-jay98/stockpot/internal/commands"
	"github.com/a-jay98/stockpot/internal/identity"
)

// ImportCreate is recipe.import.create: one row per link, photo set or
// pasted text. A bulk import sends one command per line with the same batchId.
type ImportCreate struct {
	ID       uuid.UUID  `json:"id"`
	Kind     string     `json:"kind"`
	Input    *string    `json:"input"`
	BlobRefs []string   `json:"blobRefs"`
	BatchID  *uuid.UUID `json:"batchId"`
}

// ImportRef names an import (retry, cancel).
type ImportRef struct {
	ID uuid.UUID `json:"id"`
}

// ImportProvide is recipe.import.provide: the text the user pastes when an
// import needs input (a reel's caption, or the recipe itself).
type ImportProvide struct {
	ID   uuid.UUID `json:"id"`
	Text string    `json:"text"`
}

// Register adds the import commands to r.
func (s *Service) Register(r *commands.Registry) {
	commands.Register(r, "recipe.import.create", s.create)
	commands.Register(r, "recipe.import.retry", s.retry)
	commands.Register(r, "recipe.import.cancel", s.cancel)
	commands.Register(r, "recipe.import.provide", s.provide)
}

func (s *Service) create(ctx context.Context, tx pgx.Tx, p ImportCreate) error {
	hh, err := identity.HouseholdID(ctx)
	if err != nil {
		return err
	}
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM recipe_import WHERE id = $1)`, p.ID).Scan(&exists); err != nil {
		return err
	}
	if exists { // a replay under a new command id
		return commands.CheckOwned(ctx, tx, "recipe_import", p.ID, hh)
	}
	kind, input, refs, err := s.validateCreate(ctx, tx, hh, p)
	if err != nil {
		return err
	}
	pos := 0
	if p.BatchID != nil {
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM recipe_import WHERE batch_id = $1 AND household_id = $2`, *p.BatchID, hh).Scan(&pos); err != nil {
			return err
		}
		if pos >= maxBatch {
			return commands.Invalid("at most %d recipes in one bulk import", maxBatch)
		}
	}
	if refs == nil {
		refs = []string{}
	}
	if _, err := tx.Exec(ctx, `INSERT INTO recipe_import (id, household_id, batch_id, kind, input, blob_refs, status)
		VALUES ($1, $2, $3, $4, $5, $6, 'queued')`, p.ID, hh, p.BatchID, kind, input, refs); err != nil {
		return err
	}
	return s.enqueue(ctx, tx, p.ID, pos >= backgroundFrom)
}

func (s *Service) validateCreate(ctx context.Context, tx pgx.Tx, hh uuid.UUID, p ImportCreate) (kind string, input *string, refs []string, err error) {
	text := ""
	if p.Input != nil {
		text = strings.TrimSpace(*p.Input)
	}
	switch p.Kind {
	case KindURL, KindInstagram:
		if len(p.BlobRefs) > 0 {
			return "", nil, nil, commands.Invalid("a link import takes no photos")
		}
		if len(text) > 2000 {
			return "", nil, nil, commands.Invalid("the link is longer than 2000 characters")
		}
		u, err := ParseURL(text)
		if err != nil {
			return "", nil, nil, commands.Invalid("%q is not a web link", truncate(text, 80))
		}
		text = u.String()
		kind = KindURL
		if IsInstagram(text) {
			if _, err := canonicalReel(text); err != nil {
				return "", nil, nil, commands.Invalid("that Instagram link is not a post or reel")
			}
			kind = KindInstagram
		} else if !FlagWebImport.Get(s.d.Settings) {
			return "", nil, nil, commands.Invalid("recipe import from links is turned off")
		}
		return kind, &text, nil, nil
	case KindText:
		if len(p.BlobRefs) > 0 {
			return "", nil, nil, commands.Invalid("a text import takes no photos")
		}
		if text == "" || utf8.RuneCountInString(text) > maxTextInput {
			return "", nil, nil, commands.Invalid("pasted text must be 1-%d characters", maxTextInput)
		}
		return KindText, &text, nil, nil
	case KindPhoto:
		if len(p.BlobRefs) < 1 || len(p.BlobRefs) > maxPhotos {
			return "", nil, nil, commands.Invalid("a photo import takes 1-%d photos", maxPhotos)
		}
		seen := map[string]bool{}
		for _, r := range p.BlobRefs {
			if !validRef(r) {
				return "", nil, nil, commands.Invalid("photo must be a blob ref")
			}
			if seen[r] {
				continue
			}
			seen[r] = true
			ok, err := s.owned(ctx, tx, hh, r)
			if err != nil {
				return "", nil, nil, err
			}
			if !ok {
				return "", nil, nil, commands.Invalid("photo %s is not one of this household's uploads", r[:12])
			}
			refs = append(refs, r)
		}
		var note *string
		if text != "" {
			if utf8.RuneCountInString(text) > 2000 {
				return "", nil, nil, commands.Invalid("the note is longer than 2000 characters")
			}
			note = &text
		}
		return KindPhoto, note, refs, nil
	}
	return "", nil, nil, commands.Invalid("kind must be url, instagram, photo or text")
}

func (s *Service) owned(ctx context.Context, tx pgx.Tx, hh uuid.UUID, ref string) (bool, error) {
	if s.d.Owned != nil {
		return s.d.Owned(ctx, tx, hh, ref)
	}
	rc, err := s.d.Blobs.Open(ctx, blobRef(ref))
	if err != nil {
		return false, nil //nolint:nilerr // missing means not usable
	}
	_ = rc.Close()
	return true, nil
}

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

// lockImport loads an import of the caller's household for update.
func lockImport(ctx context.Context, tx pgx.Tx, id uuid.UUID) (status string, err error) {
	hh, err := identity.HouseholdID(ctx)
	if err != nil {
		return "", err
	}
	err = tx.QueryRow(ctx, `SELECT status FROM recipe_import WHERE id = $1 AND household_id = $2 AND deleted_at IS NULL FOR UPDATE`, id, hh).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", commands.Gone("that import no longer exists")
	}
	return status, err
}

func retriable(status string) bool {
	return status == StatusFailed || status == StatusNeedsInput || status == StatusWaitingQuota
}

func (s *Service) retry(ctx context.Context, tx pgx.Tx, p ImportRef) error {
	status, err := lockImport(ctx, tx, p.ID)
	if err != nil {
		return err
	}
	if !retriable(status) {
		return nil // already queued, running or done: nothing to do
	}
	if _, err := tx.Exec(ctx, `UPDATE recipe_import SET status = 'queued', error = NULL, attempts = 0 WHERE id = $1`, p.ID); err != nil {
		return err
	}
	return s.enqueue(ctx, tx, p.ID, false)
}

func (s *Service) cancel(ctx context.Context, tx pgx.Tx, p ImportRef) error {
	status, err := lockImport(ctx, tx, p.ID)
	var ce *commands.Error
	if errors.As(err, &ce) && ce.Code == commands.CodeGone {
		return nil // already cancelled
	}
	if err != nil {
		return err
	}
	if status == StatusDone {
		return commands.Invalid("that import is done; delete the recipe instead")
	}
	// The job sees the tombstone at its next stage and stops.
	_, err = tx.Exec(ctx, `UPDATE recipe_import SET deleted_at = now() WHERE id = $1`, p.ID)
	return err
}

func (s *Service) provide(ctx context.Context, tx pgx.Tx, p ImportProvide) error {
	status, err := lockImport(ctx, tx, p.ID)
	if err != nil {
		return err
	}
	text := strings.TrimSpace(p.Text)
	if text == "" || utf8.RuneCountInString(text) > maxTextInput {
		return commands.Invalid("the text must be 1-%d characters", maxTextInput)
	}
	if status == StatusDone {
		return commands.Invalid("that import is done")
	}
	if _, err := tx.Exec(ctx, `INSERT INTO recipe_import_detail (import_id, provided_text) VALUES ($1, $2)
		ON CONFLICT (import_id) DO UPDATE SET provided_text = excluded.provided_text, updated_at = now()`, p.ID, text); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE recipe_import SET status = 'queued', error = NULL, attempts = 0 WHERE id = $1`, p.ID); err != nil {
		return err
	}
	return s.enqueue(ctx, tx, p.ID, false)
}

func (s *Service) enqueue(ctx context.Context, tx pgx.Tx, id uuid.UUID, background bool) error {
	if s.client == nil {
		return ErrNotAttached
	}
	if _, err := s.client.InsertTx(ctx, tx, Args{ImportID: id, Background: background}, nil); err != nil {
		return fmt.Errorf("queue recipe import: %w", err)
	}
	return nil
}
