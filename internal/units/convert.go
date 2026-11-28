package units

import (
	"math/big"
	"sort"
	"strings"
)

// Conversions map a unit code to an amount in a mass or volume unit:
// {"ea": "150 g", "cup": "185 g", "can": "400 ml"} means one of the key unit
// is that amount. They bridge dimensions (count ↔ mass, volume ↔ mass) for
// one product or ingredient.
type Conversions map[string]string

// Base factors: grams for mass, millilitres for volume. Cooking measures are
// metric (Canada): cup 250 ml, tbsp 15 ml, tsp 5 ml. fl_oz is US.
var base = map[string]*big.Rat{
	"g": rat("1"), "kg": rat("1000"), "lb": rat("453.59237"), "oz": rat("28.349523125"),
	"ml": rat("1"), "l": rat("1000"), "tsp": rat("5"), "tbsp": rat("15"), "cup": rat("250"), "fl_oz": rat("29.5735295625"),
}

func rat(s string) *big.Rat {
	r, ok := new(big.Rat).SetString(s)
	if !ok {
		panic("units: bad constant " + s)
	}
	return r
}

// node is where an amount lives in the conversion graph: "mass" or "volume"
// (in base units), or a count unit by its code (in that unit).
func node(unit string) (string, *big.Rat, bool) {
	switch DimensionOf(unit) {
	case Mass:
		return string(Mass), base[unit], true
	case Volume:
		return string(Volume), base[unit], true
	case Count:
		return unit, big.NewRat(1, 1), true
	}
	return "", nil, false
}

type edge struct {
	to     string
	factor *big.Rat // amount in `to` per one of `from`
}

// ParseAmount reads a conversion value such as "150 g" or "0.5 cup".
func ParseAmount(s string) (*big.Rat, string, bool) {
	f := strings.Fields(s)
	if len(f) != 2 || !Valid(f[1]) {
		return nil, "", false
	}
	q, err := ParseQuantity(f[0])
	if err != nil {
		return nil, "", false
	}
	r := rat(q)
	if r.Sign() <= 0 {
		return nil, "", false
	}
	return r, f[1], true
}

// graph merges conversion sets; for a key present in several, the first set
// wins (product beats ingredient beats ancestor).
func graph(sets []Conversions) map[string][]edge {
	seen := map[string]bool{}
	g := map[string][]edge{}
	for _, set := range sets {
		keys := make([]string, 0, len(set))
		for k := range set {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if seen[k] {
				continue
			}
			from, fk, ok := node(k)
			if !ok {
				continue
			}
			q, u, ok := ParseAmount(set[k])
			if !ok {
				continue
			}
			to, tu, _ := node(u)
			if to == from {
				continue
			}
			seen[k] = true
			// 1 k = q u  ⇒  one unit of `from` (fk of k's base) is q*tu/fk of `to`.
			f := new(big.Rat).Mul(q, tu)
			f.Quo(f, fk)
			g[from] = append(g[from], edge{to, f})
			g[to] = append(g[to], edge{from, new(big.Rat).Inv(f)})
		}
	}
	return g
}

// Convert turns qty of `from` into `to`. Within mass or volume it is exact;
// otherwise it walks the conversions (first set preferred per key) by the
// shortest path. ok is false when no path exists or a unit is unknown.
func Convert(qty *big.Rat, from, to string, sets ...Conversions) (*big.Rat, bool) {
	fn, ff, ok1 := node(from)
	tn, tf, ok2 := node(to)
	if !ok1 || !ok2 {
		return nil, false
	}
	v := new(big.Rat).Mul(qty, ff) // in fn's base
	if fn != tn {
		f, ok := path(graph(sets), fn, tn)
		if !ok {
			return nil, false
		}
		v.Mul(v, f)
	}
	return v.Quo(v, tf), true
}

func path(g map[string][]edge, from, to string) (*big.Rat, bool) {
	type step struct {
		n string
		f *big.Rat
	}
	visited := map[string]bool{from: true}
	queue := []step{{from, big.NewRat(1, 1)}}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, e := range g[cur.n] {
			if visited[e.to] {
				continue
			}
			f := new(big.Rat).Mul(cur.f, e.factor)
			if e.to == to {
				return f, true
			}
			visited[e.to] = true
			queue = append(queue, step{e.to, f})
		}
	}
	return nil, false
}

// Format renders r as a decimal string rounded half away from zero to
// places decimals, without trailing zeros ("1.5", "250").
func Format(r *big.Rat, places int) string {
	neg := r.Sign() < 0
	a := new(big.Rat).Abs(r)
	scale := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(places)), nil)
	n := new(big.Int).Mul(a.Num(), scale)
	n.Mul(n, big.NewInt(2))
	n.Add(n, a.Denom())
	n.Quo(n, new(big.Int).Mul(a.Denom(), big.NewInt(2)))
	s := n.String()
	if places > 0 {
		if len(s) <= places {
			s = strings.Repeat("0", places-len(s)+1) + s
		}
		s = strings.TrimRight(s[:len(s)-places]+"."+s[len(s)-places:], "0")
		s = strings.TrimSuffix(s, ".")
	}
	if neg && s != "0" {
		s = "-" + s
	}
	return s
}
