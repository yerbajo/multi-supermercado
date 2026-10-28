package basket

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"multisuper/internal/aggregate"
	"multisuper/internal/contract"
)

// The pinned entry a human writes after choosing from the candidate list (L7).
const pinnedEggName = "Huevos extragrandes L/XL de gallinas criadas en suelo Dia 6 unidades"

var pinnedEggs = Item{Store: "dia", ID: "305802", Name: pinnedEggName, Quantity: 2}

var sixStores = []string{"alcampo", "aldi", "carrefour", "dia", "lidl", "mas"}

// A pinned entry resolves by exact id, at the price the chain publishes now: the search
// asks for a wider page and never trusts a remembered price.
func TestPriceResolvesAPinnedEntryByExactID(t *testing.T) {
	dia, s := chainFor("dia", map[string][]contract.Hit{
		pinnedEggName: {
			// The same product with another id is not the user's choice.
			product("111", pinnedEggName, 1.10),
			product("305802", pinnedEggName, 2.25),
		},
	})
	res := Price(context.Background(), req(pinnedEggs), []aggregate.Chain{dia}, DefaultCandidates, time.Second)

	got := itemOf(t, res, "")
	if r, ok := got.Resolved["dia"]; !ok || r.ID != "305802" || r.UnitCents != 225 || r.LineCents != 450 {
		t.Fatalf("dia resolved = %+v, want id 305802 at 225 unit and 450 line", got.Resolved)
	}
	// The name is the search term, with the wider page pinned ids need to be found in.
	if len(s.terms) != 1 || s.terms[0] != pinnedEggName || s.limits[0] != PinnedLimit {
		t.Fatalf("searched %v with limits %v, want the pinned name and %d", s.terms, s.limits, PinnedLimit)
	}
	if c := chainOf(t, res, "dia"); c.Coverage != "1/1" || c.TotalCents != 450 || len(c.Stale) != 0 {
		t.Errorf("dia = %+v, want 1/1 at 450 cents and no stale", c)
	}
	if len(res.Ranking) != 1 || res.Ranking[0].Store != "dia" {
		t.Errorf("ranking = %+v, want dia", res.Ranking)
	}
}

// A pinned id that is gone is stale, not missing and never a fuzzy match: the chain
// publishes the product the user chose under another id, and using it would be a lie.
func TestPriceMarksAPinnedEntryStaleWhenItsIDIsGone(t *testing.T) {
	dia, _ := chainFor("dia", map[string][]contract.Hit{
		pinnedEggName: {product("999", pinnedEggName, 2.25)},
	})
	mas, _ := chainFor("mas", map[string][]contract.Hit{
		pinnedEggName: {product("305802", "Huevos frescos clase M 12 ud", 2.05)},
	})
	res := Price(context.Background(), req(pinnedEggs), []aggregate.Chain{dia, mas}, DefaultCandidates, time.Second)

	got := itemOf(t, res, "")
	if _, ok := got.Resolved["dia"]; ok {
		t.Fatalf("dia resolved = %+v, want the pinned id to stay unresolved", got.Resolved)
	}
	if len(got.Stale) != 1 || got.Stale[0] != "dia" {
		t.Errorf("item stale = %v, want [dia]", got.Stale)
	}
	diaRes := chainOf(t, res, "dia")
	if len(diaRes.Stale) != 1 || diaRes.Stale[0] != pinnedEggName {
		t.Fatalf("dia = %+v, want the pinned name stale", diaRes)
	}
	if diaRes.Error != nil || diaRes.TotalCents != 0 || diaRes.Coverage != "0/1" {
		t.Errorf("dia = %+v, want no error, no total and 0/1", diaRes)
	}
	// A chain with a stale item is out of the ranking, exactly like a chain missing it.
	if len(res.Ranking) != 0 {
		t.Errorf("ranking = %+v, want empty", res.Ranking)
	}
	// mas holds another product with the pinned id: ids are per chain, so that proves
	// nothing about the user's DIA choice, and the item is simply not sold there.
	masRes := chainOf(t, res, "mas")
	if len(masRes.Stale) != 0 || len(masRes.Missing) != 1 || masRes.Missing[0] != pinnedEggName {
		t.Errorf("mas = %+v, want the pinned item missing and nothing stale", masRes)
	}
}

