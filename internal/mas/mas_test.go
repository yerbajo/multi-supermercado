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
func TestParseQuantity(t *testing.T) {
	tests := []struct {
		name   string
		amount float64
		unit   string
	}{
		{"Leche semidesnatada PASCUAL botella 1,5l", 1.5, "L"},
		{"Leche condensada LA LECHERA 370 gr", 0.37, "kg"},
		{"Leche condensada entera LA LECHERA lata 740gr", 0.74, "kg"},
		{"Coca-cola sabor original 6x200ml lata", 1.2, "L"},
		{"Coca-cola light 2x2l pet", 4, "L"},
		{"Refresco cola COCA COLA lata 50cl", 0.5, "L"},
		{"Agua mineral BEZOYA botella 1,5 L", 1.5, "L"},
		{"Aceite de oliva virgen extra 1l", 1, "L"},
		{"Huevos camperos SAN RAFAEL 12ud", 12, "unit"},
		{"Pan de molde BIMBO 460g", 0.46, "kg"},
		// no parsable size: leave the unit price empty instead of guessing
		{"Leche entera PASCUAL", 0, ""},
		{"Pan de molde BIMBO sin corteza", 0, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			amount, unit := parseQuantity(tt.name)
			if unit != tt.unit {
				t.Fatalf("unit = %q, want %q", unit, tt.unit)
			}
			if diff := amount - tt.amount; diff > 1e-9 || diff < -1e-9 {
				t.Fatalf("amount = %v, want %v", amount, tt.amount)
			}
		})
	}
}

const searchFixture = `{"data":{"products":{"total_count":245,"items":[
{"name":"Leche semidesnatada PASCUAL botella 1,5l","sku":"062595","url_key":"lacteos/leche-semidesnatada-pascual-botella-1-5l","stock_status":"IN_STOCK","price":{"regularPrice":{"amount":{"value":1.84,"currency":"EUR"}}}},
{"name":"Pipas TOSFERINA","sku":"077777","url_key":"snacks/pipas-tosferina","stock_status":"OUT_OF_STOCK","price":{"regularPrice":{"amount":{"value":2.5,"currency":"EUR"}}}}
]}}}`

// The hit shape is asserted as JSON, never as Go struct internals.
func TestSearchMapsProductsToHits(t *testing.T) {
	got := searchJSON(t, searchFixture, http.StatusOK)

	want := `{"store":"mas","query":"leche","count":2,"hits":[
{"store":"mas","id":"062595","name":"Leche semidesnatada PASCUAL botella 1,5l","price":1.84,"pricePerUnit":1.23,"unit":"L","currency":"EUR","available":true,"url":"https://www.supermercadosmas.com/lacteos/leche-semidesnatada-pascual-botella-1-5l/"},
{"store":"mas","id":"077777","name":"Pipas TOSFERINA","price":2.5,"currency":"EUR","available":false,"url":"https://www.supermercadosmas.com/snacks/pipas-tosferina/"}
]}`
	if got, want := compact(t, got), compact(t, want); got != want {
		t.Fatalf("json mismatch\n got: %s\nwant: %s", got, want)
	}
}

func TestSearchWithoutSizeOmitsUnitPrice(t *testing.T) {
	body := `{"data":{"products":{"items":[{"name":"Pan de molde BIMBO","sku":"1","url_key":"pan/bimbo","stock_status":"IN_STOCK","price":{"regularPrice":{"amount":{"value":2.1,"currency":"EUR"}}}}]}}}`
	want := `{"store":"mas","query":"leche","count":1,"hits":[{"store":"mas","id":"1","name":"Pan de molde BIMBO","price":2.1,"currency":"EUR","available":true,"url":"https://www.supermercadosmas.com/pan/bimbo/"}]}`
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
