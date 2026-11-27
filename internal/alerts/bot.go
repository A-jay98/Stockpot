package alerts

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/a-jay98/stockpot/internal/telegram"
)

// pollLockKey is the advisory lock that makes one process the only consumer
// of getUpdates (Telegram allows a single poller per bot).
var pollLockKey = func() int64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte("stockpot.telegram.poll"))
	return int64(h.Sum64())
}()

const (
	pollTimeout  = 25 // seconds Telegram holds a getUpdates call
	idleWait     = 30 * time.Second
	maxErrorWait = time.Minute
)

// pollState is the poller's health, read by the admin status.
type pollState struct {
	mu      sync.Mutex
	running bool
	last    time.Time
	err     string
}

func (p *pollState) set(running bool, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.running = running
	if err != nil {
		p.err = err.Error()
	} else if running {
		p.last, p.err = time.Now(), ""
	}
}

func (p *pollState) snapshot() (running bool, last *time.Time, errText string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.last.IsZero() {
		t := p.last
		last = &t
	}
	return p.running, last, p.err
}

// RunBot long-polls Telegram and answers the bot's commands until ctx ends.
// Only one process holds the poll lock; the others wait and take over if it
// goes away. It returns nil on shutdown.
func (s *Service) RunBot(ctx context.Context) error {
	for ctx.Err() == nil {
		if err := s.pollOnce(ctx); err != nil && ctx.Err() == nil {
			slog.WarnContext(ctx, "telegram poller stopped", "err", err)
			s.state.set(false, err)
		}
		if s.d.Sleep(ctx, idleWait) != nil {
			break
		}
	}
	s.state.set(false, nil)
	return nil
}

// pollOnce holds the poll lock and polls until it fails or ctx ends. It
// returns nil when there was nothing to do (no token, lock held elsewhere).
func (s *Service) pollOnce(ctx context.Context) error {
	if telegram.SettingBotToken.Get(s.d.Settings) == "" || !FlagEnabled.Get(s.d.Settings) {
		return nil
	}
	conn, err := s.d.Pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	var got bool
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, pollLockKey).Scan(&got); err != nil || !got {
		return err
	}
	defer func() {
		// Unlock on a fresh context: ctx is probably cancelled. If this fails
		// the connection is discarded and Postgres drops the lock with it.
		uctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if _, err := conn.Exec(uctx, `SELECT pg_advisory_unlock($1)`, pollLockKey); err != nil {
			_ = conn.Conn().Close(uctx)
		}
	}()

	var offset int64
	if err := conn.QueryRow(ctx, `SELECT update_offset FROM telegram_poll`).Scan(&offset); err != nil {
		return err
	}
	s.state.set(true, nil)
	defer s.state.set(false, nil)
	errWait := time.Second
	for ctx.Err() == nil {
		if telegram.SettingBotToken.Get(s.d.Settings) == "" || !FlagEnabled.Get(s.d.Settings) {
			return nil
		}
		pctx, cancel := context.WithTimeout(ctx, (pollTimeout+15)*time.Second)
		updates, err := s.d.Telegram.GetUpdates(pctx, offset, pollTimeout)
		cancel()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			s.state.set(true, err)
			var te *telegram.Error
			if errors.As(err, &te) && (te.Code == 401 || te.Code == 409) {
				return err // bad token, or a webhook / other poller owns the bot: back off at the outer loop
			}
			if serr := s.d.Sleep(ctx, errWait); serr != nil {
				return nil
			}
			errWait = min(errWait*2, maxErrorWait)
			continue
		}
		errWait = time.Second
		s.state.set(true, nil)
		for _, u := range updates {
			s.handle(ctx, u)
			offset = u.UpdateID + 1
			if _, err := conn.Exec(ctx, `UPDATE telegram_poll SET update_offset = $1`, offset); err != nil {
				return err
			}
		}
	}
	return nil
}

