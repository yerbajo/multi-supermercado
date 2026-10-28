package aggregate

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"multisuper/internal/contract"
)

// stub is one chain client. It answers from memory so the suite never touches the
// network, and it counts its calls so a test can prove a chain was asked once.
type stub struct {
	delay time.Duration
	hits  []contract.Hit
	err   error
	block bool // wait for the context deadline instead of answering
	calls atomic.Int32
}

func (s *stub) Search(ctx context.Context, term string, limit int) (contract.Result, error) {
	s.calls.Add(1)
	if s.block {
		<-ctx.Done()
		return contract.Result{}, ctx.Err()
	}
	if s.delay > 0 {
		select {
		case <-time.After(s.delay):
		case <-ctx.Done():
			return contract.Result{}, ctx.Err()
		}
	}
	return contract.Result{Store: "stub", Query: term, Count: len(s.hits), Hits: s.hits}, s.err
}

func hits(n int) []contract.Hit {
	out := make([]contract.Hit, n)
	for i := range out {
		out[i] = contract.Hit{Store: "stub", ID: fmt.Sprint(i), Name: "leche"}
	}
	return out
}

// sixChains returns one stub per chain with three hits each.
func sixChains() ([]Chain, map[string]*stub) {
	names := []string{"alcampo", "aldi", "carrefour", "dia", "lidl", "mas"}
	byName := make(map[string]*stub, len(names))
	chains := make([]Chain, len(names))
	for i, name := range names {
		s := &stub{hits: hits(3)}
		byName[name] = s
		chains[i] = Chain{Store: name, Searcher: s}
	}
	return chains, byName
}

// Six chains asked once each, every answer kept in the order the chains were given.
func TestFindQueriesEveryChainOnceAndSumsTheCounts(t *testing.T) {
	chains, byName := sixChains()

	res := Find(context.Background(), "leche", 20, time.Second, chains)

	if len(res.Results) != 6 {
		t.Fatalf("got %d results, want 6: %+v", len(res.Results), res.Results)
	}
	if res.Query != "leche" {
		t.Errorf("query = %q, want leche", res.Query)
	}
	if res.Count != 18 {
		t.Errorf("count = %d, want 18 (six chains of 3)", res.Count)
	}
	for i, r := range res.Results {
		if r.Store != chains[i].Store {
			t.Errorf("result %d is %q, want %q in the order given", i, r.Store, chains[i].Store)
		}
		if r.Error != nil {
			t.Errorf("%s reported an error with no failure injected: %+v", r.Store, r.Error)
		}
		if r.Count != 3 || len(r.Hits) != 3 {
			t.Errorf("%s count = %d with %d hits, want 3", r.Store, r.Count, len(r.Hits))
		}
		if got := byName[r.Store].calls.Load(); got != 1 {
			t.Errorf("%s was called %d times, want exactly one request per chain", r.Store, got)
		}
	}
}

// Chains really run concurrently: six 100ms answers must finish in about 100ms, not 600.
func TestFindFansOutInParallel(t *testing.T) {
	names := []string{"a", "b", "c", "d", "e", "f"}
	chains := make([]Chain, len(names))
	for i, name := range names {
		chains[i] = Chain{Store: name, Searcher: &stub{delay: 100 * time.Millisecond}}
	}

	start := time.Now()
	Find(context.Background(), "leche", 20, 2*time.Second, chains)
	elapsed := time.Since(start)

	if elapsed > 400*time.Millisecond {
		t.Fatalf("six 100ms chains took %v: they did not run in parallel", elapsed)
	}
}

// A failing chain keeps its error and never voids the chains that answered.
func TestFindKeepsTheOtherChainsWhenOneFails(t *testing.T) {
	chains, byName := sixChains()
	byName["dia"].err = &StatusError{Status: 403, Msg: "dia: search status 403 Forbidden"}
	byName["carrefour"].err = &StatusError{Status: 429, Msg: "carrefour: rate limit"}

	res := Find(context.Background(), "leche", 20, time.Second, chains)

	if res.Count != 12 {
		t.Errorf("count = %d, want 12 from the four chains that answered", res.Count)
	}
	got := make(map[string]StoreResult, len(res.Results))
	for _, r := range res.Results {
		got[r.Store] = r
	}
	for _, name := range []string{"alcampo", "aldi", "lidl", "mas"} {
		if r := got[name]; r.Error != nil || r.Count != 3 {
			t.Errorf("%s = %+v, want 3 hits and no error", name, r)
		}
	}
	for _, name := range []string{"dia", "carrefour"} {
		if got[name].Count != 0 || len(got[name].Hits) != 0 {
			t.Errorf("%s kept hits from a failed search: %+v", name, got[name])
		}
	}
	if e := got["dia"].Error; e == nil || e.Code != CodeBlocked || e.Status != 403 || e.Retryable {
		t.Errorf("dia error = %+v, want blocked/403/not retryable", e)
	}
	if e := got["carrefour"].Error; e == nil || e.Code != CodeQuota || e.Status != 429 || !e.Retryable {
		t.Errorf("carrefour error = %+v, want quota/429/retryable", e)
	}
}

