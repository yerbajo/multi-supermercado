package alcampo

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"multisuper/internal/contract"
)

// storefrontPage is the minimum of the real homepage the client needs: the CSRF
// token and the asset version it echoes back on every request.
const storefrontPage = `<html><body><script>window.__INITIAL_STATE__={"session":{"metadata":{"assetVersion":"2.0.0-test","basePath":""},"csrf":{"token":"tok-123"}}}</script></body></html>`

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatalf("reading fixture %s: %v", name, err)
	}
	return raw
}

// searchJSON runs one Search against a fake storefront and returns the envelope as
// JSON, so assertions never reach into Go struct internals.
func searchJSON(t *testing.T, body []byte, status int, limit int, hdr map[string]string) string {
	t.Helper()
	c := newTestClient(t, body, status, hdr)
	res, err := c.Search(context.Background(), "leche", limit)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	var buf strings.Builder
	if err := contract.Encode(&buf, res); err != nil {
		t.Fatalf("Encode: %v", err)
	}
	return buf.String()
}

// newTestClient points a client at a fake storefront that answers the homepage and
// the product-pages search endpoint.
func newTestClient(t *testing.T, body []byte, status int, hdr map[string]string) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			w.Header().Set("Content-Type", "text/html;charset=utf-8")
			_, _ = w.Write([]byte(storefrontPage))
			return
		}
		for k, v := range hdr {
			w.Header().Set(k, v)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	c := New()
	c.BaseURL = srv.URL
	c.Sleep = func(context.Context, time.Duration) error { return nil }
	return c
}

// A real (trimmed) capture of the storefront answer for "leche".
func TestSearchMapsProductsToHits(t *testing.T) {
	got := compact(t, searchJSON(t, fixture(t, "search_leche.json"), http.StatusOK, 20, nil))

	want := `{"store":"alcampo","query":"leche","count":3,"hits":[
{"store":"alcampo","id":"54180","name":"AUCHAN Leche semidesnatada de vaca 6 x 1l Producto Alcampo.","brand":"PRODUCTO ALCAMPO","price":5.28,"pricePerUnit":0.88,"unit":"L","currency":"EUR","available":true,"url":"https://www.compraonline.alcampo.es/products/54180/details","image":"https://www.compraonline.alcampo.es/images-v3/37ea0506-72ec-4543-93c8-a77bb916ec12/1aec1514-3cce-46d9-8133-20fdebbdb2cc/500x500.jpg"},
{"store":"alcampo","id":"54178","name":"AUCHAN Leche entera de vaca 6 x 1 l Producto Alcampo.","brand":"PRODUCTO ALCAMPO","price":5.76,"pricePerUnit":0.96,"unit":"L","currency":"EUR","available":true,"url":"https://www.compraonline.alcampo.es/products/54178/details","image":"https://www.compraonline.alcampo.es/images-v3/37ea0506-72ec-4543-93c8-a77bb916ec12/845a9f8d-6b50-4055-81ce-1b7e621987f3/500x500.jpg"},
{"store":"alcampo","id":"53096","name":"PULEVA EcoLeche entera pack de 6 uds x 1 l.","brand":"PULEVA","price":8.26,"pricePerUnit":1.38,"unit":"L","currency":"EUR","available":true,"url":"https://www.compraonline.alcampo.es/products/53096/details","image":"https://www.compraonline.alcampo.es/images-v3/37ea0506-72ec-4543-93c8-a77bb916ec12/2ee9a3e6-478e-42d5-941f-900f605d9b0e/500x500.jpg"}
]}`
	if got != compact(t, want) {
		t.Fatalf("json mismatch\n got: %s\nwant: %s", got, compact(t, want))
	}
}

