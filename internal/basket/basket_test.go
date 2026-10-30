package basket

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"multisuper/internal/aggregate"
	"multisuper/internal/contract"
)

// stub is one chain client answering from memory: the suite never touches a network.
type stub struct {
	mu     sync.Mutex
	hits   map[string][]contract.Hit
	err    error
	terms  []string
	limits []int
}

func (s *stub) Search(_ context.Context, term string, limit int) (contract.Result, error) {
	s.mu.Lock()
	s.terms = append(s.terms, term)
	s.limits = append(s.limits, limit)
	s.mu.Unlock()
	if s.err != nil {
		return contract.Result{}, s.err
	}
	return contract.Result{Store: "stub", Query: term, Hits: s.hits[term]}, nil
}

func (s *stub) calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.terms)
}

func product(id, name string, price float64) contract.Hit {
	return contract.Hit{ID: id, Name: name, Price: ptr(price)}
}

// chainFor is one chain whose stub answers with the given hits per search term.
func chainFor(store string, hits map[string][]contract.Hit) (aggregate.Chain, *stub) {
	s := &stub{hits: hits}
	return aggregate.Chain{Store: store, Searcher: s}, s
}

func req(items ...Item) Request { return Request{Items: items} }

func chainOf(t *testing.T, res Result, store string) ChainResult {
	t.Helper()
	for _, c := range res.Chains {
		if c.Store == store {
			return c
		}
	}
	t.Fatalf("no chain entry for %q in %+v", store, res.Chains)
	return ChainResult{}
}

func itemOf(t *testing.T, res Result, query string) ItemResult {
	t.Helper()
	for _, it := range res.Items {
		if it.Query == query {
			return it
		}
	}
	t.Fatalf("no item entry for %q in %+v", query, res.Items)
	return ItemResult{}
}

// Three items, exact cents: quantity x unit price, added as integers.
func TestPriceTotalsAreExactInCents(t *testing.T) {
	req := req(
		Item{Query: "leche entera", Quantity: 2},
		Item{Query: "pan de molde", Quantity: 1},
		Item{Query: "huevos", Quantity: 2},
	)
	dia, _ := chainFor("dia", map[string][]contract.Hit{
		"leche entera": {product("d1", "Leche entera Dia Láctea pack 6 x 1 L", 2.89)},
		"pan de molde": {product("d2", "Pan de molde blanco 460 g", 1.50)},
		"huevos":       {product("d3", "Huevos frescos clase M 12 ud", 1.99)},
	})
	mas, _ := chainFor("mas", map[string][]contract.Hit{
		"leche entera": {product("m1", "Leche entera Pascual 1 L", 3.10)},
		"pan de molde": {product("m2", "Pan de molde integral 500 g", 1.25)},
		"huevos":       {product("m3", "Huevos frescos 12 ud", 2.05)},
	})

	res := Price(context.Background(), req,
		[]aggregate.Chain{mas, dia}, DefaultCandidates, time.Second)

	// dia: 2x289 + 1x150 + 2x199 = 578 + 150 + 398 = 1126
	if got := chainOf(t, res, "dia"); got.TotalCents != 1126 || got.Coverage != "3/3" {
		t.Errorf("dia = %+v, want 1126 cents and 3/3", got)
	}
	// mas: 2x310 + 125 + 2x205 = 620 + 125 + 410 = 1155
	if got := chainOf(t, res, "mas"); got.TotalCents != 1155 || got.Coverage != "3/3" {
		t.Errorf("mas = %+v, want 1155 cents and 3/3", got)
	}
	leche := itemOf(t, res, "leche entera")
	if got := leche.Resolved["dia"]; got.UnitCents != 289 || got.LineCents != 578 {
		t.Errorf("dia leche = %+v, want 289 cents unit and 578 line", got)
	}
	// ranking: cheapest full-coverage chain first.
	if len(res.Ranking) != 2 || res.Ranking[0].Store != "dia" || res.Ranking[0].TotalCents != 1126 {
		t.Fatalf("ranking = %+v, want dia 1126 first", res.Ranking)
	}
}