// Pinned and automatic entries live in the same basket: the pinned one is priced only in
// its chain, and every chain that cannot serve it covers one item less.
func TestPricePinsAndAutomaticallyMatchesInTheSameBasket(t *testing.T) {
	items := req(pinnedEggs, Item{Query: "leche entera", Quantity: 1})
	dia, _ := chainFor("dia", map[string][]contract.Hit{
		pinnedEggName:  {product("305802", pinnedEggName, 2.25)},
		"leche entera": {product("d1", "Leche entera Dia Láctea 1 L", 0.90)},
	})
	mas, _ := chainFor("mas", map[string][]contract.Hit{
		"leche entera": {product("m1", "Leche entera Pascual 1 L", 0.80)},
	})
	res := Price(context.Background(), items, []aggregate.Chain{dia, mas}, DefaultCandidates, time.Second)

	// dia: 2 x 225 + 90 = 540.
	if got := chainOf(t, res, "dia"); got.TotalCents != 540 || got.Coverage != "2/2" {
		t.Fatalf("dia = %+v, want 540 cents and 2/2", got)
	}
	masRes := chainOf(t, res, "mas")
	if masRes.Coverage != "1/2" || len(masRes.Missing) != 1 || masRes.Missing[0] != pinnedEggName {
		t.Fatalf("mas = %+v, want 1/2 missing the pinned eggs", masRes)
	}
	if len(masRes.Stale) != 0 {
		t.Errorf("mas stale = %v, want empty: it never had a pinned id to confirm", masRes.Stale)
	}
	if len(res.Ranking) != 1 || res.Ranking[0].Store != "dia" || res.Ranking[0].TotalCents != 540 {
		t.Errorf("ranking = %+v, want dia 540 only", res.Ranking)
	}
	// The pinned line carries the choice; the automatic line still carries its query.
	if res.Items[0].Name != pinnedEggName || res.Items[0].ID != "305802" || res.Items[0].Store != "dia" {
		t.Errorf("items[0] = %+v, want the pinned choice", res.Items[0])
	}
	if res.Items[1].Query != "leche entera" {
		t.Errorf("items[1].Query = %q, want leche entera", res.Items[1].Query)
	}
}

// A chain that fails keeps its error: a broken chain cannot confirm the id, so it is not
// a product that went stale.
func TestPriceChainErrorBeatsStale(t *testing.T) {
	bad := &stub{err: &aggregate.StatusError{Status: 429, Msg: "lidl: rate limit"}}
	pinned := Item{Store: "lidl", ID: "305802", Name: pinnedEggName, Quantity: 1}
	res := Price(context.Background(), req(pinned), []aggregate.Chain{{Store: "lidl", Searcher: bad}}, DefaultCandidates, time.Second)

	lidl := chainOf(t, res, "lidl")
	if lidl.Error == nil || lidl.Error.Code != aggregate.CodeQuota {
		t.Fatalf("lidl = %+v, want a quota error", lidl)
	}
	if len(lidl.Stale) != 0 || len(lidl.Missing) != 0 {
		t.Errorf("lidl = %+v, want no stale and no missing", lidl)
	}
	if got := itemOf(t, res, ""); len(got.Stale) != 0 || len(got.Missing) != 0 {
		t.Errorf("item = %+v, want no stale and no missing", got)
	}
}

// Every broken pinned shape is a usage error with its own sentence.
func TestDecodeRejectsInvalidPinnedItems(t *testing.T) {
	ok := func(store, id, name string, qty int) string {
		return fmt.Sprintf(`{"items":[{"store":%q,"id":%q,"name":%q,"quantity":%d}]}`, store, id, name, qty)
	}
	tests := []struct {
		name   string
		in     string
		stores []string
		want   string
	}{
		{"unknown store", ok("mercadona", "1", "Huevos", 1), nil, `store "mercadona"`},
		{"empty id", ok("dia", " ", "Huevos", 1), nil, "id is empty"},
		{"empty name", ok("dia", "1", " ", 1), nil, "name is empty"},
		{"zero quantity", ok("dia", "1", "Huevos", 0), nil, "quantity must be"},
		{"query and choice", `{"items":[{"query":"huevos","store":"dia","id":"1","name":"Huevos","quantity":1}]}`, nil,
			"either query or store"},
		{"no item at all", `{"items":[{"quantity":1}]}`, nil, "query is empty"},
		{"store not priced here", ok("dia", "1", "Huevos", 1), []string{"aldi", "lidl"}, `store "dia" is not one of`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stores := tt.stores
			if stores == nil {
				stores = sixStores
			}
			_, err := Decode(strings.NewReader(tt.in), stores)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want it to mention %q", err, tt.want)
			}
		})
	}
}

// A mixed basket decodes, trimmed, and keeps the item as the user wrote it.
func TestDecodeAcceptsAMixedBasket(t *testing.T) {
	in := `{"items":[{"query":" leche entera ","quantity":1},
		{"store":" dia ","id":" 305802 ","name":" Huevos frescos 12 ud ","quantity":2}]}`
	got, err := Decode(strings.NewReader(in), sixStores)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if len(got.Items) != 2 {
		t.Fatalf("items = %+v, want 2", got.Items)
	}
	if got.Items[0].Query != "leche entera" || got.Items[0].Pinned() {
		t.Errorf("items[0] = %+v, want an automatic leche entera", got.Items[0])
	}
	if w := got.Items[1]; w.Store != "dia" || w.ID != "305802" || w.Name != "Huevos frescos 12 ud" || !w.Pinned() {
		t.Errorf("items[1] = %+v, want a trimmed pinned dia 305802", w)
	}
}
