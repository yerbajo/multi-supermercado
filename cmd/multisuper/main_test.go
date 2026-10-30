package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync/atomic"
	"testing"

	"multisuper/internal/aggregate"
	"multisuper/internal/contract"
)

// stub is one chain client that answers from memory, so the suite stays offline and
// can prove whether the command called it at all.
type stub struct {
	hits  []contract.Hit
	err   error
	block bool
	calls atomic.Int32
}

func (s *stub) Search(ctx context.Context, term string, limit int) (contract.Result, error) {
	s.calls.Add(1)
	if s.block {
		<-ctx.Done()
		return contract.Result{}, ctx.Err()
	}
	return contract.Result{Store: "stub", Query: term, Count: len(s.hits), Hits: s.hits}, s.err
}

func hit(id string, perUnit float64, unit string) contract.Hit {
	return contract.Hit{Store: "stub", ID: id, Name: "leche " + id, PricePerUnit: perUnit, Unit: unit}
}

var storeNames = []string{"alcampo", "aldi", "carrefour", "dia", "lidl", "mas"}

// stubChains returns one stub per chain, all answering with the given hits.
func stubChains(hs []contract.Hit) ([]aggregate.Chain, map[string]*stub) {
	byName := make(map[string]*stub, len(storeNames))
	out := make([]aggregate.Chain, len(storeNames))
	for i, name := range storeNames {
		s := &stub{hits: hs}
		byName[name] = s
		out[i] = aggregate.Chain{Store: name, Searcher: s}
	}
	return out, byName
}

// decodeOne fails unless stdout holds exactly one JSON document that decodes into the
// aggregate result without unknown fields, which is what the web app requires.
func decodeOne(t *testing.T, stdout string) aggregate.Result {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(stdout))
	dec.DisallowUnknownFields()
	var res aggregate.Result
	if err := dec.Decode(&res); err != nil {
		t.Fatalf("stdout is not the aggregate JSON document: %v\n%s", err, stdout)
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		t.Fatalf("stdout carries more than one JSON document (%v):\n%s", err, stdout)
	}
	return res
}

func resultFor(t *testing.T, res aggregate.Result, store string) aggregate.StoreResult {
	t.Helper()
	for _, r := range res.Results {
		if r.Store == store {
			return r
		}
	}
	t.Fatalf("no result for %q in %+v", store, res.Results)
	return aggregate.StoreResult{}
}

// All six chains answering: six results, no errors, count is the sum of the hit counts.
func TestFindAllChainsSucceed(t *testing.T) {
	all, _ := stubChains([]contract.Hit{hit("1", 1.29, "L")})

	var stdout, stderr strings.Builder
	code := run([]string{"find", "leche", "--json"}, &stdout, &stderr, all)

	if code != 0 {
		t.Fatalf("exit = %d, want 0: %s", code, stderr.String())
	}
	res := decodeOne(t, stdout.String())
	if res.Query != "leche" || res.Count != 6 || len(res.Results) != 6 {
		t.Fatalf("got query=%q count=%d results=%d, want leche/6/6", res.Query, res.Count, len(res.Results))
	}
	for _, r := range res.Results {
		if r.Error != nil || r.Count != 1 {
			t.Errorf("%s = %+v, want 1 hit and no error", r.Store, r)
		}
	}
}

// One chain failing does not stop the others and does not change the exit code.
func TestFindOneChainFailsAndStillExitsZero(t *testing.T) {
	all, byName := stubChains([]contract.Hit{hit("1", 1.29, "L")})
	byName["dia"].err = &aggregate.StatusError{Status: 403, Msg: "dia: search status 403 Forbidden"}
	byName["carrefour"].err = &aggregate.StatusError{Status: 429, Msg: "carrefour: rate limit"}

	var stdout, stderr strings.Builder
	code := run([]string{"find", "leche", "--json"}, &stdout, &stderr, all)

	if code != 0 {
		t.Fatalf("exit = %d, want 0 while four chains answered: %s", code, stderr.String())
	}
	res := decodeOne(t, stdout.String())
	if res.Count != 4 {
		t.Errorf("count = %d, want 4", res.Count)
	}
	if r := resultFor(t, res, "dia"); r.Error == nil || r.Error.Code != aggregate.CodeBlocked || r.Error.Status != 403 {
		t.Errorf("dia = %+v, want a blocked error with status 403", r)
	}
	if r := resultFor(t, res, "carrefour"); r.Error == nil || r.Error.Code != aggregate.CodeQuota || !r.Error.Retryable {
		t.Errorf("carrefour = %+v, want a retryable quota error", r)
	}
	for _, name := range []string{"alcampo", "aldi", "lidl", "mas"} {
		if r := resultFor(t, res, name); r.Error != nil || r.Count != 1 {
			t.Errorf("%s = %+v, want 1 hit and no error", name, r)
		}
	}
}

