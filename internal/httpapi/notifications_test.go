package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/a-jay98/stockpot/internal/alerts"
	"github.com/a-jay98/stockpot/internal/identity"
	"github.com/a-jay98/stockpot/internal/telegram"
)

type fakeAlerts struct {
	user     uuid.UUID
	prefs    alerts.Prefs
	setErr   error
	linkErr  error
	testErr  error
	read     []uuid.UUID
	readCall int
	unlinked int
}

func (f *fakeAlerts) List(_ context.Context, u uuid.UUID, _ alerts.ListParams) ([]alerts.Notification, int, error) {
	f.user = u
	return []alerts.Notification{{
		ID: uuid.New(), HouseholdID: uuid.New(), Kind: "expiry_digest", Title: "1 expired", Body: "Expired\n• Milk",
		Payload: json.RawMessage(`{"expired":[{"name":"Milk"}]}`), CreatedAt: time.Now(),
	}}, 3, nil
}

func (f *fakeAlerts) MarkRead(_ context.Context, _ uuid.UUID, ids []uuid.UUID) error {
	f.read, f.readCall = ids, f.readCall+1
	return nil
}

func (f *fakeAlerts) GetPrefs(context.Context, uuid.UUID) (alerts.Prefs, error) { return f.prefs, nil }

func (f *fakeAlerts) SetPrefs(_ context.Context, _ uuid.UUID, p alerts.PrefsUpdate) error {
	if f.setErr == nil {
		if p.DigestTime != nil {
			f.prefs.DigestTime = *p.DigestTime
		}
		if p.LeadDays != nil {
			f.prefs.LeadDays = *p.LeadDays
		}
	}
	return f.setErr
}

func (f *fakeAlerts) Telegram(context.Context, uuid.UUID) (alerts.TelegramState, error) {
	return alerts.TelegramState{Available: true, BotUsername: "bot", Linked: true, Enabled: true, Username: "kim"}, nil
}

func (f *fakeAlerts) StartLink(context.Context, uuid.UUID) (alerts.Link, error) {
	return alerts.Link{URL: "https://t.me/bot?start=abc", ExpiresAt: time.Now().Add(time.Minute)}, f.linkErr
}
func (f *fakeAlerts) Unlink(context.Context, uuid.UUID) error   { f.unlinked++; return nil }
func (f *fakeAlerts) SendTest(context.Context, uuid.UUID) error { return f.testErr }
func (f *fakeAlerts) Admin(context.Context) (alerts.AdminStatus, error) {
	return alerts.AdminStatus{Bot: alerts.BotStatus{Status: "ok", Username: "bot"}, LinkedChats: 2, Polling: true}, nil
}

func alertsHandler(t *testing.T, a Alerts, signedIn *uuid.UUID) http.Handler {
	t.Helper()
	d := Deps{}
	if a != nil {
		d.Alerts = a
	}
	opts := Options{PublicURL: testOrigin, Authorizer: allowAll{}}
	if signedIn != nil {
		opts.Session = func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				p := &identity.Principal{UserID: *signedIn, Status: identity.StatusActive}
				next.ServeHTTP(w, r.WithContext(identity.WithPrincipal(r.Context(), p)))
			})
		}
	}
	return newHandler(t, NewServer(d), opts)
}

