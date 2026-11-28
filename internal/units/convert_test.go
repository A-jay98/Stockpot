package units

import (
	"encoding/json"
	"math/big"
	"os"
	"testing"
)

type convertCase struct {
	Name        string        `json:"name"`
	Qty         string        `json:"qty"`
	From        string        `json:"from"`
	To          string        `json:"to"`
	Conversions []Conversions `json:"conversions"`
	Want        *string       `json:"want"`
}

// The same fixture runs in web/src/lib/units.test.ts.
func TestConvertFixture(t *testing.T) {
	raw, err := os.ReadFile("testdata/convert.json")
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Cases []convertCase `json:"cases"`
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	for _, c := range f.Cases {
		t.Run(c.Name, func(t *testing.T) {
			q, _ := new(big.Rat).SetString(c.Qty)
			got, ok := Convert(q, c.From, c.To, c.Conversions...)
			switch {
			case c.Want == nil && ok:
				t.Fatalf("got %s, want no conversion", Format(got, 3))
			case c.Want != nil && !ok:
				t.Fatalf("no conversion, want %s", *c.Want)
			case c.Want != nil && Format(got, 3) != *c.Want:
				t.Fatalf("got %s, want %s", Format(got, 3), *c.Want)
			}
		})
	}
}

func TestFormat(t *testing.T) {
	for in, want := range map[string]string{"1/3": "0.333", "2/3": "0.667", "-1/2": "-0.5", "0": "0", "1000": "1000", "1/2000": "0.001"} {
		r, _ := new(big.Rat).SetString(in)
		if got := Format(r, 3); got != want {
			t.Errorf("Format(%s) = %s, want %s", in, got, want)
		}
	}
}
