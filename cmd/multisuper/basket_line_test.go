package main

import (
	"strings"
	"testing"

	"multisuper/internal/basket"
	"multisuper/internal/contract"
)

// legacyBasket is a basket file from before the line key existed: automatic entries only.
const legacyBasket = `{"items":[
	{"query":"leche entera","quantity":2},
	{"query":"pan de molde","quantity":1},
	{"query":"huevos","quantity":3}]}`

// legacyBasketJSON is the exact document this basket printed before the line key, byte for
// byte. Adding the line identity must not change the answer to a basket that never used it.
const legacyBasketJSON = `{
  "items": [
    {
      "query": "leche entera",
      "quantity": 2,
      "resolved": {
        "dia": {
          "id": "d1",
          "name": "Leche entera d 1 L",
          "unitCents": 100,
          "lineCents": 200
        }
      },
      "missing": [],
      "stale": []
    },
    {
      "query": "pan de molde",
      "quantity": 1,
      "resolved": {
        "dia": {
          "id": "d2",
          "name": "Pan de molde blanco 460 g",
          "unitCents": 20,
          "lineCents": 20
        }
      },
      "missing": [],
      "stale": []
    },
    {
      "query": "huevos",
      "quantity": 3,
      "resolved": {
        "dia": {
          "id": "d3",
          "name": "Huevos frescos clase M 12 ud",
          "unitCents": 10,
          "lineCents": 30
        }
      },
      "missing": [],
      "stale": []
    }
  ],
  "chains": [
    {
      "store": "dia",
      "totalCents": 250,
      "coverage": "3/3",
      "elapsedMs": 0,
      "missing": [],
      "stale": []
    }
  ],
  "ranking": [
    {
      "store": "dia",
      "totalCents": 250
    }
  ]
}
`

// A basket written before the line key prints exactly what it printed then.
func TestBasketLegacyFileKeepsItsExactJSON(t *testing.T) {
	all, _ := basketChains(map[string]map[string][]contract.Hit{"dia": fullHits("d", 1)})
	path := writeBasket(t, legacyBasket)

	var stdout, stderr strings.Builder
	code := runBasket([]string{"-f", path, "--only", "dia", "--json"}, &stdout, &stderr, all, strings.NewReader(""))
	if code != 0 {
		t.Fatalf("exit = %d, want 0: %s", code, stderr.String())
	}
	if stdout.String() != legacyBasketJSON {
		t.Fatalf("legacy basket JSON changed\nwant:\n%s\ngot:\n%s", legacyBasketJSON, stdout.String())
	}
	// --json purity: one document on stdout, nothing else anywhere.
	if stderr.String() != "" {
		t.Errorf("--json wrote to stderr: %q", stderr.String())
	}
	if strings.Count(stdout.String(), `"ranking"`) != 1 {
		t.Errorf("stdout carries more than one document:\n%s", stdout.String())
	}
}

// The live shape of T32: one basket, one line per product, and the eggs chosen per chain.
// Each chain that has a pin for the line reaches full coverage and can be ranked, while a
// chain without one simply lacks that line.
const mixedBasket = `{"items":[
	{"query":"leche entera","quantity":1},
	{"query":"pan de molde","quantity":1},
	{"line":"huevos","store":"dia","id":"305802","name":"Huevos frescos clase M 12 ud","quantity":2},
	{"line":"huevos","store":"mas","id":"116867","name":"Huevos frescos 12 ud","quantity":2},
	{"line":"huevos","store":"alcampo","id":"99261","name":"Huevos frescos clase L 12 ud","quantity":2}]}`

