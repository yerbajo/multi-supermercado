package dia

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"multisuper/internal/contract"
)

// baseItem is the real `search-back` item captured from dia.es, trimmed to the
// fields the CLI reads. Envelope keys (cart, customer, facets, footer) are real
// names but their subtrees are trimmed to empty values in the fixture.
const baseItem = `{
  "brand": "Asturiana",
  "display_name": "Leche entera Asturiana pack 6 x 1.5 L",
  "image": "/product_images/74720P6/74720P6_ISO_0_ES.jpg",
  "l1_category_description": "Huevos, leche y mantequilla",
  "l2_category_description": "Leche",
  "object_id": "74720P6",
  "prices": {
    "currency": "EUR",
    "measure_unit": "LITRO",
    "price": 11.82,
    "price_per_unit": 1.31
  },
  "units_in_stock": 41,
  "url": "/huevos-leche-y-mantequilla/leche/p/74720P6"
}`

func newServer(t *testing.T, handler http.HandlerFunc) (*Client, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	c := NewClient()
	c.BaseURL = srv.URL
	return c, srv
}

func envelope(items string) string {
	return `{"cart":{},"customer":{},"facets":[],"footer":{},"search_items":[` + items + `]}`
}

// newTLSServer starts an h2-capable test server and a client that trusts it, so
// the fingerprint path can be exercised end to end without dialing dia.es.
func newTLSServer(t *testing.T, handler http.HandlerFunc) (*Client, *httptest.Server) {
	t.Helper()
	srv := httptest.NewUnstartedServer(handler)
	srv.EnableHTTP2 = true
	srv.StartTLS()
	t.Cleanup(srv.Close)

	c := NewClient()
	c.BaseURL = srv.URL
	c.RootCAs = x509.NewCertPool()
	c.RootCAs.AddCert(srv.Certificate())
	return c, srv
}

// T10: dia.es sits behind Akamai, which answers 403 to Go's own ClientHello even
// with browser headers. HTTPS must therefore leave through the uTLS Chrome
// fingerprint, not through net/http's own TLS stack.
func TestNewClientUsesChromeFingerprint(t *testing.T) {
	tr, ok := NewClient().HTTP.Transport.(*transport)
	if !ok {
		t.Fatalf("transport = %T, want *dia.transport", NewClient().HTTP.Transport)
	}
	if tr.fingerprint.DialTLSContext == nil {
		t.Fatal("https would go through net/http's own ClientHello, not uTLS")
	}
	if tr.fingerprint.TLSClientConfig != nil {
		t.Fatal("TLSClientConfig would replace the fingerprinted handshake")
	}
	if tr.plain == nil {
		t.Fatal("plain http has no transport")
	}
}

// The fingerprinted transport must stay an HTTP/2 browser: same search, same
// parsed result, but the server has to see HTTP/2.
func TestSearchOverChromeFingerprint(t *testing.T) {
	var proto string
	c, srv := newTLSServer(t, func(w http.ResponseWriter, r *http.Request) {
		proto = r.Proto
		w.Write([]byte(envelope(baseItem)))
	})

	res, err := c.Search(context.Background(), "leche", 20)
	if err != nil {
		t.Fatalf("Search over TLS: %v", err)
	}
	if res.Count != 1 || res.Hits[0].ID != "74720P6" || res.Hits[0].Price != 11.82 {
		t.Fatalf("result = %+v", res)
	}
	if res.Hits[0].URL != srv.URL+"/huevos-leche-y-mantequilla/leche/p/74720P6" {
		t.Fatalf("url = %q", res.Hits[0].URL)
	}
	if proto != "HTTP/2.0" {
		t.Fatalf("server saw %q, want HTTP/2.0", proto)
	}
}

// failFirstListener closes the first accepted connection, so the fingerprinted
// handshake fails exactly once.
type failFirstListener struct {
	net.Listener
	failed bool
}

func (l *failFirstListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	if l.failed {
		return conn, nil
	}
	l.failed = true
	conn.Close()
	return l.Accept()
}

