package alerts

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/a-jay98/stockpot/internal/jobs"
	"github.com/a-jay98/stockpot/internal/telegram"
)

// KindExpiryDigest is the kind of the daily digest notification.
const KindExpiryDigest = "expiry_digest"

// runEvery is the job's cadence: fine enough for digest times on the quarter
// hour and for prompt retries of undelivered messages.
const runEvery = 15 * time.Minute

// deliveryWindow is how long an undelivered notification keeps being retried
// on Telegram; the inbox keeps it regardless.
const deliveryWindow = 24 * time.Hour

// sendAttempts per message within one run; later runs try again.
const sendAttempts = 3

// Args is the periodic alerts job.
type Args struct{}

// Kind names the job in River's tables.
func (Args) Kind() string { return "alerts.expiry" }

// InsertOpts allows one run at a time, so a slow Telegram cannot make two
// runs send the same message.
func (Args) InsertOpts() river.InsertOpts {
	return river.InsertOpts{
		MaxAttempts: 3,
		UniqueOpts:  river.UniqueOpts{ByState: []rivertype.JobState{rivertype.JobStateAvailable, rivertype.JobStateRunning, rivertype.JobStateRetryable, rivertype.JobStateScheduled, rivertype.JobStatePending}},
	}
}

// Worker runs the alerts job.
type Worker struct {
	river.WorkerDefaults[Args]
	svc *Service
}

// Timeout bounds one run.
func (*Worker) Timeout(*river.Job[Args]) time.Duration { return 10 * time.Minute }

// Work creates the due digests, then delivers what is waiting on Telegram.
func (w *Worker) Work(ctx context.Context, _ *river.Job[Args]) error {
	if !FlagEnabled.Get(w.svc.d.Settings) {
		return nil
	}
	return w.svc.Run(ctx)
}

// Registrar adds the worker to a jobs.Deps.
func (s *Service) Registrar() func(*river.Workers) error {
	return func(ws *river.Workers) error { return river.AddWorkerSafely(ws, &Worker{svc: s}) }
}

// PeriodicJob is the schedule to pass to jobs.Deps.Periodic.
func PeriodicJob() *river.PeriodicJob {
	return river.NewPeriodicJob(
		river.PeriodicInterval(runEvery),
		func() (river.JobArgs, *river.InsertOpts) { return Args{}, nil },
		&river.PeriodicJobOpts{ID: "alerts", RunOnStart: true},
	)
}

// Run is one pass: generate then deliver. Per-recipient failures are logged
// and do not stop the others; database errors fail the run (River retries).
func (s *Service) Run(ctx context.Context) error {
	now := s.d.Now()
	genErr := s.generate(ctx, now)
	delErr := s.deliver(ctx, now)
	return errors.Join(genErr, delErr)
}

type recipient struct {
	user, household uuid.UUID
	prefs           Prefs
}

func (s *Service) recipients(ctx context.Context) ([]recipient, error) {
	rows, err := s.d.Pool.Query(ctx, `
		SELECT m.user_id, m.household_id, p.digest_time, p.lead_days, p.quiet_start, p.quiet_end
		FROM membership m
		JOIN app_user u ON u.id = m.user_id AND u.status = 'active'
		LEFT JOIN notification_pref p ON p.user_id = m.user_id
		ORDER BY m.user_id, m.household_id`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (rc recipient, err error) {
		var digest, qs, qe *string
		var lead *int
		if err = r.Scan(&rc.user, &rc.household, &digest, &lead, &qs, &qe); err != nil {
			return
		}
		rc.prefs = s.resolve(digest, qs, qe, lead)
		return
	})
}

// candidates are the household's items that may belong in a digest: expiring
// within lead days (or already expired) or flagged low.
func candidates(ctx context.Context, q pgx.Tx, household uuid.UUID, today string, lead int) ([]Candidate, error) {
	rows, err := q.Query(ctx, `
		SELECT s.id, p.name, coalesce(l.name, ''), s.expires_on,
		       coalesce(s.low OR s.level IN ('low', 'almost_out'), false)
		FROM stock_item s
		JOIN product p ON p.id = s.product_id
		LEFT JOIN location l ON l.id = s.location_id
		WHERE s.household_id = $1 AND s.deleted_at IS NULL
		  AND NOT (s.amount_kind = 'exact' AND coalesce(s.quantity, 1) <= 0)
		  AND (s.low OR coalesce(s.level IN ('low', 'almost_out'), false) OR s.expires_on <= $2::date + $3::int)
		ORDER BY s.expires_on NULLS LAST, lower(p.name), s.id`, household, today, lead)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (c Candidate, err error) {
		err = r.Scan(&c.ID, &c.Name, &c.Location, &c.ExpiresOn, &c.Low)
		return
	})
}

