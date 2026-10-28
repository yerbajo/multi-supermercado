package main

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"multisuper/internal/aggregate"
	"multisuper/internal/basket"
	"multisuper/internal/contract"
)

// basketStub is one chain client answering from memory, so no test reaches the network.
type basketStub struct {
	mu     sync.Mutex
	hits   map[string][]contract.Hit
	err    error
	terms  []string
	limits []int
}

func (s *basketStub) Search(_ context.Context, term string, limit int) (contract.Result, error) {
	s.mu.Lock()
	s.terms = append(s.terms, term)
	s.limits = append(s.limits, limit)
	s.mu.Unlock()
	if s.err != nil {
		return contract.Result{}, s.err
	}
	return contract.Result{Store: "stub", Query: term, Hits: s.hits[term]}, nil
}

func (s *basketStub) calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.terms)
}

func offer(id, name string, price float64) contract.Hit {
	return contract.Hit{ID: id, Name: name, Price: pricePtr(price)}
}

func pricePtr(f float64) *float64 { return &f }

// basketChains builds the six named chains from a per-store hit table; a store that is
// absent answers with no hits at all.
func basketChains(hits map[string]map[string][]contract.Hit) ([]aggregate.Chain, map[string]*basketStub) {
	stubs := make(map[string]*basketStub, len(storeNames))
	out := make([]aggregate.Chain, 0, len(storeNames))
	for _, name := range storeNames {
		h := hits[name]
		if h == nil {
			h = map[string][]contract.Hit{}
		}
		s := &basketStub{hits: h}
		stubs[name] = s
		out = append(out, aggregate.Chain{Store: name, Searcher: s})
	}
	return out, stubs
}

// writeBasket puts a basket file in the test's own temp dir and returns its path.
func writeBasket(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "basket.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write basket: %v", err)
	}
	return path
}

// decodeBasket fails unless stdout holds exactly one documented JSON document.
func decodeBasket(t *testing.T, stdout string) basket.Result {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(stdout))
	dec.DisallowUnknownFields()
	var res basket.Result
	if err := dec.Decode(&res); err != nil {
		t.Fatalf("stdout is not the basket JSON document: %v\n%s", err, stdout)
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		t.Fatalf("stdout carries more than one JSON document (%v):\n%s", err, stdout)
	}
	return res
}

func chainEntry(t *testing.T, res basket.Result, store string) basket.ChainResult {
	t.Helper()
	for _, c := range res.Chains {
		if c.Store == store {
			return c
		}
	}
	t.Fatalf("no chain entry for %q in %+v", store, res.Chains)
	return basket.ChainResult{}
}

const threeItemBasket = `{"items":[
	{"query":"leche entera","quantity":2},
	{"query":"pan de molde","quantity":1},
	{"query":"huevos","quantity":3}]}`

// fullHits is one answer per basket item, from a single chain's catalog.
func fullHits(prefix string, scale float64) map[string][]contract.Hit {
	return map[string][]contract.Hit{
		"leche entera": {offer(prefix+"1", "Leche entera "+prefix+" 1 L", 1.00*scale)},
		"pan de molde": {offer(prefix+"2", "Pan de molde blanco 460 g", 0.20*scale)},
		"huevos":       {offer(prefix+"3", "Huevos frescos clase M 12 ud", 0.10*scale)},
	}
}

// Every chain covering the basket: integer cents, one row per chain, cheapest first.
func TestBasketJSONPricesEveryChainInCents(t *testing.T) {
	all, _ := basketChains(map[string]map[string][]contract.Hit{
		"dia": fullHits("d", 1),
		"mas": fullHits("m", 2),
	})
	path := writeBasket(t, threeItemBasket)

	var stdout, stderr strings.Builder
	code := runBasket([]string{"-f", path, "--json"}, &stdout, &stderr, all, strings.NewReader(""))
	if code != 0 {
		t.Fatalf("exit = %d, want 0: %s", code, stderr.String())
	}
	res := decodeBasket(t, stdout.String())

	// dia: 2x100 + 1x20 + 3x10 = 250; mas doubles that: 500.
	if got := chainEntry(t, res, "dia"); got.TotalCents != 250 || got.Coverage != "3/3" {
		t.Errorf("dia = %+v, want 250 cents and 3/3", got)
	}
	if got := chainEntry(t, res, "mas"); got.TotalCents != 500 || got.Coverage != "3/3" {
		t.Errorf("mas = %+v, want 500 cents and 3/3", got)
	}
	if len(res.Ranking) != 2 || res.Ranking[0].Store != "dia" || res.Ranking[0].TotalCents != 250 || res.Ranking[1].Store != "mas" {
		t.Fatalf("ranking = %+v, want dia 250 then mas 500", res.Ranking)
	}
	if got := res.Items[0].Resolved["dia"]; got.UnitCents != 100 || got.LineCents != 200 {
		t.Errorf("leche dia = %+v, want 100 unit and 200 line", got)
	}
}

