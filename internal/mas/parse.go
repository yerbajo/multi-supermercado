package mas

import (
	"math"
	"regexp"
	"strconv"
	"strings"

	"multisuper/internal/contract"
)

// store is the store name every hit from this chain carries (S2).
const store = "mas"

// baseURL is the MAS storefront; product pages live at <baseURL>/<url_key>/.
const baseURL = "https://www.supermercadosmas.com"

// Size units as they appear in product names. The longest spelling goes first so
// "gr" wins over "g" and "cl" over "l" before the word boundary is checked.
const sizeUnits = `kg|gramos|gramo|grs|gr|g|litros|litro|lts|lt|mililitros|cl|ml|l`

var (
	// "6x200ml", "2 x 1,5 L": the pack multiplier, applied before a single size.
	reMulti = regexp.MustCompile(`(\d+)\s*[x×]\s*(\d+(?:[.,]\d+)?)\s*(` + sizeUnits + `)\b`)
	// "370 gr", "1,5l", "740gr", "50cl".
	reSize = regexp.MustCompile(`(\d+(?:[.,]\d+)?)\s*(` + sizeUnits + `)\b`)
	// "12ud", "6 uds", "4 unidades".
	reCount = regexp.MustCompile(`(\d+)\s*(unidades|unidad|uds|ud)\b`)
)

var unitDivisors = map[string]struct {
	div  float64
	unit string
}{
	"kg": {1, "kg"}, "gramos": {1000, "kg"}, "gramo": {1000, "kg"},
	"grs": {1000, "kg"}, "gr": {1000, "kg"}, "g": {1000, "kg"},
	"l": {1, "L"}, "litros": {1, "L"}, "litro": {1, "L"}, "lts": {1, "L"}, "lt": {1, "L"},
	"ml": {1000, "L"}, "mililitros": {1000, "L"}, "cl": {100, "L"},
}

// parseQuantity reads the sold size out of a product name, e.g. "lata 740gr" -> 0.74 kg.
// It returns (0, "") when the name carries no parsable size: MAS gives no unit price,
// and guessing one would invent a comparison the catalog does not support.
func parseQuantity(name string) (float64, string) {
	lower := strings.ToLower(name)

	if m := reMulti.FindStringSubmatch(lower); m != nil {
		count := mustParse(m[1])
		size, unit := quantityOf(m[2], m[3])
		return round(count*size, 6), unit
	}
	if m := reSize.FindStringSubmatch(lower); m != nil {
		return quantityOf(m[1], m[2])
	}
	if m := reCount.FindStringSubmatch(lower); m != nil {
		return mustParse(m[1]), "unit"
	}
	return 0, ""
}

// quantityOf converts one measured size to its canonical unit (kg, L, unit).
func quantityOf(number, unit string) (float64, string) {
	n := mustParse(number)
	d, ok := unitDivisors[unit]
	if !ok {
		return 0, ""
	}
	return round(n/d.div, 6), d.unit
}

// mustParse turns a decimal-comma number ("1,5") into a float.
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

// gqlProduct is the part of a Magento product we use. price.regularPrice is the only
// price field MAS answers without an HTTP 500 (price_range) or an unknown-field error
// (finalPrice).
type gqlProduct struct {
	Name        string `json:"name"`
	SKU         string `json:"sku"`
	URLKey      string `json:"url_key"`
	StockStatus string `json:"stock_status"`
	Price       struct {
		RegularPrice struct {
			Amount struct {
				// A nil value means MAS published no price for the product (S8).
				Value    *float64 `json:"value"`
				Currency string   `json:"currency"`
			} `json:"amount"`
		} `json:"regularPrice"`
	} `json:"price"`
}

// hit converts one catalog product into the shared JSON envelope shape.
func (p gqlProduct) hit() contract.Hit {
	h := contract.Hit{
		Store:     store,
		ID:        p.SKU,
		Name:      p.Name,
		Available: p.StockStatus == "IN_STOCK",
	}
	// url_key already carries the category path; the storefront only serves it with a
	// trailing slash (".html" or no slash both 404/redirect).
	if p.URLKey != "" {
		h.URL = baseURL + "/" + strings.Trim(p.URLKey, "/") + "/"
	}
	if price := p.Price.RegularPrice.Amount.Value; price != nil {
		h.Price = price
		h.Currency = p.Price.RegularPrice.Amount.Currency
		// Euro per kg/L, rounded to cents like the unit prices DIA publishes directly.
		if amount, unit := parseQuantity(p.Name); amount > 0 && *price > 0 {
			h.PricePerUnit = round(*price/amount, 2)
			h.Unit = unit
		}
	}
	return h
}
