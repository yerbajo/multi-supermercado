// Package contract defines the JSON output shared by every supermarket CLI, so the
// aggregating web app parses one shape regardless of which chain answered.
package contract

import (
	"encoding/json"
	"io"
)

// Hit is one product offered by a store, normalised for cross-store comparison.
type Hit struct {
	Store        string  `json:"store"`
	ID           string  `json:"id"`
	Name         string  `json:"name"`
	Brand        string  `json:"brand,omitempty"`
	Price        float64 `json:"price"`
	PricePerUnit float64 `json:"pricePerUnit,omitempty"`
	Unit         string  `json:"unit,omitempty"` // kg | L | unit
	Currency     string  `json:"currency"`
	Available    bool    `json:"available"`
	Category     string  `json:"category,omitempty"`
	URL          string  `json:"url,omitempty"`
	Image        string  `json:"image,omitempty"`
}

// Result is the envelope printed by `search`, both as JSON and as a table.
type Result struct {
	Store string `json:"store"`
	Query string `json:"query"`
	Count int    `json:"count"`
	Hits  []Hit  `json:"hits"`
}

// Encode writes the JSON the web app consumes: indented, one document, trailing newline.
func Encode(w io.Writer, r Result) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}
