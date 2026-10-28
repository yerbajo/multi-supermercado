package lidl

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"multisuper/internal/contract"
)

func ptr(f float64) *float64 { return &f }

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

func newServer(t *testing.T, handler http.HandlerFunc) (*Client, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	c := New()
	c.BaseURL = srv.URL
	return c, srv
}

// The endpoint answers 401 unless the request carries a `version` query parameter, so
// the client must always send the SPA's parameter set and browser headers.
func TestSearchRequestsTheVersionedSearchAPI(t *testing.T) {
	var got *http.Request
	c, srv := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		got = r
		w.Write([]byte(`{"numFound":0,"items":[]}`))
	})

	if _, err := c.Search(context.Background(), "leche entera", 5); err != nil {
		t.Fatalf("Search: %v", err)
	}

	if got.URL.Path != "/q/api/search" {
		t.Errorf("path = %q, want /q/api/search", got.URL.Path)
	}
	q := got.URL.Query()
	for param, want := range map[string]string{
		"q":          "leche entera",
		"locale":     "es_ES",
		"assortment": "ES",
		"version":    "2.1.0",
		"fetchsize":  "5",
		"offset":     "0",
	} {
		if got := q.Get(param); got != want {
			t.Errorf("%s = %q, want %q", param, got, want)
		}
	}

	for header, want := range map[string]string{
		"Accept":          "application/mindshift.search+json;version=2",
		"Accept-Language": "es-ES",
		"Referer":         srv.URL + "/",
		"User-Agent":      "Mozilla/5.0",
	} {
		if v := got.Header.Get(header); !strings.Contains(v, want) {
			t.Errorf("%s = %q, want it to contain %q", header, v, want)
		}
	}
}

// The fixture is a real es_ES answer for "leche entera", trimmed to the fields the
// client reads (the search payload also carries encrypted relevancy xPayload blobs).
func TestSearchMapsCapturedResponse(t *testing.T) {
	fixture, err := os.ReadFile("testdata/search_leche_entera.json")
	if err != nil {
		t.Fatal(err)
	}
	c, _ := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write(fixture)
	})

	res, err := c.Search(context.Background(), "leche entera", 20)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if res.Store != "lidl" || res.Query != "leche entera" || res.Count != 10 || len(res.Hits) != 10 {
		t.Fatalf("envelope = %+v", res)
	}

	want := contract.Hit{
		Store:        "lidl",
		ID:           "11001765",
		Name:         "FIN CARRÉ Chocolate",
		Brand:        "FIN CARRÉ",
		Price:        ptr(0.85),
		PricePerUnit: 8.5,
		Unit:         "kg",
		Currency:     "EUR",
		Available:    true,
		Category:     "Food",
		URL:          "https://www.lidl.es/p/fin-carre-chocolate/p11001765",
		Image:        "https://www.lidl.es/assets/gcp98c0ffc034244b6ebf3b9c083019d401.png",
	}
	if !sameHit(res.Hits[0], want) {
		t.Fatalf("first hit = %+v\nwant       %+v", res.Hits[0], want)
	}
	// 1.29 € for 500 ml is 2.58 €/L.
	if h := res.Hits[2]; h.PricePerUnit != 2.58 || h.Unit != "L" {
		t.Fatalf("500 ml hit unit price = %v %q", h.PricePerUnit, h.Unit)
	}
}

// Lidl publishes no price at all for some in-store articles (only a Lidl Plus member
// price, or none): the hit must carry no price and no currency instead of a zero that
// reads as 0,00 € (S8).
func TestSearchKeepsUnpublishedPriceEmpty(t *testing.T) {
	fixture, err := os.ReadFile("testdata/search_leche_entera.json")
	if err != nil {
		t.Fatal(err)
	}
	c, _ := newServer(t, func(w http.ResponseWriter, r *http.Request) { w.Write(fixture) })

	res, err := c.Search(context.Background(), "leche entera", 20)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	h := res.Hits[1]
	if h.Name != "REALVALLE Lacón al horno" {
		t.Fatalf("hit 1 = %+v", h)
	}
	if h.Price != nil || h.PricePerUnit != 0 || h.Unit != "" || h.Currency != "" {
		t.Fatalf("unpriced hit = %+v, want no price and no unit price", h)
	}

	var buf strings.Builder
	if err := contract.Encode(&buf, contract.Result{Store: "lidl", Query: "q", Hits: []contract.Hit{h}}); err != nil {
		t.Fatalf("Encode: %v", err)
	}
	for _, key := range []string{`"price":`, `"currency":`} {
		if strings.Contains(buf.String(), key) {
			t.Errorf("unpriced hit must omit %s:\n%s", key, buf.String())
		}
	}
}

// A hit with a price but no published size (neither packaging nor a size in the name)
// keeps an empty unit price rather than inventing one.
func TestSearchLeavesUnitPriceEmptyWithoutPublishedSize(t *testing.T) {
	fixture, err := os.ReadFile("testdata/search_leche_entera.json")
	if err != nil {
		t.Fatal(err)
	}
	c, _ := newServer(t, func(w http.ResponseWriter, r *http.Request) { w.Write(fixture) })

	res, err := c.Search(context.Background(), "leche entera", 20)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	h := res.Hits[8]
	if h.Name != "GELATELLI Helado almendrado" || h.Price == nil || *h.Price != 2.65 {
		t.Fatalf("hit 8 = %+v", h)
	}
	if h.PricePerUnit != 0 || h.Unit != "" {
		t.Fatalf("unit price = %v %q, want it left empty", h.PricePerUnit, h.Unit)
	}
}

