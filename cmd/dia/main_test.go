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
	return contract.Result{Store: "dia", Query: "leche", Count: 1, Hits: []contract.Hit{{
		Store: "dia", ID: "74720P6", Name: "Leche entera Asturiana pack 6 x 1.5 L",
		Brand: "Asturiana", Price: ptr(11.82), PricePerUnit: 1.31, Unit: "L",
		Currency: "EUR", Available: true, Category: "Leche",
		URL: "https://www.dia.es/p/74720P6", Image: "https://www.dia.es/i.jpg",
	}}}
}

func TestJSONModePrintsOnlyTheContract(t *testing.T) {
	stub := &stubSearcher{res: sample()}
	var out, errOut bytes.Buffer

	if code := run([]string{"search", "leche", "--limit", "3", "--json"}, &out, &errOut, stub); code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, errOut.String())
	}
	if stub.term != "leche" || stub.lim != 3 {
		t.Fatalf("search got term=%q limit=%d", stub.term, stub.lim)
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
	if got.Store != "dia" || got.Query != "leche" || got.Count != 1 || got.Hits[0].ID != "74720P6" {
		t.Fatalf("got %+v", got)
	}
	if errOut.Len() != 0 {
		t.Fatalf("stderr = %s", errOut.String())
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
	for _, want := range []string{"ID", "NAME", "PRICE", "€/UNIT", "IN STOCK"} {
		if !strings.Contains(lines[0], want) {
			t.Errorf("header %q missing %q", lines[0], want)
		}
	}
	for _, want := range []string{"74720P6", "Leche entera", "11.82", "1.31 €/L", "yes"} {
		if !strings.Contains(lines[1], want) {
			t.Errorf("row %q missing %q", lines[1], want)
		}
	}
	if errOut.Len() != 0 {
		t.Fatalf("stderr = %s", errOut.String())
	}
}

func TestTableWithoutPerUnitOrStock(t *testing.T) {
	res := contract.Result{Store: "dia", Query: "x", Count: 1, Hits: []contract.Hit{{
		Store: "dia", ID: "X1", Name: "Bolsa", Price: ptr(0.05), Currency: "EUR",
	}}}
	var out bytes.Buffer
	if code := run([]string{"search", "x"}, &out, io.Discard, &stubSearcher{res: res}); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	row := strings.Split(strings.TrimSpace(out.String()), "\n")[1]
	if !strings.Contains(row, "-") || !strings.Contains(row, "no") {
		t.Fatalf("row = %q", row)
	}
}

// S8: an unpublished price prints "-" instead of reading as 0,00 €.
func TestTableMarksAnUnpublishedPrice(t *testing.T) {
	res := contract.Result{Store: "dia", Query: "x", Count: 1, Hits: []contract.Hit{{
		Store: "dia", ID: "X1", Name: "Bolsa", Available: true,
	}}}
	var out bytes.Buffer
	if code := run([]string{"search", "x"}, &out, io.Discard, &stubSearcher{res: res}); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	row := strings.Split(strings.TrimSpace(out.String()), "\n")[1]
	if !regexp.MustCompile(`^X1\s+Bolsa\s+-\s+-\s+yes$`).MatchString(row) {
		t.Fatalf("row = %q, want - for price and unit price", row)
	}
	if strings.Contains(row, "0.00") {
		t.Fatalf("unknown price must not render as 0.00: %q", row)
	}
}

func TestFetchErrorGoesToStderrAndExitsOne(t *testing.T) {
	var out, errOut bytes.Buffer
	stub := &stubSearcher{err: errors.New("boom")}
	if code := run([]string{"search", "leche", "--json"}, &out, &errOut, stub); code != 1 {
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
	for _, args := range [][]string{{}, {"search"}, {"product", "x"}} {
		var out, errOut bytes.Buffer
		if code := run(args, &out, &errOut, &stubSearcher{}); code != 2 {
			t.Errorf("args %v: exit = %d, want 2", args, code)
		}
		if out.Len() != 0 {
			t.Errorf("args %v: stdout = %q", args, out.String())
		}
		if !strings.Contains(errOut.String(), "usage: dia search") {
			t.Errorf("args %v: stderr = %q", args, errOut.String())
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
		if !strings.Contains(out.String(), "usage: dia search") {
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

// S3: the search term is the whole phrase, never just its first word.
func TestMultiWordTermIsSearchedAsOnePhrase(t *testing.T) {
	stub := &stubSearcher{res: sample()}
	var out, errOut bytes.Buffer
	if code := run([]string{"search", "leche", "entera", "--limit", "3", "--json"}, &out, &errOut, stub); code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, errOut.String())
	}
	if stub.term != "leche entera" {
		t.Fatalf("searcher got term %q, want %q", stub.term, "leche entera")
	}
	if stub.lim != 3 {
		t.Fatalf("searcher got limit %d, want 3", stub.lim)
	}
}

// S3: an out-of-range --limit is rejected, never silently clamped.
func TestLimitOutOfRangeIsRejected(t *testing.T) {
	for _, limit := range []string{"0", "-1", "101", "1000"} {
		stub := &stubSearcher{res: sample()}
		var out, errOut bytes.Buffer
		if code := run([]string{"search", "leche", "--limit", limit, "--json"}, &out, &errOut, stub); code != 2 {
			t.Errorf("--limit %s: exit = %d, want 2", limit, code)
		}
		if out.Len() != 0 {
			t.Errorf("--limit %s: stdout = %q, want nothing", limit, out.String())
		}
		if stub.term != "" || stub.lim != 0 {
			t.Errorf("--limit %s: searcher was called with term=%q limit=%d", limit, stub.term, stub.lim)
		}
		if !strings.Contains(errOut.String(), "limit must be between 1 and 100") {
			t.Errorf("--limit %s: stderr = %q", limit, errOut.String())
		}
	}
}
