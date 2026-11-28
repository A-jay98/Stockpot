package recipes

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/a-jay98/stockpot/internal/commands"
	"github.com/a-jay98/stockpot/internal/identity"
	"github.com/a-jay98/stockpot/internal/media"
)

// PhotoAdd is recipe.photo.add: attach an uploaded photo to a recipe
// (and optionally to one cook).
type PhotoAdd struct {
	ID            uuid.UUID  `json:"id"`
	RecipeID      uuid.UUID  `json:"recipeId"`
	BlobRef       string     `json:"blobRef"`
	CookSessionID *uuid.UUID `json:"cookSessionId"`
	TakenOn       *string    `json:"takenOn"`
	Caption       *string    `json:"caption"`
}

// PhotoRemove is recipe.photo.remove.
type PhotoRemove struct {
	ID uuid.UUID `json:"id"`
}

// requireBlob rejects a ref the household has not uploaded: the outbox
// uploads photos before the commands that use them, so a missing owner row
// means the upload failed for good.
func requireBlob(ctx context.Context, tx pgx.Tx, hh uuid.UUID, ref string) error {
	if !validRef(ref) {
		return commands.Invalid("photo must be a blob ref")
	}
	ok, err := media.Owned(ctx, tx, hh, ref)
	if err != nil {
		return err
	}
	if !ok {
		return commands.Invalid("the photo was not uploaded")
	}
	return nil
}

func (h *Handlers) photoAdd(ctx context.Context, tx pgx.Tx, p PhotoAdd) error {
	hh, err := identity.HouseholdID(ctx)
	if err != nil {
		return err
	}
	caption, err := optText("caption", p.Caption, 300)
	if err != nil {
		return err
	}
	var takenOn *time.Time
	if p.TakenOn != nil {
		d, err := time.Parse(time.DateOnly, *p.TakenOn)
		if err != nil {
			return commands.Invalid("takenOn must be a date (YYYY-MM-DD)")
		}
		takenOn = &d
	}
	if err := requireBlob(ctx, tx, hh, p.BlobRef); err != nil {
		return err
	}
	var live bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM recipe WHERE id = $1 AND household_id = $2 AND deleted_at IS NULL)`,
		p.RecipeID, hh).Scan(&live); err != nil {
		return err
	}
	if !live {
		return commands.Gone("recipe no longer exists")
	}
	if p.CookSessionID != nil {
		var ok bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM cook_session WHERE id = $1 AND household_id = $2 AND recipe_id = $3 AND deleted_at IS NULL)`,
			*p.CookSessionID, hh, p.RecipeID).Scan(&ok); err != nil {
			return err
		}
		if !ok {
			return commands.Gone("that cook no longer exists")
		}
	}
	tag, err := tx.Exec(ctx, `INSERT INTO recipe_photo (id, household_id, recipe_id, cook_session_id, blob_ref, taken_on, caption, created_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8) ON CONFLICT (id) DO NOTHING`,
		p.ID, hh, p.RecipeID, p.CookSessionID, p.BlobRef, takenOn, caption, user(ctx))
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return commands.CheckOwned(ctx, tx, "recipe_photo", p.ID, hh)
	}
	return nil
}

func (h *Handlers) photoRemove(ctx context.Context, tx pgx.Tx, p PhotoRemove) error {
	hh, err := identity.HouseholdID(ctx)
	if err != nil {
		return err
	}
	// Removing an already removed photo is a no-op (the user's intent holds).
	_, err = tx.Exec(ctx, `UPDATE recipe_photo SET deleted_at = now() WHERE id = $1 AND household_id = $2 AND deleted_at IS NULL`, p.ID, hh)
	return err
}
