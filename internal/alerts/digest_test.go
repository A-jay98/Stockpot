package alerts

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func at(t *testing.T, s string) time.Time {
	t.Helper()
	v, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestDigestDay(t *testing.T) {
	for _, tc := range []struct {
		name, now, digest string
		day               string
		due               bool
	}{
		// Winter (EST, UTC-5): 08:00 Toronto is 13:00 UTC.
		{"before time", "2026-01-15T12:59:00Z", "08:00", "2026-01-15", false},
		{"at time", "2026-01-15T13:00:00Z", "08:00", "2026-01-15", true},
		{"inside catch-up", "2026-01-15T18:59:00Z", "08:00", "2026-01-15", true},
		{"after catch-up", "2026-01-15T19:00:00Z", "08:00", "2026-01-15", false},
		// Summer (EDT, UTC-4): 08:00 Toronto is 12:00 UTC.
		{"summer before", "2026-07-15T11:59:00Z", "08:00", "2026-07-15", false},
		{"summer at", "2026-07-15T12:00:00Z", "08:00", "2026-07-15", true},
		// The local day is Toronto's, not UTC's: 23:30 EST is already the 16th in UTC.
		{"local day behind UTC", "2026-11-27T04:30:00Z", "23:00", "2026-11-26", true},
		// Spring forward 2026-03-08: before 02:00 EST (UTC-5), after 03:00 EDT (UTC-4).
		{"dst eve", "2026-03-07T12:00:00Z", "08:00", "2026-03-07", false}, // 07:00 EST
		{"dst eve due", "2026-03-07T13:00:00Z", "08:00", "2026-03-07", true},
		{"dst day before", "2026-03-08T11:59:00Z", "08:00", "2026-03-08", false}, // 07:59 EDT
		{"dst day due", "2026-03-08T12:00:00Z", "08:00", "2026-03-08", true},
		// A time inside the skipped hour still fires once that morning.
		{"skipped hour", "2026-03-08T07:30:00Z", "02:30", "2026-03-08", true}, // 03:30 EDT
		// A late digest's catch-up window runs past midnight: 22:00 + 6 h.
		{"past midnight", "2026-01-16T05:30:00Z", "22:00", "2026-01-15", true}, // 00:30 on the 16th
		{"past midnight, window over", "2026-01-16T09:00:00Z", "22:00", "2026-01-16", false},
		// Fall back 2026-11-01: 08:00 EST is 13:00 UTC.
		{"fall back before", "2026-11-01T12:59:00Z", "08:00", "2026-11-01", false},
		{"fall back at", "2026-11-01T13:00:00Z", "08:00", "2026-11-01", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			day, due := DigestDay(at(t, tc.now), tc.digest)
			if day != tc.day || due != tc.due {
				t.Fatalf("DigestDay = %s, %v; want %s, %v", day, due, tc.day, tc.due)
			}
		})
	}
}

func TestInQuietHours(t *testing.T) {
	for _, tc := range []struct {
		now, start, end string
		quiet           bool
	}{
		{"2026-01-15T03:00:00Z", "22:00", "07:00", true},  // 22:00 EST, wraps midnight
		{"2026-01-15T05:00:00Z", "22:00", "07:00", true},  // 00:00
		{"2026-01-15T11:59:00Z", "22:00", "07:00", true},  // 06:59
		{"2026-01-15T12:00:00Z", "22:00", "07:00", false}, // 07:00 (end is exclusive)
		{"2026-01-15T14:00:00Z", "22:00", "07:00", false}, // 09:00
		{"2026-01-15T02:59:00Z", "22:00", "07:00", false}, // 21:59
		{"2026-01-15T19:00:00Z", "13:00", "15:00", true},  // 14:00, same-day window
		{"2026-01-15T14:00:00Z", "13:00", "15:00", false}, // 09:00
		{"2026-01-15T05:00:00Z", "07:00", "07:00", false}, // equal: off
	} {
		if got := InQuietHours(at(t, tc.now), tc.start, tc.end); got != tc.quiet {
			t.Errorf("InQuietHours(%s, %s-%s) = %v", tc.now, tc.start, tc.end, got)
		}
	}
}