// A broken handshake degrades to the standard transport instead of failing the
// search, and says so on stderr once per client. An HTTP status is not a
// handshake failure, so a 403 still surfaces as an error.
func TestSearchFallsBackWhenHandshakeFails(t *testing.T) {
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(envelope(baseItem)))
	}))
	srv.EnableHTTP2 = true
	srv.Listener = &failFirstListener{Listener: srv.Listener}
	srv.StartTLS()
	t.Cleanup(srv.Close)

	c := NewClient()
	c.BaseURL = srv.URL
	c.RootCAs = x509.NewCertPool()
	c.RootCAs.AddCert(srv.Certificate())
	var stderr strings.Builder
	c.warn = &stderr

	for i := 0; i < 2; i++ {
		res, err := c.Search(context.Background(), "leche", 20)
		if err != nil {
			t.Fatalf("search %d did not fall back: %v", i, err)
		}
		if res.Count != 1 {
			t.Fatalf("search %d: count = %d, want 1", i, res.Count)
		}
	}
	if n := strings.Count(stderr.String(), "falling back"); n != 1 {
		t.Fatalf("fallback logged %d times, want exactly 1: %q", n, stderr.String())
	}
}

// A real answer we dislike is not a transport failure: 403 must be reported.
func TestForbiddenIsNotAFallback(t *testing.T) {
	c, _ := newTLSServer(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "Access Denied", http.StatusForbidden)
	})
	var stderr strings.Builder
	c.warn = &stderr
	_, err := c.Search(context.Background(), "leche", 20)
	if err == nil {
		t.Fatal("expected an error for 403")
	}
	if !strings.Contains(err.Error(), "403") {
		t.Fatalf("err = %v, want the 403 reported", err)
	}
	if stderr.Len() != 0 {
		t.Fatalf("403 must not fall back, logged: %q", stderr.String())
	}
}

func TestSearchMapsRealResponse(t *testing.T) {
	fixture, err := os.ReadFile("testdata/search_leche_entera.json")
	if err != nil {
		t.Fatal(err)
	}

	c, srv := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(fixture)
	})

	res, err := c.Search(context.Background(), "leche entera", 20)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if res.Store != "dia" || res.Query != "leche entera" || res.Count != 1 || len(res.Hits) != 1 {
		t.Fatalf("envelope = %+v", res)
	}
	want := contract.Hit{
		Store:        "dia",
		ID:           "74720P6",
		Name:         "Leche entera Asturiana pack 6 x 1.5 L",
		Brand:        "Asturiana",
		Price:        11.82,
		PricePerUnit: 1.31,
		Unit:         "L",
		Currency:     "EUR",
		Available:    true,
		Category:     "Leche",
		URL:          srv.URL + "/huevos-leche-y-mantequilla/leche/p/74720P6",
		Image:        srv.URL + "/product_images/74720P6/74720P6_ISO_0_ES.jpg",
	}
	if got := res.Hits[0]; got != want {
		t.Fatalf("hit =\n%+v\nwant\n%+v", got, want)
	}
}