// A chain that lacks an item is still reported, with what it lacks, but it is never
// ranked: a cart you cannot fill is not a cheaper cart.
func TestBasketKeepsAMissingChainOutOfTheRanking(t *testing.T) {
	incomplete := fullHits("x", 1)
	delete(incomplete, "pan de molde")
	all, _ := basketChains(map[string]map[string][]contract.Hit{
		"dia": fullHits("d", 3),
		"mas": incomplete,
	})
	path := writeBasket(t, threeItemBasket)

	var stdout, stderr strings.Builder
	code := runBasket([]string{"-f", path, "--only", "dia,mas", "--json"}, &stdout, &stderr, all, strings.NewReader(""))
	if code != 0 {
		t.Fatalf("exit = %d, want 0 while dia covers the basket: %s", code, stderr.String())
	}
	res := decodeBasket(t, stdout.String())

	if len(res.Ranking) != 1 || res.Ranking[0].Store != "dia" {
		t.Fatalf("ranking = %+v, want only dia", res.Ranking)
	}
	mas := chainEntry(t, res, "mas")
	if mas.Coverage != "2/3" || len(mas.Missing) != 1 || mas.Missing[0] != "pan de molde" {
		t.Fatalf("mas = %+v, want 2/3 missing pan de molde", mas)
	}
	if items := res.Items[1]; len(items.Missing) != 1 || items.Missing[0] != "mas" {
		t.Errorf("pan missing = %v, want [mas]", items.Missing)
	}
}

// A wrong product is worse than no product: the chocolate is not «leche entera».
func TestBasketNeverMatchesChocolateForLecheEntera(t *testing.T) {
	all, _ := basketChains(map[string]map[string][]contract.Hit{
		"dia": {"leche entera": {offer("c", "Chocolate leche LINDT 100gr", 2.19)}},
	})
	path := writeBasket(t, `{"items":[{"query":"leche entera","quantity":1}]}`)

	var stdout, stderr strings.Builder
	code := runBasket([]string{"-f", path, "--json"}, &stdout, &stderr, all, strings.NewReader(""))
	if code != 1 {
		t.Fatalf("exit = %d, want 1 when no chain covers the basket", code)
	}
	res := decodeBasket(t, stdout.String())
	if len(res.Items[0].Resolved) != 0 {
		t.Fatalf("resolved = %+v, want nothing", res.Items[0].Resolved)
	}
	if len(res.Ranking) != 0 {
		t.Fatalf("ranking = %+v, want empty", res.Ranking)
	}
	if got := chainEntry(t, res, "dia"); got.Missing[0] != "leche entera" {
		t.Errorf("dia missing = %v, want leche entera", got.Missing)
	}
}

// A failed chain is reported with its stable code and leaves the other totals untouched.
func TestBasketReportsAChainErrorAndKeepsTheOthers(t *testing.T) {
	all, stubs := basketChains(map[string]map[string][]contract.Hit{
		"dia":  fullHits("d", 1),
		"lidl": fullHits("l", 1),
	})
	stubs["carrefour"].err = &aggregate.StatusError{Status: 429, Msg: "carrefour: rate limit"}

	path := writeBasket(t, threeItemBasket)
	var stdout, stderr strings.Builder
	code := runBasket([]string{"-f", path, "--json"}, &stdout, &stderr, all, strings.NewReader(""))
	if code != 0 {
		t.Fatalf("exit = %d, want 0: %s", code, stderr.String())
	}
	res := decodeBasket(t, stdout.String())

	carrefour := chainEntry(t, res, "carrefour")
	if carrefour.Error == nil || carrefour.Error.Code != aggregate.CodeQuota || !carrefour.Error.Retryable {
		t.Fatalf("carrefour = %+v, want a retryable quota error", carrefour)
	}
	if carrefour.TotalCents != 0 {
		t.Errorf("carrefour total = %d, want 0 on error", carrefour.TotalCents)
	}
	for _, name := range []string{"dia", "lidl"} {
		if got := chainEntry(t, res, name); got.TotalCents != 250 || got.Error != nil {
			t.Errorf("%s = %+v, want 250 cents and no error", name, got)
		}
	}
	if len(res.Ranking) != 2 {
		t.Errorf("ranking = %+v, want the two chains that answered", res.Ranking)
	}
}

