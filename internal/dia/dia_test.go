package dia

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"multisuper/internal/contract"
)

func ptr(f float64) *float64 { return &f }

// sameHit compares hits by value: Price and Available are pointers, so == would compare
// addresses.
func sameHit(a, b contract.Hit) bool {
	if (a.Price == nil) != (b.Price == nil) {
		return false
	}
	if a.Price != nil && *a.Price != *b.Price {
		return false
	}
	if (a.Available == nil) != (b.Available == nil) {
		return false
	}
	if a.Available != nil && *a.Available != *b.Available {
		return false
	}
	a.Price, b.Price = nil, nil
	a.Available, b.Available = nil, nil
	return a == b
}

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

// The Android app's /reduced endpoint answers plain curl 5 times out of 5, so the
// client must leave through the standard library. A custom Transport (the uTLS
// fingerprint and its fallback) must not creep back.
func TestNewClientUsesTheStandardTransport(t *testing.T) {
	if tr := NewClient().HTTP.Transport; tr != nil {
		t.Fatalf("HTTP.Transport = %T, want nil (net/http's own TLS stack)", tr)
	}
	if c := NewClient().HTTP; c.Timeout != 20*time.Second {
		t.Fatalf("timeout = %v, want 20s", c.Timeout)
	}
}

// readFixture returns the trimmed real /reduced response captured from dia.es.
func readFixture(t *testing.T) []byte {
	t.Helper()
	fixture, err := os.ReadFile("testdata/search_leche_entera.json")
	if err != nil {
		t.Fatal(err)
	}
	return fixture
}

func TestSearchMapsTrimmedReducedResponse(t *testing.T) {
	c, srv := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write(readFixture(t))
	})

	res, err := c.Search(context.Background(), "leche", 20)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if res.Store != "dia" || res.Query != "leche" || res.Count != 3 || len(res.Hits) != 3 {
		t.Fatalf("envelope = %+v", res)
	}
	want := contract.Hit{
		Store:        "dia",
		ID:           "504P6",
		Name:         "Leche semidesnatada Dia Láctea pack 6 x 1 L",
		Brand:        "Dia Láctea",
		Price:        ptr(4.98),
		PricePerUnit: 0.83,
		Unit:         "L",
		Currency:     "EUR",
		Available:    contract.Bool(true),
		Category:     "Leche",
		URL:          srv.URL + "/huevos-leche-y-mantequilla/leche/p/504P6",
		Image:        srv.URL + "/product_images/504P6/504P6_ISO_0_ES.jpg",
	}
	if got := res.Hits[0]; !sameHit(got, want) {
		t.Fatalf("hit =\n%+v\nwant\n%+v", got, want)
	}
	// price_per_unit and measure_unit are copied, never recomputed (S8).
	if h := res.Hits[1]; h.PricePerUnit != 0.96 || h.Unit != "L" || h.Price == nil || *h.Price != 5.76 {
		t.Fatalf("second hit = %+v", h)
	}
	if h := res.Hits[2]; h.ID != "130063P6" || h.Category != "Leche sin lactosa y enriquecidas" {
		t.Fatalf("third hit = %+v", h)
	}
}

