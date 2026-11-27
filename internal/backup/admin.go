package backup

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/a-jay98/stockpot/internal/jobs"
	"github.com/a-jay98/stockpot/internal/telegram"
)

// ChannelStatus is the result of checking the bot.
type ChannelStatus struct {
	Status      string // ok | not_configured | error
	BotUsername string
	Detail      string
}

// Status is what Admin → Backups shows.
type Status struct {
	Enabled, Configured bool
	Time, Timezone      string
	NextRunAt           *time.Time
	Keep                int
	HasToken            bool
	ChatID              string
	HasRecipient        bool
	Recipient           string
	Channel             ChannelStatus
	Runs                []Run
}

// Status gathers the page. The bot is checked live (getMe, 5 s timeout).
func (s *Service) Status(ctx context.Context) (Status, error) {
	st := Status{
		Enabled:      FlagEnabled.Get(s.d.Settings),
		Configured:   s.Configured(),
		Time:         SettingTime.Get(s.d.Settings),
		Timezone:     Location,
		Keep:         SettingKeep.Get(s.d.Settings),
		HasToken:     telegram.SettingBotToken.Get(s.d.Settings) != "",
		ChatID:       SettingChatID.Get(s.d.Settings),
		Recipient:    SettingAgeRecipient.Get(s.d.Settings),
		HasRecipient: SettingAgeRecipient.Get(s.d.Settings) != "",
	}
	if st.Enabled {
		n := NextRun(st.Time, s.d.Now())
		st.NextRunAt = &n
	}
	var err error
	if st.Runs, err = recentRuns(ctx, s.d.Pool, 30); err != nil {
		return st, err
	}
	switch {
	case !st.HasToken:
		st.Channel = ChannelStatus{Status: "not_configured", Detail: "No bot token set (telegram.bot_token)."}
	case st.ChatID == "":
		st.Channel = ChannelStatus{Status: "not_configured", Detail: "No chat id set (backup.telegram_chat_id)."}
	default:
		cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		u, err := s.d.Telegram.GetMe(cctx)
		if err != nil {
			st.Channel = ChannelStatus{Status: "error", Detail: err.Error()}
		} else {
			st.Channel = ChannelStatus{Status: "ok", BotUsername: u.Username}
		}
	}
	return st, nil
}

// TestChannel posts a message to the backup channel, proving the bot may
// write there. A *telegram.Error carries Telegram's reason.
func (s *Service) TestChannel(ctx context.Context) error {
	chat := SettingChatID.Get(s.d.Settings)
	if chat == "" || telegram.SettingBotToken.Get(s.d.Settings) == "" {
		return ErrNotConfigured
	}
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	_, err := s.d.Telegram.SendMessage(cctx, telegram.ChatID(chat), "Stockpot: test message. Backups will be posted here.")
	return err
}

// Health is the overview's backup tile.
type Health struct {
	Enabled bool
	LastOK  *time.Time
	// LastStatus and LastError describe the newest run.
	LastStatus string
	LastError  string
	// Stale: enabled, but the newest run failed or no run succeeded in 36 h.
	Stale bool
}

const staleAfter = 36 * time.Hour

// Health summarises backup health for the overview.
func (s *Service) Health(ctx context.Context) (Health, error) {
	h := Health{Enabled: FlagEnabled.Get(s.d.Settings)}
	last, lastOK, err := lastRuns(ctx, s.d.Pool)
	if err != nil {
		return h, err
	}
	if last != nil {
		h.LastStatus = last.Status
		if last.Error != nil {
			h.LastError = *last.Error
		}
	}
	if lastOK != nil && lastOK.FinishedAt != nil {
		h.LastOK = lastOK.FinishedAt
	}
	if h.Enabled {
		h.Stale = h.LastStatus == "failed" || h.LastOK == nil || s.d.Now().Sub(*h.LastOK) > staleAfter
	}
	return h, nil
}

// Sweepers prune old run records (the retention job).
func Sweepers(pool *pgxpool.Pool) []jobs.Sweeper {
	return []jobs.Sweeper{{
		Name: "backup_run",
		Days: SettingRunRetentionDays,
		Sweep: func(ctx context.Context, before time.Time) (int64, error) {
			tag, err := pool.Exec(ctx, `DELETE FROM backup_run WHERE started_at < $1 AND status <> 'running'`, before)
			return tag.RowsAffected(), err
		},
	}}
}