// Invalid baskets and bad flags are usage errors: exit 2, stderr, and no chain asked.
func TestBasketRejectsBadInput(t *testing.T) {
	tests := []struct {
		name  string
		body  string
		args  []string
		match string
	}{
		{"empty basket", `{"items":[]}`, nil, "basket has no items"},
		{"zero quantity", `{"items":[{"query":"leche","quantity":0}]}`, nil, "quantity must be"},
		{"empty query", `{"items":[{"query":"","quantity":1}]}`, nil, "query is empty"},
		{"not json", `leche`, nil, "basket is not valid JSON"},
		{"missing -f", "", []string{"--json"}, "missing -f"},
		{"unknown flag", "", []string{"-f", "-", "--bogus"}, "flag provided but not defined"},
		{"limit zero", "", []string{"-f", "-", "--limit", "0"}, "--limit must be between 1 and 100"},
		{"limit not a number", "", []string{"-f", "-", "--limit", "muchos"}, "invalid value"},
		{"zero timeout", "", []string{"-f", "-", "--timeout", "0s"}, "--timeout must be greater than 0"},
		{"unknown chain", "", []string{"-f", "-", "--only", "mercadona"}, "unknown store"},
		{"stray argument", "", []string{"-f", "-", "leche"}, "unexpected argument"},
		{"missing file", "", []string{"-f", "/nonexistent/basket.json"}, "no such file"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			all, stubs := basketChains(nil)
			args := tt.args
			if tt.body != "" {
				args = append([]string{"-f", writeBasket(t, tt.body), "--json"}, args...)
			}
			var stdout, stderr strings.Builder
			stdin := strings.NewReader(`{"items":[{"query":"leche","quantity":1}]}`)
			if code := runBasket(args, &stdout, &stderr, all, stdin); code != 2 {
				t.Fatalf("exit = %d, want 2", code)
			}
			if !strings.Contains(stderr.String(), tt.match) {
				t.Errorf("stderr = %q, want it to mention %q", stderr.String(), tt.match)
			}
			if strings.TrimSpace(stdout.String()) != "" {
				t.Errorf("usage errors must not write to stdout: %q", stdout.String())
			}
			for name, s := range stubs {
				if got := s.calls(); got != 0 {
					t.Errorf("%s was queried on a usage error (%d calls)", name, got)
				}
			}
		})
	}
}

// -f - reads the same basket from stdin and prints the same document as the file form.
func TestBasketStdinMatchesTheFile(t *testing.T) {
	all, _ := basketChains(map[string]map[string][]contract.Hit{"dia": fullHits("d", 1)})

	var fromFile, fromStdin, stderr strings.Builder
	if code := runBasket([]string{"-f", writeBasket(t, threeItemBasket), "--json"}, &fromFile, &stderr, all, strings.NewReader("")); code != 0 {
		t.Fatalf("file exit = %d: %s", code, stderr.String())
	}
	stderr.Reset()
	if code := runBasket([]string{"-f", "-", "--json"}, &fromStdin, &stderr, all, strings.NewReader(threeItemBasket)); code != 0 {
		t.Fatalf("stdin exit = %d: %s", code, stderr.String())
	}
	if fromFile.String() != fromStdin.String() {
		t.Fatalf("stdin and file output differ\nfile:\n%s\nstdin:\n%s", fromFile.String(), fromStdin.String())
	}
	if !strings.Contains(fromStdin.String(), `"totalCents": 250`) {
		t.Errorf("stdin basket was not priced:\n%s", fromStdin.String())
	}
}

