package alcampo

import (
	"strconv"
	"strings"

	"multisuper/internal/contract"
)

// product is the part of a catalogue product this CLI uses. The store publishes the
// unit price itself, so Unit and PricePerUnit never come from parsing the name.
type product struct {
	ProductID         string `json:"productId"`
	RetailerProductID string `json:"retailerProductId"`
	Name              string `json:"name"`
	Brand             string `json:"brand"`
	Price             amount `json:"price"`
	UnitPrice         struct {
		Price    amount `json:"price"`
		UnitName string `json:"unitName"`
	} `json:"unitPrice"`
	Available bool `json:"available"`
	Images    []struct {
		Src string `json:"src"`
	} `json:"images"`
}

// amount is how the storefront encodes money: decimal strings, not numbers.
type amount struct {
	Amount   string `json:"amount"`
	Currency string `json:"currency"`
}

// hit converts one catalogue product into the shared JSON envelope shape.
func (p product) hit() contract.Hit {
	h := contract.Hit{
		Store:     store,
		ID:        p.RetailerProductID,
		Name:      p.Name,
		Brand:     p.Brand,
		Price:     parseAmount(p.Price.Amount),
		Currency:  p.Price.Currency,
		Available: p.Available,
	}
	if p.RetailerProductID != "" {
		h.URL = baseURL + "/products/" + p.RetailerProductID + "/details"
	}
	if len(p.Images) > 0 {
		h.Image = p.Images[0].Src
	}
	// The store's own unit price, never a derived one: a product without it keeps both
	// fields empty rather than inventing a comparison (S8).
	if unit := contractUnit(p.UnitPrice.UnitName); unit != "" {
		h.Unit = unit
		h.PricePerUnit = parseAmount(p.UnitPrice.Price.Amount)
	}
	return h
}

// contractUnit maps the storefront's unit names onto the shared contract's units.
// Unknown names return "" so the caller leaves the unit price out.
func contractUnit(unitName string) string {
	switch unitName {
	case "PER_LITRE":
		return "L"
	case "PER_KG":
		return "kg"
	case "EACH":
		return "unit"
	}
	return ""
}

// parseAmount turns "5.28" into 5.28; an absent amount stays zero.
func parseAmount(s string) float64 {
	n, err := strconv.ParseFloat(strings.Replace(s, ",", ".", 1), 64)
	if err != nil {
		return 0
	}
	return n
}
