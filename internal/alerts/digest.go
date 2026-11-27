package alerts

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

// catchUp is how long after its time a day's digest may still be created
// (the server may have been down or the job late). Later than that the day is
// skipped rather than sending a stale "good morning".
const catchUp = 6 * time.Hour

// maxListed caps each group in a message.
const maxListed = 8

// Prefs are a user's resolved preferences (their own, else the system's).
type Prefs struct {
	DigestTime string // HH:MM
	LeadDays   int
	QuietStart string // HH:MM
	QuietEnd   string // HH:MM
}

func hm(s string) (h, m int) {
	if !timePattern.MatchString(s) {
		return 0, 0
	}
	h, _ = strconv.Atoi(s[:2])
	m, _ = strconv.Atoi(s[3:])
	return h, m
}

// DigestDay reports which local day's digest (YYYY-MM-DD) is due at now: the
// day whose digestTime is at or before now and within catchUp of it, which
// may be yesterday when the window runs past midnight. When none is due it
// returns today and false.
func DigestDay(now time.Time, digestTime string) (day string, due bool) {
	loc := location()
	l := now.In(loc)
	h, m := hm(digestTime)
	// Today's digest, and yesterday's when its catch-up window runs past midnight.
	for back := 0; back <= 1; back++ {
		d := l.AddDate(0, 0, -back)
		at := time.Date(d.Year(), d.Month(), d.Day(), h, m, 0, 0, loc)
		if !l.Before(at) && l.Before(at.Add(catchUp)) {
			return d.Format(time.DateOnly), true
		}
	}
	return l.Format(time.DateOnly), false
}

// InQuietHours reports whether Telegram messages are held at now. The window
// may wrap midnight; equal start and end mean no quiet hours.
func InQuietHours(now time.Time, start, end string) bool {
	if start == end {
		return false
	}
	l := now.In(location())
	t := l.Hour()*60 + l.Minute()
	sh, sm := hm(start)
	eh, em := hm(end)
	s, e := sh*60+sm, eh*60+em
	if s < e {
		return t >= s && t < e
	}
	return t >= s || t < e
}

// Item is one stock item in a digest.
type Item struct {
	ID        uuid.UUID `json:"id"`
	Name      string    `json:"name"`
	Location  string    `json:"location,omitempty"`
	ExpiresOn string    `json:"expiresOn,omitempty"` // YYYY-MM-DD
	Days      *int      `json:"days,omitempty"`      // until expiry; negative = already expired
}

// Digest groups what needs attention.
type Digest struct {
	Expired  []Item `json:"expired"`
	Expiring []Item `json:"expiring"`
	Low      []Item `json:"low"`
	// Older counts items expired more than expiredWindowDays ago; they are
	// summarised, not listed, and never make a digest on their own.
	Older int `json:"older,omitempty"`
}

// Empty reports whether there is nothing to say.
func (d Digest) Empty() bool { return len(d.Expired)+len(d.Expiring)+len(d.Low) == 0 }

// Candidate is a stock item as read from the database.
type Candidate struct {
	ID        uuid.UUID
	Name      string
	Location  string
	ExpiresOn *time.Time // a date (UTC midnight)
	Low       bool
}

// expiredWindowDays: items expired longer ago than this are only counted ("N
// older expired"), so a forgotten item doesn't fill every digest.
const expiredWindowDays = 7

// Classify sorts candidates into groups for the local day today (YYYY-MM-DD):
// expired (before today), expiring (today up to leadDays ahead, inclusive) and
// low (flagged low and not already in a group above).
func Classify(cands []Candidate, today string, leadDays int) (Digest, error) {
	t0, err := time.Parse(time.DateOnly, today)
	if err != nil {
		return Digest{}, err
	}
	var d Digest
	for _, c := range cands {
		it := Item{ID: c.ID, Name: c.Name, Location: c.Location}
		if c.ExpiresOn != nil {
			ex := time.Date(c.ExpiresOn.Year(), c.ExpiresOn.Month(), c.ExpiresOn.Day(), 0, 0, 0, 0, time.UTC)
			days := int(ex.Sub(t0).Hours() / 24)
			it.ExpiresOn, it.Days = ex.Format(time.DateOnly), &days
			switch {
			case days < -expiredWindowDays:
				d.Older++ // only counted
			case days < 0:
				d.Expired = append(d.Expired, it)
				continue
			case days <= leadDays:
				d.Expiring = append(d.Expiring, it)
				continue
			}
		}
		if c.Low {
			d.Low = append(d.Low, it)
		}
	}
	return d, nil
}

// Title is the one-line summary, e.g. "2 expired · 3 expiring soon".
func (d Digest) Title() string {
	var parts []string
	if n := len(d.Expired); n > 0 {
		parts = append(parts, fmt.Sprintf("%d expired", n))
	}
	if n := len(d.Expiring); n > 0 {
		parts = append(parts, fmt.Sprintf("%d expiring soon", n))
	}
	if n := len(d.Low); n > 0 {
		parts = append(parts, fmt.Sprintf("%d running low", n))
	}
	return strings.Join(parts, " · ")
}

func relative(days int) string {
	switch {
	case days == 0:
		return "today"
	case days == 1:
		return "tomorrow"
	case days == -1:
		return "yesterday"
	case days > 1:
		return fmt.Sprintf("in %d days", days)
	}
	return fmt.Sprintf("%d days ago", -days)
}

func line(it Item) string {
	s := "• " + it.Name
	if it.Location != "" {
		s += " (" + it.Location + ")"
	}
	if it.Days != nil {
		s += ", " + relative(*it.Days)
	}
	return s
}

func group(b *strings.Builder, title string, items []Item) {
	if len(items) == 0 {
		return
	}
	if b.Len() > 0 {
		b.WriteString("\n\n")
	}
	b.WriteString(title)
	for i, it := range items {
		if i == maxListed {
			fmt.Fprintf(b, "\n+ %d more", len(items)-maxListed)
			break
		}
		b.WriteString("\n" + line(it))
	}
}

// Body is the plain-text list shown in the inbox and sent to Telegram.
func (d Digest) Body() string {
	var b strings.Builder
	group(&b, "Expired", d.Expired)
	group(&b, "Expiring soon", d.Expiring)
	group(&b, "Running low", d.Low)
	if d.Older > 0 && b.Len() > 0 {
		fmt.Fprintf(&b, "\n\n+ %d older expired", d.Older)
	}
	return b.String()
}
