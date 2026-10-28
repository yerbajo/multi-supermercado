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
		Available: p.Available,
	}
	// A published amount is the only price source; a missing or unparsable one leaves
	// the price absent instead of 0,00 € (S8).
	if price := parseAmount(p.Price.Amount); price != nil {
		h.Price = price
		h.Currency = p.Price.Currency
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
		if unitPrice := parseAmount(p.UnitPrice.Price.Amount); unitPrice != nil {
			h.PricePerUnit = *unitPrice
		}
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

// parseAmount turns "5.28" into 5.28; a missing or malformed amount is not a price.
func parseAmount(s string) *float64 {
	n, err := strconv.ParseFloat(strings.Replace(s, ",", ".", 1), 64)
	if err != nil {
		return nil
	}
	return &n
}