func date(s string) *time.Time {
	t, _ := time.Parse(time.DateOnly, s)
	return &t
}

func TestClassifyBoundaries(t *testing.T) {
	cands := []Candidate{
		{ID: uuid.New(), Name: "old", ExpiresOn: date("2026-11-19")},  // 8 days ago: only counted
		{ID: uuid.New(), Name: "week", ExpiresOn: date("2026-11-20")}, // 7 days ago: listed
		{ID: uuid.New(), Name: "yesterday", ExpiresOn: date("2026-11-26")},
		{ID: uuid.New(), Name: "today", ExpiresOn: date("2026-11-27")},
		{ID: uuid.New(), Name: "edge", ExpiresOn: date("2026-11-29")},   // today + lead
		{ID: uuid.New(), Name: "beyond", ExpiresOn: date("2026-11-30")}, // one past
		{ID: uuid.New(), Name: "low", Low: true},
		{ID: uuid.New(), Name: "low and soon", Low: true, ExpiresOn: date("2026-11-28")}, // counted once
		{ID: uuid.New(), Name: "low and far", Low: true, ExpiresOn: date("2027-01-01")},
	}
	d, err := Classify(cands, "2026-11-27", 2)
	if err != nil {
		t.Fatal(err)
	}
	names := func(its []Item) string {
		var s []string
		for _, i := range its {
			s = append(s, i.Name)
		}
		return strings.Join(s, ",")
	}
	if got := names(d.Expired); got != "week,yesterday" {
		t.Errorf("expired = %s", got)
	}
	if got := names(d.Expiring); got != "today,edge,low and soon" {
		t.Errorf("expiring = %s", got)
	}
	if got := names(d.Low); got != "low,low and far" {
		t.Errorf("low = %s", got)
	}
	if *d.Expired[1].Days != -1 || *d.Expiring[0].Days != 0 || *d.Expiring[1].Days != 2 {
		t.Errorf("days: %d %d %d", *d.Expired[1].Days, *d.Expiring[0].Days, *d.Expiring[1].Days)
	}
	// Expired more than a week ago is only counted, and never makes a digest alone.
	if d.Older != 1 {
		t.Errorf("older = %d", d.Older)
	}
	if !strings.HasSuffix(d.Body(), "+ 1 older expired") {
		t.Errorf("body %q", d.Body())
	}
	if !(Digest{Older: 3}).Empty() || (Digest{Older: 3}).Body() != "" {
		t.Error("older-only digest must be empty")
	}
	// Lead 0: only today.
	d, _ = Classify(cands, "2026-11-27", 0)
	if got := names(d.Expiring); got != "today" {
		t.Errorf("lead 0 expiring = %s", got)
	}
}

func TestDigestText(t *testing.T) {
	two, m1 := 2, -1
	d := Digest{
		Expired:  []Item{{Name: "Milk", Location: "Fridge", Days: &m1}},
		Expiring: []Item{{Name: "Yogurt", Location: "Fridge", Days: &two}},
	}
	if got := d.Title(); got != "1 expired · 1 expiring soon" {
		t.Errorf("title %q", got)
	}
	want := "Expired\n• Milk (Fridge), yesterday\n\nExpiring soon\n• Yogurt (Fridge), in 2 days"
	if got := d.Body(); got != want {
		t.Errorf("body %q", got)
	}
	var many Digest
	for i := 0; i < maxListed+3; i++ {
		many.Low = append(many.Low, Item{Name: "x"})
	}
	if !strings.HasSuffix(many.Body(), "\n+ 3 more") {
		t.Errorf("cap: %q", many.Body())
	}
	if (Digest{}).Empty() != true || d.Empty() {
		t.Error("Empty")
	}
}