// generate creates the digests that are due: one per user, household and
// local day, even across overlapping runs (digest_run is the claim).
func (s *Service) generate(ctx context.Context, now time.Time) error {
	rcs, err := s.recipients(ctx)
	if err != nil {
		return err
	}
	for _, rc := range rcs {
		day, due := DigestDay(now, rc.prefs.DigestTime)
		if !due {
			continue
		}
		if err := s.generateOne(ctx, rc, day); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			slog.ErrorContext(ctx, "alerts: digest failed", "user", rc.user, "household", rc.household, "err", err)
		}
	}
	return nil
}

func (s *Service) generateOne(ctx context.Context, rc recipient, day string) error {
	tx, err := s.d.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	tag, err := tx.Exec(ctx, `INSERT INTO digest_run (user_id, household_id, day) VALUES ($1, $2, $3::date) ON CONFLICT DO NOTHING`, rc.user, rc.household, day)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return nil // already evaluated today
	}
	cands, err := candidates(ctx, tx, rc.household, day, rc.prefs.LeadDays)
	if err != nil {
		return err
	}
	d, err := Classify(cands, day, rc.prefs.LeadDays)
	if err != nil {
		return err
	}
	if !d.Empty() {
		var id uuid.UUID
		err := tx.QueryRow(ctx, `
			INSERT INTO notification (user_id, household_id, kind, title, body, payload, dedupe_key)
			VALUES ($1, $2, $3, $4, $5, $6, $7)
			ON CONFLICT (user_id, dedupe_key) DO NOTHING RETURNING id`,
			rc.user, rc.household, KindExpiryDigest, d.Title(), d.Body(), d, "digest:"+day+":"+rc.household.String()).Scan(&id)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if err == nil {
			if _, err := tx.Exec(ctx, `UPDATE digest_run SET notification_id = $4 WHERE user_id = $1 AND household_id = $2 AND day = $3::date`, rc.user, rc.household, day, id); err != nil {
				return err
			}
		}
	}
	return tx.Commit(ctx)
}

type pending struct {
	id             uuid.UUID
	user           uuid.UUID
	title, body    string
	chat           int64
	quietS, quietE string
}

// deliver sends notifications that are not on Telegram yet, outside quiet
// hours, to users with an enabled link. Only notifications created since the
// chat was linked and within deliveryWindow are sent.
func (s *Service) deliver(ctx context.Context, now time.Time) error {
	if telegram.SettingBotToken.Get(s.d.Settings) == "" {
		return nil
	}
	rows, err := s.d.Pool.Query(ctx, `
		SELECT n.id, n.user_id, n.title, n.body, c.chat_id, p.quiet_start, p.quiet_end
		FROM notification n
		JOIN notification_channel c ON c.user_id = n.user_id AND c.kind = 'telegram' AND c.enabled
		JOIN app_user u ON u.id = n.user_id AND u.status = 'active'
		LEFT JOIN notification_pref p ON p.user_id = n.user_id
		WHERE n.sent_telegram_at IS NULL AND n.created_at >= c.linked_at AND n.created_at > $1
		ORDER BY n.created_at`, now.Add(-deliveryWindow))
	if err != nil {
		return err
	}
	defs := s.defaults()
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (p pending, err error) {
		var qs, qe *string
		if err = r.Scan(&p.id, &p.user, &p.title, &p.body, &p.chat, &qs, &qe); err != nil {
			return
		}
		p.quietS, p.quietE = defs.QuietStart, defs.QuietEnd
		if qs != nil {
			p.quietS = *qs
		}
		if qe != nil {
			p.quietE = *qe
		}
		return
	})
	if err != nil {
		return err
	}
	disabled := map[uuid.UUID]bool{}
	for _, p := range list {
		if disabled[p.user] || InQuietHours(now, p.quietS, p.quietE) {
			continue
		}
		text := p.title + "\n\n" + p.body
		if l := s.appLink("/inventory"); l != "" {
			text += "\n\n" + l
		}
		text = truncate(text, maxMessageRunes)
		err := s.sendWithRetry(ctx, chatID(p.chat), text)
		switch {
		case err == nil:
			if _, err := s.d.Pool.Exec(ctx, `UPDATE notification SET sent_telegram_at = now() WHERE id = $1`, p.id); err != nil {
				return err
			}
		case ctx.Err() != nil:
			return ctx.Err()
		case rejected(err):
			// The user blocked the bot or deleted the chat: stop trying until
			// they connect again.
			slog.WarnContext(ctx, "alerts: telegram chat unusable; disabling", "user", p.user, "err", err)
			disabled[p.user] = true
			if _, err := s.d.Pool.Exec(ctx, `UPDATE notification_channel SET enabled = false, last_error = $2 WHERE user_id = $1 AND kind = 'telegram'`, p.user, err.Error()); err != nil {
				return err
			}
		case isMessageRejected(err):
			// Telegram refused this message but the chat is fine: leave the
			// channel on and go on with the next one.
			slog.ErrorContext(ctx, "alerts: telegram rejected a message", "user", p.user, "notification", p.id, "err", err)
		default:
			// Rate limit or outage: leave it for the next run, and stop this
			// pass so the rest are not hammered.
			slog.WarnContext(ctx, "alerts: telegram send failed; will retry", "user", p.user, "err", err)
			return nil
		}
	}
	return nil
}

