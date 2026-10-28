package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"multisuper/internal/aggregate"
	"multisuper/internal/basket"
	"multisuper/internal/capabilities"
	"multisuper/internal/contract"
	"multisuper/internal/dia"
	"multisuper/web"
)

// stubChain answers from memory and counts its calls, so a cache hit is observable as
// one call instead of two.
type stubChain struct {
	mu    sync.Mutex
	calls int64
	hits  []contract.Hit
	err   error
}

func (s *stubChain) Search(_ context.Context, term string, limit int) (contract.Result, error) {
	atomic.AddInt64(&s.calls, 1)
	s.mu.Lock()
	err, hits := s.err, s.hits
	s.mu.Unlock()
	if err != nil {
		return contract.Result{}, err
	}
	if limit > 0 && limit < len(hits) {
		hits = hits[:limit]
	}
	return contract.Result{Query: term, Count: len(hits), Hits: hits}, nil
}

func (s *stubChain) callCount() int64 { return atomic.LoadInt64(&s.calls) }

func (s *stubChain) setErr(err error) {
	s.mu.Lock()
	s.err = err
	s.mu.Unlock()
}

func (s *stubChain) setHits(hits []contract.Hit) {
	s.mu.Lock()
	s.hits = hits
	s.mu.Unlock()
}

func price(v float64) *float64 { return &v }

// fakeClock is the injected clock: no test waits a real minute for a TTL.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// testServer has one answering chain and one over quota, which is the shape the front end
// must render without losing the comparison.
func testServer(t *testing.T) (*server, *stubChain, *stubChain, *fakeClock) {
	t.Helper()
	clock := &fakeClock{t: time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)}
	answering := &stubChain{hits: []contract.Hit{
		{Store: "dia", ID: "312953", Name: "Leche entera Dia Láctea 1 l", Price: price(0.92)},
		{Store: "dia", ID: "999", Name: "Leche entera Dia 6 x 1 l", Price: price(4.98)},
	}}
	quota := &stubChain{err: &aggregate.StatusError{Status: http.StatusTooManyRequests, Msg: "HTTP 429: rate limit"}}
	srv := newServer(options{
		chains: []aggregate.Chain{
			{Store: "dia", Searcher: answering},
			{Store: "carrefour", Searcher: quota},
		},
		cacheTTL:     time.Minute,
		now:          clock.now,
		chainTimeout: time.Second,
		assets:       web.Files,
	})
	return srv, answering, quota, clock
}

