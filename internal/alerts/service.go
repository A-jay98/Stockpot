package alerts

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/a-jay98/stockpot/internal/audit"
	"github.com/a-jay98/stockpot/internal/settings"
	"github.com/a-jay98/stockpot/internal/telegram"
)

// Errors the API maps to problems.
var (
	ErrInvalid      = errors.New("invalid value")
	ErrNoBot        = errors.New("telegram is not set up: an admin must set the bot token")
	ErrNotLinked    = errors.New("telegram is not connected")
	ErrChatTaken    = errors.New("this Telegram chat is already linked to another Stockpot user")
	ErrLinkNotFound = errors.New("that link code is invalid or has expired")
)

const (
	linkTTL     = 15 * time.Minute
	botCacheTTL = time.Minute
)

// Deps are the service's dependencies.
type Deps struct {
	Pool     *pgxpool.Pool
	Settings *settings.Store
	Telegram *telegram.Client
	// PublicURL is the app's origin, for links in Telegram messages.
	PublicURL string
	// Now and Sleep may be replaced in tests.
	Now   func() time.Time
	Sleep func(ctx context.Context, d time.Duration) error
}

// Service owns notifications, preferences and Telegram linking.
type Service struct {
	d Deps

	mu    sync.Mutex
	bot   botCache
	state pollState
}

type botCache struct {
	key  string
	at   time.Time
	user telegram.User
	err  error
}

// NewService returns a service; call Run on it to start the Telegram worker.
func NewService(d Deps) *Service {
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Sleep == nil {
		d.Sleep = sleep
	}
	return &Service{d: d}
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// --- inbox ---

// Notification is an inbox entry.
type Notification struct {
	ID          uuid.UUID
	HouseholdID uuid.UUID
	Kind        string
	Title       string
	Body        string
	Payload     json.RawMessage
	CreatedAt   time.Time
	ReadAt      *time.Time
}

// ListParams selects a page of the inbox, newest first.
type ListParams struct {
	UnreadOnly bool
	Before     *time.Time
	Limit      int
}

// List returns a page of the user's notifications and their unread count.
func (s *Service) List(ctx context.Context, user uuid.UUID, p ListParams) ([]Notification, int, error) {
	if p.Limit <= 0 || p.Limit > 100 {
		p.Limit = 30
	}
	rows, err := s.d.Pool.Query(ctx, `
		SELECT id, household_id, kind, title, body, payload, created_at, read_at
		FROM notification
		WHERE user_id = $1 AND (NOT $2 OR read_at IS NULL) AND ($3::timestamptz IS NULL OR created_at < $3)
		ORDER BY created_at DESC, id DESC LIMIT $4`, user, p.UnreadOnly, p.Before, p.Limit)
	if err != nil {
		return nil, 0, err
	}
	items, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (n Notification, err error) {
		err = r.Scan(&n.ID, &n.HouseholdID, &n.Kind, &n.Title, &n.Body, &n.Payload, &n.CreatedAt, &n.ReadAt)
		return
	})
	if err != nil {
		return nil, 0, err
	}
	var unread int
	if err := s.d.Pool.QueryRow(ctx, `SELECT count(*) FROM notification WHERE user_id = $1 AND read_at IS NULL`, user).Scan(&unread); err != nil {
		return nil, 0, err
	}
	return items, unread, nil
}

// MarkRead marks the given notifications read; no ids marks all of the user's.
func (s *Service) MarkRead(ctx context.Context, user uuid.UUID, ids []uuid.UUID) error {
	_, err := s.d.Pool.Exec(ctx, `
		UPDATE notification SET read_at = now()
		WHERE user_id = $1 AND read_at IS NULL AND (coalesce(cardinality($2::uuid[]), 0) = 0 OR id = ANY($2))`, user, ids)
	return err
}

// --- preferences ---

// Preferences are a user's alert settings as stored (nil = system default).
type Preferences struct {
	DigestTime string
	LeadDays   int
	QuietStart string
	QuietEnd   string
}

func (s *Service) defaults() Prefs {
	return Prefs{
		DigestTime: SettingDigestTime.Get(s.d.Settings), LeadDays: SettingLeadDays.Get(s.d.Settings),
		QuietStart: SettingQuietStart.Get(s.d.Settings), QuietEnd: SettingQuietEnd.Get(s.d.Settings),
	}
}

// resolve overlays a user's stored values on the system defaults.
func (s *Service) resolve(digest, qs, qe *string, lead *int) Prefs {
	p := s.defaults()
	if digest != nil {
		p.DigestTime = *digest
	}
	if qs != nil {
		p.QuietStart = *qs
	}
	if qe != nil {
		p.QuietEnd = *qe
	}
	if lead != nil {
		p.LeadDays = *lead
	}
	return p
}

