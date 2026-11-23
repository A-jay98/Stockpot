package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/a-jay98/stockpot/internal/reqctx"
	"github.com/a-jay98/stockpot/internal/settings"
)

// Settings lists every declared setting with its effective value (secrets
// redacted).
func (s *Service) Settings() []settings.View { return s.d.Settings.List() }

// SetSetting validates and stores a value, audited. A *settings.ValidationError
// means the value was rejected.
func (s *Service) SetSetting(ctx context.Context, key string, value json.RawMessage) (settings.View, error) {
	return s.changeSetting(ctx, key, "setting.update", func(tx pgx.Tx) (settings.Change, error) {
		return s.d.Settings.SetTx(ctx, tx, key, value, actor(ctx))
	})
}

// ResetSetting deletes the stored value so the default applies, audited.
func (s *Service) ResetSetting(ctx context.Context, key string) (settings.View, error) {
	return s.changeSetting(ctx, key, "setting.reset", func(tx pgx.Tx) (settings.Change, error) {
		return s.d.Settings.ResetTx(ctx, tx, key)
	})
}

func (s *Service) changeSetting(ctx context.Context, key, action string, change func(pgx.Tx) (settings.Change, error)) (settings.View, error) {
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		ch, err := change(tx)
		if err != nil {
			return err
		}
		return s.d.Audit(ctx, tx, action, Target{Type: "setting", ID: key}, settingSnapshot(ch.WasSet, ch.Before), settingSnapshot(ch.IsSet, ch.After))
	})
	if errors.Is(err, settings.ErrUnknownKey) {
		return settings.View{}, ErrNotFound
	}
	if err != nil {
		return settings.View{}, err
	}
	if err := s.d.Settings.Refresh(ctx, key); err != nil {
		return settings.View{}, fmt.Errorf("refresh setting %s: %w", key, err)
	}
	v, _ := s.d.Settings.View(key)
	return v, nil
}

// settingSnapshot is the audit form of a stored value: {"set": false} when
// the default applies, and no value for secrets (Change leaves it nil).
func settingSnapshot(set bool, v any) map[string]any {
	m := map[string]any{"set": set}
	if set && v != nil {
		m["value"] = v
	}
	return m
}

func actor(ctx context.Context) *uuid.UUID {
	if info := reqctx.From(ctx); info != nil && info.UserID != uuid.Nil {
		id := info.UserID
		return &id
	}
	return nil
}
