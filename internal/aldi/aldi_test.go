package aldi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
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

// fixture serves a captured Algolia response and records what the client sent.
type fixture struct {
	*httptest.Server
	path   string
	query  string
	body   string
	header http.Header
}

func serve(t *testing.T, name string) *fixture {
	t.Helper()
	body, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	f := &fixture{}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.path, f.query, f.header = r.URL.Path, r.URL.RawQuery, r.Header.Clone()
		raw, _ := io.ReadAll(r.Body)
		f.body = string(raw)
		w.Header().Set("Content-Type", "application/json")
		w.Write(body)
	}))
	t.Cleanup(f.Close)
	return f
}

func clientFor(f *fixture) *Client {
	return &Client{BaseURL: f.URL, HTTP: f.Client(), Sleep: func(context.Context, time.Duration) error { return nil }}
}

// The real response carries published prices and a published unit price; both are
// mapped straight through, and nothing is invented when they are absent.
func TestSearchMapsCapturedHits(t *testing.T) {
	f := serve(t, "search_leche_pen.json")
	res, err := clientFor(f).Search(context.Background(), "pen", "leche", 3)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if res.Store != "aldi" || res.Query != "leche" || res.Count != 3 || len(res.Hits) != 3 {
		t.Fatalf("envelope = %+v", res)
	}

	want := contract.Hit{
		Store:        "aldi",
		ID:           "907505",
		Name:         "Tortitas de arroz integral con chocolate con leche",
		Brand:        "BICENTURY®",
		Price:        ptr(1.37),
		PricePerUnit: 10.54,
		Unit:         "kg",
		Currency:     "EUR",
		Available:    true,
		Category:     "Desayuno > Barritas y tortitas",
		URL:          "https://www.aldi.es/producto/tortitas-de-arroz-integral-con-chocolate-con-leche-907505.html",
		Image:        "https://s7g10.scene7.com/is/image/aldinord/9075_05_foto_chocolate_leche",
	}
	if got := res.Hits[0]; !sameHit(got, want) {
		t.Errorf("hit[0]\n got %+v\nwant %+v", got, want)
	}

	// Fresh milk publishes no base price; its declared sales unit ("1 l unidad")
	// is the only published size, so €/L is 0.96 without estimating anything.
	if h := res.Hits[1]; h.ID != "872200" || h.Price == nil || *h.Price != 1.03 || h.PricePerUnit != 1.03 || h.Unit != "L" {
		t.Errorf("hit[1] = %+v", h)
	}
}