// The classic float trap: 0.1 + 0.2 must be 30 cents exactly, not 0.30000000000000004 €.
func TestPriceAddsDecimalsWithoutFloatDrift(t *testing.T) {
	req := req(Item{Query: "pan", Quantity: 1}, Item{Query: "huevos", Quantity: 1})
	dia, _ := chainFor("dia", map[string][]contract.Hit{
		"pan":    {product("p", "Pan de molde blanco", 0.1)},
		"huevos": {product("h", "Huevos frescos clase M", 0.2)},
	})
	res := Price(context.Background(), req, []aggregate.Chain{dia}, DefaultCandidates, time.Second)
	if got := chainOf(t, res, "dia").TotalCents; got != 30 {
		t.Fatalf("total = %d cents, want 30", got)
	}
}

// A chain that cannot cover everything is listed with what it lacks, and never ranked.
func TestPriceExcludesAChainMissingAnItemFromTheRanking(t *testing.T) {
	req := req(Item{Query: "leche entera", Quantity: 1}, Item{Query: "pan de molde", Quantity: 1})
	dia, _ := chainFor("dia", map[string][]contract.Hit{
		"leche entera": {product("d1", "Leche entera Dia Láctea 1 L", 2.89)},
		"pan de molde": {product("d2", "Pan de molde blanco 460 g", 1.50)},
	})
	mas, _ := chainFor("mas", map[string][]contract.Hit{
		"leche entera": {product("m1", "Leche entera Pascual 1 L", 1.00)},
	})

	res := Price(context.Background(), req, []aggregate.Chain{dia, mas}, DefaultCandidates, time.Second)

	if len(res.Ranking) != 1 || res.Ranking[0].Store != "dia" {
		t.Fatalf("ranking = %+v, want only dia", res.Ranking)
	}
	got := chainOf(t, res, "mas")
	if got.Coverage != "1/2" || len(got.Missing) != 1 || got.Missing[0] != "pan de molde" {
		t.Fatalf("mas = %+v, want 1/2 missing pan de molde", got)
	}
	if got.TotalCents != 100 {
		t.Errorf("mas total = %d, want the 100 cents it can price", got.TotalCents)
	}
	pan := itemOf(t, res, "pan de molde")
	if len(pan.Missing) != 1 || pan.Missing[0] != "mas" {
		t.Errorf("pan missing = %v, want [mas]", pan.Missing)
	}
}

// A weak candidate is not a match: the item is reported missing instead of being tied
// to whatever product happened to be in the first ten results.
func TestPriceReportsAWeakCandidateAsMissing(t *testing.T) {
	req := req(Item{Query: "leche entera", Quantity: 1})
	dia, _ := chainFor("dia", map[string][]contract.Hit{
		"leche entera": {
			product("c", "Chocolate leche LINDT 100gr", 2.19),
			product("k", "Leche condensada LA LECHERA 370 gr", 1.85),
		},
	})
	res := Price(context.Background(), req, []aggregate.Chain{dia}, DefaultCandidates, time.Second)

	got := chainOf(t, res, "dia")
	if got.Coverage != "0/1" || len(got.Missing) != 1 || got.Missing[0] != "leche entera" {
		t.Fatalf("dia = %+v, want 0/1 missing leche entera", got)
	}
	if len(itemOf(t, res, "leche entera").Resolved) != 0 {
		t.Error("a below-threshold candidate was resolved to the item")
	}
	if len(res.Ranking) != 0 {
		t.Errorf("ranking = %+v, want empty when nothing is covered", res.Ranking)
	}
}