// The table: one row per chain (store, state, coverage, total, missing count, ms), the
// missing items per chain, and the cheapest chain that covers the whole basket.
func TestBasketTablePrintsRowsAndMissingItems(t *testing.T) {
	incomplete := fullHits("x", 1)
	delete(incomplete, "huevos")
	all, _ := basketChains(map[string]map[string][]contract.Hit{
		"dia": fullHits("d", 1),
		"mas": incomplete,
	})
	path := writeBasket(t, threeItemBasket)

	var stdout, stderr strings.Builder
	if code := runBasket([]string{"-f", path, "--only", "dia,mas"}, &stdout, &stderr, all, strings.NewReader("")); code != 0 {
		t.Fatalf("exit = %d, want 0: %s", code, stderr.String())
	}
	out := stdout.String()
	header := strings.Split(strings.TrimSpace(out), "\n")[0]
	for _, want := range []string{"STORE", "ESTADO", "COBERTURA", "TOTAL", "FALTAN", "MS"} {
		if !strings.Contains(header, want) {
			t.Errorf("header %q is missing %q", header, want)
		}
	}
	if !strings.Contains(out, "2.50 €") {
		t.Errorf("table does not print the total in €:\n%s", out)
	}
	if !strings.Contains(out, "faltan en mas: huevos") {
		t.Errorf("table does not list the missing item per chain:\n%s", out)
	}
	if !strings.Contains(out, "dia 2.50 €") {
		t.Errorf("table does not name the cheapest complete chain:\n%s", out)
	}
}

// A chain that failed shows its code and no total: 0,00 € would read as a free basket.
func TestBasketTablePrintsDashForAFailedChain(t *testing.T) {
	all, stubs := basketChains(map[string]map[string][]contract.Hit{"dia": fullHits("d", 1)})
	stubs["carrefour"].err = &aggregate.StatusError{Status: 429, Msg: "carrefour: rate limit"}
	path := writeBasket(t, threeItemBasket)

	var stdout, stderr strings.Builder
	if code := runBasket([]string{"-f", path, "--only", "carrefour,dia"}, &stdout, &stderr, all, strings.NewReader("")); code != 0 {
		t.Fatalf("exit = %d, want 0: %s", code, stderr.String())
	}
	for _, row := range strings.Split(strings.TrimSpace(stdout.String()), "\n") {
		if strings.HasPrefix(row, "carrefour") {
			if strings.Contains(row, "0.00 €") {
				t.Errorf("failed chain shows 0,00 €: %q", row)
			}
			if !strings.Contains(row, aggregate.CodeQuota) {
				t.Errorf("failed chain does not show its code: %q", row)
			}
			return
		}
	}
	t.Fatalf("no carrefour row:\n%s", stdout.String())
}

// Nothing covers the basket: that is a real answer with exit 1, not a crash.
func TestBasketTableSaysWhenNothingIsComparable(t *testing.T) {
	all, _ := basketChains(nil)
	path := writeBasket(t, threeItemBasket)

	var stdout, stderr strings.Builder
	if code := runBasket([]string{"-f", path}, &stdout, &stderr, all, strings.NewReader("")); code != 1 {
		t.Fatalf("exit = %d, want 1: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "ninguna cadena cubre la cesta completa") {
		t.Errorf("table does not say the basket is not comparable:\n%s", stdout.String())
	}
}

// --only queries exactly the named chains, in table order, and each item once per chain.
func TestBasketOnlyQueriesTheRequestedChains(t *testing.T) {
	all, stubs := basketChains(map[string]map[string][]contract.Hit{"dia": fullHits("d", 1)})
	path := writeBasket(t, threeItemBasket)

	var stdout, stderr strings.Builder
	if code := runBasket([]string{"-f", path, "--only", "mas,dia", "--json"}, &stdout, &stderr, all, strings.NewReader("")); code != 0 {
		t.Fatalf("exit = %d, want 0: %s", code, stderr.String())
	}
	res := decodeBasket(t, stdout.String())
	if len(res.Chains) != 2 || res.Chains[0].Store != "dia" || res.Chains[1].Store != "mas" {
		t.Fatalf("chains = %+v, want dia then mas", res.Chains)
	}
	for name, s := range stubs {
		want := 0
		if name == "dia" || name == "mas" {
			want = 3 // one search per basket item
		}
		if got := s.calls(); got != want {
			t.Errorf("%s was called %d times, want %d", name, got, want)
		}
		for _, limit := range s.limits {
			if limit != basket.DefaultCandidates {
				t.Errorf("%s searched with limit %d, want the bounded %d", name, limit, basket.DefaultCandidates)
			}
		}
	}
}

// Asking for help is not an error, in any position, and goes to stdout.
func TestBasketHelpExitsZero(t *testing.T) {
	for _, args := range [][]string{{"basket", "--help"}, {"basket", "-h"}, {"--help"}, {"basket", "-f", "-", "--help"}} {
		all, _ := basketChains(nil)
		var stdout, stderr strings.Builder
		if code := run(args, &stdout, &stderr, all); code != 0 {
			t.Errorf("%v: exit = %d, want 0", args, code)
		}
		if !strings.Contains(stdout.String(), "multisuper basket -f") {
			t.Errorf("%v: help does not show the usage line:\n%s", args, stdout.String())
		}
	}
}

// decodeCandidates fails unless stdout holds exactly one documented candidates document.
func decodeCandidates(t *testing.T, stdout string) basket.CandidatesResult {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(stdout))
	dec.DisallowUnknownFields()
	var res basket.CandidatesResult
	if err := dec.Decode(&res); err != nil {
		t.Fatalf("stdout is not the candidates JSON document: %v\n%s", err, stdout)
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		t.Fatalf("stdout carries more than one JSON document (%v):\n%s", err, stdout)
	}
	return res
}

