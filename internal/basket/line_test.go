package basket

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"multisuper/internal/aggregate"
	"multisuper/internal/contract"
)

// The three real chains of the live defect (T32): the same basket line, eggs, with a
// different chosen product per chain.
const (
	eggDia  = "Huevos extragrandes L/XL de gallinas criadas en suelo Dia 6 unidades"
	eggMas  = "Huevos frescos clase M 12 ud"
	eggAldi = "Huevos camperos Alipende 12 ud"
)

// lineOf finds the result of one line by its id.
func lineOf(t *testing.T, res Result, id string) ItemResult {
	t.Helper()
	for _, it := range res.Items {
		if it.Line == id {
			return it
		}
	}
	t.Fatalf("no item entry for line %q in %+v", id, res.Items)
	return ItemResult{}
}

// One line may hold one product per chain: pinning eggs at DIA, MAS and ALDI is one line
// and not three, so every one of those chains covers the whole basket and can be ranked.
// Before the line key the same basket collapsed to 1/3 everywhere and named no winner.
func TestPriceCoversEveryChainWhenEachLinePinsADifferentChain(t *testing.T) {
	items := req(
		Item{Query: "leche entera", Quantity: 1},
		Item{Query: "pan de molde", Quantity: 1},
		Item{Line: "huevos", Store: "dia", ID: "305802", Name: eggDia, Quantity: 2},
		Item{Line: "huevos", Store: "mas", ID: "116867", Name: eggMas, Quantity: 2},
		Item{Line: "huevos", Store: "aldi", ID: "a99", Name: eggAldi, Quantity: 2},
	)
	dia, _ := chainFor("dia", map[string][]contract.Hit{
		"leche entera": {product("d1", "Leche entera Dia Láctea 1 L", 0.90)},
		"pan de molde": {product("d2", "Pan de molde blanco 460 g", 1.20)},
		eggDia:         {product("305802", eggDia, 2.20)},
	})
	mas, _ := chainFor("mas", map[string][]contract.Hit{
		"leche entera": {product("m1", "Leche entera Pascual 1 L", 1.00)},
		"pan de molde": {product("m2", "Pan de molde integral 500 g", 1.30)},
		eggMas:         {product("116867", eggMas, 1.80)},
	})
	aldi, _ := chainFor("aldi", map[string][]contract.Hit{
		"leche entera": {product("a1", "Leche entera Alipende 1 L", 1.10)},
		"pan de molde": {product("a2", "Pan de molde Alipende 500 g", 1.40)},
		eggAldi:        {product("a99", eggAldi, 2.00)},
	})

	res := Price(context.Background(), items,
		[]aggregate.Chain{dia, mas, aldi}, DefaultCandidates, time.Second)

	// Each chain prices its own three lines: 2 eggs + leche + pan.
	want := map[string]int64{"dia": 90 + 120 + 440, "mas": 100 + 130 + 360, "aldi": 110 + 140 + 400}
	for store, total := range want {
		got := chainOf(t, res, store)
		if got.Coverage != "3/3" || got.TotalCents != total {
			t.Errorf("%s = %+v, want 3/3 and %d cents", store, got, total)
		}
		if len(got.Missing) != 0 || len(got.Stale) != 0 {
			t.Errorf("%s = %+v, want nothing missing and nothing stale", store, got)
		}
	}
	// Cheapest complete chain first: mas 590, then dia 650, then aldi 650 in table order.
	if len(res.Ranking) != 3 || res.Ranking[0].Store != "mas" || res.Ranking[0].TotalCents != 590 ||
		res.Ranking[1].Store != "dia" || res.Ranking[2].Store != "aldi" {
		t.Fatalf("ranking = %+v, want mas, dia, aldi", res.Ranking)
	}

	// One item per line, not per chain entry, and the eggs line is the union of the pins.
	if len(res.Items) != 3 {
		t.Fatalf("items = %+v, want the three lines", res.Items)
	}
	eggs := lineOf(t, res, "huevos")
	if eggs.Quantity != 2 || eggs.Query != "" {
		t.Errorf("eggs line = %+v, want quantity 2 and no query", eggs)
	}
	for store, id := range map[string]string{"dia": "305802", "mas": "116867", "aldi": "a99"} {
		if r := eggs.Resolved[store]; r.ID != id || r.LineCents != 2*r.UnitCents {
			t.Errorf("eggs in %s = %+v, want id %s priced by quantity", store, r, id)
		}
	}
	if len(eggs.Missing) != 0 || len(eggs.Stale) != 0 {
		t.Errorf("eggs = %+v, want nothing missing and nothing stale", eggs)
	}
}

