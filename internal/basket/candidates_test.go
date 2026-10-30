package basket

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"multisuper/internal/aggregate"
	"multisuper/internal/contract"
)

// The candidate list exists because «huevos» matched chocolate eggs: the two products
// score the same, so both must be offered, with the price that separates them.
func TestCandidatesOffersTheRealEggsAndTheChocolateOnes(t *testing.T) {
	dia, _ := chainFor("dia", map[string][]contract.Hit{
		"huevos": {
			product("305802", "Huevos frescos clase M 12 ud", 2.20),
			product("770011", "Huevos de chocolate rellenos", 2.99),
			product("999", "SILVERCREST Cuecehuevos", 11.99),
		},
	})
	res := Candidates(context.Background(), req(Item{Query: "huevos", Quantity: 2}),
		[]aggregate.Chain{dia}, DefaultProposals, time.Second)

	if len(res.Items) != 1 || res.Items[0].Quantity != 2 {
		t.Fatalf("items = %+v, want one huevos x2", res.Items)
	}
	got := res.Items[0].Candidates["dia"]
	if len(got) != DefaultProposals {
		t.Fatalf("candidates = %+v, want %d", got, DefaultProposals)
	}
	// Score ties are broken by price, so the real eggs come first: 2,20 € against 2,99 €.
	want := []Candidate{
		{ID: "305802", Name: "Huevos frescos clase M 12 ud", UnitCents: 220, Score: 1},
		{ID: "770011", Name: "Huevos de chocolate rellenos", UnitCents: 299, Score: 1},
		{ID: "999", Name: "SILVERCREST Cuecehuevos", UnitCents: 1199, Score: 0},
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("candidate %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// Up to N, ranked by score desc, then cheapest, then id: the same ordering the automatic
// pick uses, so a user who likes the first candidate gets what the basket would buy.
func TestCandidatesKeepsAtMostNPerChainInTheDocumentedOrder(t *testing.T) {
	mas, _ := chainFor("mas", map[string][]contract.Hit{
		"leche entera": {
			product("z", "Leche entera Pascual 1 L", 1.10),
			product("a", "Leche entera Pilos 1 l", 1.10),
			product("b", "Leche entera Milbona 1 l", 0.95),
			product("c", "Chocolate leche LINDT 100gr", 0.50),
		},
	})
	res := Candidates(context.Background(), req(Item{Query: "leche entera", Quantity: 1}),
		[]aggregate.Chain{mas}, 2, time.Second)

	got := res.Items[0].Candidates["mas"]
	if len(got) != 2 {
		t.Fatalf("candidates = %+v, want 2", got)
	}
	// Equal scores: cheapest first, and the lowest id between the two 1,10 € cartons.
	if got[0].ID != "b" || got[1].ID != "a" {
		t.Fatalf("candidates = %+v, want b then a", got)
	}
	if got[0].Score != 1 || got[1].Score != 1 {
		t.Errorf("scores = %v, %v, want both 1", got[0].Score, got[1].Score)
	}
}

// A below-threshold candidate is still offered, with the score that says so: the list is
// a proposal the user judges, not a pick this code makes.
func TestCandidatesOffersABelowThresholdCandidateWithItsScore(t *testing.T) {
	lidl, _ := chainFor("lidl", map[string][]contract.Hit{
		"huevos": {product("9", "SILVERCREST Cuecehuevos", 11.99)},
	})
	res := Candidates(context.Background(), req(Item{Query: "huevos", Quantity: 1}),
		[]aggregate.Chain{lidl}, DefaultProposals, time.Second)

	got := res.Items[0].Candidates["lidl"]
	if len(got) != 1 || got[0].Score != 0 {
		t.Fatalf("candidates = %+v, want the cuecehuevos at score 0", got)
	}
}

// A chain that answered without candidates says so with an empty list, and a chain that
// failed carries its code, never a silently absent key.
func TestCandidatesReportsAnswersAndChainErrors(t *testing.T) {
	empty, _ := chainFor("aldi", map[string][]contract.Hit{})
	bad := &stub{err: &aggregate.StatusError{Status: 429, Msg: "carrefour: rate limit"}}
	res := Candidates(context.Background(), req(Item{Query: "huevos", Quantity: 1}),
		[]aggregate.Chain{empty, {Store: "carrefour", Searcher: bad}}, DefaultProposals, time.Second)

	if got := res.Items[0].Candidates["aldi"]; got == nil || len(got) != 0 {
		t.Errorf("aldi candidates = %+v, want an empty list", got)
	}
	if _, ok := res.Items[0].Candidates["carrefour"]; ok {
		t.Error("a failed chain proposes candidates")
	}
	if len(res.Chains) != 2 || res.Chains[1].Error == nil || res.Chains[1].Error.Code != aggregate.CodeQuota {
		t.Fatalf("chains = %+v, want carrefour with a quota error", res.Chains)
	}
}

// The candidate run asks for the same bounded page the basket asks for, one search per
// item and chain, and prints one JSON document of its own.
func TestCandidatesAsksForABoundedPageAndEncodesOneDocument(t *testing.T) {
	dia, s := chainFor("dia", map[string][]contract.Hit{
		"huevos": {product("1", "Huevos frescos clase M 12 ud", 2.20)},
	})
	res := Candidates(context.Background(), req(Item{Query: "huevos", Quantity: 1}),
		[]aggregate.Chain{dia}, DefaultProposals, time.Second)
	if s.calls() != 1 || s.limits[0] != DefaultCandidates {
		t.Fatalf("calls = %v, limits = %v, want one search of %d", s.terms, s.limits, DefaultCandidates)
	}

	var buf bytes.Buffer
	if err := EncodeCandidates(&buf, res); err != nil {
		t.Fatalf("EncodeCandidates: %v", err)
	}
	dec := json.NewDecoder(bytes.NewReader(buf.Bytes()))
	dec.DisallowUnknownFields()
	var back CandidatesResult
	if err := dec.Decode(&back); err != nil {
		t.Fatalf("output is not the documented document: %v\n%s", err, buf.String())
	}
	if err := dec.Decode(new(json.RawMessage)); err == nil {
		t.Fatal("stdout carries more than one JSON document")
	}
	for _, key := range []string{`"items"`, `"query"`, `"quantity"`, `"candidates"`,
		`"id"`, `"name"`, `"unitCents"`, `"score"`, `"chains"`, `"elapsedMs"`} {
		if !strings.Contains(buf.String(), key) {
			t.Errorf("encoded candidates are missing %s:\n%s", key, buf.String())
		}
	}
	if !strings.Contains(buf.String(), `"unitCents": 220`) || !strings.Contains(buf.String(), `"score": 1`) {
		t.Errorf("candidate is not scored in cents:\n%s", buf.String())
	}
}
