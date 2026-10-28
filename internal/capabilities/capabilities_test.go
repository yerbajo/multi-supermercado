package capabilities

import (
	"encoding/json"
	"testing"
)

// TestTableIsComplete pins the six chains and their rows: a chain that is not in the
// table can never be offered a zone step, and a row that loses a field would make the
// web guess instead of reading.
func TestTableIsComplete(t *testing.T) {
	want := []string{"alcampo", "aldi", "carrefour", "dia", "lidl", "mas"}
	got := Stores()
	if len(got) != len(want) {
		t.Fatalf("stores: got %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("stores: got %v, want %v", got, want)
		}
	}

	modes := make(map[string]bool, len(Modes()))
	for _, m := range Modes() {
		modes[m] = true
	}

	// Every chain is searchable today, and every row says why its price is scoped or not.
	for _, c := range All() {
		if !c.Search {
			t.Errorf("%s: search is false, all six chains search", c.Store)
		}
		if !modes[c.Zone.Mode] {
			t.Errorf("%s: zone mode %q is not one of the four measured modes", c.Store, c.Zone.Mode)
		}
		if c.Zone.Label == "" {
			t.Errorf("%s: zone label is empty, the web has nothing to show", c.Store)
		}
		one, ok := Get(c.Store)
		if !ok || one != c {
			t.Errorf("%s: Get does not return the table row", c.Store)
		}
	}
	if _, ok := Get("mercadona"); ok {
		t.Error("Get accepted a store that is not in the table")
	}

	// All hands out a copy: the shared table must not be mutable from outside.
	first := All()
	first[0].Store = "mutated"
	if All()[0].Store != "alcampo" {
		t.Error("All returned the shared slice")
	}
}

// TestOnlyDIAHasACart keeps the hand-off honest: one working retailer cart, never six.
func TestOnlyDIAHasACart(t *testing.T) {
	for _, c := range All() {
		if c.Cart != (c.Store == "dia") {
			t.Errorf("%s: cart is %v, only dia has a working one", c.Store, c.Cart)
		}
	}
}

// TestPriceScopeMatchesT29 pins what the investigation measured: DIA changes prices with
// the warehouse zone and ALDI with the region. MAS, LIDL, ALCAMPO and CARREFOUR do not,
// and promising them a scoped price would invent one.
func TestPriceScopeMatchesT29(t *testing.T) {
	scoped := map[string]bool{"dia": true, "aldi": true}
	for _, c := range All() {
		if c.Zone.PriceScoped != scoped[c.Store] {
			t.Errorf("%s: price_scoped is %v, T29 measured %v", c.Store, c.Zone.PriceScoped, scoped[c.Store])
		}
	}
	if got, _ := Get("dia"); got.Zone.Mode != ModeWarehouse {
		t.Errorf("dia: mode %q, want warehouse", got.Zone.Mode)
	}
	if got, _ := Get("aldi"); got.Zone.Mode != ModeRegion {
		t.Errorf("aldi: mode %q, want region", got.Zone.Mode)
	}
	if got, _ := Get("mas"); got.Zone.Mode != ModePickup {
		t.Errorf("mas: mode %q, want pickup", got.Zone.Mode)
	}
	if got, _ := Get("lidl"); got.Zone.Mode != ModePickup {
		t.Errorf("lidl: mode %q, want pickup", got.Zone.Mode)
	}
	for _, store := range []string{"alcampo", "carrefour"} {
		if got, _ := Get(store); got.Zone.Mode != ModeNone {
			t.Errorf("%s: mode %q, want none", store, got.Zone.Mode)
		}
	}
}

// TestJSONShape is the contract the front end reads: mode and price_scoped under zone,
// with the store key outside it.
func TestJSONShape(t *testing.T) {
	diaRow, ok := Get("dia")
	if !ok {
		t.Fatal("dia is missing from the table")
	}
	raw, err := json.Marshal(diaRow)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var doc struct {
		Store string `json:"store"`
		Cart  bool   `json:"cart"`
		Zone  struct {
			Mode        string `json:"mode"`
			PriceScoped bool   `json:"price_scoped"`
			Label       string `json:"label"`
		} `json:"zone"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if doc.Store != "dia" || !doc.Cart || doc.Zone.Mode != ModeWarehouse || !doc.Zone.PriceScoped {
		t.Fatalf("unexpected JSON: %s", raw)
	}
}