// GetPrefs returns the user's effective preferences.
func (s *Service) GetPrefs(ctx context.Context, user uuid.UUID) (Prefs, error) {
	var digest, qs, qe *string
	var lead *int
	err := s.d.Pool.QueryRow(ctx, `SELECT digest_time, lead_days, quiet_start, quiet_end FROM notification_pref WHERE user_id = $1`, user).
		Scan(&digest, &lead, &qs, &qe)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return Prefs{}, err
	}
	return s.resolve(digest, qs, qe, lead), nil
}

// PrefsUpdate changes only the fields that are set; the others keep their
// stored value (or keep following the system default).
type PrefsUpdate struct {
	DigestTime, QuietStart, QuietEnd *string
	LeadDays                         *int
}

// SetPrefs stores the given preferences after validating them.
func (s *Service) SetPrefs(ctx context.Context, user uuid.UUID, p PrefsUpdate) error {
	for _, v := range []*string{p.DigestTime, p.QuietStart, p.QuietEnd} {
		if v != nil && !timePattern.MatchString(*v) {
			return fmt.Errorf("%w: times are HH:MM (24 h)", ErrInvalid)
		}
	}
	if p.LeadDays != nil && (*p.LeadDays < 0 || *p.LeadDays > 30) {
		return fmt.Errorf("%w: lead days are 0 to 30", ErrInvalid)
	}
	_, err := s.d.Pool.Exec(ctx, `
		INSERT INTO notification_pref (user_id, digest_time, lead_days, quiet_start, quiet_end)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (user_id) DO UPDATE SET
			digest_time = coalesce($2, notification_pref.digest_time),
			lead_days = coalesce($3, notification_pref.lead_days),
			quiet_start = coalesce($4, notification_pref.quiet_start),
			quiet_end = coalesce($5, notification_pref.quiet_end),
			updated_at = now()`,
		user, p.DigestTime, p.LeadDays, p.QuietStart, p.QuietEnd)
	return err
}

// --- Telegram: bot status and linking ---

// BotStatus is the live state of the bot token.
type BotStatus struct {
	Status   string // ok | not_configured | error
	Username string
	Detail   string
}

// Bot checks the token with getMe, cached for a minute per token.
func (s *Service) Bot(ctx context.Context) BotStatus {
	token := telegram.SettingBotToken.Get(s.d.Settings)
	if token == "" {
		return BotStatus{Status: "not_configured", Detail: "No bot token set (telegram.bot_token)."}
	}
	s.mu.Lock()
	c := s.bot
	s.mu.Unlock()
	if c.key != token || s.d.Now().Sub(c.at) >= botCacheTTL {
		cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		u, err := s.d.Telegram.GetMe(cctx)
		cancel()
		c = botCache{key: token, at: s.d.Now(), user: u, err: err}
		s.mu.Lock()
		s.bot = c
		s.mu.Unlock()
	}
	if c.err != nil {
		return BotStatus{Status: "error", Detail: c.err.Error()}
	}
	return BotStatus{Status: "ok", Username: c.user.Username}
}

// TelegramState is what Settings → Notifications shows.
type TelegramState struct {
	BotUsername string // empty when the bot is not usable
	Available   bool   // an admin has set up a working bot
	Linked      bool
	Username    string // the linked Telegram account
	Enabled     bool
	LastError   string
	LinkedAt    *time.Time
}

// Telegram returns the user's Telegram connection.
func (s *Service) Telegram(ctx context.Context, user uuid.UUID) (TelegramState, error) {
	b := s.Bot(ctx)
	st := TelegramState{Available: b.Status == "ok", BotUsername: b.Username}
	var username, lastErr *string
	err := s.d.Pool.QueryRow(ctx, `SELECT username, enabled, last_error, linked_at FROM notification_channel WHERE user_id = $1 AND kind = 'telegram'`, user).
		Scan(&username, &st.Enabled, &lastErr, &st.LinkedAt)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return st, nil
	case err != nil:
		return st, err
	}
	st.Linked = true
	st.Username, st.LastError = deref(username), deref(lastErr)
	return st, nil
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// Link is a pending "Connect Telegram" request.
type Link struct {
	URL       string // t.me deep link
	Code      string
	ExpiresAt time.Time
}

func hashCode(code string) []byte { h := sha256.Sum256([]byte(code)); return h[:] }

