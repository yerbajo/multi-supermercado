package aldi

import (
	"math"
	"regexp"
	"strconv"
	"strings"
)

// ALDI publishes a declared sales unit ("130 g unidad", "1 l unidad", "pack de 3 x
// 150 g peso esc.", "precio kg (a granel)"). The size it declares is published data,
// so turning it into €/kg or €/L divides two published numbers instead of guessing.

const sizeUnits = `kg|g|ml|cl|l`

var (
	// "pack de 3 x 150 g": the pack multiplier, applied before a single size.
	reMulti = regexp.MustCompile(`(\d+)\s*[x×]\s*(\d+(?:[.,]\d+)?)\s*(` + sizeUnits + `)\b`)
	// "130 g", "0,75 l", "3 kg".
	reSize = regexp.MustCompile(`(\d+(?:[.,]\d+)?)\s*(` + sizeUnits + `)\b`)
	// "pack de 10 unidades", "6 uds".
	reCount = regexp.MustCompile(`(\d+)\s*(unidades|unidad|uds|ud)\b`)
)

var unitDivisors = map[string]struct {
	div  float64
	unit string
}{
	"kg": {1, "kg"}, "g": {1000, "kg"},
	"l": {1, "L"}, "ml": {1000, "L"}, "cl": {100, "L"},
}

// parseQuantity reads the sold size out of a sales unit, e.g. "740 g unidad" -> 0.74
// kg. It returns (0, "") when nothing parsable is declared: an unpriced or
// unmeasured product must not get an invented comparison (S8).
func parseQuantity(salesUnit string) (float64, string) {
	lower := strings.ToLower(salesUnit)

	if m := reMulti.FindStringSubmatch(lower); m != nil {
		size, unit := quantityOf(m[2], m[3])
		return round(mustParse(m[1])*size, 6), unit
	}
	if m := reSize.FindStringSubmatch(lower); m != nil {
		return quantityOf(m[1], m[2])
	}
	if m := reCount.FindStringSubmatch(lower); m != nil {
		return mustParse(m[1]), "unit"
	}
	return 0, ""
}

// quantityOf converts one declared size to its canonical unit (kg, L).
func quantityOf(number, unit string) (float64, string) {
	d, ok := unitDivisors[unit]
	if !ok {
		return 0, ""
	}
	return round(mustParse(number)/d.div, 6), d.unit
}

// mustParse turns a decimal-comma number ("0,75") into a float.
func mustParse(s string) float64 {
	n, err := strconv.ParseFloat(strings.Replace(s, ",", ".", 1), 64)
	if err != nil {
		return 0
	}
	return n
}

func round(v float64, places float64) float64 {
	scale := math.Pow(10, places)
	return math.Round(v*scale) / scale
}