func do(t *testing.T, h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var reader *bytes.Reader
	if body == "" {
		reader = bytes.NewReader(nil)
	} else {
		reader = bytes.NewReader([]byte(body))
	}
	req := httptest.NewRequest(method, path, reader)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// TestCapsEndpoint is the table the store cards are built from: six chains, and only DIA
// claims a cart.
func TestCapsEndpoint(t *testing.T) {
	srv, _, _, _ := testServer(t)
	rec := do(t, srv.handler(), http.MethodGet, "/api/caps", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, want 200: %s", rec.Code, rec.Body)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json; charset=utf-8" {
		t.Fatalf("content type %q", ct)
	}
	var doc capsDoc
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(doc.Chains) != 6 {
		t.Fatalf("got %d chains, want 6", len(doc.Chains))
	}
	var carts []string
	for _, c := range doc.Chains {
		if c.Cart {
			carts = append(carts, c.Store)
		}
		if c.Zone.Label == "" || c.Zone.Mode == "" {
			t.Errorf("%s: zone is empty in the JSON", c.Store)
		}
	}
	if len(carts) != 1 || carts[0] != "dia" {
		t.Fatalf("cart is true for %v, want only dia", carts)
	}
	if doc.Chains[1].Store != "aldi" || !doc.Chains[1].Zone.PriceScoped {
		t.Fatalf("aldi row lost its price scope: %+v", doc.Chains[1])
	}
}

// TestSearchEndpoint checks the envelope and the per-chain error passthrough: a chain in
// quota arrives as its own code and never voids the chain that answered.
func TestSearchEndpoint(t *testing.T) {
	srv, _, _, _ := testServer(t)
	rec := do(t, srv.handler(), http.MethodPost, "/api/search", `{"term":"leche","limit":2}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var res aggregate.Result
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if res.Query != "leche" || res.Count != 2 || len(res.Results) != 2 {
		t.Fatalf("unexpected result: %+v", res)
	}
	if res.Results[0].Store != "dia" || res.Results[0].Count != 2 || res.Results[0].Error != nil {
		t.Fatalf("dia result: %+v", res.Results[0])
	}
	quota := res.Results[1]
	if quota.Store != "carrefour" || quota.Error == nil || quota.Error.Code != aggregate.CodeQuota || !quota.Error.Retryable {
		t.Fatalf("carrefour result: %+v", quota)
	}
	if quota.Hits == nil || len(quota.Hits) != 0 {
		t.Fatalf("a failed chain must still carry an empty hits array: %+v", quota.Hits)
	}
}

// TestSearchCacheHit is the quota guard: the same search twice costs one network pass per
// chain, and the answer is reused until the injected clock passes the TTL.
func TestSearchCacheHit(t *testing.T) {
	srv, answering, quota, clock := testServer(t)
	h := srv.handler()
	body := `{"term":"leche","limit":2}`

	first := do(t, h, http.MethodPost, "/api/search", body)
	second := do(t, h, http.MethodPost, "/api/search", body)
	if first.Code != http.StatusOK || second.Code != http.StatusOK {
		t.Fatalf("statuses %d and %d", first.Code, second.Code)
	}
	if got := answering.callCount(); got != 1 {
		t.Fatalf("dia searched %d times, want 1 (cache hit)", got)
	}
	if got := quota.callCount(); got != 1 {
		t.Fatalf("carrefour searched %d times, want 1 (cache hit)", got)
	}
	if first.Body.String() != second.Body.String() {
		t.Fatalf("cached answer differs:\n%s\n%s", first.Body, second.Body)
	}

	clock.advance(61 * time.Second)
	if rec := do(t, h, http.MethodPost, "/api/search", body); rec.Code != http.StatusOK {
		t.Fatalf("status %d after TTL", rec.Code)
	}
	if got := answering.callCount(); got != 2 {
		t.Fatalf("dia searched %d times after the TTL, want 2", got)
	}

	// A different limit is a different page: it may not be served the cached one.
	do(t, h, http.MethodPost, "/api/search", `{"term":"leche","limit":5}`)
	if got := answering.callCount(); got != 3 {
		t.Fatalf("dia searched %d times, want 3 for a different limit", got)
	}
}

// TestSearchRetryBypassesTheCache keeps both promises: a repeated identical search never
// hits the network, even when the first answer was a quota failure, and the «reintentar»
// button is still honest because it asks with ?fresh=1.
func TestSearchRetryBypassesTheCache(t *testing.T) {
	srv, _, quota, _ := testServer(t)
	h := srv.handler()
	body := `{"term":"leche","limit":2}`
	do(t, h, http.MethodPost, "/api/search", body)
	do(t, h, http.MethodPost, "/api/search", body)
	if quota.callCount() != 1 {
		t.Fatalf("carrefour was asked %d times for the same search, want 1", quota.callCount())
	}

	quota.setErr(nil)
	quota.setHits([]contract.Hit{{Store: "carrefour", ID: "1", Name: "Leche", Price: price(1.10)}})
	rec := do(t, h, http.MethodPost, "/api/search?fresh=1", body)
	if quota.callCount() != 2 {
		t.Fatalf("carrefour was asked %d times with ?fresh=1, want 2", quota.callCount())
	}
	var res aggregate.Result
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if res.Results[1].Error != nil || res.Results[1].Count != 1 {
		t.Fatalf("retry kept the old failure: %+v", res.Results[1])
	}
}

// TestSearchRejectsBadInput pins the 400s: an empty term, a limit outside 1..100 and a
// wrong method never reach the chains.
func TestSearchRejectsBadInput(t *testing.T) {
	srv, answering, _, _ := testServer(t)
	h := srv.handler()
	cases := []struct {
		name, body string
		status     int
	}{
		{"empty term", `{"term":"  "}`, http.StatusBadRequest},
		{"limit too high", `{"term":"leche","limit":101}`, http.StatusBadRequest},
		{"limit negative", `{"term":"leche","limit":-1}`, http.StatusBadRequest},
		{"unknown field", `{"term":"leche","nope":1}`, http.StatusBadRequest},
		{"empty body", ``, http.StatusBadRequest},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := do(t, h, http.MethodPost, "/api/search", c.body)
			if rec.Code != c.status {
				t.Fatalf("status %d, want %d: %s", rec.Code, c.status, rec.Body)
			}
			var doc errDoc
			if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil || doc.Error == "" {
				t.Fatalf("error envelope: %s", rec.Body)
			}
		})
	}
	if rec := do(t, h, http.MethodGet, "/api/search", ""); rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET /api/search: status %d, want 405", rec.Code)
	}
	if answering.callCount() != 0 {
		t.Fatalf("a rejected request reached the chain %d times", answering.callCount())
	}
}

// TestCandidatesEndpoint checks the proposal document keeps its shape, including the
// chain that failed proposing nothing and saying why.
func TestCandidatesEndpoint(t *testing.T) {
	srv, _, _, _ := testServer(t)
	rec := do(t, srv.handler(), http.MethodPost, "/api/candidates",
		`{"items":[{"line":"leche","query":"leche","quantity":2}]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var res basket.CandidatesResult
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(res.Items) != 1 || res.Items[0].Line != "leche" || res.Items[0].Query != "leche" {
		t.Fatalf("items: %+v", res.Items)
	}
	cands := res.Items[0].Candidates["dia"]
	if len(cands) == 0 || cands[0].ID == "" || cands[0].UnitCents <= 0 {
		t.Fatalf("dia candidates: %+v", cands)
	}
	if len(res.Chains) != 2 || res.Chains[1].Store != "carrefour" ||
		res.Chains[1].Error == nil || res.Chains[1].Error.Code != aggregate.CodeQuota {
		t.Fatalf("chains: %+v", res.Chains)
	}
}

// TestBasketEndpoint prices the basket in exact cents and ranks only the chain that
// covers it, keeping the failed chain's code.
func TestBasketEndpoint(t *testing.T) {
	srv, _, _, _ := testServer(t)
	rec := do(t, srv.handler(), http.MethodPost, "/api/basket",
		`{"items":[{"line":"leche","query":"leche","quantity":2}]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var res basket.Result
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(res.Chains) != 2 {
		t.Fatalf("chains: %+v", res.Chains)
	}
	got := res.Chains[0]
	if got.Store != "dia" || got.TotalCents != 184 || got.Coverage != "1/1" || got.Error != nil {
		t.Fatalf("dia chain: %+v", got)
	}
	if res.Chains[1].Error == nil || res.Chains[1].Error.Code != aggregate.CodeQuota {
		t.Fatalf("carrefour chain: %+v", res.Chains[1])
	}
	if len(res.Ranking) != 1 || res.Ranking[0].Store != "dia" || res.Ranking[0].TotalCents != 184 {
		t.Fatalf("ranking: %+v", res.Ranking)
	}
	if res.Items[0].Resolved["dia"].LineCents != 184 {
		t.Fatalf("item: %+v", res.Items[0])
	}
}

// TestBasketRejectsInvalidInput keeps every basket rule the CLI enforces: an empty basket
// and a broken line group are the user's input, so they are 400 and never priced.
func TestBasketRejectsInvalidInput(t *testing.T) {
	srv, answering, _, _ := testServer(t)
	h := srv.handler()
	for _, body := range []string{
		`{"items":[]}`,
		`{"items":[{"query":"leche"}]}`,
		`{"items":[{"line":"huevos","store":"dia","id":"1","name":"Huevos","quantity":1},{"line":"huevos","query":"huevos","quantity":2}]}`,
	} {
		rec := do(t, h, http.MethodPost, "/api/basket", body)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("body %s: status %d, want 400: %s", body, rec.Code, rec.Body)
		}
	}
	if answering.callCount() != 0 {
		t.Fatalf("an invalid basket reached a chain %d times", answering.callCount())
	}
}

// stubCart is the DIA cart interface offline: it records what the handler put in it.
type stubCart struct {
	mu       sync.Mutex
	items    []string
	session  string
	failNext error
}

func (c *stubCart) SetSessionID(id string) {
	c.mu.Lock()
	c.session = id
	c.mu.Unlock()
}

func (c *stubCart) SessionID() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.session
}

func (c *stubCart) AddItem(_ context.Context, id string, _ int) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.failNext != nil {
		return c.failNext
	}
	c.items = append(c.items, id)
	c.session = "sess-new"
	return nil
}

func (c *stubCart) Cart(_ context.Context) (dia.Cart, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	total := 4.17
	return dia.Cart{ID: "cart-1", Total: &total, Lines: []dia.Line{
		{ObjectID: "305802", Name: "Huevos", Quantity: 2},
	}}, nil
}

type stubStore struct {
	loaded string
	saved  string
}

func (s *stubStore) Load() (string, error) { return s.loaded, nil }
func (s *stubStore) Save(id string) error  { s.saved = id; return nil }

// TestCartDIAFillsTheOnlyRealCart covers the delivery step: the lines go into DIA's cart,
// the anonymous session is kept, and only DIA is ever touched.
func TestCartDIAFillsTheOnlyRealCart(t *testing.T) {
	srv, _, _, _ := testServer(t)
	cart := &stubCart{}
	store := &stubStore{}
	srv.cart, srv.cartStore = cart, store

	rec := do(t, srv.handler(), http.MethodPost, "/api/cart/dia",
		`{"lines":[{"id":"305802","quantity":2},{"id":"312953","quantity":1}]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var got dia.Cart
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.ID != "cart-1" || got.Total == nil || *got.Total != 4.17 || len(got.Lines) != 1 {
		t.Fatalf("cart document: %+v", got)
	}
	if len(cart.items) != 2 || cart.items[0] != "305802" || cart.items[1] != "312953" {
		t.Fatalf("items added: %v", cart.items)
	}
	if store.saved != "sess-new" {
		t.Fatalf("session saved %q, want the new one", store.saved)
	}
}

// TestCartDIAReportsChainErrors keeps the envelope one contract: a rejected cart answers
// with the same store/error document a failed search uses.
func TestCartDIAReportsChainErrors(t *testing.T) {
	srv, _, _, _ := testServer(t)
	cart := &stubCart{failNext: &aggregate.StatusError{Status: http.StatusTooManyRequests, Msg: "HTTP 429: rate limit"}}
	srv.cart, srv.cartStore = cart, &stubStore{}

	rec := do(t, srv.handler(), http.MethodPost, "/api/cart/dia", `{"lines":[{"id":"1","quantity":1}]}`)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status %d, want 502: %s", rec.Code, rec.Body)
	}
	var doc chainErrDoc
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if doc.Store != "dia" || doc.Error == nil || doc.Error.Code != aggregate.CodeQuota {
		t.Fatalf("chain error envelope: %+v", doc)
	}
}

// TestCartDIAValidatesInput: an empty list or a zero quantity never reaches the store.
func TestCartDIAValidatesInput(t *testing.T) {
	srv, _, _, _ := testServer(t)
	cart := &stubCart{}
	srv.cart, srv.cartStore = cart, &stubStore{}
	h := srv.handler()
	for _, body := range []string{`{"lines":[]}`, `{"lines":[{"id":"","quantity":1}]}`, `{"lines":[{"id":"1","quantity":0}]}`} {
		if rec := do(t, h, http.MethodPost, "/api/cart/dia", body); rec.Code != http.StatusBadRequest {
			t.Fatalf("body %s: status %d, want 400", body, rec.Code)
		}
	}
	if len(cart.items) != 0 {
		t.Fatalf("invalid cart reached the store: %v", cart.items)
	}
}

// TestCartDIAAbsentIsHonest: without a cart client the endpoint says so instead of
// pretending DIA has one.
func TestCartDIAAbsentIsHonest(t *testing.T) {
	srv, _, _, _ := testServer(t)
	if rec := do(t, srv.handler(), http.MethodPost, "/api/cart/dia", `{"lines":[{"id":"1","quantity":1}]}`); rec.Code != http.StatusNotFound {
		t.Fatalf("status %d, want 404", rec.Code)
	}
}

// TestStaticFrontEndIsServed: the page comes from the embedded files, so the server runs
// from any directory.
func TestStaticFrontEndIsServed(t *testing.T) {
	srv, _, _, _ := testServer(t)
	h := srv.handler()
	for path, want := range map[string]string{
		"/":           "Tienda cercana",
		"/app.js":     "loadCaps",
		"/styles.css": "--serif",
	} {
		rec := do(t, h, http.MethodGet, path, "")
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status %d", path, rec.Code)
		}
		if !bytes.Contains(rec.Body.Bytes(), []byte(want)) {
			t.Fatalf("%s: body does not contain %q", path, want)
		}
	}
}

// TestCapabilitiesMatchTheServerTable keeps one source of truth: the endpoint serves the
// package's table, so a chain added to the table appears without a change here.
func TestCapabilitiesMatchTheServerTable(t *testing.T) {
	srv, _, _, _ := testServer(t)
	rec := do(t, srv.handler(), http.MethodGet, "/api/caps", "")
	var doc capsDoc
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatalf("decode: %v", err)
	}
	want := capabilities.All()
	if len(doc.Chains) != len(want) {
		t.Fatalf("got %d rows, want %d", len(doc.Chains), len(want))
	}
	for i := range want {
		if doc.Chains[i] != want[i] {
			t.Fatalf("row %d: got %+v, want %+v", i, doc.Chains[i], want[i])
		}
	}
}