// S8: a product the store publishes without a price must omit both keys instead of
// serialising 0,00 €.
func TestSearchOmitsAnUnpublishedPrice(t *testing.T) {
	body := []byte(`{"productGroups":[{"decoratedProducts":[{"productId":"a","retailerProductId":"1","name":"Plátano de Canarias","available":true}]}],"metadata":{}}`)
	got := compact(t, searchJSON(t, body, http.StatusOK, 20, nil))
	want := `{"store":"alcampo","query":"leche","count":1,"hits":[{"store":"alcampo","id":"1","name":"Plátano de Canarias","available":true,"url":"https://www.compraonline.alcampo.es/products/1/details"}]}`
	if got != compact(t, want) {
		t.Fatalf("json mismatch\n got: %s\nwant: %s", got, compact(t, want))
	}
}

// The store publishes a per-package price ("EACH"); it is mapped, never derived.
func TestSearchKeepsPublishedEachUnitPrice(t *testing.T) {
	got := compact(t, searchJSON(t, fixture(t, "search_detergente.json"), http.StatusOK, 20, nil))
	want := `{"store":"alcampo","query":"leche","count":2,"hits":[
{"store":"alcampo","id":"499461","name":"SKIP Detergente líquido ciclos cortos anti olor 37 ds.","brand":"SKIP","price":8.99,"pricePerUnit":0.24,"unit":"unit","currency":"EUR","available":true,"url":"https://www.compraonline.alcampo.es/products/499461/details","image":"https://www.compraonline.alcampo.es/images-v3/37ea0506-72ec-4543-93c8-a77bb916ec12/1ba985ea-01ef-4105-9fe3-f47c495e4227/500x500.jpg"},
{"store":"alcampo","id":"644160","name":"PRODUCTO ALCAMPO Detergente líquido Jabón de Marsella 74 ds. 4 l.","brand":"PRODUCTO ALCAMPO","price":5.32,"pricePerUnit":0.07,"unit":"unit","currency":"EUR","available":true,"url":"https://www.compraonline.alcampo.es/products/644160/details","image":"https://www.compraonline.alcampo.es/images-v3/37ea0506-72ec-4543-93c8-a77bb916ec12/0608e42a-6c4b-4740-a33f-62c1297b47bd/500x500.jpg"}]}`
	if got != compact(t, want) {
		t.Fatalf("json mismatch\n got: %s\nwant: %s", got, compact(t, want))
	}
}

func TestSearchTruncatesToLimit(t *testing.T) {
	res := searchResult(t, fixture(t, "search_leche.json"), 2)
	if res.Count != 2 || len(res.Hits) != 2 {
		t.Fatalf("count = %d, len(hits) = %d, want 2 and 2", res.Count, len(res.Hits))
	}
}

// The exact request shape the storefront frontend sends, asserted on the wire.
func TestSearchSendsStorefrontRequest(t *testing.T) {
	var gotSearch bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			_, _ = w.Write([]byte(storefrontPage))
			return
		}
		gotSearch = true
		if r.Method != http.MethodGet {
			t.Errorf("method = %s, want GET", r.Method)
		}
		if r.URL.Path != "/api/webproductpagews/v6/product-pages/search" {
			t.Errorf("path = %s", r.URL.Path)
		}
		wantQuery := map[string]string{
			"q":                         "leche entera",
			"tag":                       "web",
			"includeAdditionalPageInfo": "true",
			"maxProductsToDecorate":     "7",
			"maxPageSize":               "300",
		}
		for k, v := range wantQuery {
			if got := r.URL.Query().Get(k); got != v {
				t.Errorf("query %s = %q, want %q", k, got, v)
			}
		}
		if n := len(r.URL.Query()); n != len(wantQuery) {
			t.Errorf("got %d query params (%v), want %d", n, r.URL.Query(), len(wantQuery))
		}
		wantHeaders := map[string]string{
			"Accept":                      "application/json",
			"X-Csrf-Token":                "tok-123",
			"Ecom-Request-Source":         "web",
			"Ecom-Request-Source-Version": "2.0.0-test",
			"Client-Route-Id":             "SEARCH",
		}
		for k, v := range wantHeaders {
			if got := r.Header.Get(k); got != v {
				t.Errorf("header %s = %q, want %q", k, got, v)
			}
		}
		if ua := r.Header.Get("User-Agent"); !strings.Contains(ua, "Chrome/") {
			t.Errorf("user-agent = %q, want a Chrome user agent", ua)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(fixture(t, "search_leche.json"))
	}))
	t.Cleanup(srv.Close)

	c := New()
	c.BaseURL = srv.URL
	c.Sleep = func(context.Context, time.Duration) error { return nil }
	if _, err := c.Search(context.Background(), "leche entera", 7); err != nil {
		t.Fatalf("Search: %v", err)
	}
	if !gotSearch {
		t.Fatal("the search endpoint was never called")
	}
}

