package inventory

import (
	"encoding/json"
	"os"
	"testing"
	"time"
)

// testdata/estimate.json is shared with the web client's estimator tests, so
// the two implementations agree on every case.
func TestEstimateFixture(t *testing.T) {
	b, err := os.ReadFile("testdata/estimate.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Name     string       `json:"name"`
		Chain    Chain        `json:"chain"`
		Op       Op           `json:"op"`
		Location LocationKind `json:"location"`
		On       string       `json:"on"`
		Want     *string      `json:"want"`
	}
	if err := json.Unmarshal(b, &cases); err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			on, _ := time.Parse(DateLayout, c.On)
			got, ok := Estimate(c.Chain, c.Op, c.Location, on)
			switch {
			case c.Want == nil && ok:
				t.Fatalf("got %s, want no estimate", got.Format(DateLayout))
			case c.Want != nil && (!ok || got.Format(DateLayout) != *c.Want):
				t.Fatalf("got %s (%v), want %s", got.Format(DateLayout), ok, *c.Want)
			}
		})
	}
}

func TestComputeStat(t *testing.T) {
	d := func(day int) time.Time { return time.Date(2026, 11, day, 12, 0, 0, 0, time.UTC) }
	q := func(s string) *string { return &s }
	u := q("lb")
	// newest first: 2 lb on the 20th, 2 lb on the 14th, 3 lb on the 10th, 3 lb on the 10th again.
	s := computeStat([]purchase{{q("2"), u, d(20)}, {q("2"), u, d(14)}, {q("3"), u, d(10)}, {q("3"), u, d(10)}}, 9)
	if s.TimesBought != 9 || !s.LastBoughtAt.Equal(d(20)) || *s.LastQty != "2" {
		t.Fatalf("basics: %+v", s)
	}
	if *s.TypicalQty != "2" || *s.TypicalUnit != "lb" {
		t.Fatalf("a tie between 2 and 3 goes to the newer: %v %v", *s.TypicalQty, *s.TypicalUnit)
	}
	// Same-day purchases count once: gaps are 6 and 4 days, median 5.
	if s.MedianIntervalDays == nil || *s.MedianIntervalDays != 5 {
		t.Fatalf("median interval = %v, want 5", s.MedianIntervalDays)
	}
	one := computeStat([]purchase{{nil, nil, d(1)}}, 1)
	if one.MedianIntervalDays != nil || one.TypicalQty != nil {
		t.Fatalf("a single purchase has no interval or typical amount: %+v", one)
	}
}
