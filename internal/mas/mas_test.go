package mas

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"multisuper/internal/contract"
)

// Real names observed from the live MAS catalog, plus the no-size cases.
const searchFixture = `{"data":{"products":{"total_count":245,"items":[
{"name":"Leche semidesnatada PASCUAL botella 1,5l","sku":"062595","url_key":"lacteos/leche-semidesnatada-pascual-botella-1-5l","stock_status":"IN_STOCK","price":{"regularPrice":{"amount":{"value":1.84,"currency":"EUR"}}}},
{"name":"Pipas TOSFERINA","sku":"077777","url_key":"snacks/pipas-tosferina","stock_status":"OUT_OF_STOCK","price":{"regularPrice":{"amount":{"value":2.5,"currency":"EUR"}}}}
]}}}`

// The hit shape is asserted as JSON, never as Go struct internals. MAS publishes no
// sold size, so no hit carries a unit price, whatever its name looks like.
func TestSearchMapsProductsToHits(t *testing.T) {
	got := searchJSON(t, searchFixture, http.StatusOK)

	want := `{"store":"mas","query":"leche","count":2,"hits":[
{"store":"mas","id":"062595","name":"Leche semidesnatada PASCUAL botella 1,5l","price":1.84,"currency":"EUR","available":true,"url":"https://www.supermercadosmas.com/lacteos/leche-semidesnatada-pascual-botella-1-5l/"},
{"store":"mas","id":"077777","name":"Pipas TOSFERINA","price":2.5,"currency":"EUR","available":false,"url":"https://www.supermercadosmas.com/snacks/pipas-tosferina/"}
]}`
	if got, want := compact(t, got), compact(t, want); got != want {
		t.Fatalf("json mismatch\n got: %s\nwant: %s", got, want)
	}
}

// MAS publishes no sold size, so a size-shaped product name must never become a unit
// price: the name parser this replaces reported 0.09 €/L for a 30-litre bin bag
// ("HANDY BAG 30l 12ud") and 13.50 €/L for "IFA SABE 10ux100ml", neither of which
// anyone publishes (S8). Both fields stay empty, exactly as dia does when it cannot
// know the unit.
func TestSearchNeverDerivesAUnitPriceFromTheName(t *testing.T) {
	body := `{"data":{"products":{"items":[
{"name":"HANDY BAG 30l 12ud bolsas basura","sku":"1","url_key":"hogar/handy-bag","stock_status":"IN_STOCK","price":{"regularPrice":{"amount":{"value":2.75,"currency":"EUR"}}}},
{"name":"IFA SABE 10ux100ml","sku":"2","url_key":"drogueria/ifa-sabe","stock_status":"IN_STOCK","price":{"regularPrice":{"amount":{"value":13.5,"currency":"EUR"}}}}
]}}}`
	got := searchJSON(t, body, http.StatusOK)
	var res struct {
		Hits []map[string]json.RawMessage `json:"hits"`
	}
	if err := json.Unmarshal([]byte(got), &res); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, got)
	}
	if len(res.Hits) != 2 {
		t.Fatalf("hits = %d, want 2:\n%s", len(res.Hits), got)
	}
	for i, h := range res.Hits {
		for _, key := range []string{"pricePerUnit", "unit"} {
			if _, ok := h[key]; ok {
				t.Errorf("hit %d (%s) carries %q: MAS publishes no sold size\n%s", i, h["name"], key, got)
			}
		}
		if _, ok := h["price"]; !ok {
			t.Errorf("hit %d lost its published price:\n%s", i, got)
		}
	}
}

// S8: MAS always answers with price.regularPrice, but when an item carries no published
// price the hit must omit both keys instead of serialising 0,00 €.
func TestSearchOmitsAnUnpublishedPrice(t *testing.T) {
	body := `{"data":{"products":{"items":[{"name":"Leche entera PASCUAL","sku":"9","url_key":"lacteos/pascual","stock_status":"IN_STOCK","price":{"regularPrice":{"amount":{"currency":"EUR"}}}}]}}}`
	want := `{"store":"mas","query":"leche","count":1,"hits":[{"store":"mas","id":"9","name":"Leche entera PASCUAL","available":true,"url":"https://www.supermercadosmas.com/lacteos/pascual/"}]}`
	if got := compact(t, searchJSON(t, body, http.StatusOK)); got != compact(t, want) {
		t.Fatalf("json mismatch\n got: %s\nwant: %s", got, compact(t, want))
	}
}