// A chain the line has no pin for is missing the line, never given another chain's
// product: the choice is per chain, so an unpinned chain simply has nothing to price.
func TestPriceMarksTheLineMissingInAChainWithoutAPinOrFallback(t *testing.T) {
	items := req(
		Item{Query: "leche entera", Quantity: 1},
		Item{Line: "huevos", Store: "dia", ID: "305802", Name: eggDia, Quantity: 1},
		Item{Line: "huevos", Store: "mas", ID: "116867", Name: eggMas, Quantity: 1},
	)
	dia, _ := chainFor("dia", map[string][]contract.Hit{
		"leche entera": {product("d1", "Leche entera Dia Láctea 1 L", 0.90)},
		eggDia:         {product("305802", eggDia, 2.20)},
	})
	lidl, _ := chainFor("lidl", map[string][]contract.Hit{
		"leche entera": {product("l1", "Leche entera Milbona 1 L", 0.80)},
	})

	res := Price(context.Background(), items, []aggregate.Chain{dia, lidl}, DefaultCandidates, time.Second)

	lidlRes := chainOf(t, res, "lidl")
	if lidlRes.Coverage != "1/2" || len(lidlRes.Missing) != 1 || lidlRes.Missing[0] != "huevos" {
		t.Fatalf("lidl = %+v, want 1/2 missing huevos", lidlRes)
	}
	if eggs := lineOf(t, res, "huevos"); len(eggs.Missing) != 1 || eggs.Missing[0] != "lidl" {
		t.Errorf("eggs = %+v, want lidl missing", eggs)
	}
	if len(res.Ranking) != 1 || res.Ranking[0].Store != "dia" {
		t.Errorf("ranking = %+v, want only dia", res.Ranking)
	}
}

// The line may also carry an automatic entry: it is the fallback for the chains the user
// has not chosen a product in, and the pin wins wherever there is one.
func TestPriceFallsBackToTheAutomaticEntryOfTheLine(t *testing.T) {
	items := req(
		Item{Line: "huevos", Query: "huevos", Quantity: 1},
		Item{Line: "huevos", Store: "dia", ID: "305802", Name: eggDia, Quantity: 1},
	)
	dia, diaStub := chainFor("dia", map[string][]contract.Hit{
		eggDia:   {product("305802", eggDia, 2.20)},
		"huevos": {product("d3", "Huevos frescos clase M 12 ud", 1.99)},
	})
	mas, _ := chainFor("mas", map[string][]contract.Hit{
		"huevos": {product("m3", "Huevos frescos 12 ud", 2.05)},
	})

	res := Price(context.Background(), items, []aggregate.Chain{dia, mas}, DefaultCandidates, time.Second)

	eggs := lineOf(t, res, "huevos")
	if eggs.Query != "huevos" || eggs.Quantity != 1 {
		t.Fatalf("eggs = %+v, want the huevos query and quantity 1", eggs)
	}
	if got := eggs.Resolved["dia"]; got.ID != "305802" || got.UnitCents != 220 {
		t.Errorf("dia = %+v, want the pinned 305802 at 220", got)
	}
	if got := eggs.Resolved["mas"]; got.ID != "m3" || got.UnitCents != 205 {
		t.Errorf("mas = %+v, want the best automatic match m3", got)
	}
	// The pin is the only thing asked of its own chain: the fallback query is not searched.
	if len(diaStub.terms) != 1 || diaStub.terms[0] != eggDia {
		t.Errorf("dia searched %v, want only the pinned name", diaStub.terms)
	}
	if len(res.Ranking) != 2 || res.Ranking[0].Store != "mas" {
		t.Errorf("ranking = %+v, want mas then dia", res.Ranking)
	}
}

// A stale pin is not a fallback: the user's product is gone from that chain today, and
// another product would be a different basket.
func TestPriceKeepsAStaleLineOutEvenWithAnAutomaticFallback(t *testing.T) {
	items := req(
		Item{Line: "huevos", Query: "huevos", Quantity: 1},
		Item{Line: "huevos", Store: "dia", ID: "305802", Name: eggDia, Quantity: 1},
	)
	dia, _ := chainFor("dia", map[string][]contract.Hit{
		eggDia:   {product("999", eggDia, 2.20)},
		"huevos": {product("999", eggDia, 2.20)},
	})
	mas, _ := chainFor("mas", map[string][]contract.Hit{
		"huevos": {product("m3", "Huevos frescos 12 ud", 2.05)},
	})

	res := Price(context.Background(), items, []aggregate.Chain{dia, mas}, DefaultCandidates, time.Second)

	diaRes := chainOf(t, res, "dia")
	if diaRes.Coverage != "0/1" || len(diaRes.Stale) != 1 || diaRes.Stale[0] != "huevos" {
		t.Fatalf("dia = %+v, want 0/1 with the line stale", diaRes)
	}
	if _, ok := lineOf(t, res, "huevos").Resolved["dia"]; ok {
		t.Error("a stale pin fell back to the automatic entry")
	}
	if len(res.Ranking) != 1 || res.Ranking[0].Store != "mas" {
		t.Errorf("ranking = %+v, want only mas", res.Ranking)
	}
}

