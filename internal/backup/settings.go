package backup

import (
	"fmt"
	"regexp"
	"strings"
	"time"
	_ "time/tzdata" // the schedule's zone must resolve inside the confined (chroot) service

	"filippo.io/age"

	"github.com/a-jay98/stockpot/internal/settings"
)

// Location is the zone of the nightly schedule.
const Location = "America/Toronto"

var timePattern = regexp.MustCompile(`^([01]\d|2[0-3]):([0-5]\d)$`)

var chatPattern = regexp.MustCompile(`^(-?\d{5,20}|@[A-Za-z][A-Za-z0-9_]{4,31})$`)

// Settings, edited on Admin → Settings (and summarised on Admin → Backups).
var (
	SettingChatID = settings.Define(settings.Def[string]{
		Key:         "backup.telegram_chat_id",
		Description: "Telegram chat that receives backups: the private channel's numeric id (-100…) or @username. Add the bot as an admin of it.",
		Validate: func(s string) error {
			if s != "" && !chatPattern.MatchString(s) {
				return fmt.Errorf("expected a numeric chat id like -1001234567890 or an @username")
			}
			return nil
		},
	})
	SettingAgeRecipient = settings.Define(settings.Def[string]{
		Key:         "backup.age_recipient",
		Description: "age public key (age1…) backups are encrypted to. Generate with `age-keygen` on your own machine; the private key never goes on the server.",
		Validate: func(s string) error {
			if s == "" {
				return nil
			}
			if _, err := age.ParseX25519Recipient(s); err != nil {
				return fmt.Errorf("not an age X25519 public key: %w", err)
			}
			return nil
		},
	})
	SettingTime = settings.Define(settings.Def[string]{
		Key:         "backup.time",
		Default:     "03:30",
		Description: "Time of the nightly backup, HH:MM in " + Location + ".",
		Validate: func(s string) error {
			if !timePattern.MatchString(s) {
				return fmt.Errorf("expected HH:MM (24 h)")
			}
			return nil
		},
	})
	SettingKeep = settings.Define(settings.Def[int]{
		Key:         "backup.keep",
		Default:     14,
		Description: "How many backups to keep in the channel; older ones are deleted where Telegram allows.",
		Validate:    settings.Between(1, 365),
	})
	FlagEnabled = settings.Define(settings.Def[bool]{
		Key:         "flag.backups",
		Description: "Run the nightly backup. Turn on once the channel, bot token and age key are set.",
	})
	SettingRunRetentionDays = settings.Define(settings.Def[int]{
		Key:         "retention.backup_run_days",
		Default:     365,
		Validate:    settings.Between(30, 3650),
		Description: "Days to keep backup run records.",
	})
)

// NextRun is the first scheduled time strictly after t.
func NextRun(hhmm string, t time.Time) time.Time {
	loc, err := time.LoadLocation(Location)
	if err != nil {
		loc = time.UTC
	}
	h, m := 3, 30
	if parts := strings.Split(hhmm, ":"); len(parts) == 2 && timePattern.MatchString(hhmm) {
		fmt.Sscanf(parts[0], "%d", &h)
		fmt.Sscanf(parts[1], "%d", &m)
	}
	lt := t.In(loc)
	next := time.Date(lt.Year(), lt.Month(), lt.Day(), h, m, 0, 0, loc)
	for !next.After(t) {
		next = time.Date(next.Year(), next.Month(), next.Day()+1, h, m, 0, 0, loc)
	}
	return next
}
