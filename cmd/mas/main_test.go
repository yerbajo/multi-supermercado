package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"

	"multisuper/internal/contract"
)

func ptr(f float64) *float64 { return &f }

// stubSearcher keeps the exit-code tests offline and records what run passed on.
type stubSearcher struct {
	res    contract.Result
	err    error
	called bool
	term   string
	page   int
	limit  int
}

func (s *stubSearcher) Search(_ context.Context, term string, page, limit int) (contract.Result, error) {
	s.called = true
	s.term, s.page, s.limit = term, page, limit
	return s.res, s.err
}

// S3: usage and validation errors exit 2 and are rejected before any request.
func TestRunRejectsBadInput(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"missing command", nil, "missing command"},
		{"unknown command", []string{"product", "leche"}, `unknown command "product"`},
		{"missing term", []string{"search", "--json"}, "missing search term"},
		{"limit zero", []string{"search", "leche", "--limit", "0", "--json"}, "--limit must be between 1 and 100"},
		{"limit too high", []string{"search", "leche", "--limit", "101", "--json"}, "--limit must be between 1 and 100"},
		{"limit not a number", []string{"search", "leche", "--limit", "muchos"}, "invalid value"},
		{"unknown flag", []string{"search", "leche", "--nope"}, "flag provided but not defined"},
		{"page zero", []string{"search", "leche", "--page", "0"}, "--page must be 1 or more"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr strings.Builder
			stub := &stubSearcher{}
			err := run(tt.args, &stdout, &stderr, deps{search: stub})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want it to mention %q", err, tt.want)
			}
			if code := exitCode(err); code != 2 {
				t.Fatalf("exit code = %d, want 2", code)
			}
			if stdout.Len() != 0 {
				t.Fatalf("stdout = %q, want nothing on error", stdout.String())
			}
			if stub.called {
				t.Fatalf("searcher was called with term=%q", stub.term)
			}
		})
	}
}

// S3: asking for help is not an error whatever the position of the flag.
func TestHelpExitsZeroAndPrintsUsageOnStdout(t *testing.T) {
	for _, args := range [][]string{
		{"--help"},
		{"-h"},
		{"search", "--help"},
		{"search", "-h"},
		{"search", "leche", "--help"},
		{"search", "leche", "-h"},
	} {
		var stdout, stderr strings.Builder
		stub := &stubSearcher{}
		if err := run(args, &stdout, &stderr, deps{search: stub}); err != nil {
			t.Errorf("%v: err = %v, want nil", args, err)
		}
		if !strings.Contains(stdout.String(), "usage:") {
			t.Errorf("%v: stdout = %q, want the usage", args, stdout.String())
		}
		if stderr.Len() != 0 {
			t.Errorf("%v: stderr = %q, want empty", args, stderr.String())
		}
		if stub.called {
			t.Errorf("%v: searcher was called with term=%q", args, stub.term)
		}
	}
}

// S3: the whole phrase reaches the searcher, and flags after the term still work.
func TestMultiWordTermAndFlags(t *testing.T) {
	var stdout strings.Builder
	stub := &stubSearcher{res: contract.Result{Store: "mas", Query: "leche entera", Hits: []contract.Hit{}}}
	if err := run([]string{"search", "leche", "entera", "--limit", "2", "--json"}, &stdout, &strings.Builder{}, deps{search: stub}); err != nil {
		t.Fatalf("run: %v", err)
	}
	if stub.term != "leche entera" || stub.limit != 2 || stub.page != 1 {
		t.Fatalf("search got term=%q page=%d limit=%d", stub.term, stub.page, stub.limit)
	}
}

// recordingSearcher answers per term and records every request, so a test can prove which
// queries reached the store and on which page.
type recordingSearcher struct {
	mu     sync.Mutex
	calls  []call
	byTerm map[string][]contract.Hit
}

type call struct {
	term        string
	page, limit int
}

func (s *recordingSearcher) Search(_ context.Context, term string, page, limit int) (contract.Result, error) {
	s.mu.Lock()
	s.calls = append(s.calls, call{term: term, page: page, limit: limit})
	s.mu.Unlock()
	h := s.byTerm[term]
	return contract.Result{Store: "mas", Query: term, Count: len(h), Hits: h}, nil
}

// requested returns the requests sorted by term, so the assertion does not depend on which
// of two parallel queries finished first.
func (s *recordingSearcher) requested() []call {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := append([]call(nil), s.calls...)
	sort.Slice(out, func(i, j int) bool { return out[i].term < out[j].term })
	return out
}

