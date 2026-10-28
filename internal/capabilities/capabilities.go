// Package capabilities is the one static table of what each chain can actually do, as
// the T29 investigation proved: whether it searches, whether it has a real store cart,
// and what scope its price has — a warehouse zone, a pickup point, a region, or nothing.
//
// The web reads this table to decide which chain is even offered a zone step. It never
// derives a store, a zone or a price scope from a guess: a chain whose catalogue ignores
// the postal code says so here, because presenting the server's default zone as the
// user's would be inventing a price (S11, S13).
package capabilities

// The zone modes T29 actually measured, one per chain:
//
//	warehouse — the zone is a server-side state of the session and changes prices (dia);
//	pickup    — the chain publishes pickup points, but the price does not follow them;
//	region    — the catalogue is split by region and the price follows the region;
//	none      — there is no zone in the chain's model at all.
const (
	ModeWarehouse = "warehouse"
	ModePickup    = "pickup"
	ModeRegion    = "region"
	ModeNone      = "none"
)

// Modes lists every zone mode, so a consumer can validate a table instead of trusting it.
func Modes() []string { return []string{ModeWarehouse, ModePickup, ModeRegion, ModeNone} }

// Zone is the real reach of a chain's price. PriceScoped is true only when the price
// actually changes with the zone: a pickup point that leaves the price alone is not a
// scoped price, and the web must not promise one.
type Zone struct {
	Mode        string `json:"mode"`
	PriceScoped bool   `json:"price_scoped"`
	Label       string `json:"label"`
}

// Capability is one chain's row: what it can be asked to do and the scope of its answer.
type Capability struct {
	Store  string `json:"store"`
	Search bool   `json:"search"`
	Cart   bool   `json:"cart"`
	Zone   Zone   `json:"zone"`
}

// table is the whole answer, in the order the CLIs print the chains. Every value here
// is a T29 finding, kept verbatim in the labels so the investigation stays traceable.
var table = []Capability{
	{
		Store: "alcampo", Search: true, Cart: false,
		Zone: Zone{Mode: ModeNone, PriceScoped: false,
			Label: "el código postal es una puerta de su web, no un argumento de la API"},
	},
	{
		Store: "aldi", Search: true, Cart: false,
		Zone: Zone{Mode: ModeRegion, PriceScoped: true,
			Label: "solo península/Canarias/Baleares"},
	},
	{
		Store: "carrefour", Search: true, Cart: false,
		Zone: Zone{Mode: ModeNone, PriceScoped: false,
			Label: "el catálogo de un tercero no tiene campo de ubicación"},
	},
	{
		Store: "dia", Search: true, Cart: true,
		Zone: Zone{Mode: ModeWarehouse, PriceScoped: true,
			Label: "reprecia tu cesta: la zona es del servidor, por defecto 28041"},
	},
	{
		Store: "lidl", Search: true, Cart: false,
		Zone: Zone{Mode: ModePickup, PriceScoped: false,
			Label: "tienda más cercana informativa"},
	},
	{
		Store: "mas", Search: true, Cart: false,
		Zone: Zone{Mode: ModePickup, PriceScoped: false,
			Label: "recogida en tienda; precio de catálogo nacional"},
	},
}

// All returns the table in chain order. The slice is a copy: a caller may sort or trim
// it without changing what the next request sees.
func All() []Capability { return append([]Capability(nil), table...) }

// Get returns one chain's row, and false when the store is not one of the six.
func Get(store string) (Capability, bool) {
	for _, c := range table {
		if c.Store == store {
			return c, true
		}
	}
	return Capability{}, false
}

// Stores names the six chains in table order, the same order the search and basket
// documents keep.
func Stores() []string {
	out := make([]string, len(table))
	for i, c := range table {
		out[i] = c.Store
	}
	return out
}