// The point of the candidate mode: the user sees the real eggs and the chocolate ones
// side by side, scored and priced, and picks (L7).
func TestBasketCandidatesJSONListsScoredCandidates(t *testing.T) {
	all, stubs := basketChains(map[string]map[string][]contract.Hit{
		"dia": {"huevos": {
			offer("305802", "Huevos frescos clase M 12 ud", 2.20),
			offer("770011", "Huevos de chocolate rellenos", 2.99),
			offer("999", "Cuecehuevos SILVERCREST", 11.99),
		}},
		"mas": {"huevos": {offer("m3", "Huevos frescos 12 ud", 2.05)}},
	})
	path := writeBasket(t, `{"items":[{"query":"huevos","quantity":2}]}`)

	var stdout, stderr strings.Builder
	code := runBasket([]string{"candidates", "-f", path, "--only", "dia,mas", "--candidates", "2", "--json"},
		&stdout, &stderr, all, strings.NewReader(""))
	if code != 0 {
		t.Fatalf("exit = %d, want 0: %s", code, stderr.String())
	}
	res := decodeCandidates(t, stdout.String())

	if len(res.Items) != 1 || res.Items[0].Query != "huevos" || res.Items[0].Quantity != 2 {
		t.Fatalf("items = %+v, want one huevos x2", res.Items)
	}
	dia := res.Items[0].Candidates["dia"]
	if len(dia) != 2 {
		t.Fatalf("dia candidates = %+v, want 2", dia)
	}
	if dia[0].ID != "305802" || dia[0].UnitCents != 220 || dia[0].Score != 1 {
		t.Errorf("dia[0] = %+v, want the real eggs at 220 cents scored 1", dia[0])
	}
	if dia[1].ID != "770011" || dia[1].Name != "Huevos de chocolate rellenos" {
		t.Errorf("dia[1] = %+v, want the chocolate eggs as a separate candidate", dia[1])
	}
	if got := res.Items[0].Candidates["mas"]; len(got) != 1 || got[0].ID != "m3" {
		t.Errorf("mas candidates = %+v, want m3", got)
	}
	if len(res.Chains) != 2 || res.Chains[0].Store != "dia" || res.Chains[1].Store != "mas" {
		t.Errorf("chains = %+v, want dia then mas", res.Chains)
	}
	// One search per chain, the same bounded page the basket asks for: N is a cut of the
	// answer, not a bigger request.
	for _, name := range []string{"dia", "mas"} {
		if got := stubs[name].limits; len(got) != 1 || got[0] != basket.DefaultCandidates {
			t.Errorf("%s searched with limits %v, want %d", name, got, basket.DefaultCandidates)
		}
	}
}