// Validation of the line group: one quantity, one product per chain, one fallback.
func TestDecodeRejectsBrokenLineGroups(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			"pinned without a line",
			`{"items":[{"store":"dia","id":"1","name":"Huevos","quantity":1}]}`,
			"a pinned entry needs a line",
		},
		{
			"quantities differ",
			`{"items":[{"line":"huevos","query":"huevos","quantity":1},
				{"line":"huevos","store":"dia","id":"1","name":"Huevos","quantity":2}]}`,
			`line "huevos": quantity 2 does not match the 1 of its other entries`,
		},
		{
			"two entries for one store",
			`{"items":[{"line":"huevos","store":"dia","id":"1","name":"Huevos","quantity":1},
				{"line":"huevos","store":"dia","id":"2","name":"Huevos","quantity":1}]}`,
			`line "huevos": two entries for store "dia"`,
		},
		{
			"two automatic entries",
			`{"items":[{"line":"huevos","query":"huevos","quantity":1},
				{"line":"huevos","query":"huevos frescos","quantity":1}]}`,
			`line "huevos": more than one automatic entry`,
		},
		{
			"pinned line with an unknown store",
			`{"items":[{"line":"huevos","store":"mercadona","id":"1","name":"Huevos","quantity":1}]}`,
			`store "mercadona"`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Decode(strings.NewReader(tt.in), sixStores)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want it to mention %q", err, tt.want)
			}
		})
	}
}

// An automatic entry with no line is its own line, named after its query, and a pinned
// entry keeps the line the user wrote.
func TestDecodeDefaultsTheLineToTheQuery(t *testing.T) {
	got, err := Decode(strings.NewReader(`{"items":[
		{"query":" leche entera ","quantity":1},
		{"line":" huevos ","store":"dia","id":"1","name":"Huevos","quantity":2}]}`), sixStores)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if got.Items[0].Line != "leche entera" {
		t.Errorf("items[0].Line = %q, want the query", got.Items[0].Line)
	}
	if got.Items[1].Line != "huevos" {
		t.Errorf("items[1].Line = %q, want the trimmed huevos", got.Items[1].Line)
	}
}

// A legacy basket carries no line key, so its document must stay exactly what it was: the
// line key appears only where a single query no longer describes the line.
func TestPriceOmitsTheLineForALegacyBasketAndShowsItOtherwise(t *testing.T) {
	legacy := req(Item{Query: "leche entera", Quantity: 1})
	dia, _ := chainFor("dia", map[string][]contract.Hit{
		"leche entera": {product("d1", "Leche entera Dia Láctea 1 L", 0.90)},
	})
	var buf bytes.Buffer
	if err := Encode(&buf, Price(context.Background(), legacy, []aggregate.Chain{dia}, DefaultCandidates, time.Second)); err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if strings.Contains(buf.String(), `"line"`) {
		t.Errorf("a basket without lines printed a line key:\n%s", buf.String())
	}

	pinned := req(
		Item{Line: "huevos", Store: "dia", ID: "305802", Name: eggDia, Quantity: 1},
		Item{Line: "huevos", Store: "mas", ID: "116867", Name: eggMas, Quantity: 1},
	)
	mas, _ := chainFor("mas", map[string][]contract.Hit{eggMas: {product("116867", eggMas, 1.80)}})
	buf.Reset()
	if err := Encode(&buf, Price(context.Background(), pinned, []aggregate.Chain{dia, mas}, DefaultCandidates, time.Second)); err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if !strings.Contains(buf.String(), `"line": "huevos"`) {
		t.Errorf("a two-chain line is not identified:\n%s", buf.String())
	}
}

// The candidate list of a line that already has a pinned choice shows that product first,
// marked as the current one, followed by the other proposals of the same chain.
func TestCandidatesMarksThePinnedChoiceAsCurrent(t *testing.T) {
	items := req(
		Item{Line: "huevos", Query: "huevos", Quantity: 2},
		Item{Line: "huevos", Store: "dia", ID: "305802", Name: eggDia, Quantity: 2},
	)
	dia, _ := chainFor("dia", map[string][]contract.Hit{
		eggDia: {
			product("770011", "Huevos de chocolate rellenos", 2.99),
			product("305802", eggDia, 2.20),
			product("999", "Cuecehuevos SILVERCREST", 11.99),
		},
	})
	mas, _ := chainFor("mas", map[string][]contract.Hit{
		"huevos": {product("m3", "Huevos frescos 12 ud", 2.05)},
	})

	res := Candidates(context.Background(), items, []aggregate.Chain{dia, mas}, DefaultProposals, time.Second)

	if len(res.Items) != 1 {
		t.Fatalf("items = %+v, want one line", res.Items)
	}
	line := res.Items[0]
	if line.Line != "huevos" || line.Query != "huevos" || line.Quantity != 2 {
		t.Fatalf("item = %+v, want the huevos line with its query and quantity", line)
	}
	diaCands := line.Candidates["dia"]
	if len(diaCands) != DefaultProposals {
		t.Fatalf("dia = %+v, want %d candidates", diaCands, DefaultProposals)
	}
	if diaCands[0].ID != "305802" || !diaCands[0].Current {
		t.Errorf("dia[0] = %+v, want the pinned 305802 marked current", diaCands[0])
	}
	for _, c := range diaCands[1:] {
		if c.ID == "305802" || c.Current {
			t.Errorf("candidate %+v repeats the current choice", c)
		}
	}
	if got := line.Candidates["mas"]; len(got) != 1 || got[0].Current {
		t.Errorf("mas = %+v, want a plain proposal: the line has no pin there", got)
	}
}
