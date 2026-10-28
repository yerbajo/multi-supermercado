package carrefour

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"multisuper/internal/contract"
)

func newServer(t *testing.T, handler http.HandlerFunc) (*Client, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	c := NewClient()
	c.BaseURL = srv.URL
	return c, srv
}

func fixture(t *testing.T) []byte {
	t.Helper()
	raw, err := os.ReadFile("testdata/compare_basket_leche_entera.json")
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// Superfudis caps a request at 10 items and 3 requests/minute and forbids bulk
// extraction, so one search is exactly one POST carrying a single item and the
// Carrefour retailer filter.
func TestSearchPostsExactlyOneItemOnce(t *testing.T) {
	var got *http.Request
	var body []byte
	requests := 0
	c, _ := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		requests++
		got = r
		body, _ = io.ReadAll(r.Body)
		w.Write([]byte(`{"status":"ok","items":[]}`))
	})

	if _, err := c.Search(context.Background(), "leche entera", 20); err != nil {
		t.Fatalf("Search: %v", err)
	}
	if requests != 1 {
		t.Fatalf("requests = %d, want exactly 1: the API allows 3 per minute", requests)
	}
	if got.Method != http.MethodPost || got.URL.Path != "/api/v1/compare-basket" {
		t.Fatalf("%s %s, want POST /api/v1/compare-basket", got.Method, got.URL.Path)
	}
	for header, want := range map[string]string{
		"Content-Type": "application/json",
		"Accept":       "application/json",
	} {
		if v := got.Header.Get(header); v != want {
			t.Errorf("%s = %q, want %q", header, v, want)
		}
	}

	const want = `{"items":[{"query":"leche entera","quantity":1}],"retailers":["Carrefour"]}`
	if string(body) != want {
		t.Fatalf("body = %s\nwant  %s", body, want)
	}
}

// The fixture is the real (untrimmed) Superfudis answer for "leche entera",
// captured from POST /api/v1/compare-basket with retailers:["Carrefour"].
func TestSearchMapsCapturedResponse(t *testing.T) {
	c, _ := newServer(t, func(w http.ResponseWriter, r *http.Request) { w.Write(fixture(t)) })

	res, err := c.Search(context.Background(), "leche entera", 20)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if res.Store != "carrefour" || res.Query != "leche entera" || res.Count != 1 || len(res.Hits) != 1 {
		t.Fatalf("envelope = %+v", res)
	}

	want := contract.Hit{
		Store:     "carrefour",
		ID:        "8410297110979",
		Name:      "Leche entera Central Lechera Asturiana botella 2,2 l.",
		Brand:     "central lechera asturiana",
		Price:     ptr(2.75),
		Currency:  "EUR",
		Available: true,
		// 2,75 € for 2200 ml is 1,25 €/L, derived from the published sizes only.
		PricePerUnit: 1.25,
		Unit:         "L",
		// Superfudis reports the carrefour.es page it matched; the aggregator links
		// it, so the offer stays reachable without us fetching the blocked site.
		URL: "https://www.carrefour.es/supermercado/leche-entera-central-lechera-asturiana-botella-22-l/R-785113055/p",
	}
	if !sameHit(res.Hits[0], want) {
		t.Fatalf("hit = %+v\nwant  %+v", res.Hits[0], want)
	}
}

// sameHit compares hits by value: Price is a pointer, so == would compare addresses.
func sameHit(a, b contract.Hit) bool {
	if (a.Price == nil) != (b.Price == nil) {
		return false
	}
	if a.Price != nil && *a.Price != *b.Price {
		return false
	}
	a.Price, b.Price = nil, nil
	return a == b
}

// S8: an offer Superfudis reports without price_eur must omit both keys instead of
// serialising 0,00 €.
func TestSearchOmitsAnUnpublishedPrice(t *testing.T) {
	body := `{"status":"ok","items":[{"query":"leche","offers":[{"retailer":"Carrefour","name":"A","ean":"1","match_type":"exact_ean"}]}]}`
	c, _ := newServer(t, func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(body)) })
	res, err := c.Search(context.Background(), "leche entera", 20)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	var buf strings.Builder
	if err := contract.Encode(&buf, res); err != nil {
		t.Fatalf("Encode: %v", err)
	}
	for _, key := range []string{`"price":`, `"currency":`} {
		if strings.Contains(buf.String(), key) {
			t.Errorf("unpriced hit must omit %s:\n%s", key, buf.String())
		}
	}
}

