package mas

import (
	"strings"

	"multisuper/internal/contract"
)

// store is the store name every hit from this chain carries (S2).
const store = "mas"

// baseURL is the MAS storefront; product pages live at <baseURL>/<url_key>/.
const baseURL = "https://www.supermercadosmas.com"

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
//
// MAS publishes no sold size, so PricePerUnit and Unit always stay empty. A previous
// version read a size out of the product name; it reported 0.09 €/L for a 30-litre bin
// bag ("HANDY BAG 30l 12ud") and 135 €/L for "IFA SABE 10ux100ml", prices per litre
// nobody publishes. Removed rather than gated by category (S8).
func (p gqlProduct) hit() contract.Hit {
	h := contract.Hit{
		Store:     store,
		ID:        p.SKU,
		Name:      p.Name,
		Available: contract.Bool(p.StockStatus == "IN_STOCK"),
	}
	// url_key already carries the category path; the storefront only serves it with a
	// trailing slash (".html" or no slash both 404/redirect).
	if p.URLKey != "" {
		h.URL = baseURL + "/" + strings.Trim(p.URLKey, "/") + "/"
	}
	if price := p.Price.RegularPrice.Amount.Value; price != nil {
		h.Price = price
		h.Currency = p.Price.RegularPrice.Amount.Currency
	}
	return h
}