// A chain that hangs past --timeout reports timeout/retryable and the rest still answer.
func TestFindReportsATimedOutChain(t *testing.T) {
	all, byName := stubChains([]contract.Hit{hit("1", 1.29, "L")})
	byName["alcampo"].block = true

	var stdout, stderr strings.Builder
	code := run([]string{"find", "leche", "--timeout", "20ms", "--json"}, &stdout, &stderr, all)

	if code != 0 {
		t.Fatalf("exit = %d, want 0 while five chains answered: %s", code, stderr.String())
	}
	res := decodeOne(t, stdout.String())
	if res.Count != 5 {
		t.Errorf("count = %d, want 5", res.Count)
	}
	if r := resultFor(t, res, "alcampo"); r.Error == nil || r.Error.Code != aggregate.CodeTimeout || !r.Error.Retryable {
		t.Errorf("alcampo = %+v, want a retryable timeout", r)
	}
}

// Every chain failing exits 1 and still prints one document with six error objects.
func TestFindEveryChainFailingExitsOne(t *testing.T) {
	all, byName := stubChains(nil)
	for _, s := range byName {
		s.err = errors.New("chain exploded")
	}

	var stdout, stderr strings.Builder
	code := run([]string{"find", "leche", "--json"}, &stdout, &stderr, all)

	if code != 1 {
		t.Fatalf("exit = %d, want 1 when every chain failed", code)
	}
	if strings.TrimSpace(stdout.String()) == "" {
		t.Fatal("stdout is empty: a total failure is still a result document")
	}
	res := decodeOne(t, stdout.String())
	if len(res.Results) != 6 {
		t.Fatalf("got %d results, want six error objects", len(res.Results))
	}
	for _, r := range res.Results {
		if r.Error == nil {
			t.Errorf("%s carries no error", r.Store)
		}
	}
}

// --only queries exactly the named chains and never touches the others.
func TestFindOnlyCallsTheRequestedChains(t *testing.T) {
	all, byName := stubChains([]contract.Hit{hit("1", 1.29, "L")})

	var stdout, stderr strings.Builder
	code := run([]string{"find", "leche", "--only", "mas,lidl", "--json"}, &stdout, &stderr, all)

	if code != 0 {
		t.Fatalf("exit = %d, want 0: %s", code, stderr.String())
	}
	res := decodeOne(t, stdout.String())
	if len(res.Results) != 2 {
		t.Fatalf("got %d results, want 2", len(res.Results))
	}
	if res.Results[0].Store != "lidl" || res.Results[1].Store != "mas" {
		t.Errorf("results = %q,%q, want lidl,mas in table order", res.Results[0].Store, res.Results[1].Store)
	}
	for name, s := range byName {
		want := int32(0)
		if name == "lidl" || name == "mas" {
			want = 1
		}
		if got := s.calls.Load(); got != want {
			t.Errorf("%s was called %d times, want %d", name, got, want)
		}
	}
}

// Without --json the table has one row per chain, shows the failure code, and prints
// the cheapest published €/unit with its unit.
func TestFindTablePrintsOneRowPerChain(t *testing.T) {
	all, byName := stubChains([]contract.Hit{
		hit("1", 1.29, "L"),
		hit("2", 2.10, "kg"),
	})
	byName["dia"].err = &aggregate.StatusError{Status: 403, Msg: "dia: search status 403 Forbidden"}

	var stdout, stderr strings.Builder
	code := run([]string{"find", "leche"}, &stdout, &stderr, all)

	if code != 0 {
		t.Fatalf("exit = %d, want 0: %s", code, stderr.String())
	}
	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	if len(lines) != 7 {
		t.Fatalf("got %d table lines, want a header plus six rows:\n%s", len(lines), stdout.String())
	}
	for _, want := range []string{"STORE", "ESTADO", "HITS", "MEJOR €/UNIDAD", "MS"} {
		if !strings.Contains(lines[0], want) {
			t.Errorf("header %q is missing %q", lines[0], want)
		}
	}
	if !strings.Contains(stdout.String(), aggregate.CodeBlocked) {
		t.Errorf("table does not show the failed chain's code:\n%s", stdout.String())
	}
	// cheapest €/unit wins, and its unit is printed so kg and L are not confused.
	if strings.Count(stdout.String(), "1.29 €/L") != 5 {
		t.Errorf("table does not show the cheapest €/unit for the five chains that answered:\n%s", stdout.String())
	}
}