// maxMessageRunes is Telegram's limit for a text message.
const maxMessageRunes = 4096

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

// rejected reports whether Telegram refused the chat for good (the user
// blocked the bot or deactivated, or the chat is gone), as opposed to a rate
// limit, outage, bad token or a problem with one message.
func rejected(err error) bool {
	var te *telegram.Error
	if !errors.As(err, &te) {
		return false
	}
	return te.Code == 403 || (te.Code == 400 && strings.Contains(strings.ToLower(te.Description), "chat not found"))
}

// isMessageRejected reports a 400 that is about the message, not the chat.
func isMessageRejected(err error) bool {
	var te *telegram.Error
	return errors.As(err, &te) && te.Code == 400
}

// maxRetryWait caps how long a send waits out a rate limit.
const maxRetryWait = 30 * time.Second

// sendWithRetry retries temporary failures with backoff (or Telegram's own
// retry_after, up to maxRetryWait).
func (s *Service) sendWithRetry(ctx context.Context, chat telegram.ChatID, text string) error {
	var err error
	backoff := time.Second
	for attempt := 1; attempt <= sendAttempts; attempt++ {
		if _, err = s.d.Telegram.SendMessage(ctx, chat, text); err == nil || !telegram.IsTemporary(err) {
			return err
		}
		if attempt == sendAttempts {
			break
		}
		wait := backoff
		if ra, ok := telegram.RetryAfter(err); ok {
			if ra > maxRetryWait {
				return err
			}
			wait = ra
		}
		if serr := s.d.Sleep(ctx, wait); serr != nil {
			return serr
		}
		backoff *= 2
	}
	return fmt.Errorf("after %d attempts: %w", sendAttempts, err)
}

// Sweepers prune old notifications, expired link codes and old digest runs.
func Sweepers(s *Service) []jobs.Sweeper {
	return []jobs.Sweeper{
		{
			Name: "notification", Days: SettingNotificationDays,
			Sweep: func(ctx context.Context, before time.Time) (int64, error) {
				tag, err := s.d.Pool.Exec(ctx, `DELETE FROM notification WHERE created_at < $1`, before)
				return tag.RowsAffected(), err
			},
		},
		{
			Name: "digest_run", Days: SettingNotificationDays,
			Sweep: func(ctx context.Context, before time.Time) (int64, error) {
				tag, err := s.d.Pool.Exec(ctx, `DELETE FROM digest_run WHERE day < $1::date`, before)
				return tag.RowsAffected(), err
			},
		},
		{
			Name: "telegram_link",
			Sweep: func(ctx context.Context, now time.Time) (int64, error) {
				tag, err := s.d.Pool.Exec(ctx, `DELETE FROM telegram_link WHERE expires_at < $1`, now)
				return tag.RowsAffected(), err
			},
		},
	}
}
