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