func TestNotificationsEndpoints(t *testing.T) {
	me := uuid.New()
	f := &fakeAlerts{prefs: alerts.Prefs{DigestTime: "08:00", LeadDays: 2, QuietStart: "22:00", QuietEnd: "07:00"}}
	h := alertsHandler(t, f, &me)

	rec := do(h, llmRequest(t, http.MethodGet, "/api/v1/notifications?unread=true&limit=5", ""))
	var list NotificationList
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &list) != nil || list.Unread != 3 || len(list.Items) != 1 || f.user != me {
		t.Fatalf("list: %d %s", rec.Code, rec.Body)
	}
	if list.Items[0].Read || list.Items[0].Payload == nil {
		t.Fatalf("item %+v", list.Items[0])
	}

	id := uuid.New()
	if rec := do(h, llmRequest(t, http.MethodPost, "/api/v1/notifications/read", `{"ids":["`+id.String()+`"]}`)); rec.Code != http.StatusNoContent || len(f.read) != 1 || f.read[0] != id {
		t.Fatalf("read: %d %v", rec.Code, f.read)
	}
	if rec := do(h, llmRequest(t, http.MethodPost, "/api/v1/notifications/read", `{}`)); rec.Code != http.StatusNoContent || len(f.read) != 0 || f.readCall != 2 {
		t.Fatalf("read all: %d %v", rec.Code, f.read)
	}

	rec = do(h, llmRequest(t, http.MethodPut, "/api/v1/notifications/preferences", `{"digestTime":"06:30","leadDays":5,"quietStart":"23:00","quietEnd":"06:00"}`))
	if rec.Code != http.StatusOK || f.prefs.DigestTime != "06:30" || f.prefs.LeadDays != 5 {
		t.Fatalf("set prefs: %d %s", rec.Code, rec.Body)
	}
	// Schema rejects a malformed time before it reaches the service.
	if rec := do(h, llmRequest(t, http.MethodPut, "/api/v1/notifications/preferences", `{"digestTime":"6:30","leadDays":5,"quietStart":"23:00","quietEnd":"06:00"}`)); rec.Code < 400 || rec.Code >= 500 {
		t.Fatalf("bad time: %d", rec.Code)
	}
	f.setErr = alerts.ErrInvalid
	if rec := do(h, llmRequest(t, http.MethodPut, "/api/v1/notifications/preferences", `{"digestTime":"06:30","leadDays":5,"quietStart":"23:00","quietEnd":"06:00"}`)); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("service rejection: %d", rec.Code)
	}

	var conn TelegramConnection
	rec = do(h, llmRequest(t, http.MethodGet, "/api/v1/notifications/telegram", ""))
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &conn) != nil || !conn.Linked || *conn.Username != "kim" {
		t.Fatalf("telegram: %d %s", rec.Code, rec.Body)
	}
	if rec := do(h, llmRequest(t, http.MethodDelete, "/api/v1/notifications/telegram", "")); rec.Code != http.StatusNoContent || f.unlinked != 1 {
		t.Fatalf("disconnect: %d", rec.Code)
	}
	if rec := do(h, llmRequest(t, http.MethodPost, "/api/v1/notifications/telegram/link", "")); rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), "t.me/bot?start=abc") {
		t.Fatalf("link: %d %s", rec.Code, rec.Body)
	}
	f.linkErr = alerts.ErrNoBot
	rec = do(h, llmRequest(t, http.MethodPost, "/api/v1/notifications/telegram/link", ""))
	var p Problem
	_ = json.Unmarshal(rec.Body.Bytes(), &p)
	if rec.Code != http.StatusConflict || p.Code != CodeTelegramNotConfigured {
		t.Fatalf("no bot: %d %s", rec.Code, rec.Body)
	}
	f.testErr = &telegram.Error{Method: "sendMessage", Code: 403, Description: "bot was blocked"}
	if rec := do(h, llmRequest(t, http.MethodPost, "/api/v1/notifications/telegram/test", "")); rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), "bot was blocked") {
		t.Fatalf("test: %d %s", rec.Code, rec.Body)
	}
	f.testErr = alerts.ErrNotLinked
	if rec := do(h, llmRequest(t, http.MethodPost, "/api/v1/notifications/telegram/test", "")); rec.Code != http.StatusConflict {
		t.Fatalf("not linked: %d", rec.Code)
	}

	var ad AdminTelegram
	rec = do(h, llmRequest(t, http.MethodGet, "/api/v1/admin/telegram", ""))
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &ad) != nil || ad.LinkedChats != 2 || !ad.Polling || ad.Status != "ok" {
		t.Fatalf("admin: %d %s", rec.Code, rec.Body)
	}
}

func TestNotificationsUnwiredAndSignedOut(t *testing.T) {
	me := uuid.New()
	if rec := do(alertsHandler(t, nil, &me), llmRequest(t, http.MethodGet, "/api/v1/notifications", "")); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("unwired: %d", rec.Code)
	}
	if rec := do(alertsHandler(t, &fakeAlerts{}, nil), llmRequest(t, http.MethodGet, "/api/v1/notifications", "")); rec.Code != http.StatusUnauthorized {
		t.Fatalf("signed out: %d", rec.Code)
	}
	// With the real authorizer, admin and user routes need their roles.
	h := newHandler(t, NewServer(Deps{Alerts: &fakeAlerts{}}), Options{PublicURL: testOrigin, Authorizer: IdentityAuthorizer{}})
	for _, path := range []string{"/api/v1/notifications", "/api/v1/admin/telegram"} {
		if rec := do(h, llmRequest(t, http.MethodGet, path, "")); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s: %d", path, rec.Code)
		}
	}
}
