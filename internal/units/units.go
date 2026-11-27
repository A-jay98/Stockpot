// Package units is the fixed unit table shared (by code) with the web client.
package units

import (
	"fmt"
	"math/big"
	"strings"
)

// Dimension of a unit; conversion is only defined within one.
type Dimension string

// Dimensions.
const (
	Mass   Dimension = "mass"
	Volume Dimension = "volume"
	Count  Dimension = "count"
)

var table = map[string]Dimension{
	"g": Mass, "kg": Mass, "lb": Mass, "oz": Mass,
	"ml": Volume, "l": Volume, "tsp": Volume, "tbsp": Volume, "cup": Volume, "fl_oz": Volume,
	"ea": Count, "pack": Count, "bag": Count, "can": Count, "jar": Count, "bottle": Count,
	"box": Count, "bunch": Count, "head": Count, "clove": Count, "slice": Count, "portion": Count,
}

// Valid reports whether code is a known unit.
func Valid(code string) bool { _, ok := table[code]; return ok }

// DimensionOf returns the unit's dimension, or "" for an unknown code.
func DimensionOf(code string) Dimension { return table[code] }

// Codes lists every unit code.
func Codes() []string {
	out := make([]string, 0, len(table))
	for c := range table {
		out = append(out, c)
	}
	return out
}

// ParseQuantity accepts a non-negative decimal string ("1.5", "250") and
// returns it normalized, or an error. Quantities travel as strings.
func ParseQuantity(s string) (string, error) {
	s = strings.TrimSpace(s)
	r, ok := new(big.Rat).SetString(s)
	if !ok || r.Sign() < 0 || strings.ContainsAny(s, "/eE") || len(s) > 18 {
		return "", fmt.Errorf("invalid quantity %q", s)
	}
	return s, nil
}