// A chain that overruns the per-chain budget reports timeout and the rest still answer.
func TestFindReportsAChainThatOverrunsTheTimeout(t *testing.T) {
	chains, byName := sixChains()
	byName["alcampo"].block = true

	res := Find(context.Background(), "leche", 20, 20*time.Millisecond, chains)

	if res.Count != 15 {
		t.Errorf("count = %d, want 15 from the five chains that answered", res.Count)
	}
	for _, r := range res.Results {
		if r.Store != "alcampo" {
			if r.Error != nil {
				t.Errorf("%s failed because alcampo timed out: %+v", r.Store, r.Error)
			}
			continue
		}
		if r.Error == nil || r.Error.Code != CodeTimeout || !r.Error.Retryable {
			t.Errorf("alcampo error = %+v, want timeout/retryable", r.Error)
		}
	}
}

// Every chain failing is still a result document, never an empty answer.
func TestFindReportsEveryChainFailing(t *testing.T) {
	chains, byName := sixChains()
	for _, s := range byName {
		s.err = errors.New("chain exploded")
	}

	res := Find(context.Background(), "leche", 20, time.Second, chains)

	if res.Count != 0 {
		t.Errorf("count = %d, want 0", res.Count)
	}
	if len(res.Results) != 6 {
		t.Fatalf("got %d results, want six error objects", len(res.Results))
	}
	for _, r := range res.Results {
		if r.Error == nil || r.Error.Code != CodeUnknown {
			t.Errorf("%s error = %+v, want unknown", r.Store, r.Error)
		}
	}
}

// A chain with no hits still serialises `"hits":[]`, never null: the web app iterates
// the field without a nil check.
func TestFindEncodesEmptyHitsAsAnArray(t *testing.T) {
	chains := []Chain{{Store: "dia", Searcher: &stub{}}}
	res := Find(context.Background(), "leche", 20, time.Second, chains)

	var buf bytes.Buffer
	if err := Encode(&buf, res); err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if !strings.Contains(buf.String(), `"hits": []`) {
		t.Errorf("empty hits are not an array:\n%s", buf.String())
	}
	if strings.Contains(buf.String(), "null") {
		t.Errorf("encoded result carries a null:\n%s", buf.String())
	}
}

// Classify maps every chain failure onto the stable codes the web app switches on.
func TestClassify(t *testing.T) {
	syntax := json.Unmarshal([]byte("{"), &struct{}{})

	tests := []struct {
		name      string
		err       error
		code      string
		status    int
		retryable bool
	}{
		{"403 from mas prose", errors.New(`mas: HTTP 403: <html>Access Denied</html>`), CodeBlocked, 403, false},
		{"403 typed", &StatusError{Status: 403, Msg: "denied"}, CodeBlocked, 403, false},
		{"401 from lidl", errors.New("lidl: HTTP 401: the search API refused the request"), CodeBlocked, 401, false},
		{"429 prose", errors.New("aldi: search unavailable after retries (last status 429 Too Many Requests)"), CodeQuota, 429, true},
		{"429 typed", &StatusError{Status: 429}, CodeQuota, 429, true},
		{"alcampo WAF 202", errors.New("storefront served an AWS WAF challenge (HTTP 202, block)"), CodeQuota, 202, true},
		{"deadline", fmt.Errorf("dia: search request: %w", context.DeadlineExceeded), CodeTimeout, 0, true},
		{"stub deadline", context.DeadlineExceeded, CodeTimeout, 0, true},
		{"dns", errors.New("dia: search request: dial tcp: lookup www.dia.es: no such host"), CodeNetwork, 0, false},
		{"truncated json", fmt.Errorf("lidl: invalid search response: %w", syntax), CodeParse, 0, false},
		{"carrefour 500", errors.New(`carrefour: compare-basket status 500 Internal Server Error: {"detail":"basket_comparison_failed"}`), CodeUnknown, 500, false},
		{"typed 500", &StatusError{Status: 500, Msg: "upstream failed"}, CodeUnknown, 500, false},
		{"chain prose", errors.New("storefront page carries no csrf token"), CodeUnknown, 0, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Classify(tt.err)
			if got.Code != tt.code {
				t.Errorf("code = %q, want %q (%+v)", got.Code, tt.code, got)
			}
			if got.Status != tt.status {
				t.Errorf("status = %d, want %d", got.Status, tt.status)
			}
			if got.Retryable != tt.retryable {
				t.Errorf("retryable = %v, want %v", got.Retryable, tt.retryable)
			}
			if got.Message != tt.err.Error() {
				t.Errorf("message = %q, want the chain error %q", got.Message, tt.err)
			}
		})
	}
}

// Only quota and timeout are worth retrying: a block or a parse failure never is.
func TestRetryableOnlyForQuotaAndTimeout(t *testing.T) {
	for _, code := range []string{CodeQuota, CodeTimeout} {
		if !Classify(&StatusError{Status: 429, Msg: code}).Retryable && code == CodeQuota {
			t.Errorf("%s should be retryable", code)
		}
	}
	for _, err := range []error{
		&StatusError{Status: 403},
		&StatusError{Status: 401},
		errors.New("unknown trouble"),
		errors.New("bad json"),
	} {
		if e := Classify(err); e.Retryable {
			t.Errorf("%q classified as retryable: %+v", err, e)
		}
	}
}