func TestHitMapping(t *testing.T) {
	tests := []struct {
		name string
		item string
		want contract.Hit
	}{
		{
			name: "kilo",
			item: strings.Replace(strings.Replace(baseItem, "LITRO", "KILO", 1), "11.82", "2.45", 1),
			want: contract.Hit{Store: "dia", ID: "74720P6", Name: "Leche entera Asturiana pack 6 x 1.5 L",
				Brand: "Asturiana", Price: 2.45, PricePerUnit: 1.31, Unit: "kg", Currency: "EUR",
				Available: true, Category: "Leche",
				URL:   "https://www.dia.es/huevos-leche-y-mantequilla/leche/p/74720P6",
				Image: "https://www.dia.es/product_images/74720P6/74720P6_ISO_0_ES.jpg"},
		},
		{
			name: "unidad",
			item: strings.Replace(baseItem, `"LITRO"`, `"UNIDAD"`, 1),
			want: contract.Hit{Store: "dia", ID: "74720P6", Name: "Leche entera Asturiana pack 6 x 1.5 L",
				Brand: "Asturiana", Price: 11.82, PricePerUnit: 1.31, Unit: "unit", Currency: "EUR",
				Available: true, Category: "Leche",
				URL:   "https://www.dia.es/huevos-leche-y-mantequilla/leche/p/74720P6",
				Image: "https://www.dia.es/product_images/74720P6/74720P6_ISO_0_ES.jpg"},
		},
		{
			name: "unknown measure unit stays empty",
			item: strings.Replace(baseItem, `"LITRO"`, `"GRAMO"`, 1),
			want: contract.Hit{Store: "dia", ID: "74720P6", Name: "Leche entera Asturiana pack 6 x 1.5 L",
				Brand: "Asturiana", Price: 11.82, PricePerUnit: 1.31, Unit: "", Currency: "EUR",
				Available: true, Category: "Leche",
				URL:   "https://www.dia.es/huevos-leche-y-mantequilla/leche/p/74720P6",
				Image: "https://www.dia.es/product_images/74720P6/74720P6_ISO_0_ES.jpg"},
		},
		{
			name: "out of stock",
			item: strings.Replace(baseItem, `"units_in_stock": 41`, `"units_in_stock": 0`, 1),
			want: contract.Hit{Store: "dia", ID: "74720P6", Name: "Leche entera Asturiana pack 6 x 1.5 L",
				Brand: "Asturiana", Price: 11.82, PricePerUnit: 1.31, Unit: "L", Currency: "EUR",
				Available: false, Category: "Leche",
				URL:   "https://www.dia.es/huevos-leche-y-mantequilla/leche/p/74720P6",
				Image: "https://www.dia.es/product_images/74720P6/74720P6_ISO_0_ES.jpg"},
		},
		{
			name: "no prices",
			item: `{"brand":"Dia","display_name":"Bolsa reutilizable","object_id":"X1",
			       "l2_category_description":"Bazar","units_in_stock":5,"url":"/bazar/p/X1","image":"/img/X1.jpg"}`,
			want: contract.Hit{Store: "dia", ID: "X1", Name: "Bolsa reutilizable", Brand: "Dia",
				Price: 0, PricePerUnit: 0, Unit: "", Currency: "", Available: true,
				Category: "Bazar", URL: "https://www.dia.es/bazar/p/X1",
				Image: "https://www.dia.es/img/X1.jpg"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := newServer(t, func(w http.ResponseWriter, r *http.Request) {
				w.Write([]byte(envelope(tc.item)))
			})
			res, err := c.Search(context.Background(), "x", 20)
			if err != nil {
				t.Fatalf("Search: %v", err)
			}
			if len(res.Hits) != 1 || res.Count != 1 {
				t.Fatalf("hits = %+v", res)
			}
			// Relative product paths resolve against the client base, not the live site.
			want := tc.want
			want.URL = strings.Replace(want.URL, DefaultBaseURL, c.BaseURL, 1)
			want.Image = strings.Replace(want.Image, DefaultBaseURL, c.BaseURL, 1)
			if got := res.Hits[0]; got != want {
				t.Fatalf("hit =\n%+v\nwant\n%+v", got, want)
			}
		})
	}
}

func TestSearchEmptyResultsIsNotAnError(t *testing.T) {
	c, _ := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(envelope("")))
	})
	res, err := c.Search(context.Background(), "zzzzz", 20)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if res.Count != 0 || res.Hits == nil || len(res.Hits) != 0 {
		t.Fatalf("hits = %#v", res.Hits)
	}
	var buf strings.Builder
	if err := contract.Encode(&buf, res); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), `"hits": []`) {
		t.Fatalf("empty hits must encode as [], got:\n%s", buf.String())
	}
}