// A failed chain is an error, not a chain that is missing everything: it keeps no
// partial totals and does not touch the other chains' arithmetic.
func TestPriceKeepsOtherTotalsWhenAChainFails(t *testing.T) {
	req := req(Item{Query: "leche entera", Quantity: 2})
	dia, _ := chainFor("dia", map[string][]contract.Hit{
		"leche entera": {product("d1", "Leche entera Dia Láctea 1 L", 2.89)},
	})
	bad := &stub{err: &aggregate.StatusError{Status: 429, Msg: "mas: rate limit"}}
	chains := []aggregate.Chain{dia, {Store: "mas", Searcher: bad}}

	res := Price(context.Background(), req, chains, DefaultCandidates, time.Second)

	mas := chainOf(t, res, "mas")
	if mas.Error == nil || mas.Error.Code != aggregate.CodeQuota || !mas.Error.Retryable {
		t.Fatalf("mas = %+v, want a retryable quota error", mas)
	}
	if mas.TotalCents != 0 || mas.Coverage != "" || len(mas.Missing) != 0 {
		t.Errorf("mas = %+v, want no total, no coverage, no missing", mas)
	}
	if got := chainOf(t, res, "dia"); got.TotalCents != 578 || got.Error != nil {
		t.Errorf("dia = %+v, want 578 cents and no error", got)
	}
	leche := itemOf(t, res, "leche entera")
	if _, ok := leche.Resolved["mas"]; ok {
		t.Error("a failed chain resolved an item")
	}
	// A failed chain is not a chain where the item is missing.
	if len(leche.Missing) != 0 {
		t.Errorf("leche missing = %v, want empty: mas failed, it did not lack the item", leche.Missing)
	}
}

// The search asks each chain for a bounded number of candidates, never for a whole page.
func TestPriceRequestsABoundedNumberOfCandidates(t *testing.T) {
	req := req(Item{Query: "leche entera", Quantity: 1}, Item{Query: "huevos", Quantity: 1})
	dia, s := chainFor("dia", map[string][]contract.Hit{})
	res := Price(context.Background(), req, []aggregate.Chain{dia}, DefaultCandidates, time.Second)

	if s.calls() != 2 {
		t.Fatalf("search called %d times, want one per item", s.calls())
	}
	for i, limit := range s.limits {
		if limit != DefaultCandidates {
			t.Errorf("call %d used limit %d, want %d", i, limit, DefaultCandidates)
		}
	}
	if len(res.Items) != 2 || res.Chains[0].Coverage != "0/2" {
		t.Errorf("got %+v, want two missing items", res)
	}
}

// Equal totals keep the chain order, so the ranking is stable from one run to the next.
func TestRankingIsStableOnATie(t *testing.T) {
	req := req(Item{Query: "leche entera", Quantity: 1})
	dia, _ := chainFor("dia", map[string][]contract.Hit{
		"leche entera": {product("d", "Leche entera Dia Láctea 1 L", 1.00)},
	})
	mas, _ := chainFor("mas", map[string][]contract.Hit{
		"leche entera": {product("m", "Leche entera Pascual 1 L", 1.00)},
	})
	res := Price(context.Background(), req, []aggregate.Chain{dia, mas}, DefaultCandidates, time.Second)
	if len(res.Ranking) != 2 || res.Ranking[0].Store != "dia" || res.Ranking[1].Store != "mas" {
		t.Fatalf("ranking = %+v, want dia then mas", res.Ranking)
	}
}

// Validation: the shape of a usable basket, and one clear error per broken input.
func TestDecodeRejectsInvalidBaskets(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"empty basket", `{"items":[]}`, "basket has no items"},
		{"no items key", `{}`, "basket has no items"},
		{"empty query", `{"items":[{"query":"  ","quantity":1}]}`, "query is empty"},
		{"zero quantity", `{"items":[{"query":"leche","quantity":0}]}`, "quantity must be"},
		{"negative quantity", `{"items":[{"query":"leche","quantity":-2}]}`, "quantity must be"},
		{"fractional quantity", `{"items":[{"query":"leche","quantity":1.5}]}`, "basket is not valid JSON"},
		{"unknown field", `{"items":[{"query":"leche","quantity":1,"color":"azul"}]}`, "basket is not valid JSON"},
		{"not json", `leche`, "basket is not valid JSON"},
		{"trailing document", `{"items":[{"query":"leche","quantity":1}]}{"items":[]}`, "more than one JSON document"},
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

