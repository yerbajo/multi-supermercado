package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"strings"
	"testing"

	"multisuper/internal/contract"
)

func ptr(f float64) *float64 { return &f }

type stubSearcher struct {
	res  contract.Result
	err  error
	term string
	lim  int
}

func (s *stubSearcher) Search(_ context.Context, term string, limit int) (contract.Result, error) {
	s.term, s.lim = term, limit
	return s.res, s.err
}

func sample() contract.Result {
	return contract.Result{Store: "lidl", Query: "leche", Count: 1, Hits: []contract.Hit{{
		Store: "lidl", ID: "11001765", Name: "FIN CARRÉ Chocolate",
		Brand: "FIN CARRÉ", Price: ptr(0.85), PricePerUnit: 8.5, Unit: "kg",
		Currency: "EUR", Available: contract.Bool(true), Category: "Food",
		URL: "https://www.lidl.es/p/fin-carre-chocolate/p11001765", Image: "https://www.lidl.es/i.png",
	}}}
}

// The web app parses contract.Result and nothing else, so --json stdout must be that
// document alone, with the exact envelope keys, under either the real or a stub reader.
func TestJSONModePrintsOnlyTheContract(t *testing.T) {
	stub := &stubSearcher{res: sample()}
	var out, errOut bytes.Buffer

	if code := run([]string{"search", "leche", "--limit", "3", "--json"}, &out, &errOut, stub); code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, errOut.String())
	}
	if stub.term != "leche" || stub.lim != 3 {
		t.Fatalf("search got term=%q limit=%d", stub.term, stub.lim)
	}

	for _, key := range []string{`"store"`, `"query"`, `"count"`, `"hits"`, `"pricePerUnit"`, `"available"`} {
		if !strings.Contains(out.String(), key) {
			t.Errorf("stdout is missing %s:\n%s", key, out.String())
		}
	}

	var got contract.Result
	dec := json.NewDecoder(&out)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&got); err != nil {
		t.Fatalf("stdout is not a bare contract.Result: %v\n%s", err, out.String())
	}
	if dec.More() {
		t.Fatalf("stdout has trailing content:\n%s", out.String())
	}
	if got.Store != "lidl" || got.Query != "leche" || got.Count != 1 || got.Hits[0].ID != "11001765" ||
		got.Hits[0].Price == nil || *got.Hits[0].Price != 0.85 || got.Hits[0].PricePerUnit != 8.5 || got.Hits[0].Unit != "kg" {
		t.Fatalf("got %+v", got)
	}
	if errOut.Len() != 0 {
		t.Fatalf("stderr = %s", errOut.String())
	}
}

// S8: when Lidl publishes no price the hit carries no price and the CLI says so — on
// stderr, never inside the JSON document.
func TestUnpricedHitsAreDeclaredOnStderrOnly(t *testing.T) {
	res := sample()
	res.Hits = append(res.Hits, contract.Hit{Store: "lidl", ID: "11137495", Name: "REALVALLE Lacón al horno", Available: contract.Bool(true)})
	res.Count = 2
	var out, errOut bytes.Buffer

	if code := run([]string{"search", "leche", "--json"}, &out, &errOut, &stubSearcher{res: res}); code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, errOut.String())
	}
	var got contract.Result
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not JSON: %v\n%s", err, out.String())
	}
	if got.Hits[1].Price != nil {
		t.Fatalf("unpriced hit = %+v, want no price", got.Hits[1])
	}
	var doc struct {
		Hits []map[string]json.RawMessage `json:"hits"`
	}
	if err := json.Unmarshal(out.Bytes(), &doc); err != nil {
		t.Fatalf("stdout is not JSON: %v", err)
	}
	for _, key := range []string{"price", "currency"} {
		if _, ok := doc.Hits[1][key]; ok {
			t.Errorf("an unknown price must omit %q from the JSON:\n%s", key, out.String())
		}
	}
	if _, ok := doc.Hits[0]["price"]; !ok {
		t.Error("a published price must stay in the JSON")
	}
	if !strings.Contains(errOut.String(), "1 of 2 hits have no published price") {
		t.Fatalf("stderr = %q, want the missing-price notice", errOut.String())
	}
}

func TestTableMode(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run([]string{"search", "leche"}, &out, &errOut, &stubSearcher{res: sample()}); code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, errOut.String())
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("table = %q", out.String())
	}
	for _, want := range []string{"ID", "NAME", "PRICE", "€/UNIT"} {
		if !strings.Contains(lines[0], want) {
			t.Errorf("header %q missing %q", lines[0], want)
		}
	}
	for _, want := range []string{"11001765", "FIN CARRÉ Chocolate", "0.85", "8.50 €/kg"} {
		if !strings.Contains(lines[1], want) {
			t.Errorf("row %q missing %q", lines[1], want)
		}
	}
	if errOut.Len() != 0 {
		t.Fatalf("stderr = %s", errOut.String())
	}
}

