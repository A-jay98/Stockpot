// Package alerts creates expiry digests (in-app inbox and Telegram) and runs
// the Telegram bot that links chats to users.
package alerts

import (
	"fmt"
	"regexp"
	"time"
	_ "time/tzdata" // the digest's zone must resolve inside the confined (chroot) service

	"github.com/a-jay98/stockpot/internal/settings"
)

// Location is the zone digests and quiet hours are evaluated in.
const Location = "America/Toronto"

var timePattern = regexp.MustCompile(`^([01]\d|2[0-3]):([0-5]\d)$`)

func validTime(s string) error {
	if !timePattern.MatchString(s) {
		return fmt.Errorf("expected HH:MM (24 h)")
	}
	return nil
}

// Settings, edited on Admin → Settings. Users may override the lead days,
// digest time and quiet hours for themselves (Settings → Notifications).
var (
	FlagEnabled = settings.Define(settings.Def[bool]{
		Key:         "flag.alerts",
		Default:     true,
		Description: "Create expiry digests and deliver them (in-app, and Telegram where linked).",
	})
	SettingLeadDays = settings.Define(settings.Def[int]{
		Key:         "alerts.lead_days",
		Default:     2,
		Description: "Default number of days ahead an item counts as expiring soon.",
		Validate:    settings.Between(0, 30),
	})
	SettingDigestTime = settings.Define(settings.Def[string]{
		Key:         "alerts.digest_time",
		Default:     "08:00",
		Description: "Default time of the daily digest, HH:MM in " + Location + ".",
		Validate:    validTime,
	})
	SettingQuietStart = settings.Define(settings.Def[string]{
		Key:         "alerts.quiet_start",
		Default:     "22:00",
		Description: "Default start of Telegram quiet hours, HH:MM in " + Location + ". Equal start and end turns quiet hours off.",
		Validate:    validTime,
	})
	SettingQuietEnd = settings.Define(settings.Def[string]{
		Key:         "alerts.quiet_end",
		Default:     "07:00",
		Description: "Default end of Telegram quiet hours, HH:MM in " + Location + ".",
		Validate:    validTime,
	})
	SettingNotificationDays = settings.Define(settings.Def[int]{
		Key:         "retention.notification_days",
		Default:     90,
		Description: "Days to keep inbox notifications.",
		Validate:    settings.Between(7, 3650),
	})
)

func location() *time.Location {
	loc, err := time.LoadLocation(Location)
	if err != nil {
		return time.UTC
	}
	return loc
}
