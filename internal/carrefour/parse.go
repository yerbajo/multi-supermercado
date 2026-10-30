package carrefour

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"multisuper/internal/contract"
)

// compareBasket is the slice of the Superfudis answer the CLI reads. Every size field
// is nullable: products whose amount Superfudis does not publish arrive as null.
type compareBasket struct {
	Status string       `json:"status"`
	Items  []basketItem `json:"items"`
}

type basketItem struct {
	Query  string  `json:"query"`
	Offers []offer `json:"offers"`
}

// offer is one shop's price for the queried product.
type offer struct {
	Retailer      string   `json:"retailer"`
	Name          string   `json:"name"`
	Brand         string   `json:"brand"`
	EAN           string   `json:"ean"`
	NUnits        *float64 `json:"n_units"`
	VolumeTotal   *float64 `json:"volume_total"`
	UnitSizeValue *float64 `json:"unit_size_value"`
	UnitSizeUnit  *string  `json:"unit_size_unit"`
	// A nil price_eur means Superfudis reported no price for the offer (S8).
	PriceEUR  *float64 `json:"price_eur"`
	MatchType string   `json:"match_type"`
	SourceURL string   `json:"source_url"`
}

// hit maps one offer onto the shared envelope.
func (o offer) hit(id string) contract.Hit {
	h := contract.Hit{
		Store: store,
		ID:    id,
		Name:  o.Name,
		Brand: o.Brand,
		// Superfudis publishes no stock field, so availability stays unknown: the key is
		// absent instead of an invented "in stock" (S8).
		// source_url is the carrefour.es catalogue page Superfudis matched, so the
		// aggregator can link the offer. It is Superfudis-reported, not fetched by us:
		// carrefour.es answers Cloudflare 403 (S9).
		URL: o.SourceURL,
		// Superfudis publishes the product's barcode, so the offer carries it; when it
		// publishes none the key stays absent rather than invented.
		EAN:  strings.TrimSpace(o.EAN),
		Size: sizeText(o),
	}
	if o.PriceEUR == nil {
		return h
	}
	h.Price = o.PriceEUR
	h.Currency = "EUR"
	// S8: read the amount only from published numbers, never from the product name.
	if amount, unit := size(o); amount > 0 && *o.PriceEUR > 0 {
		h.PricePerUnit = math.Round(*o.PriceEUR/amount*100) / 100
		h.Unit = unit
	}
	return h
}

// offerID keeps every offer addressable: the EAN when Superfudis publishes one, else a
// match_type+index pair so two fuzzy matches of the same item never collide.
func offerID(o offer, index int) string {
	if o.EAN != "" {
		return o.EAN
	}
	kind := o.MatchType
	if kind == "" {
		kind = "offer"
	}
	return fmt.Sprintf("%s-%d", kind, index)
}

var sizeUnits = map[string]struct {
	div  float64
	unit string
}{
	"ml": {1000, "L"}, "cl": {100, "L"}, "l": {1, "L"},
	"g": {1000, "kg"}, "gr": {1000, "kg"}, "kg": {1, "kg"},
}

// size returns the sold amount in kg or L from the published sizes, and (0, "") when no
// known unit is published.
func size(o offer) (float64, string) {
	d, ok := sizeUnits[strings.ToLower(strings.TrimSpace(deref(o.UnitSizeUnit)))]
	if !ok {
		return 0, ""
	}
	total := deref(o.VolumeTotal)
	if total <= 0 {
		units := deref(o.NUnits)
		if units <= 0 {
			units = 1
		}
		total = deref(o.UnitSizeValue) * units
	}
	if total <= 0 {
		return 0, ""
	}
	return total / d.div, d.unit
}

// sizeText is the sold size as published text: the published unit size, prefixed by the
// published pack count when there is more than one ("6 x 1000 ml"). No unit conversion
// and no reading of the name: a size nobody published stays empty (S8).
func sizeText(o offer) string {
	value := deref(o.UnitSizeValue)
	unit := strings.TrimSpace(deref(o.UnitSizeUnit))
	if value <= 0 || unit == "" {
		return ""
	}
	number := strings.Replace(strconv.FormatFloat(value, 'f', -1, 64), ".", ",", 1)
	if units := deref(o.NUnits); units > 1 {
		count := strconv.FormatFloat(units, 'f', -1, 64)
		return count + " x " + number + " " + unit
	}
	return number + " " + unit
}

func deref[T any](p *T) T {
	var zero T
	if p == nil {
		return zero
	}
	return *p
}