func TestSearchSendsQueryToGraphQL(t *testing.T) {
	var got struct {
		Query     string            `json:"query"`
		Variables map[string]string `json:"variables"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/graphql/" {
			t.Errorf("got %s %s, want POST /graphql/", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Errorf("Content-Type = %q", got)
		}
		if !strings.Contains(r.Header.Get("User-Agent"), "Chrome") {
			t.Errorf("User-Agent = %q, want a Chrome UA", r.Header.Get("User-Agent"))
		}
		if got := r.Header.Get("Accept"); got != "application/json" {
			t.Errorf("Accept = %q", got)
		}
		var raw struct {
			Query     string `json:"query"`
			Variables struct {
				T string `json:"t"`
				P int    `json:"p"`
				N int    `json:"n"`
			} `json:"variables"`
		}
		if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
			t.Errorf("decoding body: %v", err)
		}
		got.Query = raw.Query
		got.Variables = map[string]string{"t": raw.Variables.T}
		if raw.Variables.P != 2 || raw.Variables.N != 5 {
			t.Errorf("variables p=%d n=%d, want 2 and 5", raw.Variables.P, raw.Variables.N)
		}
		w.Write([]byte(`{"data":{"products":{"items":[]}}}`))
	}))
	defer srv.Close()

	c := New()
	c.BaseURL = srv.URL
	if _, err := c.Search(context.Background(), "leche", 2, 5); err != nil {
		t.Fatalf("Search: %v", err)
	}
	if got.Variables["t"] != "leche" {
		t.Fatalf("search term = %q", got.Variables["t"])
	}
	for _, field := range []string{"products(search:$t", "currentPage:$p", "pageSize:$n", "regularPrice{amount{value currency}}"} {
		if !strings.Contains(got.Query, field) {
			t.Errorf("query missing %q", field)
		}
	}
	if strings.Contains(got.Query, "price_range") || strings.Contains(got.Query, "finalPrice") {
		t.Errorf("query uses a field known to break MAS: %s", got.Query)
	}
}

func TestSearchRetriesRateLimit(t *testing.T) {
	attempts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts < 3 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.Write([]byte(`{"data":{"products":{"items":[]}}}`))
	}))
	defer srv.Close()

	c := New()
	c.BaseURL = srv.URL
	if _, err := c.Search(context.Background(), "leche", 1, 20); err != nil {
		t.Fatalf("Search: %v", err)
	}
	if attempts != 3 {
		t.Fatalf("attempts = %d, want 3", attempts)
	}
}

func TestSearchGivesUpAfterThreeAttempts(t *testing.T) {
	attempts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.Header().Set("Retry-After", "0")
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	c := New()
	c.BaseURL = srv.URL
	if _, err := c.Search(context.Background(), "leche", 1, 20); err == nil {
		t.Fatal("Search succeeded, want error")
	}
	if attempts != 3 {
		t.Fatalf("attempts = %d, want 3", attempts)
	}
}

func TestSearchReportsFailures(t *testing.T) {
	tests := []struct {
		name string
		code int
		body string
		want string
	}{
		{"http error", http.StatusInternalServerError, `boom`, "HTTP 500"},
		{"invalid json", http.StatusOK, `{"data":`, "invalid"},
		{"graphql errors", http.StatusOK, `{"errors":[{"message":"Internal server error"}]}`, "Internal server error"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.code)
				w.Write([]byte(tt.body))
			}))
			defer srv.Close()

			c := New()
			c.BaseURL = srv.URL
			_, err := c.Search(context.Background(), "leche", 1, 20)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want it to mention %q", err, tt.want)
			}
		})
	}
}

// searchJSON runs Search against a fixture server and returns the encoded Result.
func searchJSON(t *testing.T, body string, code int) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(code)
		w.Write([]byte(body))
	}))
	defer srv.Close()

	c := New()
	c.BaseURL = srv.URL
	res, err := c.Search(context.Background(), "leche", 1, 20)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	var buf strings.Builder
	if err := contract.Encode(&buf, res); err != nil {
		t.Fatalf("encode: %v", err)
	}
	return strings.TrimRight(buf.String(), "\n")
}

func compact(t *testing.T, s string) string {
	t.Helper()
	var buf strings.Builder
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		t.Fatalf("bad fixture json: %v", err)
	}
	enc := json.NewEncoder(&buf)
	if err := enc.Encode(v); err != nil {
		t.Fatalf("encode: %v", err)
	}
	return strings.TrimRight(buf.String(), "\n")
}

// MAS publishes no barcode and no sold size through GraphQL, so the aggregator gets no
// identifier from this chain. Verified live: ProductInterface and SimpleProduct expose
// no ean/gtin/barcode field and products exposes no custom_attributes field at all.
func TestSearchCarriesNoIdentifierMASDoesNotPublish(t *testing.T) {
	body := `{"data":{"products":{"items":[{"name":"Leche entera PASCUAL botella 1,5l","sku":"062595","url_key":"lacteos/pascual","stock_status":"IN_STOCK","price":{"regularPrice":{"amount":{"value":1.5,"currency":"EUR"}}}}]}}}`
	got := searchJSON(t, body, http.StatusOK)
	for _, key := range []string{`"ean"`, `"size"`} {
		if strings.Contains(got, key) {
			t.Errorf("hit carries %s though MAS does not publish it:\n%s", key, got)
		}
	}
}