// The user's report, end to end through the command: «leche de soja» only reaches the IFA
// drinks through the catalogue form «bebida de soja», and the hits both queries return are
// kept once.
func TestSearchMergesTheCatalogueVariant(t *testing.T) {
	s := &recordingSearcher{byTerm: map[string][]contract.Hit{
		"leche de soja": {
			{Store: "mas", ID: "892841", Name: "Pan de soja 70g"},
			{Store: "mas", ID: "084231", Name: "Salsa de soja IFA ELIGES 250ml"},
			{Store: "mas", ID: "044616", Name: "Bebida soja con calcio IFA ELIGES 1l"},
		},
		"bebida de soja": {
			{Store: "mas", ID: "044616", Name: "Bebida soja con calcio IFA ELIGES 1l"},
			{Store: "mas", ID: "044619", Name: "Bebida soja sin azucar IFA ELIGES 1l"},
		},
	}}

	var stdout strings.Builder
	args := []string{"search", "leche", "de", "soja", "--limit", "3", "--json"}
	if err := run(args, &stdout, &strings.Builder{}, deps{search: s}); err != nil {
		t.Fatalf("run: %v", err)
	}

	var res contract.Result
	if err := json.Unmarshal([]byte(stdout.String()), &res); err != nil {
		t.Fatalf("stdout is not one JSON document: %v\n%s", err, stdout.String())
	}
	if res.Store != "mas" || res.Query != "leche de soja" || res.Count != len(res.Hits) || res.Count != 3 {
		t.Fatalf("envelope = %+v, want mas/leche de soja/3 rows and count = len(hits)", res)
	}
	if res.Hits[0].ID != "044616" || res.Hits[1].ID != "044619" {
		t.Fatalf("hits = %v, want the two IFA drinks first", res.Hits)
	}
	if res.Hits[2].ID != "892841" {
		t.Fatalf("last row = %s, want the soy junk ranked after the drinks", res.Hits[2].Name)
	}
	for _, h := range res.Hits {
		if h.ID == "084231" {
			t.Fatalf("a 1-of-2 soy sauce outranked a full-coverage drink: %v", res.Hits)
		}
	}

	want := []call{{"bebida de soja", 1, 3}, {"leche de soja", 1, 3}}
	got := s.requested()
	if len(got) != len(want) {
		t.Fatalf("requests = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("requests = %+v, want %+v", got, want)
		}
	}
}

// --page stays the store's own pagination: from page 2 on only the typed query is sent, so
// the variant rows are never counted twice.
func TestSearchPageTwoSendsOnlyTheTypedQuery(t *testing.T) {
	s := &recordingSearcher{byTerm: map[string][]contract.Hit{
		"leche de soja": {{Store: "mas", ID: "892841", Name: "Pan de soja 70g"}},
	}}

	args := []string{"search", "leche", "de", "soja", "--page", "2", "--limit", "6", "--json"}
	if err := run(args, &strings.Builder{}, &strings.Builder{}, deps{search: s}); err != nil {
		t.Fatalf("run: %v", err)
	}

	want := []call{{"leche de soja", 2, 6}}
	got := s.requested()
	if len(got) != 1 || got[0] != want[0] {
		t.Fatalf("requests = %+v, want only %+v", got, want)
	}
}

// A query the catalogue does not rewrite costs exactly one request, as it always did.
func TestSearchAsksOnceWithoutAVariant(t *testing.T) {
	s := &recordingSearcher{byTerm: map[string][]contract.Hit{
		"leche entera": {{Store: "mas", ID: "114925", Name: "Leche entera PASCUAL botella 1,5l"}},
	}}

	var stdout strings.Builder
	if err := run([]string{"search", "leche", "entera", "--json"}, &stdout, &strings.Builder{}, deps{search: s}); err != nil {
		t.Fatalf("run: %v", err)
	}

	want := []call{{"leche entera", 1, 20}}
	got := s.requested()
	if len(got) != 1 || got[0] != want[0] {
		t.Fatalf("requests = %+v, want only %+v", got, want)
	}
}

// S8: a hit without a published price prints "-" in the PRICE column, never 0.00.
func TestTableMarksAnUnpublishedPrice(t *testing.T) {
	res := contract.Result{Store: "mas", Query: "leche", Count: 2, Hits: []contract.Hit{
		{Store: "mas", ID: "062595", Name: "Leche entera", Price: ptr(1.84), Currency: "EUR", Available: contract.Bool(true)},
		{Store: "mas", ID: "077777", Name: "Cogollos", Available: contract.Bool(true)},
	}}
	var out strings.Builder
	if err := printTable(&out, res); err != nil {
		t.Fatalf("printTable: %v", err)
	}
	if !regexp.MustCompile(`(?m)^077777\s+Cogollos\s+-\s+-$`).MatchString(out.String()) {
		t.Fatalf("unpriced row must print - for price and unit price:\n%s", out.String())
	}
	if !regexp.MustCompile(`(?m)^062595\s+Leche entera\s+1\.84 EUR\s+-$`).MatchString(out.String()) {
		t.Fatalf("priced row must keep the published price:\n%s", out.String())
	}
}

// S3: a fetch or parse failure exits 1, not 2, and prints nothing on stdout.
func TestFetchErrorExitsOne(t *testing.T) {
	var stdout, stderr bytes.Buffer
	stub := &stubSearcher{err: errors.New("mas: HTTP 403: Forbidden")}
	err := run([]string{"search", "leche", "--json"}, &stdout, &stderr, deps{search: stub})
	if err == nil {
		t.Fatal("expected an error")
	}
	if code := exitCode(err); code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want nothing", stdout.String())
	}
}