// A chain whose hits publish no €/unit shows "-": the table never invents a unit price.
func TestFindTablePrintsDashWithoutUnitPrice(t *testing.T) {
	all, _ := stubChains([]contract.Hit{{Store: "stub", ID: "1", Name: "leche suelta"}})

	var stdout, stderr strings.Builder
	code := run([]string{"find", "leche"}, &stdout, &stderr, all)

	if code != 0 {
		t.Fatalf("exit = %d, want 0: %s", code, stderr.String())
	}
	for _, row := range strings.Split(strings.TrimSpace(stdout.String()), "\n")[1:] {
		if strings.Contains(row, "€/") {
			t.Errorf("table invented a €/unit for hits that publish none:\n%s", stdout.String())
		}
	}
	// header + six rows, one "-" per row.
	if got := strings.Count(stdout.String(), "-"); got != 6 {
		t.Errorf("table shows %d dashes, want 6 (one per chain):\n%s", got, stdout.String())
	}
}

// Bad invocations exit 2 and never reach the network.
func TestFindRejectsBadUsage(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{"no command", nil},
		{"unknown command", []string{"search", "leche"}},
		{"missing term", []string{"find"}},
		{"limit zero", []string{"find", "leche", "--limit", "0"}},
		{"limit too high", []string{"find", "leche", "--limit", "101"}},
		{"limit not a number", []string{"find", "leche", "--limit", "muchos"}},
		{"zero timeout", []string{"find", "leche", "--timeout", "0s"}},
		{"negative timeout", []string{"find", "leche", "--timeout", "-1s"}},
		{"unknown chain", []string{"find", "leche", "--only", "mercadona"}},
		{"unknown flag", []string{"find", "leche", "--bogus"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			all, byName := stubChains([]contract.Hit{hit("1", 1.29, "L")})
			var stdout, stderr strings.Builder
			if code := run(tt.args, &stdout, &stderr, all); code != 2 {
				t.Errorf("exit = %d, want 2", code)
			}
			if strings.TrimSpace(stdout.String()) != "" {
				t.Errorf("usage errors must not write to stdout: %q", stdout.String())
			}
			for name, s := range byName {
				if got := s.calls.Load(); got != 0 {
					t.Errorf("%s was queried on a usage error (%d calls)", name, got)
				}
			}
		})
	}
}

// Asking for help is not an error, in any position (the sibling binaries' convention).
func TestHelpExitsZero(t *testing.T) {
	for _, args := range [][]string{{"--help"}, {"-h"}, {"help"}, {"find", "--help"}} {
		all, _ := stubChains(nil)
		var stdout, stderr strings.Builder
		if code := run(args, &stdout, &stderr, all); code != 0 {
			t.Errorf("%v: exit = %d, want 0", args, code)
		}
		if !strings.Contains(stdout.String(), "multisuper find <term>") {
			t.Errorf("%v: help does not show the usage line: %q", args, stdout.String())
		}
	}
}

// The per-chain budget is passed through, not silently defaulted: a chain that needs
// longer than a millisecond is reported as a timeout.
func TestTimeoutFlagIsHonoured(t *testing.T) {
	all, byName := stubChains([]contract.Hit{hit("1", 1.29, "L")})
	for _, s := range byName {
		s.block = true
	}

	var stdout, stderr strings.Builder
	code := run([]string{"find", "leche", "--timeout", "10ms", "--json"}, &stdout, &stderr, all)

	if code != 1 {
		t.Fatalf("exit = %d, want 1 when every chain timed out", code)
	}
	res := decodeOne(t, stdout.String())
	for _, r := range res.Results {
		if r.Error == nil || r.Error.Code != aggregate.CodeTimeout || !r.Error.Retryable {
			t.Errorf("%s = %+v, want a retryable timeout", r.Store, r)
		}
	}
	if res.Results[0].ElapsedMs < 0 {
		t.Errorf("elapsedMs is negative: %d", res.Results[0].ElapsedMs)
	}
}