// A 202 with the WAF challenge header is not retried: no stdlib client can answer a
// JavaScript challenge, so the error has to say so instead of looking like a bug.
func TestSearchReportsWAFChallenge(t *testing.T) {
	c := newTestClient(t, nil, http.StatusAccepted, map[string]string{"X-Amzn-Waf-Action": "challenge"})
	_, err := c.Search(context.Background(), "leche", 20)
	if err == nil {
		t.Fatal("Search succeeded, want a WAF challenge error")
	}
	if !strings.Contains(err.Error(), "challenge") {
		t.Fatalf("error = %v, want it to name the challenge", err)
	}
}

// 429 and 503 are retried, honouring Retry-After and capping the wait.
func TestSearchRetriesThrottling(t *testing.T) {
	var calls int
	var slept []time.Duration
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			_, _ = w.Write([]byte(storefrontPage))
			return
		}
		calls++
		if calls < 3 {
			w.Header().Set("Retry-After", "3600")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_, _ = w.Write(fixture(t, "search_leche.json"))
	}))
	t.Cleanup(srv.Close)

	c := New()
	c.BaseURL = srv.URL
	c.Sleep = func(_ context.Context, d time.Duration) error {
		slept = append(slept, d)
		return nil
	}
	if _, err := c.Search(context.Background(), "leche", 3); err != nil {
		t.Fatalf("Search: %v", err)
	}
	if calls != 3 {
		t.Fatalf("calls = %d, want 3", calls)
	}
	for _, d := range slept {
		if d > maxRetryWait {
			t.Fatalf("slept %v, want at most %v", d, maxRetryWait)
		}
	}
}

func TestSearchGivesUpAfterThreeAttempts(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			_, _ = w.Write([]byte(storefrontPage))
			return
		}
		calls++
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(srv.Close)

	c := New()
	c.BaseURL = srv.URL
	c.Sleep = func(context.Context, time.Duration) error { return nil }
	_, err := c.Search(context.Background(), "leche", 3)
	if err == nil {
		t.Fatal("Search succeeded, want an error")
	}
	if calls != 3 {
		t.Fatalf("calls = %d, want 3", calls)
	}
}

func TestSearchFailsWithoutCSRFToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("<html>no state here</html>"))
	}))
	t.Cleanup(srv.Close)
	c := New()
	c.BaseURL = srv.URL
	c.Sleep = func(context.Context, time.Duration) error { return nil }
	if _, err := c.Search(context.Background(), "leche", 3); err == nil {
		t.Fatal("Search succeeded, want a missing-token error")
	}
}

// Only the three unit names the storefront publishes map to the shared contract, and
// an unknown one leaves both fields empty instead of guessing (S8).
func TestUnitNameMapping(t *testing.T) {
	tests := map[string]string{
		"PER_LITRE": "L",
		"PER_KG":    "kg",
		"EACH":      "unit",
		"PER_PACK":  "",
		"":          "",
	}
	for name, want := range tests {
		if got := contractUnit(name); got != want {
			t.Errorf("contractUnit(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestSearchRejectsMalformedBody(t *testing.T) {
	c := newTestClient(t, []byte(`<html>not json</html>`), http.StatusOK, nil)
	if _, err := c.Search(context.Background(), "leche", 3); err == nil {
		t.Fatal("Search succeeded, want a parse error")
	}
}

func searchResult(t *testing.T, body []byte, limit int) contract.Result {
	t.Helper()
	c := newTestClient(t, body, http.StatusOK, nil)
	res, err := c.Search(context.Background(), "leche", limit)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	return res
}

func compact(t *testing.T, s string) string {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, s)
	}
	out, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(out)
}
