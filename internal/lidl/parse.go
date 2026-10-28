package lidl

import (
	"math"
	"regexp"
	"strconv"
	"strings"

	"multisuper/internal/contract"
)

// searchResponse is the part of the mindshift search answer the CLI reads. Items also
// carry encrypted relevancy blobs (xPayload), teasers and advisors.
type searchResponse struct {
	Items []item `json:"items"`
}

type item struct {
	ResultClass string `json:"resultClass"`
	// Code is the article number (erpNumber); gridbox.data.code is null on search hits.
	Code    string `json:"code"`
	Gridbox struct {
		Data product `json:"data"`
	} `json:"gridbox"`
}

type product struct {
	ProductID    int64  `json:"productId"`
	ERPNumber    string `json:"erpNumber"`
	FullTitle    string `json:"fullTitle"`
	CanonicalURL string `json:"canonicalUrl"`
	Category     string `json:"category"`
	Image        string `json:"image"`
	Brand        struct {
		Name string `json:"name"`
	} `json:"brand"`
	Price struct {
		// Price is null when Lidl publishes no price for the article in this region.
		Price        *float64 `json:"price"`
		CurrencyCode string   `json:"currencyCode"`
		Packaging    struct {
			Text string `json:"text"`
		} `json:"packaging"`
	} `json:"price"`
	StockAvailability struct {
		OnlineAvailable bool `json:"onlineAvailable"`
		BadgeInfo       struct {
			Badges []struct {
				Text string `json:"text"`
				Type string `json:"type"`
			} `json:"badges"`
		} `json:"badgeInfo"`
	} `json:"stockAvailability"`
}

// hit converts one search result into the shared envelope shape. It reports false for
// non-product results, which the API mixes into the items array.
func (i item) hit() (contract.Hit, bool) {
	if i.ResultClass != "product" {
		return contract.Hit{}, false
	}
	p := i.Gridbox.Data
	h := contract.Hit{
		Store:     store,
		ID:        i.Code,
		Name:      p.FullTitle,
		Brand:     p.Brand.Name,
		Available: p.available(),
		Category:  p.Category,
		URL:       baseURL + p.CanonicalURL,
		Image:     p.Image,
	}
	if h.ID == "" {
		h.ID = p.ERPNumber
	}
	if h.ID == "" && p.ProductID != 0 {
		h.ID = strconv.FormatInt(p.ProductID, 10)
	}
	// A nil price means Lidl published none (no price at all, or only a Lidl Plus
	// member price): the hit keeps it empty instead of 0 (S8).
	if price := p.Price.Price; price != nil {
		h.Price = price
		h.Currency = p.Price.CurrencyCode
		// Euro per kg/L only when Lidl publishes the sold amount and a price (S8).
		if amount, unit := quantity(p.Price.Packaging.Text, p.FullTitle, p.Category); amount > 0 && *price > 0 {
			h.PricePerUnit = math.Round(*price/amount*100) / 100
			h.Unit = unit
		}
	}
	return h, true
}

// available reports whether Lidl publishes the article as buyable: online, or in a
// store today. "Solo en tienda desde <date>" is not available yet.
func (p product) available() bool {
	if p.StockAvailability.OnlineAvailable {
		return true
	}
	for _, b := range p.StockAvailability.BadgeInfo.Badges {
		if b.Type == "IN_STORE" {
			return true
		}
	}
	return false
}

// Size units as Lidl publishes them: grams, kilos, millilitres, centilitres, litres.
const sizeUnits = `kg|mg|g|cl|ml|l`

var (
	// "2x400 g", "8x125g", "9x33 cl", "6 x 1,5 L": pack multiplier wins over one size.
	reMulti = regexp.MustCompile(`(\d+)\s*[x×]\s*(\d+(?:[.,]\d+)?)\s*(` + sizeUnits + `)\b`)
	// "100 g", "160g", "1l", "Aprox.1,1 kg", "33 cl".
	reSize = regexp.MustCompile(`(\d+(?:[.,]\d+)?)\s*(` + sizeUnits + `)\b`)
	// "1 ud", "6 uds", "4 unidades".
	reCount = regexp.MustCompile(`(\d+)\s*(unidades|unidad|uds|ud)\b`)
)

var unitDivisors = map[string]struct {
	div  float64
	unit string
}{
	"kg": {1, "kg"}, "g": {1000, "kg"}, "mg": {1000000, "kg"},
	"l": {1, "L"}, "cl": {100, "L"}, "ml": {1000, "L"},
}

// quantity returns the sold amount in kg, L or units, and "" when nothing is
// published. The packaging text Lidl ships ("500 ml", "8x125g") is authoritative; a
// product name is only read for grocery categories, because an appliance name can
// carry a capacity ("Cafetera 1,5 l") that is not a sold amount.
func quantity(packaging, name, category string) (float64, string) {
	if n, unit := parseSize(packaging); n > 0 {
		return n, unit
	}
	if category != "Food" && category != "F+V" {
		return 0, ""
	}
	return parseSize(name)
}

func parseSize(s string) (float64, string) {
	lower := strings.ToLower(s)

	if m := reMulti.FindStringSubmatch(lower); m != nil {
		size, unit := quantityOf(m[2], m[3])
		return round(mustParse(m[1]) * size), unit
	}
	if m := reSize.FindStringSubmatch(lower); m != nil {
		return quantityOf(m[1], m[2])
	}
	if m := reCount.FindStringSubmatch(lower); m != nil {
		return mustParse(m[1]), "unit"
	}
	return 0, ""
}

// quantityOf converts one measured size to its canonical unit (kg, L).
func quantityOf(number, unit string) (float64, string) {
	d, ok := unitDivisors[unit]
	if !ok {
		return 0, ""
	}
	return round(mustParse(number) / d.div), d.unit
}

// mustParse turns a decimal-comma number ("1,5") into a float.
func mustParse(s string) float64 {
	n, err := strconv.ParseFloat(strings.Replace(s, ",", ".", 1), 64)
	if err != nil {
		return 0
	}
	return n
}

func round(v float64) float64 {
	return math.Round(v*1e6) / 1e6
}