// --candidates is bounded like every other limit, and a pinned entry has no query to
// search: both are usage errors, never a silent change of meaning.
func TestBasketCandidatesRejectsBadInput(t *testing.T) {
	tests := []struct {
		name  string
		body  string
		args  []string
		match string
	}{
		{"zero candidates", `{"items":[{"query":"huevos","quantity":1}]}`,
			[]string{"--candidates", "0"}, "--candidates must be between 1 and 10"},
		{"too many candidates", `{"items":[{"query":"huevos","quantity":1}]}`,
			[]string{"--candidates", "11"}, "--candidates must be between 1 and 10"},
		{"not a number", `{"items":[{"query":"huevos","quantity":1}]}`,
			[]string{"--candidates", "muchos"}, "invalid value"},
		{"pinned entry", `{"items":[{"store":"dia","id":"1","name":"Huevos","quantity":1}]}`,
			nil, "candidates needs a query"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			all, stubs := basketChains(nil)
			args := append([]string{"candidates", "-f", writeBasket(t, tt.body), "--json"}, tt.args...)
			var stdout, stderr strings.Builder
			if code := runBasket(args, &stdout, &stderr, all, strings.NewReader("")); code != 2 {
				t.Fatalf("exit = %d, want 2: %s", code, stderr.String())
			}
			if !strings.Contains(stderr.String(), tt.match) {
				t.Errorf("stderr = %q, want it to mention %q", stderr.String(), tt.match)
			}
			if strings.TrimSpace(stdout.String()) != "" {
				t.Errorf("usage errors must not write to stdout: %q", stdout.String())
			}
			for name, s := range stubs {
				if got := s.calls(); got != 0 {
					t.Errorf("%s was queried on a usage error (%d calls)", name, got)
				}
			}
		})
	}
}

const pinnedEggName = "Huevos frescos clase M 12 ud"

// pinnedBasket mixes one automatic line with one line the user chose by id.
const pinnedBasket = `{"items":[
	{"query":"leche entera","quantity":1},
	{"store":"aldi","id":"h9","name":"` + pinnedEggName + `","quantity":2}]}`

// pinnedDiaBasket is the same list with the eggs chosen at DIA.
const pinnedDiaBasket = `{"items":[
	{"query":"leche entera","quantity":1},
	{"store":"dia","id":"h9","name":"` + pinnedEggName + `","quantity":2}]}`

// A pinned id the chain no longer publishes is stale: the chain leaves the ranking with
// the item in its stale list, and it never falls back to another product.
func TestBasketPinnedEntryThatIsGoneGoesStale(t *testing.T) {
	hits := map[string]map[string][]contract.Hit{
		"dia": {"leche entera": {offer("d1", "Leche entera Dia Láctea 1 L", 1.00)}},
		"mas": {"leche entera": {offer("m1", "Leche entera Pascual 1 L", 0.90)}},
		"aldi": {"leche entera": {offer("a1", "Leche entera Alipende 1 L", 1.10)},
			// Same name, another id: the shelf the user chose is not there today.
			pinnedEggName: {offer("h8", pinnedEggName, 1.50)}},
	}
	all, _ := basketChains(hits)
	path := writeBasket(t, pinnedBasket)

	var stdout, stderr strings.Builder
	code := runBasket([]string{"-f", path, "--only", "aldi,dia,mas", "--json"}, &stdout, &stderr, all, strings.NewReader(""))
	if code != 1 {
		t.Fatalf("exit = %d, want 1: no chain can serve the pinned choice: %s", code, stderr.String())
	}
	res := decodeBasket(t, stdout.String())

	eggs := res.Items[1]
	if len(eggs.Stale) != 1 || eggs.Stale[0] != "aldi" {
		t.Fatalf("item stale = %v, want [aldi]", eggs.Stale)
	}
	if _, ok := eggs.Resolved["aldi"]; ok {
		t.Errorf("aldi resolved = %+v, want the pinned item unresolved", eggs.Resolved)
	}
	if len(eggs.Missing) != 2 {
		t.Errorf("item missing = %v, want dia and mas", eggs.Missing)
	}
	aldi := chainEntry(t, res, "aldi")
	if len(aldi.Stale) != 1 || aldi.Stale[0] != pinnedEggName {
		t.Fatalf("aldi = %+v, want the pinned name stale", aldi)
	}
	if aldi.Error != nil || aldi.Coverage != "1/2" {
		t.Errorf("aldi = %+v, want no error and 1/2", aldi)
	}
	if len(res.Ranking) != 0 {
		t.Fatalf("ranking = %+v, want empty: a stale chain is never ranked", res.Ranking)
	}

	stdout.Reset()
	stderr.Reset()
	if code := runBasket([]string{"-f", path, "--only", "aldi,dia,mas"}, &stdout, &stderr, all, strings.NewReader("")); code != 1 {
		t.Fatalf("table exit = %d, want 1: %s", code, stderr.String())
	}
	header := strings.Split(strings.TrimSpace(stdout.String()), "\n")[0]
	for _, want := range []string{"STORE", "ESTADO", "COBERTURA", "TOTAL", "FALTAN", "STALE", "MS"} {
		if !strings.Contains(header, want) {
			t.Errorf("header %q is missing %q", header, want)
		}
	}
	if !strings.Contains(stdout.String(), "sin confirmar en aldi: "+pinnedEggName) {
		t.Errorf("table does not list what is stale:\n%s", stdout.String())
	}
}