func TestSearchTrimsHitsToLimit(t *testing.T) {
	fixture, err := os.ReadFile("testdata/search_leche_entera.json")
	if err != nil {
		t.Fatal(err)
	}
	var fetchsize string
	c, _ := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		fetchsize = r.URL.Query().Get("fetchsize")
		w.Write(fixture)
	})

	res, err := c.Search(context.Background(), "leche entera", 3)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if fetchsize != "3" {
		t.Errorf("fetchsize = %q, want 3", fetchsize)
	}
	if res.Count != 3 || len(res.Hits) != 3 {
		t.Fatalf("count = %d with %d hits, want 3", res.Count, len(res.Hits))
	}
}

// The 401 gate is what a caller sees when version is dropped or Lidl changes the
// contract: it must surface as an error, never as an empty result.
func TestSearchReportsTheGateAsAnError(t *testing.T) {
	c, _ := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	})
	_, err := c.Search(context.Background(), "leche", 20)
	if err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("err = %v, want an HTTP 401 error", err)
	}
}

func TestSearchRetriesAndHonoursRetryAfter(t *testing.T) {
	attempts := 0
	c, _ := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts == 1 {
			w.Header().Set("Retry-After", "3")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.Write([]byte(`{"numFound":1,"items":[{"resultClass":"product","code":"1","gridbox":{"data":{"fullTitle":"Leche","price":{"price":1,"currencyCode":"EUR"}}}}]}`))
	})
	var waits []time.Duration
	c.Sleep = func(_ context.Context, d time.Duration) error {
		waits = append(waits, d)
		return nil
	}

	res, err := c.Search(context.Background(), "leche", 20)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if res.Count != 1 {
		t.Fatalf("count = %d, want the retried response", res.Count)
	}
	if len(waits) != 1 || waits[0] != 3*time.Second {
		t.Fatalf("waits = %v, want one 3s wait", waits)
	}
}

func TestSearchCapsTheRetryWaitAndGivesUp(t *testing.T) {
	attempts := 0
	c, _ := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.Header().Set("Retry-After", "3600")
		w.WriteHeader(http.StatusServiceUnavailable)
	})
	var waits []time.Duration
	c.Sleep = func(_ context.Context, d time.Duration) error {
		waits = append(waits, d)
		return nil
	}

	if _, err := c.Search(context.Background(), "leche", 20); err == nil {
		t.Fatal("Search succeeded, want an error after 3 attempts")
	}
	if attempts != 3 {
		t.Fatalf("attempts = %d, want 3", attempts)
	}
	for _, d := range waits {
		if d > maxRetryWait {
			t.Fatalf("waits = %v, want every wait capped at %v", waits, maxRetryWait)
		}
	}
}

// Search answers teasers and advisors in the same items array; only products are hits.
func TestSearchSkipsNonProductResults(t *testing.T) {
	c, _ := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"numFound":2,"items":[
			{"resultClass":"teaser","type":"teaser"},
			{"resultClass":"product","code":"7","gridbox":{"data":{"fullTitle":"Leche entera","price":{"price":1.05,"currencyCode":"EUR"}}}}]}`))
	})

	res, err := c.Search(context.Background(), "leche", 20)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if res.Count != 1 || res.Hits[0].ID != "7" {
		t.Fatalf("hits = %+v", res.Hits)
	}
}

// Lidl publishes the sold amount in price.packaging.text; product names carry one for
// groceries but appliance names may carry a capacity instead ("Cafetera 1,5 l"), which
// must never become a unit price (S8).
func TestQuantity(t *testing.T) {
	tests := []struct {
		packaging string
		name      string
		category  string
		want      float64
		unit      string
	}{
		{"100 g", "", "Food", 0.1, "kg"},
		{"160g", "", "Food", 0.16, "kg"},
		{"1l", "", "Food", 1, "L"},
		{"3l", "", "Food", 3, "L"},
		{"33 cl", "", "Food", 0.33, "L"},
		{"9x33 cl", "", "Food", 2.97, "L"},
		{"2x400 g", "", "Food", 0.8, "kg"},
		{"8x125g", "", "Food", 1, "kg"},
		{"Aprox.1,1 kg", "", "F+V", 1.1, "kg"},
		{"", "Leche entera 6 x 1,5 L", "Food", 9, "L"},
		{"", "Café molido 250 g", "F+V", 0.25, "kg"},
		{"A granel", "Plátanos", "F+V", 0, ""},
		{"Paquete", "Pan de molde", "Food", 0, ""},
		{"1 ud", "Bicicleta", "Categorías/Deporte y ocio", 1, "unit"},
		{"", "Cafetera 1,5 l", "Categorías/Hogar y cocina", 0, ""},
		{"", "GELATELLI Helado almendrado", "Food", 0, ""},
	}
	for _, tt := range tests {
		got, unit := quantity(tt.packaging, tt.name, tt.category)
		if got != tt.want || unit != tt.unit {
			t.Errorf("quantity(%q, %q, %q) = %v %q, want %v %q",
				tt.packaging, tt.name, tt.category, got, unit, tt.want, tt.unit)
		}
	}
}
