package inventory

import (
	"strings"
	"testing"
)

func TestSplitEvenly(t *testing.T) {
	for _, c := range []struct {
		total string
		n     int
		want  []string
	}{
		{"1", 3, []string{"0.333", "0.333", "0.334"}},
		{"1.500", 3, []string{"0.5", "0.5", "0.5"}},
		{"10", 4, []string{"2.5", "2.5", "2.5", "2.5"}},
		{"0.0005", 1, []string{"0.0005"}},
		{"0.002", 3, nil},
	} {
		got, ok := splitEvenly(c.total, c.n, 3)
		if ok != (c.want != nil) || strings.Join(got, ",") != strings.Join(c.want, ",") {
			t.Errorf("splitEvenly(%s, %d) = %v %v, want %v", c.total, c.n, got, ok, c.want)
		}
	}
}