func TestBasketPricesOneLinePinnedPerChain(t *testing.T) {
	all, stubs := basketChains(map[string]map[string][]contract.Hit{
		"dia": {
			"leche entera":                 {offer("d1", "Leche entera Dia Láctea 1 L", 0.90)},
			"pan de molde":                 {offer("d2", "Pan de molde blanco 460 g", 1.20)},
			"Huevos frescos clase M 12 ud": {offer("305802", "Huevos frescos clase M 12 ud", 2.20)},
		},
		"mas": {
			"leche entera":         {offer("m1", "Leche entera Pascual 1 L", 1.00)},
			"pan de molde":         {offer("m2", "Pan de molde integral 500 g", 1.30)},
			"Huevos frescos 12 ud": {offer("116867", "Huevos frescos 12 ud", 1.80)},
		},
		"alcampo": {
			"leche entera":                 {offer("c1", "Leche entera Alcampo 1 L", 0.95)},
			"pan de molde":                 {offer("c2", "Pan de molde Alcampo 460 g", 1.25)},
			"Huevos frescos clase L 12 ud": {offer("99261", "Huevos frescos clase L 12 ud", 2.36)},
		},
		// A chain with no pin for the line and no fallback: it prices two lines out of three.
		"lidl": {
			"leche entera": {offer("l1", "Leche entera Milbona 1 L", 0.80)},
			"pan de molde": {offer("l2", "Pan de molde Milbona 500 g", 1.00)},
		},
	})
	path := writeBasket(t, mixedBasket)

	var stdout, stderr strings.Builder
	code := runBasket([]string{"-f", path, "--only", "dia,mas,alcampo,lidl", "--json"},
		&stdout, &stderr, all, strings.NewReader(""))
	if code != 0 {
		t.Fatalf("exit = %d, want 0: %s", code, stderr.String())
	}
	if stderr.String() != "" {
		t.Errorf("--json wrote to stderr: %q", stderr.String())
	}
	res := decodeBasket(t, stdout.String())

	if len(res.Items) != 3 {
		t.Fatalf("items = %+v, want the three lines", res.Items)
	}
	eggs := res.Items[2]
	if eggs.Line != "huevos" || eggs.Query != "" || eggs.Quantity != 2 {
		t.Fatalf("items[2] = %+v, want the pinned huevos line", eggs)
	}
	// DIA 90+120+440, MAS 100+130+360, ALCAMPO 95+125+472: all three at 3/3.
	for store, want := range map[string]int64{"dia": 650, "mas": 590, "alcampo": 692} {
		if got := chainEntry(t, res, store); got.Coverage != "3/3" || got.TotalCents != want {
			t.Errorf("%s = %+v, want 3/3 and %d cents", store, got, want)
		}
	}
	if got := chainEntry(t, res, "lidl"); got.Coverage != "2/3" || len(got.Missing) != 1 || got.Missing[0] != "huevos" {
		t.Errorf("lidl = %+v, want 2/3 missing huevos", got)
	}
	if len(res.Ranking) != 3 || res.Ranking[0].Store != "mas" || res.Ranking[0].TotalCents != 590 {
		t.Fatalf("ranking = %+v, want mas first of three", res.Ranking)
	}
	// Every pinned id is confirmed in its own chain, at the price that chain publishes.
	for store, id := range map[string]string{"dia": "305802", "mas": "116867", "alcampo": "99261"} {
		if got := eggs.Resolved[store]; got.ID != id {
			t.Errorf("eggs in %s = %+v, want id %s", store, got, id)
		}
	}
	// Each chain searched its own lines only: two automatic plus its own pinned name.
	for store, stub := range stubs {
		eggsSearches := 0
		for _, term := range stub.terms {
			if strings.Contains(term, "Huevos") {
				eggsSearches++
			}
		}
		want := 0
		if store == "dia" || store == "mas" || store == "alcampo" {
			want = 1
		}
		if eggsSearches != want {
			t.Errorf("%s searched the eggs %d times, want %d: %v", store, eggsSearches, want, stub.terms)
		}
	}
}