// A pinned entry whose id is still published is priced from the chain's live answer, and
// that chain is the only one that can cover the basket.
func TestBasketPinnedEntryResolvesAndKeepsTheChainInTheRanking(t *testing.T) {
	all, stubs := basketChains(map[string]map[string][]contract.Hit{
		"dia": {
			"leche entera": {offer("d1", "Leche entera Dia Láctea 1 L", 1.00)},
			pinnedEggName:  {offer("h9", pinnedEggName, 2.50)},
		},
		"mas": {"leche entera": {offer("m1", "Leche entera Pascual 1 L", 0.90)}},
	})
	path := writeBasket(t, pinnedDiaBasket)

	var stdout, stderr strings.Builder
	if code := runBasket([]string{"-f", path, "--json"}, &stdout, &stderr, all, strings.NewReader("")); code != 0 {
		t.Fatalf("exit = %d, want 0: %s", code, stderr.String())
	}
	res := decodeBasket(t, stdout.String())

	// dia: 100 + 2 x 250 = 600.
	if got := chainEntry(t, res, "dia"); got.TotalCents != 600 || got.Coverage != "2/2" || len(got.Stale) != 0 {
		t.Fatalf("dia = %+v, want 600 cents and 2/2", got)
	}
	if len(res.Ranking) != 1 || res.Ranking[0].Store != "dia" || res.Ranking[0].TotalCents != 600 {
		t.Errorf("ranking = %+v, want dia 600 only", res.Ranking)
	}
	// The pinned search is wider than the automatic one, so the id can be found at all.
	wide := 0
	for i, term := range stubs["dia"].terms {
		if term == pinnedEggName {
			wide++
			if got := stubs["dia"].limits[i]; got != basket.PinnedLimit {
				t.Errorf("pinned search used limit %d, want %d", got, basket.PinnedLimit)
			}
		}
	}
	if wide != 1 {
		t.Errorf("pinned name searched %d times, want once: %v", wide, stubs["dia"].terms)
	}

	stdout.Reset()
	stderr.Reset()
	if code := runBasket([]string{"-f", path}, &stdout, &stderr, all, strings.NewReader("")); code != 0 {
		t.Fatalf("table exit = %d, want 0: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "más barata con la cesta completa: dia 6.00 €") {
		t.Errorf("table does not name the pinned chain as the winner:\n%s", stdout.String())
	}
}

// Table mode of candidates: one line per candidate, score and price visible, so the user
// can pick without reading JSON.
func TestBasketCandidatesTableListsTheProposals(t *testing.T) {
	all, _ := basketChains(map[string]map[string][]contract.Hit{
		"dia": {"huevos": {
			offer("305802", "Huevos frescos clase M 12 ud", 2.20),
			offer("770011", "Huevos de chocolate rellenos", 2.99),
		}},
	})
	path := writeBasket(t, `{"items":[{"query":"huevos","quantity":1}]}`)

	var stdout, stderr strings.Builder
	if code := runBasket([]string{"candidates", "-f", path, "--only", "dia"}, &stdout, &stderr, all, strings.NewReader("")); code != 0 {
		t.Fatalf("exit = %d, want 0: %s", code, stderr.String())
	}
	out := stdout.String()
	header := strings.Split(strings.TrimSpace(out), "\n")[0]
	for _, want := range []string{"ITEM", "STORE", "PUNTOS", "PRECIO", "PRODUCTO"} {
		if !strings.Contains(header, want) {
			t.Errorf("header %q is missing %q", header, want)
		}
	}
	for _, want := range []string{"2.20 €", "Huevos frescos clase M 12 ud", "2.99 €", "Huevos de chocolate rellenos"} {
		if !strings.Contains(out, want) {
			t.Errorf("table does not show %q:\n%s", want, out)
		}
	}
}