// Fresh produce is listed without any published price. The hit must survive with an
// empty price rather than a guessed one (S8).
func TestSearchKeepsHitsWithoutPublishedPrice(t *testing.T) {
	f := serve(t, "search_cogollos_pen.json")
	res, err := clientFor(f).Search(context.Background(), "pen", "cogollos", 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if res.Count != 2 {
		t.Fatalf("count = %d, want 2", res.Count)
	}
	first := res.Hits[0]
	if first.ID != "998600" || first.Price != nil || first.PricePerUnit != 0 || first.Unit != "" {
		t.Errorf("hit[0] = %+v, want no price and no unit", first)
	}
	// A declared size without a price is still not a unit price: 0/0.45kg would be
	// a made-up number.
	if h := res.Hits[1]; h.Price != nil || h.PricePerUnit != 0 || h.Unit != "" {
		t.Errorf("hit[1] = %+v, want no unit price", h)
	}
	if first.Brand != "" {
		t.Errorf("brand = %q, want omitted", first.Brand)
	}
}

// The site's own search is Algolia; the path, the public search key and the body are
// what the browser sends, so any drift shows up here.
func TestSearchSendsTheAlgoliaSearchRequest(t *testing.T) {
	f := serve(t, "search_leche_pen.json")
	if _, err := clientFor(f).Search(context.Background(), "pen", "leche", 3); err != nil {
		t.Fatalf("Search: %v", err)
	}
	if got, want := f.path, "/1/indexes/an_prd_es_es_pen_products2/query"; got != want {
		t.Errorf("path = %q, want %q", got, want)
	}
	if got := f.header.Get("X-Algolia-Application-Id"); got != "L9KNU74IO7" {
		t.Errorf("application id = %q", got)
	}
	if got := f.header.Get("X-Algolia-API-Key"); got != "83df5acd172c42ab174afa4583232b5d" {
		t.Errorf("api key = %q", got)
	}
	if got := f.header.Get("Content-Type"); got != "application/json" {
		t.Errorf("content type = %q", got)
	}
	if !strings.HasPrefix(f.header.Get("User-Agent"), "Mozilla/5.0") {
		t.Errorf("user agent = %q", f.header.Get("User-Agent"))
	}
}

func TestSearchRequestsTheTermAndLimit(t *testing.T) {
	f := serve(t, "search_leche_pen.json")
	if _, err := clientFor(f).Search(context.Background(), "pen", "leche entera", 3); err != nil {
		t.Fatalf("Search: %v", err)
	}
	body, err := json.Marshal(query{Query: "leche entera", HitsPerPage: 3})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if got := f.query; got != "" {
		t.Errorf("search must post the term in the body, not the query string: %q", got)
	}
	var got query
	if err := json.Unmarshal([]byte(f.lastBody(t)), &got); err != nil {
		t.Fatalf("request body is not JSON: %v", err)
	}
	if got != (query{Query: "leche entera", HitsPerPage: 3}) {
		t.Errorf("body = %+v, want %s", got, body)
	}
}

// Prices differ per region and so does the index; the CLI exposes the switch.
func TestRegionSelectsTheIndex(t *testing.T) {
	for _, region := range []string{"pen", "can", "bal"} {
		f := serve(t, "search_leche_pen.json")
		if _, err := clientFor(f).Search(context.Background(), region, "leche", 1); err != nil {
			t.Fatalf("Search(%s): %v", region, err)
		}
		if want := "/1/indexes/an_prd_es_es_" + region + "_products2/query"; f.path != want {
			t.Errorf("region %s path = %q, want %q", region, f.path, want)
		}
	}
}

func TestSearchRejectsAnUnknownRegion(t *testing.T) {
	f := serve(t, "search_leche_pen.json")
	if _, err := clientFor(f).Search(context.Background(), "esp", "leche", 1); !errors.Is(err, ErrRegion) {
		t.Fatalf("err = %v, want ErrRegion", err)
	}
	if f.path != "" {
		t.Errorf("an unknown region must not reach the network, got path %q", f.path)
	}
}

// S3: a limit outside 1..MaxLimit is a usage error, never silently clamped, so the
// client must reject it before building any request.
func TestSearchRejectsAnOutOfRangeLimit(t *testing.T) {
	for _, limit := range []int{0, -1, 101, 1000} {
		calls := 0
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls++
			io.WriteString(w, `{"hits":[]}`)
		}))
		c := &Client{BaseURL: srv.URL, HTTP: srv.Client(), Sleep: func(context.Context, time.Duration) error { return nil }}
		_, err := c.Search(context.Background(), "pen", "leche", limit)
		srv.Close()
		if err == nil {
			t.Errorf("limit %d: err = nil, want a rejection", limit)
		}
		if calls != 0 {
			t.Errorf("limit %d: %d requests reached the server, want 0", limit, calls)
		}
	}
}

// A valid limit still goes out as the page size and still truncates a longer page.
func TestSearchAppliesAValidLimit(t *testing.T) {
	f := serve(t, "search_leche_pen.json")
	res, err := clientFor(f).Search(context.Background(), "pen", "leche", 7)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	var got query
	if err := json.Unmarshal([]byte(f.lastBody(t)), &got); err != nil {
		t.Fatalf("body: %v", err)
	}
	if got.HitsPerPage != 7 {
		t.Errorf("sent %d hits per page, want 7", got.HitsPerPage)
	}
	if res.Count != 7 || len(res.Hits) != 7 {
		t.Errorf("count=%d hits=%d, want 7 (the fixture serves 10)", res.Count, len(res.Hits))
	}
}

func TestSearchRetriesOn429And503HonouringRetryAfter(t *testing.T) {
	body, err := os.ReadFile("testdata/search_leche_pen.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	attempts, waits := 0, []time.Duration{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		switch attempts {
		case 1:
			w.Header().Set("Retry-After", "2")
			w.WriteHeader(http.StatusTooManyRequests)
		case 2:
			w.Header().Set("Retry-After", "3600") // capped, never an hour-long sleep
			w.WriteHeader(http.StatusServiceUnavailable)
		default:
			w.Write(body)
		}
	}))
	defer srv.Close()

	c := &Client{BaseURL: srv.URL, HTTP: srv.Client(), Sleep: func(_ context.Context, d time.Duration) error {
		waits = append(waits, d)
		return nil
	}}
	res, err := c.Search(context.Background(), "pen", "leche", 2)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if res.Count != 2 || attempts != 3 {
		t.Fatalf("count=%d attempts=%d, want 2 and 3", res.Count, attempts)
	}
	if len(waits) != 2 || waits[0] != 2*time.Second || waits[1] != maxRetryWait {
		t.Errorf("waits = %v, want [2s %s]", waits, maxRetryWait)
	}
}

