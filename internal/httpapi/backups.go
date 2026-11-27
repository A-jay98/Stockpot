package httpapi

import (
	"context"
	"errors"
	"net/http"

	"github.com/google/uuid"

	"github.com/a-jay98/stockpot/internal/backup"
	"github.com/a-jay98/stockpot/internal/telegram"
)

// Problem codes of the backups endpoints.
const (
	CodeBackupNotConfigured = "backup_not_configured"
	CodeBackupRunning       = "backup_running"
	CodeTelegramError       = "telegram_error"
)

// BackupAdmin is the part of backup.Service the API uses.
type BackupAdmin interface {
	Status(ctx context.Context) (backup.Status, error)
	RunNow(ctx context.Context) error
	TestChannel(ctx context.Context) error
}

var _ BackupAdmin = (*backup.Service)(nil)

func noBackups(ctx context.Context) Problem {
	return problemCtx(ctx, http.StatusServiceUnavailable, CodeNotFound, "Backups are not available in this build.")
}

func backupProblem(ctx context.Context, err error) (Problem, bool) {
	var te *telegram.Error
	switch {
	case errors.Is(err, backup.ErrNotConfigured), errors.Is(err, telegram.ErrNoToken):
		return problemCtx(ctx, http.StatusConflict, CodeBackupNotConfigured, backup.ErrNotConfigured.Error()), true
	case errors.Is(err, backup.ErrAlreadyRunning):
		return problemCtx(ctx, http.StatusConflict, CodeBackupRunning, "A backup is already running."), true
	case errors.As(err, &te):
		return problemCtx(ctx, http.StatusBadGateway, CodeTelegramError, "Telegram refused: "+te.Description), true
	}
	return Problem{}, false
}

// GetAdminBackups implements StrictServerInterface.
func (s *Server) GetAdminBackups(ctx context.Context, _ GetAdminBackupsRequestObject) (GetAdminBackupsResponseObject, error) {
	if s.backups == nil {
		p := noBackups(ctx)
		return GetAdminBackupsdefaultApplicationProblemPlusJSONResponse{Body: p, StatusCode: p.Status}, nil
	}
	st, err := s.backups.Status(ctx)
	if err != nil {
		return nil, err
	}
	out := AdminBackups{
		Enabled: st.Enabled, Configured: st.Configured, Time: st.Time, Timezone: st.Timezone,
		NextRunAt: st.NextRunAt, Keep: st.Keep, HasToken: st.HasToken, HasRecipient: st.HasRecipient,
		Runs: make([]AdminBackupRun, 0, len(st.Runs)),
	}
	out.ChatId = optString(st.ChatID)
	out.Recipient = optString(st.Recipient)
	out.Channel.Status = AdminBackupsChannelStatus(st.Channel.Status)
	out.Channel.BotUsername = optString(st.Channel.BotUsername)
	out.Channel.Detail = optString(st.Channel.Detail)
	for _, r := range st.Runs {
		out.Runs = append(out.Runs, AdminBackupRun{
			Id: uuid.UUID(r.ID), Trigger: AdminBackupRunTrigger(r.Trigger), Status: AdminBackupRunStatus(r.Status),
			StartedAt: r.StartedAt, FinishedAt: r.FinishedAt, SizeBytes: r.SizeBytes, Parts: r.Parts,
			AppVersion: optString(r.AppVersion), SchemaVersion: r.SchemaVersion, Error: r.Error,
			Pruned: r.PrunedAt != nil, DeleteNote: r.DeleteNote,
		})
	}
	return GetAdminBackups200JSONResponse(out), nil
}

// RunAdminBackup implements StrictServerInterface.
func (s *Server) RunAdminBackup(ctx context.Context, _ RunAdminBackupRequestObject) (RunAdminBackupResponseObject, error) {
	if s.backups == nil {
		p := noBackups(ctx)
		return RunAdminBackupdefaultApplicationProblemPlusJSONResponse{Body: p, StatusCode: p.Status}, nil
	}
	if err := s.backups.RunNow(ctx); err != nil {
		if p, ok := backupProblem(ctx, err); ok {
			return RunAdminBackupdefaultApplicationProblemPlusJSONResponse{Body: p, StatusCode: p.Status}, nil
		}
		return nil, err
	}
	return RunAdminBackup202Response{}, nil
}

// TestAdminBackupChannel implements StrictServerInterface.
func (s *Server) TestAdminBackupChannel(ctx context.Context, _ TestAdminBackupChannelRequestObject) (TestAdminBackupChannelResponseObject, error) {
	if s.backups == nil {
		p := noBackups(ctx)
		return TestAdminBackupChanneldefaultApplicationProblemPlusJSONResponse{Body: p, StatusCode: p.Status}, nil
	}
	if err := s.backups.TestChannel(ctx); err != nil {
		if p, ok := backupProblem(ctx, err); ok {
			return TestAdminBackupChanneldefaultApplicationProblemPlusJSONResponse{Body: p, StatusCode: p.Status}, nil
		}
		return nil, err
	}
	return TestAdminBackupChannel204Response{}, nil
}

func optString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
