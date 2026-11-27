package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/google/uuid"

	"github.com/a-jay98/stockpot/internal/alerts"
	"github.com/a-jay98/stockpot/internal/identity"
	"github.com/a-jay98/stockpot/internal/telegram"
)

// Problem codes of the notifications endpoints.
const (
	CodeTelegramNotConfigured = "telegram_not_configured"
	CodeTelegramNotConnected  = "telegram_not_connected"
)

// Alerts is the part of alerts.Service the API uses.
type Alerts interface {
	List(ctx context.Context, user uuid.UUID, p alerts.ListParams) ([]alerts.Notification, int, error)
	MarkRead(ctx context.Context, user uuid.UUID, ids []uuid.UUID) error
	GetPrefs(ctx context.Context, user uuid.UUID) (alerts.Prefs, error)
	SetPrefs(ctx context.Context, user uuid.UUID, p alerts.PrefsUpdate) error
	Telegram(ctx context.Context, user uuid.UUID) (alerts.TelegramState, error)
	StartLink(ctx context.Context, user uuid.UUID) (alerts.Link, error)
	Unlink(ctx context.Context, user uuid.UUID) error
	SendTest(ctx context.Context, user uuid.UUID) error
	Admin(ctx context.Context) (alerts.AdminStatus, error)
}

var _ Alerts = (*alerts.Service)(nil)

// alertsUser returns the signed-in user, or a 503 problem when alerts are not
// wired in this build.
func (s *Server) alertsUser(ctx context.Context) (uuid.UUID, *Problem) {
	if s.alerts == nil {
		p := problemCtx(ctx, http.StatusServiceUnavailable, CodeNotFound, "Notifications are not available in this build.")
		return uuid.Nil, &p
	}
	p := identity.PrincipalFrom(ctx)
	if p == nil {
		pr := problemCtx(ctx, http.StatusUnauthorized, CodeUnauthenticated, "Sign in to continue.")
		return uuid.Nil, &pr
	}
	return p.UserID, nil
}

// alertsProblem maps service errors to problems.
func alertsProblem(ctx context.Context, err error) (Problem, bool) {
	var te *telegram.Error
	switch {
	case errors.Is(err, alerts.ErrInvalid):
		return problemCtx(ctx, http.StatusUnprocessableEntity, CodeInvalidValue, err.Error()), true
	case errors.Is(err, alerts.ErrNoBot), errors.Is(err, telegram.ErrNoToken):
		return problemCtx(ctx, http.StatusConflict, CodeTelegramNotConfigured, alerts.ErrNoBot.Error()), true
	case errors.Is(err, alerts.ErrNotLinked):
		return problemCtx(ctx, http.StatusConflict, CodeTelegramNotConnected, alerts.ErrNotLinked.Error()), true
	case errors.As(err, &te):
		return problemCtx(ctx, http.StatusBadGateway, CodeTelegramError, "Telegram refused: "+te.Description), true
	}
	return Problem{}, false
}

// ListNotifications serves listNotifications.
func (s *Server) ListNotifications(ctx context.Context, req ListNotificationsRequestObject) (ListNotificationsResponseObject, error) {
	user, p := s.alertsUser(ctx)
	if p != nil {
		return ListNotificationsdefaultApplicationProblemPlusJSONResponse{Body: *p, StatusCode: p.Status}, nil
	}
	lp := alerts.ListParams{Before: req.Params.Before}
	if req.Params.Unread != nil {
		lp.UnreadOnly = *req.Params.Unread
	}
	if req.Params.Limit != nil {
		lp.Limit = *req.Params.Limit
	}
	items, unread, err := s.alerts.List(ctx, user, lp)
	if err != nil {
		return nil, err
	}
	out := NotificationList{Items: make([]Notification, 0, len(items)), Unread: unread}
	for _, n := range items {
		item := Notification{
			Id: n.ID, HouseholdId: n.HouseholdID, Kind: NotificationKind(n.Kind),
			Title: n.Title, Body: n.Body, CreatedAt: n.CreatedAt, Read: n.ReadAt != nil,
		}
		var payload map[string]interface{}
		if json.Unmarshal(n.Payload, &payload) == nil && len(payload) > 0 {
			item.Payload = &payload
		}
		out.Items = append(out.Items, item)
	}
	return ListNotifications200JSONResponse(out), nil
}

// MarkNotificationsRead serves markNotificationsRead.
func (s *Server) MarkNotificationsRead(ctx context.Context, req MarkNotificationsReadRequestObject) (MarkNotificationsReadResponseObject, error) {
	user, p := s.alertsUser(ctx)
	if p != nil {
		return MarkNotificationsReaddefaultApplicationProblemPlusJSONResponse{Body: *p, StatusCode: p.Status}, nil
	}
	var ids []uuid.UUID
	if req.Body != nil && req.Body.Ids != nil {
		ids = *req.Body.Ids
	}
	if err := s.alerts.MarkRead(ctx, user, ids); err != nil {
		return nil, err
	}
	return MarkNotificationsRead204Response{}, nil
}

func prefsOut(p alerts.Prefs) NotificationPreferences {
	return NotificationPreferences{DigestTime: p.DigestTime, LeadDays: p.LeadDays, QuietStart: p.QuietStart, QuietEnd: p.QuietEnd}
}

