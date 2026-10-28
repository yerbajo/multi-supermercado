package main

import (
	"bytes"
	"context"
	"errors"
	"regexp"
	"strings"
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