// The table of the same basket: one row per chain with its own coverage, and the winner
// named from the chains that cover every line.
func TestBasketTableShowsTheLineCoveragePerChain(t *testing.T) {
	all, _ := basketChains(map[string]map[string][]contract.Hit{
		"dia": {
			"leche entera":                 {offer("d1", "Leche entera Dia Láctea 1 L", 0.90)},
			"pan de molde":                 {offer("d2", "Pan de molde blanco 460 g", 1.20)},
			"Huevos frescos clase M 12 ud": {offer("305802", "Huevos frescos clase M 12 ud", 2.20)},
		},
		"lidl": {
			"leche entera": {offer("l1", "Leche entera Milbona 1 L", 0.80)},
			"pan de molde": {offer("l2", "Pan de molde Milbona 500 g", 1.00)},
		},
	})
	basketPath := writeBasket(t, `{"items":[
		{"query":"leche entera","quantity":1},
		{"query":"pan de molde","quantity":1},
		{"line":"huevos","store":"dia","id":"305802","name":"Huevos frescos clase M 12 ud","quantity":2}]}`)

	var stdout, stderr strings.Builder
	if code := runBasket([]string{"-f", basketPath, "--only", "dia,lidl"}, &stdout, &stderr, all, strings.NewReader("")); code != 0 {
		t.Fatalf("exit = %d, want 0: %s", code, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "faltan en lidl: Huevos frescos clase M 12 ud") {
		t.Errorf("table does not name the missing line:\n%s", out)
	}
	if !strings.Contains(out, "más barata con la cesta completa: dia 6.50 €") {
		t.Errorf("table does not name the chain that covers every line:\n%s", out)
	}
}

// candidates accepts a basket that already holds pinned choices and shows them as the
// current one for their chain, with the other proposals after them.
func TestBasketCandidatesShowsTheCurrentChoicePerChain(t *testing.T) {
	all, _ := basketChains(map[string]map[string][]contract.Hit{
		"dia": {"Huevos frescos clase M 12 ud": {
			offer("770011", "Huevos de chocolate rellenos", 2.99),
			offer("305802", "Huevos frescos clase M 12 ud", 2.20),
			offer("999", "Cuecehuevos SILVERCREST", 11.99),
		}},
		"mas": {"huevos": {offer("m3", "Huevos frescos 12 ud", 2.05)}},
	})
	path := writeBasket(t, `{"items":[
		{"line":"huevos","query":"huevos","quantity":2},
		{"line":"huevos","store":"dia","id":"305802","name":"Huevos frescos clase M 12 ud","quantity":2}]}`)

	var stdout, stderr strings.Builder
	code := runBasket([]string{"candidates", "-f", path, "--only", "dia,mas", "--json"},
		&stdout, &stderr, all, strings.NewReader(""))
	if code != 0 {
		t.Fatalf("exit = %d, want 0: %s", code, stderr.String())
	}
	res := decodeCandidates(t, stdout.String())

	if len(res.Items) != 1 || res.Items[0].Line != "huevos" || res.Items[0].Quantity != 2 {
		t.Fatalf("items = %+v, want one huevos line", res.Items)
	}
	dia := res.Items[0].Candidates["dia"]
	if len(dia) != basket.DefaultProposals || dia[0].ID != "305802" || !dia[0].Current {
		t.Fatalf("dia = %+v, want the pinned 305802 first and marked current", dia)
	}
	if got := res.Items[0].Candidates["mas"]; len(got) == 0 || got[0].Current {
		t.Errorf("mas = %+v, want an unmarked proposal", got)
	}
	if !strings.Contains(stdout.String(), `"current": true`) {
		t.Errorf("the current choice is not marked in the document:\n%s", stdout.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := runBasket([]string{"candidates", "-f", path, "--only", "dia,mas"},
		&stdout, &stderr, all, strings.NewReader("")); code != 0 {
		t.Fatalf("table exit = %d, want 0: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Huevos frescos clase M 12 ud (actual)") {
		t.Errorf("table does not mark the current choice:\n%s", stdout.String())
	}
}

// A line whose entries disagree with each other is the user's input, not a chain answer:
// exit 2, one clear sentence, and no chain is asked.
func TestBasketRejectsABrokenLineGroup(t *testing.T) {
	tests := []struct {
		name  string
		body  string
		match string
	}{
		{"different quantities", `{"items":[
			{"line":"huevos","query":"huevos","quantity":1},
			{"line":"huevos","store":"dia","id":"1","name":"Huevos","quantity":2}]}`,
			`line "huevos": quantity 2 does not match the 1`},
		{"two choices in one chain", `{"items":[
			{"line":"huevos","store":"dia","id":"1","name":"Huevos","quantity":1},
			{"line":"huevos","store":"dia","id":"2","name":"Huevos","quantity":1}]}`,
			`two entries for store "dia"`},
		{"pinned without a line", `{"items":[{"store":"dia","id":"1","name":"Huevos","quantity":1}]}`,
			"a pinned entry needs a line"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			all, stubs := basketChains(nil)
			var stdout, stderr strings.Builder
			code := runBasket([]string{"-f", writeBasket(t, tt.body), "--json"}, &stdout, &stderr, all, strings.NewReader(""))
			if code != 2 {
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