// S8: an item the store answered without a prices object keeps price, currency and
// unit absent instead of reading as 0,00 EUR.
func TestSearchMapsRealItemWithoutPrices(t *testing.T) {
	var doc map[string]any
	if err := json.Unmarshal(readFixture(t), &doc); err != nil {
		t.Fatal(err)
	}
	items := doc["search_items"].([]any)
	delete(items[0].(map[string]any), "prices")
	fixture, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}

	c, _ := newServer(t, func(w http.ResponseWriter, r *http.Request) { w.Write(fixture) })
	res, err := c.Search(context.Background(), "leche", 20)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	h := res.Hits[0]
	if h.ID != "504P6" || h.Price != nil || h.PricePerUnit != 0 || h.Unit != "" || h.Currency != "" {
		t.Fatalf("hit without prices = %+v", h)
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
				Brand: "Asturiana", Price: ptr(2.45), PricePerUnit: 1.31, Unit: "kg", Currency: "EUR",
				Available: contract.Bool(true), Category: "Leche",
				URL:   "https://www.dia.es/huevos-leche-y-mantequilla/leche/p/74720P6",
				Image: "https://www.dia.es/product_images/74720P6/74720P6_ISO_0_ES.jpg"},
		},
		{
			name: "unidad",
			item: strings.Replace(baseItem, `"LITRO"`, `"UNIDAD"`, 1),
			want: contract.Hit{Store: "dia", ID: "74720P6", Name: "Leche entera Asturiana pack 6 x 1.5 L",
				Brand: "Asturiana", Price: ptr(11.82), PricePerUnit: 1.31, Unit: "unit", Currency: "EUR",
				Available: contract.Bool(true), Category: "Leche",
				URL:   "https://www.dia.es/huevos-leche-y-mantequilla/leche/p/74720P6",
				Image: "https://www.dia.es/product_images/74720P6/74720P6_ISO_0_ES.jpg"},
		},
		{
			name: "unknown measure unit leaves the unit price empty",
			item: strings.Replace(baseItem, `"LITRO"`, `"GRAMO"`, 1),
			want: contract.Hit{Store: "dia", ID: "74720P6", Name: "Leche entera Asturiana pack 6 x 1.5 L",
				Brand: "Asturiana", Price: ptr(11.82), PricePerUnit: 0, Unit: "", Currency: "EUR",
				Available: contract.Bool(true), Category: "Leche",
				URL:   "https://www.dia.es/huevos-leche-y-mantequilla/leche/p/74720P6",
				Image: "https://www.dia.es/product_images/74720P6/74720P6_ISO_0_ES.jpg"},
		},
		{
			name: "out of stock",
			item: strings.Replace(baseItem, `"units_in_stock": 41`, `"units_in_stock": 0`, 1),
			want: contract.Hit{Store: "dia", ID: "74720P6", Name: "Leche entera Asturiana pack 6 x 1.5 L",
				Brand: "Asturiana", Price: ptr(11.82), PricePerUnit: 1.31, Unit: "L", Currency: "EUR",
				Available: contract.Bool(false), Category: "Leche",
				URL:   "https://www.dia.es/huevos-leche-y-mantequilla/leche/p/74720P6",
				Image: "https://www.dia.es/product_images/74720P6/74720P6_ISO_0_ES.jpg"},
		},
		{
			name: "no prices",
			item: `{"brand":"Dia","display_name":"Bolsa reutilizable","object_id":"X1",
			       "l2_category_description":"Bazar","units_in_stock":5,"url":"/bazar/p/X1","image":"/img/X1.jpg"}`,
			want: contract.Hit{Store: "dia", ID: "X1", Name: "Bolsa reutilizable", Brand: "Dia",
				PricePerUnit: 0, Unit: "", Currency: "", Available: contract.Bool(true),
				Category: "Bazar", URL: "https://www.dia.es/bazar/p/X1",
				Image: "https://www.dia.es/img/X1.jpg"},
		},
		{
			// S8: the store answered a prices object but published no price number.
			name: "prices without a price",
			item: strings.Replace(baseItem, `"price": 11.82,`, "", 1),
			want: contract.Hit{Store: "dia", ID: "74720P6", Name: "Leche entera Asturiana pack 6 x 1.5 L",
				Brand: "Asturiana", PricePerUnit: 1.31, Unit: "L",
				Available: contract.Bool(true), Category: "Leche",
				URL:   "https://www.dia.es/huevos-leche-y-mantequilla/leche/p/74720P6",
				Image: "https://www.dia.es/product_images/74720P6/74720P6_ISO_0_ES.jpg"},
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
			if got := res.Hits[0]; !sameHit(got, want) {
				t.Fatalf("hit =\n%+v\nwant\n%+v", got, want)
			}
		})
	}
}