// GetNotificationPreferences serves getNotificationPreferences.
func (s *Server) GetNotificationPreferences(ctx context.Context, _ GetNotificationPreferencesRequestObject) (GetNotificationPreferencesResponseObject, error) {
	user, p := s.alertsUser(ctx)
	if p != nil {
		return GetNotificationPreferencesdefaultApplicationProblemPlusJSONResponse{Body: *p, StatusCode: p.Status}, nil
	}
	prefs, err := s.alerts.GetPrefs(ctx, user)
	if err != nil {
		return nil, err
	}
	return GetNotificationPreferences200JSONResponse(prefsOut(prefs)), nil
}

// SetNotificationPreferences serves setNotificationPreferences.
func (s *Server) SetNotificationPreferences(ctx context.Context, req SetNotificationPreferencesRequestObject) (SetNotificationPreferencesResponseObject, error) {
	user, p := s.alertsUser(ctx)
	if p != nil {
		return SetNotificationPreferencesdefaultApplicationProblemPlusJSONResponse{Body: *p, StatusCode: p.Status}, nil
	}
	b := req.Body
	in := alerts.PrefsUpdate{DigestTime: b.DigestTime, LeadDays: b.LeadDays, QuietStart: b.QuietStart, QuietEnd: b.QuietEnd}
	if err := s.alerts.SetPrefs(ctx, user, in); err != nil {
		if pr, ok := alertsProblem(ctx, err); ok {
			return SetNotificationPreferencesdefaultApplicationProblemPlusJSONResponse{Body: pr, StatusCode: pr.Status}, nil
		}
		return nil, err
	}
	prefs, err := s.alerts.GetPrefs(ctx, user)
	if err != nil {
		return nil, err
	}
	return SetNotificationPreferences200JSONResponse(prefsOut(prefs)), nil
}

// GetTelegramConnection serves getTelegramConnection.
func (s *Server) GetTelegramConnection(ctx context.Context, _ GetTelegramConnectionRequestObject) (GetTelegramConnectionResponseObject, error) {
	user, p := s.alertsUser(ctx)
	if p != nil {
		return GetTelegramConnectiondefaultApplicationProblemPlusJSONResponse{Body: *p, StatusCode: p.Status}, nil
	}
	st, err := s.alerts.Telegram(ctx, user)
	if err != nil {
		return nil, err
	}
	return GetTelegramConnection200JSONResponse(TelegramConnection{
		Available: st.Available, BotUsername: optString(st.BotUsername), Linked: st.Linked, Enabled: st.Enabled,
		Username: optString(st.Username), LinkedAt: st.LinkedAt, LastError: optString(st.LastError),
	}), nil
}

// DisconnectTelegram serves disconnectTelegram.
func (s *Server) DisconnectTelegram(ctx context.Context, _ DisconnectTelegramRequestObject) (DisconnectTelegramResponseObject, error) {
	user, p := s.alertsUser(ctx)
	if p != nil {
		return DisconnectTelegramdefaultApplicationProblemPlusJSONResponse{Body: *p, StatusCode: p.Status}, nil
	}
	if err := s.alerts.Unlink(ctx, user); err != nil {
		return nil, err
	}
	return DisconnectTelegram204Response{}, nil
}

// StartTelegramLink serves startTelegramLink.
func (s *Server) StartTelegramLink(ctx context.Context, _ StartTelegramLinkRequestObject) (StartTelegramLinkResponseObject, error) {
	user, p := s.alertsUser(ctx)
	if p != nil {
		return StartTelegramLinkdefaultApplicationProblemPlusJSONResponse{Body: *p, StatusCode: p.Status}, nil
	}
	l, err := s.alerts.StartLink(ctx, user)
	if err != nil {
		if pr, ok := alertsProblem(ctx, err); ok {
			return StartTelegramLinkdefaultApplicationProblemPlusJSONResponse{Body: pr, StatusCode: pr.Status}, nil
		}
		return nil, err
	}
	return StartTelegramLink201JSONResponse(TelegramLink{Url: l.URL, ExpiresAt: l.ExpiresAt}), nil
}

// TestTelegramConnection serves testTelegramConnection.
func (s *Server) TestTelegramConnection(ctx context.Context, _ TestTelegramConnectionRequestObject) (TestTelegramConnectionResponseObject, error) {
	user, p := s.alertsUser(ctx)
	if p != nil {
		return TestTelegramConnectiondefaultApplicationProblemPlusJSONResponse{Body: *p, StatusCode: p.Status}, nil
	}
	if err := s.alerts.SendTest(ctx, user); err != nil {
		if pr, ok := alertsProblem(ctx, err); ok {
			return TestTelegramConnectiondefaultApplicationProblemPlusJSONResponse{Body: pr, StatusCode: pr.Status}, nil
		}
		return nil, err
	}
	return TestTelegramConnection204Response{}, nil
}

// GetAdminTelegram serves getAdminTelegram.
func (s *Server) GetAdminTelegram(ctx context.Context, _ GetAdminTelegramRequestObject) (GetAdminTelegramResponseObject, error) {
	if s.alerts == nil {
		p := problemCtx(ctx, http.StatusServiceUnavailable, CodeNotFound, "Notifications are not available in this build.")
		return GetAdminTelegramdefaultApplicationProblemPlusJSONResponse{Body: p, StatusCode: p.Status}, nil
	}
	st, err := s.alerts.Admin(ctx)
	if err != nil {
		return nil, err
	}
	return GetAdminTelegram200JSONResponse(AdminTelegram{
		Status: AdminTelegramStatus(st.Bot.Status), BotUsername: optString(st.Bot.Username), Detail: optString(st.Bot.Detail),
		LinkedChats: st.LinkedChats, Polling: st.Polling, LastPollAt: st.LastPollAt, LastError: optString(st.LastError),
	}), nil
}
