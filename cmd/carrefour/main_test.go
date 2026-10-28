package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"multisuper/internal/contract"
)

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
	return contract.Result{Store: "carrefour", Query: "leche entera", Count: 1, Hits: []contract.Hit{{
		Store: "carrefour", ID: "8410297110979",
		Name:  "Leche entera Central Lechera Asturiana botella 2,2 l.",
		Brand: "central lechera asturiana", Price: 2.75, PricePerUnit: 1.25, Unit: "L",
		Currency: "EUR", Available: true,
	}}}
}

// The web app parses contract.Result and nothing else: --json stdout is that document
// alone, and S9's provenance notice never leaks into it.
func TestJSONModePrintsOnlyTheContract(t *testing.T) {
	stub := &stubSearcher{res: sample()}
	var out, errOut bytes.Buffer

	if code := run([]string{"search", "leche", "entera", "--limit", "3", "--json"}, &out, &errOut, stub); code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, errOut.String())
	}
	if stub.term != "leche entera" || stub.lim != 3 {
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
	if got.Store != "carrefour" || got.Query != "leche entera" || got.Count != 1 ||
		got.Hits[0].ID != "8410297110979" || got.Hits[0].Price != 2.75 ||
		got.Hits[0].PricePerUnit != 1.25 || got.Hits[0].Unit != "L" {
		t.Fatalf("got %+v", got)
	}
}

// S9: the price is reported by Superfudis, not published by Carrefour, and the notice
// says so on stderr in both modes.
func TestProvenanceNoticeGoesToStderrInBothModes(t *testing.T) {
	want := "carrefour: prices are reported by Superfudis, not published by Carrefour"

	for _, args := range [][]string{
		{"search", "leche", "--json"},
		{"search", "leche"},
	} {
		var out, errOut bytes.Buffer
		if code := run(args, &out, &errOut, &stubSearcher{res: sample()}); code != 0 {
			t.Fatalf("args %v: exit = %d, stderr = %s", args, code, errOut.String())
		}
		if !strings.Contains(errOut.String(), want) {
			t.Errorf("args %v: stderr = %q, want the provenance notice", args, errOut.String())
		}
		if strings.Contains(out.String(), "Superfudis") {
			t.Errorf("args %v: stdout carries the notice:\n%s", args, out.String())
		}
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
	for _, want := range []string{"8410297110979", "Central Lechera Asturiana", "2.75", "1.25 €/L"} {
		if !strings.Contains(lines[1], want) {
			t.Errorf("row %q missing %q", lines[1], want)
		}
	}
}

func TestTableWithoutPerUnitPrice(t *testing.T) {
	res := contract.Result{Store: "carrefour", Query: "arroz", Count: 1, Hits: []contract.Hit{{
		Store: "carrefour", ID: "8411495116732", Name: "Arroz redondo 2x200 g", Price: 2.69, Currency: "EUR", Available: true,
	}}}
	var out bytes.Buffer
	if code := run([]string{"search", "arroz"}, &out, io.Discard, &stubSearcher{res: res}); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	row := strings.Split(strings.TrimSpace(out.String()), "\n")[1]
	if !strings.Contains(row, "-") || !strings.Contains(row, "2.69") {
		t.Fatalf("row = %q", row)
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
		{[]string{}, "usage: carrefour search"},
		{[]string{"search"}, "usage: carrefour search"},
		{[]string{"product", "x"}, "usage: carrefour search"},
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

// Flags must work after the term, and the term is every remaining word (S3).
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