// Real hits carry measure_unit values outside the contract vocabulary: "LAVADO" on 25
// of the 30 "detergente" hits, and "100 ML." on others. An unknown unit means no unit
// price at all — both fields stay out of the JSON — because DIA published no €/kg or
// €/L anyone could compare (S8). Converting "100 ML." would be a x10 assumption.
func TestSearchDropsTheUnitPriceForAUnitItCannotMap(t *testing.T) {
	for _, measureUnit := range []string{"LAVADO", "100 ML."} {
		item := strings.Replace(baseItem, `"LITRO"`, `"`+measureUnit+`"`, 1)
		c, _ := newServer(t, func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte(envelope(item)))
		})
		res, err := c.Search(context.Background(), "detergente", 20)
		if err != nil {
			t.Fatalf("Search: %v", err)
		}
		if h := res.Hits[0]; h.Price == nil || *h.Price != 11.82 {
			t.Fatalf("measure_unit %q: published price lost: %+v", measureUnit, h)
		}
		if h := res.Hits[0]; h.PricePerUnit != 0 || h.Unit != "" {
			t.Fatalf("measure_unit %q: unit price %v %q emitted without a unit", measureUnit, h.PricePerUnit, h.Unit)
		}
		var buf strings.Builder
		if err := contract.Encode(&buf, res); err != nil {
			t.Fatal(err)
		}
		for _, key := range []string{`"pricePerUnit"`, `"unit"`} {
			if strings.Contains(buf.String(), key) {
				t.Errorf("measure_unit %q: JSON carries %s without a unit:\n%s", measureUnit, key, buf.String())
			}
		}
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

// The app's search request is the whole contract with dia.es: the /reduced path,
// page 0, and the platform headers. Measured 5/5 HTTP 200 with exactly these.
func TestSearchRequestShape(t *testing.T) {
	var got *http.Request
	c, _ := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		got = r.Clone(context.Background())
		w.Write([]byte(envelope(baseItem)))
	})
	if _, err := c.Search(context.Background(), "leche entera", 20); err != nil {
		t.Fatal(err)
	}

	if got.URL.Path != "/api/v1/search-back/search/reduced" {
		t.Fatalf("path = %q", got.URL.Path)
	}
	if q := got.URL.Query().Get("q"); q != "leche entera" {
		t.Fatalf("q = %q", q)
	}
	if p := got.URL.Query().Get("page"); p != "0" {
		t.Fatalf("page = %q", p)
	}

	// The DIA app's platform headers, verbatim from odd/research/dia-android-api.md.
	want := map[string]string{
		"User-Agent":                "okhttp/4.12.0",
		"Accept":                    "application/json",
		"Accept-Language":           "es-ES",
		"X-Id-Application-From":     "8",
		"X-Mob-Platform-From":       "OneApp-Android",
		"order_source":              "app",
		"x-avoid-seo-redirections":  "true",
		"X-Mob-Os-Version":          "14",
		"X-Mob-Application-Version": "6.18.1",
		"X-Mob-Manufacturer":        "Google",
		"X-Mob-Deviceid":            "3f2a9c1be7d54a80",
		"X-Retail-Visitor-Id":       "20250702180000000000000000000001",
		"X-Device-Trace-Id":         "8f14e45fceea167a",
		"X-App-Supports-L1-As-L2":   "true",
	}
	for k, v := range want {
		if got := got.Header.Get(k); got != v {
			t.Errorf("header %s = %q, want %q", k, got, v)
		}
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
	for _, k := range []string{"cart", "facets", "locale", "login_status", "pagination", "query_id", "search_items", "sort", "suggestions", "total_items"} {
		if _, ok := env[k]; !ok {
			t.Errorf("fixture missing envelope key %q", k)
		}
	}
}