func TestDecodeAcceptsAValidBasket(t *testing.T) {
	got, err := Decode(strings.NewReader(`{"items":[{"query":" leche entera ","quantity":2}]}`), sixStores)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if len(got.Items) != 1 || got.Items[0].Query != "leche entera" || got.Items[0].Quantity != 2 {
		t.Fatalf("got %+v, want a trimmed leche entera x2", got)
	}
}

// The JSON the web app consumes: one document, the documented keys, cents as integers.
func TestEncodeWritesOneDocumentWithIntegerCents(t *testing.T) {
	res := Result{
		Items: []ItemResult{{
			Query: "leche entera", Quantity: 2,
			Resolved: map[string]Resolved{"mas": {ID: "1", Name: "Leche entera Pascual 1 L", UnitCents: 289, LineCents: 578}},
			Missing:  []string{"carrefour"},
			Stale:    []string{},
		}},
		Chains: []ChainResult{
			{Store: "mas", TotalCents: 578, Coverage: "1/1", Missing: []string{}, Stale: []string{}},
			{Store: "carrefour", Error: &aggregate.StoreError{Code: aggregate.CodeQuota, Retryable: true, Message: "quota"}},
		},
		Ranking: []Rank{{Store: "mas", TotalCents: 578}},
	}
	var buf bytes.Buffer
	if err := Encode(&buf, res); err != nil {
		t.Fatalf("Encode: %v", err)
	}

	dec := json.NewDecoder(bytes.NewReader(buf.Bytes()))
	dec.DisallowUnknownFields()
	var back Result
	if err := dec.Decode(&back); err != nil {
		t.Fatalf("output is not the documented document: %v\n%s", err, buf.String())
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		t.Fatalf("stdout carries more than one JSON document (%v):\n%s", err, buf.String())
	}
	for _, key := range []string{`"items"`, `"chains"`, `"ranking"`, `"query"`, `"quantity"`,
		`"resolved"`, `"missing"`, `"stale"`, `"id"`, `"name"`, `"unitCents"`, `"lineCents"`,
		`"store"`, `"totalCents"`, `"coverage"`, `"elapsedMs"`, `"error"`} {
		if !strings.Contains(buf.String(), key) {
			t.Errorf("encoded result is missing %s:\n%s", key, buf.String())
		}
	}
	if !strings.Contains(buf.String(), `"lineCents": 578`) {
		t.Errorf("cents are not integers:\n%s", buf.String())
	}
	if back.Items[0].Resolved["mas"].LineCents != 578 {
		t.Errorf("round trip lost the line total: %+v", back.Items[0].Resolved["mas"])
	}
}

// An empty ranking is [] for the web app, never null.
func TestEncodeWritesAnEmptyRankingAsAnArray(t *testing.T) {
	var buf bytes.Buffer
	res := Price(context.Background(), req(Item{Query: "leche entera", Quantity: 1}),
		[]aggregate.Chain{{Store: "dia", Searcher: &stub{hits: map[string][]contract.Hit{}}}},
		DefaultCandidates, time.Second)
	if err := Encode(&buf, res); err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if !strings.Contains(buf.String(), `"ranking": []`) {
		t.Errorf("empty ranking is not an array:\n%s", buf.String())
	}
	if !strings.Contains(buf.String(), `"resolved": {}`) {
		t.Errorf("empty resolved is not an object:\n%s", buf.String())
	}
}

// Classify must survive a chain client that wraps its error, so one failing chain is a
// code and not a crash.
func TestPriceClassifiesAWrappedChainError(t *testing.T) {
	req := req(Item{Query: "leche entera", Quantity: 1})
	bad := &stub{err: errors.New("dial tcp 1.2.3.4:443: connect: connection refused")}
	res := Price(context.Background(), req, []aggregate.Chain{{Store: "lidl", Searcher: bad}}, DefaultCandidates, time.Second)
	if got := chainOf(t, res, "lidl"); got.Error == nil || got.Error.Code != aggregate.CodeNetwork {
		t.Fatalf("lidl = %+v, want a network error", got)
	}
}
