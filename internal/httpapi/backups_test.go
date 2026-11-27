package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/a-jay98/stockpot/internal/backup"
	"github.com/a-jay98/stockpot/internal/telegram"
)

type fakeBackups struct {
	runErr, testErr error
	ran, tested     int
}

func (f *fakeBackups) Status(context.Context) (backup.Status, error) {
	fin := time.Date(2026, 11, 27, 8, 31, 0, 0, time.UTC)
	return backup.Status{
		Enabled: true, Configured: true, Time: "03:30", Timezone: backup.Location, Keep: 14, HasToken: true,
		ChatID: "-100123", HasRecipient: true, Recipient: "age1x",
		Channel: backup.ChannelStatus{Status: "ok", BotUsername: "bot"},
		Runs:    []backup.Run{{ID: uuid.New(), Trigger: "manual", Status: "ok", StartedAt: fin.Add(-time.Minute), FinishedAt: &fin, SizeBytes: 42, Parts: 2}},
	}, nil
}
func (f *fakeBackups) RunNow(context.Context) error      { f.ran++; return f.runErr }
func (f *fakeBackups) TestChannel(context.Context) error { f.tested++; return f.testErr }

func backupsHandler(t *testing.T, b BackupAdmin, az Authorizer) http.Handler {
	t.Helper()
	d := Deps{}
	if b != nil {
		d.Backups = b
	}
	return newHandler(t, NewServer(d), Options{PublicURL: testOrigin, Authorizer: az})
}

func TestBackupsAdminNeedsAdmin(t *testing.T) {
	h := backupsHandler(t, &fakeBackups{}, nil)
	for _, c := range [][2]string{{http.MethodGet, "/api/v1/admin/backups"}, {http.MethodPost, "/api/v1/admin/backups/run"}, {http.MethodPost, "/api/v1/admin/backups/test"}} {
		if rec := do(h, llmRequest(t, c[0], c[1], "")); rec.Code != http.StatusUnauthorized {
			t.Errorf("%v: %d", c, rec.Code)
		}
	}
}

func TestBackupsStatus(t *testing.T) {
	h := backupsHandler(t, &fakeBackups{}, allowAll{})
	rec := do(h, llmRequest(t, http.MethodGet, "/api/v1/admin/backups", ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	var b AdminBackups
	if err := json.Unmarshal(rec.Body.Bytes(), &b); err != nil {
		t.Fatal(err)
	}
	if !b.Enabled || b.Channel.Status != "ok" || len(b.Runs) != 1 || b.Runs[0].SizeBytes != 42 || b.Time != "03:30" {
		t.Fatalf("%+v", b)
	}
}

func TestBackupsRunAndTestErrors(t *testing.T) {
	for _, tc := range []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{"ok", nil, http.StatusAccepted, ""},
		{"not configured", backup.ErrNotConfigured, http.StatusConflict, CodeBackupNotConfigured},
		{"running", backup.ErrAlreadyRunning, http.StatusConflict, CodeBackupRunning},
	} {
		t.Run("run "+tc.name, func(t *testing.T) {
			f := &fakeBackups{runErr: tc.err}
			rec := do(backupsHandler(t, f, allowAll{}), llmRequest(t, http.MethodPost, "/api/v1/admin/backups/run", ""))
			if rec.Code != tc.status || f.ran != 1 {
				t.Fatalf("%d %s", rec.Code, rec.Body)
			}
			if tc.code != "" {
				var p Problem
				_ = json.Unmarshal(rec.Body.Bytes(), &p)
				if p.Code != tc.code {
					t.Fatalf("code %q", p.Code)
				}
			}
		})
	}
	f := &fakeBackups{testErr: &telegram.Error{Method: "sendMessage", Code: 400, Description: "chat not found"}}
	rec := do(backupsHandler(t, f, allowAll{}), llmRequest(t, http.MethodPost, "/api/v1/admin/backups/test", ""))
	if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), "chat not found") {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	rec = do(backupsHandler(t, &fakeBackups{}, allowAll{}), llmRequest(t, http.MethodPost, "/api/v1/admin/backups/test", ""))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("%d", rec.Code)
	}
	rec = do(backupsHandler(t, nil, allowAll{}), llmRequest(t, http.MethodGet, "/api/v1/admin/backups", ""))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("unwired: %d", rec.Code)
	}
}