// status != "ok" is a parse failure, never an empty result that hides a broken source.
func TestSearchRejectsNonOkStatus(t *testing.T) {
	c, _ := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"status":"error","detail":"no match"}`))
	})
	_, err := c.Search(context.Background(), "leche", 20)
	if err == nil || !strings.Contains(err.Error(), `status "error"`) {
		t.Fatalf("err = %v, want a status error", err)
	}
}

func TestSearchRejectsMalformedJSON(t *testing.T) {
	c, _ := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`<html>502</html>`))
	})
	if _, err := c.Search(context.Background(), "leche", 20); err == nil {
		t.Fatal("Search succeeded on a non-JSON body")
	}
}

// The quota is 3 requests/minute: one 429 gets one retry, honouring Retry-After.
func TestSearchRetries429OnceAfterRetryAfter(t *testing.T) {
	attempts := 0
	c, _ := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts == 1 {
			w.Header().Set("Retry-After", "2")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.Write(fixture(t))
	})
	var waits []time.Duration
	c.Sleep = func(_ context.Context, d time.Duration) error {
		waits = append(waits, d)
		return nil
	}

	res, err := c.Search(context.Background(), "leche entera", 20)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if res.Count != 1 {
		t.Fatalf("count = %d, want the retried response", res.Count)
	}
	if attempts != 2 {
		t.Fatalf("attempts = %d, want 2", attempts)
	}
	if len(waits) != 1 || waits[0] != 2*time.Second {
		t.Fatalf("waits = %v, want one 2s wait", waits)
	}
}

// A persistent 429 stops after the single allowed retry and names the quota.
func TestSearchGivesUpAfterOneRetry(t *testing.T) {
	attempts := 0
	c, _ := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.Header().Set("Retry-After", "1")
		w.WriteHeader(http.StatusTooManyRequests)
	})
	c.Sleep = func(context.Context, time.Duration) error { return nil }

	_, err := c.Search(context.Background(), "leche", 20)
	if err == nil || !strings.Contains(err.Error(), "3 requests/minute") {
		t.Fatalf("err = %v, want the quota in the message", err)
	}
	if attempts != 2 {
		t.Fatalf("attempts = %d, want 2 (one retry at most)", attempts)
	}
}

// When the server asks for a longer pause than the CLI is willing to block for, it
// reports the quota instead of retrying earlier than asked.
func TestSearchDoesNotRetryEarlierThanASlowRetryAfter(t *testing.T) {
	attempts := 0
	c, _ := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.Header().Set("Retry-After", "600")
		w.WriteHeader(http.StatusTooManyRequests)
	})
	slept := false
	c.Sleep = func(context.Context, time.Duration) error { slept = true; return nil }

	_, err := c.Search(context.Background(), "leche", 20)
	if err == nil {
		t.Fatal("Search succeeded, want an error")
	}
	if attempts != 1 || slept {
		t.Fatalf("attempts = %d, slept = %t, want one attempt and no retry", attempts, slept)
	}
}

// A sleeping Superfudis (free-tier Render host) is a fetch error whose message says
// the 45s timeout, so the operator knows why a cold start failed.
func TestSearchReportsTheTimeoutOnNetworkFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close() // nothing is listening any more

	c := NewClient()
	c.BaseURL = url
	_, err := c.Search(context.Background(), "leche", 20)
	if err == nil {
		t.Fatal("Search succeeded against a closed server")
	}
	for _, want := range []string{"45s", "Superfudis"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err = %v, want it to mention %q", err, want)
		}
	}
}

// --limit is applied to the offers list client-side: the request never asks for more.
func TestSearchTrimsHitsToLimit(t *testing.T) {
	body := `{"status":"ok","items":[{"query":"leche","offers":[
		{"retailer":"Carrefour","name":"A","ean":"1","price_eur":1,"match_type":"exact_ean"},
		{"retailer":"Carrefour","name":"B","ean":"2","price_eur":2,"match_type":"exact_ean"},
		{"retailer":"Carrefour","name":"C","ean":"3","price_eur":3,"match_type":"exact_ean"}]}]}`
	c, _ := newServer(t, func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(body)) })

	res, err := c.Search(context.Background(), "leche", 2)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if res.Count != 2 || len(res.Hits) != 2 || res.Hits[1].ID != "2" {
		t.Fatalf("hits = %+v", res.Hits)
	}
}

// Two offers of the same item must never collide on the contract ID.
func TestOfferIDsFallBackToMatchTypeAndIndex(t *testing.T) {
	body := `{"status":"ok","items":[{"query":"leche","offers":[
		{"retailer":"Carrefour","name":"A","price_eur":1,"match_type":"name"},
		{"retailer":"Carrefour","name":"B","price_eur":2,"match_type":"name"}]}]}`
	c, _ := newServer(t, func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(body)) })

	res, err := c.Search(context.Background(), "leche", 20)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if got := res.Hits[0].ID; got != "name-0" {
		t.Errorf("id[0] = %q, want name-0", got)
	}
	if got := res.Hits[1].ID; got != "name-1" {
		t.Errorf("id[1] = %q, want name-1", got)
	}
	if res.Hits[0].ID == res.Hits[1].ID {
		t.Fatalf("ids collide: %q", res.Hits[0].ID)
	}
}

// No match is not an error: the envelope stays valid with an empty hits array.
func TestSearchReturnsAnEmptyEnvelopeWhenNothingMatches(t *testing.T) {
	c, _ := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"status":"ok","items":[{"query":"xyz","offers":[]}]}`))
	})

	res, err := c.Search(context.Background(), "xyz", 20)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if res.Count != 0 || len(res.Hits) != 0 {
		t.Fatalf("result = %+v, want no hits", res)
	}

	var buf strings.Builder
	if err := contract.Encode(&buf, res); err != nil {
		t.Fatalf("Encode: %v", err)
	}
	var back map[string]json.RawMessage
	if err := json.Unmarshal([]byte(buf.String()), &back); err != nil {
		t.Fatalf("encoded result is not JSON: %v", err)
	}
	if string(back["hits"]) != "[]" {
		t.Fatalf("hits = %s, want [] so the aggregator iterates instead of null-checking", back["hits"])
	}
}

// Non-2xx answers that are not 429 are fetch errors carrying the body snippet.
func TestSearchReportsUnexpectedStatus(t *testing.T) {
	c, _ := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		w.Write([]byte("upstream down"))
	})
	_, err := c.Search(context.Background(), "leche", 20)
	if err == nil || !strings.Contains(err.Error(), "502") || !strings.Contains(err.Error(), "upstream down") {
		t.Fatalf("err = %v, want the status and the body", err)
	}
}