func TestTableWithoutPerUnitPrice(t *testing.T) {
	res := contract.Result{Store: "lidl", Query: "x", Count: 1, Hits: []contract.Hit{{
		Store: "lidl", ID: "7", Name: "GELATELLI Helado almendrado", Price: ptr(2.65), Currency: "EUR",
	}}}
	var out bytes.Buffer
	if code := run([]string{"search", "x"}, &out, io.Discard, &stubSearcher{res: res}); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	row := strings.Split(strings.TrimSpace(out.String()), "\n")[1]
	if !strings.Contains(row, "-") || !strings.Contains(row, "2.65") {
		t.Fatalf("row = %q", row)
	}
}

// S8: an unpublished price prints "-" in the PRICE column instead of 0,00 €.
func TestTableMarksAnUnpublishedPrice(t *testing.T) {
	res := contract.Result{Store: "lidl", Query: "x", Count: 1, Hits: []contract.Hit{{
		Store: "lidl", ID: "11137495", Name: "Lacón", Available: contract.Bool(true),
	}}}
	var out bytes.Buffer
	if code := run([]string{"search", "x"}, &out, io.Discard, &stubSearcher{res: res}); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	row := strings.Split(strings.TrimSpace(out.String()), "\n")[1]
	if !regexp.MustCompile(`^11137495\s+Lacón\s+-\s+-$`).MatchString(row) {
		t.Fatalf("row = %q, want - for price and unit price", row)
	}
	if strings.Contains(row, "0.00") {
		t.Fatalf("unknown price must not render as 0.00: %q", row)
	}
}

func TestFetchErrorGoesToStderrAndExitsOne(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run([]string{"search", "leche", "--json"}, &out, &errOut, &stubSearcher{err: errors.New("boom")}); code != 1 {
		t.Fatalf("exit = %d", code)
	}
	if out.Len() != 0 {
		t.Fatalf("stdout must stay empty, got %q", out.String())
	}
	if !strings.Contains(errOut.String(), "boom") {
		t.Fatalf("stderr = %q", errOut.String())
	}
}

func TestUsageErrors(t *testing.T) {
	tests := []struct {
		args []string
		want string
	}{
		{[]string{}, "usage: lidl search"},
		{[]string{"search"}, "usage: lidl search"},
		{[]string{"product", "x"}, "usage: lidl search"},
		{[]string{"search", "leche", "--limit", "101"}, "--limit must be between 1 and 100"},
		{[]string{"search", "leche", "--limit", "0"}, "--limit must be between 1 and 100"},
	}
	for _, tt := range tests {
		var out, errOut bytes.Buffer
		if code := run(tt.args, &out, &errOut, &stubSearcher{}); code != 2 {
			t.Errorf("args %v: exit = %d, want 2", tt.args, code)
		}
		if out.Len() != 0 {
			t.Errorf("args %v: stdout = %q", tt.args, out.String())
		}
		if !strings.Contains(errOut.String(), tt.want) {
			t.Errorf("args %v: stderr = %q, want it to mention %q", tt.args, errOut.String(), tt.want)
		}
	}
}

// S3: -h and --help print the usage on stdout and exit 0, in any position. Left to the
// flag package they return flag.ErrHelp, which this CLI reads as a usage error.
func TestHelpInAnyPositionExitsZeroWithUsageOnStdout(t *testing.T) {
	for _, args := range [][]string{
		{"--help"},
		{"-h"},
		{"search", "--help"},
		{"search", "-h"},
		{"search", "leche", "--help"},
		{"search", "leche", "-h"},
	} {
		stub := &stubSearcher{}
		var out, errOut bytes.Buffer
		if code := run(args, &out, &errOut, stub); code != 0 {
			t.Errorf("%v: exit = %d, want 0 (stderr %q)", args, code, errOut.String())
		}
		if !strings.Contains(out.String(), "usage: lidl search") {
			t.Errorf("%v: stdout = %q, want the usage", args, out.String())
		}
		if errOut.Len() != 0 {
			t.Errorf("%v: stderr = %q, want empty", args, errOut.String())
		}
		if stub.term != "" {
			t.Errorf("%v: searcher was called with term=%q", args, stub.term)
		}
	}
}

// Flags must work after the term, and the term is every remaining argument.
func TestFlagsAfterTerm(t *testing.T) {
	stub := &stubSearcher{res: sample()}
	var out, errOut bytes.Buffer
	if code := run([]string{"search", "leche", "entera", "--json", "--limit", "5"}, &out, &errOut, stub); code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, errOut.String())
	}
	if stub.term != "leche entera" || stub.lim != 5 {
		t.Fatalf("search got term=%q limit=%d", stub.term, stub.lim)
	}
}