// handle answers one update. Failures are logged: a bad message must not
// stop the poller, and an update is not retried once handled.
func (s *Service) handle(ctx context.Context, u telegram.Update) {
	m := u.Message
	if m == nil || m.Chat.Type != "private" || m.Text == "" {
		return
	}
	cmd, arg, _ := strings.Cut(strings.TrimSpace(m.Text), " ")
	cmd, _, _ = strings.Cut(cmd, "@") // /start@stockpot_bot
	arg = strings.TrimSpace(arg)
	var reply string
	var err error
	switch strings.ToLower(cmd) {
	case "/start":
		reply, err = s.cmdStart(ctx, m, arg)
	case "/stop":
		reply, err = s.cmdStop(ctx, m.Chat.ID)
	case "/expiring":
		reply, err = s.cmdExpiring(ctx, m.Chat.ID)
	default:
		reply = helpText
	}
	if err != nil {
		slog.ErrorContext(ctx, "telegram command failed", "cmd", cmd, "err", err)
		reply = "Something went wrong on my side. Try again in a moment."
	}
	if reply == "" {
		return
	}
	if _, err := s.d.Telegram.SendMessage(ctx, chatID(m.Chat.ID), reply); err != nil {
		slog.WarnContext(ctx, "telegram reply failed", "err", err)
	}
}

const helpText = "Stockpot commands:\n/expiring – what is expiring soon\n/stop – stop notifications here\n\nTo connect this chat, open Stockpot → Settings → Notifications and tap Connect Telegram."

func (s *Service) cmdStart(ctx context.Context, m *telegram.Message, code string) (string, error) {
	if code == "" {
		return "Hi! To connect this chat to Stockpot, open Stockpot → Settings → Notifications and tap Connect Telegram.", nil
	}
	username := ""
	if m.From != nil {
		username = m.From.Username
	}
	name, err := s.completeLink(ctx, code, m.Chat.ID, username)
	switch {
	case errors.Is(err, ErrLinkNotFound):
		return "That link has expired or was already used. Tap Connect Telegram in Stockpot to get a new one.", nil
	case errors.Is(err, ErrChatTaken):
		return "This chat is already linked to another Stockpot user. Send /stop here first, then connect again.", nil
	case err != nil:
		return "", err
	}
	return fmt.Sprintf("Connected, %s. You'll get your expiry digest here. Send /expiring any time, or /stop to disconnect.", name), nil
}

func (s *Service) cmdStop(ctx context.Context, chat int64) (string, error) {
	ok, err := s.unlinkChat(ctx, chat)
	if err != nil {
		return "", err
	}
	if !ok {
		return "This chat isn't connected to Stockpot.", nil
	}
	return "Disconnected. You won't get Stockpot messages here any more. Connect again from Settings → Notifications.", nil
}

func (s *Service) cmdExpiring(ctx context.Context, chat int64) (string, error) {
	var user uuid.UUID
	err := s.d.Pool.QueryRow(ctx, `SELECT user_id FROM notification_channel WHERE kind = 'telegram' AND chat_id = $1`, chat).Scan(&user)
	if errors.Is(err, pgx.ErrNoRows) {
		return "This chat isn't connected to Stockpot. Open Stockpot → Settings → Notifications and tap Connect Telegram.", nil
	}
	if err != nil {
		return "", err
	}
	prefs, err := s.GetPrefs(ctx, user)
	if err != nil {
		return "", err
	}
	rows, err := s.d.Pool.Query(ctx, `
		SELECT h.id, h.name FROM membership m JOIN household h ON h.id = m.household_id WHERE m.user_id = $1 ORDER BY h.name`, user)
	if err != nil {
		return "", err
	}
	type hh struct {
		id   uuid.UUID
		name string
	}
	households, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (h hh, err error) { err = r.Scan(&h.id, &h.name); return })
	if err != nil {
		return "", err
	}
	today := s.d.Now().In(location()).Format(time.DateOnly)
	var parts []string
	for _, h := range households {
		d, err := s.householdDigest(ctx, h.id, today, prefs.LeadDays)
		if err != nil {
			return "", err
		}
		d.Low = nil // /expiring is about dates
		if d.Empty() {
			continue
		}
		body := d.Body()
		if len(households) > 1 {
			body = h.name + "\n" + body
		}
		parts = append(parts, body)
	}
	if len(parts) == 0 {
		return fmt.Sprintf("Nothing is expiring in the next %d days.", prefs.LeadDays), nil
	}
	text := strings.Join(parts, "\n\n")
	if l := s.appLink("/inventory"); l != "" {
		text += "\n\n" + l
	}
	return text, nil
}

// householdDigest classifies a household's current stock outside a transaction.
func (s *Service) householdDigest(ctx context.Context, household uuid.UUID, today string, lead int) (Digest, error) {
	tx, err := s.d.Pool.Begin(ctx)
	if err != nil {
		return Digest{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	cands, err := candidates(ctx, tx, household, today, lead)
	if err != nil {
		return Digest{}, err
	}
	return Classify(cands, today, lead)
}