func TestSearchGivesUpAfterThreeUnavailableAttempts(t *testing.T) {
	attempts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	c := &Client{BaseURL: srv.URL, HTTP: srv.Client(), Sleep: func(context.Context, time.Duration) error { return nil }}
	if _, err := c.Search(context.Background(), "pen", "leche", 2); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v, want ErrUnavailable", err)
	}
	if attempts != maxAttempts {
		t.Errorf("attempts = %d, want %d", attempts, maxAttempts)
	}
}

// The key is public but rotates with the front-end bundle; say so instead of
// reporting a bare 403.
func TestSearchExplainsARejectedSearchKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		io.WriteString(w, `{"message":"Invalid Application-ID or API key"}`)
	}))
	defer srv.Close()

	c := &Client{BaseURL: srv.URL, HTTP: srv.Client(), Sleep: func(context.Context, time.Duration) error { return nil }}
	_, err := c.Search(context.Background(), "pen", "leche", 2)
	if !errors.Is(err, ErrSearchKey) {
		t.Fatalf("err = %v, want ErrSearchKey", err)
	}
	if !strings.Contains(err.Error(), "403") {
		t.Errorf("error should name the status: %v", err)
	}
}

func TestSearchWithoutHitsEncodesAnEmptyArray(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"hits":[],"nbHits":0,"query":"xyz"}`)
	}))
	defer srv.Close()

	c := &Client{BaseURL: srv.URL, HTTP: srv.Client(), Sleep: func(context.Context, time.Duration) error { return nil }}
	res, err := c.Search(context.Background(), "pen", "xyz", 20)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if res.Count != 0 || res.Hits == nil {
		t.Fatalf("res = %+v, want a non-nil empty slice", res)
	}
	var buf strings.Builder
	if err := contract.Encode(&buf, res); err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if !strings.Contains(buf.String(), `"hits": []`) {
		t.Errorf("empty result must encode as []: %s", buf.String())
	}
}

func TestSearchReportsAnUnparsableBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `<html>not json</html>`)
	}))
	defer srv.Close()

	c := &Client{BaseURL: srv.URL, HTTP: srv.Client(), Sleep: func(context.Context, time.Duration) error { return nil }}
	if _, err := c.Search(context.Background(), "pen", "leche", 2); err == nil {
		t.Fatal("want an error for a non-JSON body")
	}
}

// lastBody returns the JSON body of the most recent request.
func (f *fixture) lastBody(*testing.T) string {
	return f.body
}

// The declared sales unit is the only size ALDI publishes for most of the
// assortment, so its spelling variants are what €/kg and €/L hang on.
func TestParseQuantityReadsTheDeclaredSalesUnit(t *testing.T) {
	cases := []struct {
		salesUnit string
		size      float64
		unit      string
	}{
		{"130 g unidad", 0.13, "kg"},
		{"1 l unidad", 1, "L"},
		{"0,75 l unidad", 0.75, "L"},
		{"3 kg unidad", 3, "kg"},
		{"250 ml unidad", 0.25, "L"},
		{"450 g aprox. unidad", 0.45, "kg"},
		{"pack de 3 x 150 g peso esc.", 0.45, "kg"},
		{"pack de 10 unidades", 10, "unit"},
		// Declared as a bulk price, never as a size: no size, no unit price.
		{"precio kg (a granel)", 0, ""},
		{"", 0, ""},
	}
	for _, c := range cases {
		size, unit := parseQuantity(c.salesUnit)
		if size != c.size || unit != c.unit {
			t.Errorf("parseQuantity(%q) = %v %q, want %v %q", c.salesUnit, size, unit, c.size, c.unit)
		}
	}
}

// ALDI publishes "100-g" base prices whose value does not match the declared size
// (0,77 € for 20 g listed as 48,13 €/100 g), so they are not converted into €/kg.
func TestPublishedUnitPriceOnlyUsesScalesTheContractExpresses(t *testing.T) {
	cases := []struct {
		value float64
		scale string
		unit  string
	}{
		{10.54, "kg", "kg"},
		{0.96, "l", "L"},
		{0.38, "unidad", "unit"},
		{48.13, "100-g", ""},
		{48.13, "g", ""},
	}
	for _, c := range cases {
		value, unit := publishedUnitPrice(c.value, c.scale)
		if unit != c.unit || (unit != "" && value != c.value) || (unit == "" && value != 0) {
			t.Errorf("publishedUnitPrice(%v, %q) = %v %q, want %v %q", c.value, c.scale, value, unit, c.value, c.unit)
		}
	}
}