// StartLink creates a single-use code (replacing any earlier one) and the
// deep link that hands it to the bot.
func (s *Service) StartLink(ctx context.Context, user uuid.UUID) (Link, error) {
	b := s.Bot(ctx)
	switch b.Status {
	case "not_configured":
		return Link{}, ErrNoBot
	case "error":
		return Link{}, fmt.Errorf("%w: %s", ErrNoBot, b.Detail)
	}
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return Link{}, err
	}
	code := base64.RawURLEncoding.EncodeToString(raw)
	exp := s.d.Now().Add(linkTTL)
	tx, err := s.d.Pool.Begin(ctx)
	if err != nil {
		return Link{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `DELETE FROM telegram_link WHERE user_id = $1`, user); err != nil {
		return Link{}, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO telegram_link (code_hash, user_id, expires_at) VALUES ($1, $2, $3)`, hashCode(code), user, exp); err != nil {
		return Link{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Link{}, err
	}
	return Link{URL: "https://t.me/" + b.Username + "?start=" + code, Code: code, ExpiresAt: exp}, nil
}

// Unlink disconnects the user's Telegram chat (audited).
func (s *Service) Unlink(ctx context.Context, user uuid.UUID) error {
	tx, err := s.d.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	tag, err := tx.Exec(ctx, `DELETE FROM notification_channel WHERE user_id = $1 AND kind = 'telegram'`, user)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM telegram_link WHERE user_id = $1`, user); err != nil {
		return err
	}
	if tag.RowsAffected() > 0 {
		if err := audit.Record(ctx, tx, "telegram.unlink", audit.Target{Type: "user", ID: user.String()}, nil, nil); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// completeLink consumes code and links chat to its user. It returns the
// user's display name.
func (s *Service) completeLink(ctx context.Context, code string, chat int64, username string) (string, error) {
	tx, err := s.d.Pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var user uuid.UUID
	err = tx.QueryRow(ctx, `DELETE FROM telegram_link l USING app_user u
		WHERE l.code_hash = $1 AND l.expires_at > now() AND u.id = l.user_id AND u.status = 'active' RETURNING l.user_id`, hashCode(code)).Scan(&user)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrLinkNotFound
	}
	if err != nil {
		return "", err
	}
	var owner uuid.UUID
	err = tx.QueryRow(ctx, `SELECT user_id FROM notification_channel WHERE kind = 'telegram' AND chat_id = $1`, chat).Scan(&owner)
	if err == nil && owner != user {
		return "", ErrChatTaken
	}
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return "", err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO notification_channel (user_id, kind, chat_id, username, enabled, last_error, linked_at)
		VALUES ($1, 'telegram', $2, NULLIF($3, ''), true, NULL, now())
		ON CONFLICT (user_id, kind) DO UPDATE SET chat_id = $2, username = NULLIF($3, ''), enabled = true, last_error = NULL, linked_at = now()`,
		user, chat, username); err != nil {
		return "", err
	}
	// The bot acts for the user it just linked.
	actx := audit.WithActor(ctx, user, uuid.Nil)
	if err := audit.Record(actx, tx, "telegram.link", audit.Target{Type: "user", ID: user.String()}, nil, nil); err != nil {
		return "", err
	}
	var name string
	if err := tx.QueryRow(ctx, `SELECT display_name FROM app_user WHERE id = $1`, user).Scan(&name); err != nil {
		return "", err
	}
	return name, tx.Commit(ctx)
}

// unlinkChat removes the link of chat (the /stop command) and reports whether
// there was one.
func (s *Service) unlinkChat(ctx context.Context, chat int64) (bool, error) {
	tx, err := s.d.Pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var user uuid.UUID
	err = tx.QueryRow(ctx, `DELETE FROM notification_channel WHERE kind = 'telegram' AND chat_id = $1 RETURNING user_id`, chat).Scan(&user)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	actx := audit.WithActor(ctx, user, uuid.Nil)
	if err := audit.Record(actx, tx, "telegram.unlink", audit.Target{Type: "user", ID: user.String()}, nil, nil); err != nil {
		return false, err
	}
	return true, tx.Commit(ctx)
}

// SendTest sends a test message to the user's linked chat.
func (s *Service) SendTest(ctx context.Context, user uuid.UUID) error {
	var chat int64
	err := s.d.Pool.QueryRow(ctx, `SELECT chat_id FROM notification_channel WHERE user_id = $1 AND kind = 'telegram'`, user).Scan(&chat)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotLinked
	}
	if err != nil {
		return err
	}
	_, err = s.d.Telegram.SendMessage(ctx, chatID(chat), "Stockpot is connected. You'll get your expiry digest here.\n\n"+s.appLink(""))
	return err
}

func chatID(id int64) telegram.ChatID { return telegram.ChatID(fmt.Sprint(id)) }

// appLink is a link back to the app, or "" when the public URL is unknown.
func (s *Service) appLink(path string) string {
	u, err := url.Parse(s.d.PublicURL)
	if err != nil || u.Host == "" {
		return ""
	}
	return "Open Stockpot: " + strings.TrimRight(u.String(), "/") + path
}

// --- admin ---

// AdminStatus is Admin → Settings' view of the bot.
type AdminStatus struct {
	Bot         BotStatus
	LinkedChats int
	Polling     bool       // the long-poll worker is running in this process
	LastPollAt  *time.Time // last successful getUpdates
	LastError   string
}

// Admin reports the bot, the number of linked chats and the poller's health.
func (s *Service) Admin(ctx context.Context) (AdminStatus, error) {
	st := AdminStatus{Bot: s.Bot(ctx)}
	if err := s.d.Pool.QueryRow(ctx, `SELECT count(*) FROM notification_channel WHERE kind = 'telegram'`).Scan(&st.LinkedChats); err != nil {
		return st, err
	}
	st.Polling, st.LastPollAt, st.LastError = s.state.snapshot()
	return st, nil
}