func TestSearchFailures(t *testing.T) {
	tests := []struct {
		name    string
		handler http.HandlerFunc
	}{
		{"not found", func(w http.ResponseWriter, r *http.Request) { http.Error(w, "nope", http.StatusNotFound) }},
		{"server error", func(w http.ResponseWriter, r *http.Request) { http.Error(w, "boom", http.StatusInternalServerError) }},
		{"malformed json", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{"search_items":`)) }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := newServer(t, tc.handler)
			if _, err := c.Search(context.Background(), "leche", 20); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestSearchHonoursRetryAfter(t *testing.T) {
	var calls int
	c, _ := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.Write([]byte(envelope(baseItem)))
	})

	start := time.Now()
	res, err := c.Search(context.Background(), "leche", 20)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if res.Count != 1 || calls != 2 {
		t.Fatalf("count=%d calls=%d", res.Count, calls)
	}
	if elapsed < 900*time.Millisecond {
		t.Fatalf("Retry-After not honoured, elapsed=%v", elapsed)
	}
}

func TestSearchGivesUpAfterThreeAttempts(t *testing.T) {
	var calls int
	c, _ := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Retry-After", "0")
		w.WriteHeader(http.StatusServiceUnavailable)
	})
	_, err := c.Search(context.Background(), "leche", 20)
	if err == nil {
		t.Fatal("expected error after retries")
	}
	if calls != 3 {
		t.Fatalf("calls = %d, want 3", calls)
	}
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v, want ErrUnavailable", err)
	}
}

// S3: the client rejects an out-of-range limit instead of clamping it, and the
// rejected call must not reach the network.
func TestSearchRejectsLimitOutOfRange(t *testing.T) {
	for _, limit := range []int{-1, 0, MaxLimit + 1, 1000} {
		var called bool
		c, _ := newServer(t, func(w http.ResponseWriter, r *http.Request) {
			called = true
			w.Write([]byte(envelope(baseItem)))
		})
		if _, err := c.Search(context.Background(), "leche", limit); err == nil {
			t.Errorf("limit %d: expected an error, got nil", limit)
		}
		if called {
			t.Errorf("limit %d: request was performed", limit)
		}
	}
}

// A valid limit still truncates the response, as before.
func TestSearchTruncatesToValidLimit(t *testing.T) {
	items := strings.Join([]string{baseItem, baseItem, baseItem}, ",")
	c, _ := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(envelope(items)))
	})
	res, err := c.Search(context.Background(), "leche", 2)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if res.Count != 2 || len(res.Hits) != 2 {
		t.Fatalf("count=%d hits=%d, want 2", res.Count, len(res.Hits))
	}
}

func TestSearchRequestShape(t *testing.T) {
	var gotPath, gotQuery, gotUA, gotAccept string
	c, _ := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.Query().Get("q")
		gotUA = r.Header.Get("User-Agent")
		gotAccept = r.Header.Get("Accept")
		w.Write([]byte(envelope(baseItem)))
	})
	if _, err := c.Search(context.Background(), "leche entera", 20); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/api/v1/search-back/search" {
		t.Fatalf("path = %q", gotPath)
	}
	if gotQuery != "leche entera" {
		t.Fatalf("q = %q", gotQuery)
	}
	if !strings.Contains(gotUA, "Chrome/") {
		t.Fatalf("user-agent = %q", gotUA)
	}
	if gotAccept != "application/json" {
		t.Fatalf("accept = %q", gotAccept)
	}
}

// The aggregator parses one shape for every chain, so the JSON keys are part of
// the public API and must not drift.
func TestJSONFieldNames(t *testing.T) {
	c, _ := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(envelope(baseItem)))
	})
	res, err := c.Search(context.Background(), "leche", 20)
	if err != nil {
		t.Fatal(err)
	}
	var buf strings.Builder
	if err := contract.Encode(&buf, res); err != nil {
		t.Fatal(err)
	}

	var doc map[string]json.RawMessage
	if err := json.Unmarshal([]byte(buf.String()), &doc); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"store", "query", "count", "hits"} {
		if _, ok := doc[k]; !ok {
			t.Errorf("envelope missing %q", k)
		}
	}
	var hits []map[string]json.RawMessage
	if err := json.Unmarshal(doc["hits"], &hits); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"store", "id", "name", "brand", "price", "pricePerUnit", "unit", "currency", "available", "category", "url", "image"} {
		if _, ok := hits[0][k]; !ok {
			t.Errorf("hit missing %q", k)
		}
	}

	// The fixture must keep the real envelope field names the parser relies on.
	fixture, err := os.ReadFile("testdata/search_leche_entera.json")
	if err != nil {
		t.Fatal(err)
	}
	var env map[string]json.RawMessage
	if err := json.Unmarshal(fixture, &env); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"cart", "customer", "facets", "footer", "search_items"} {
		if _, ok := env[k]; !ok {
			t.Errorf("fixture missing envelope key %q", k)
		}
	}
}
