package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"slices"
	"strings"
	"time"

	"multisuper/internal/aggregate"
	"multisuper/internal/basket"
	"multisuper/internal/capabilities"
	"multisuper/internal/dia"
)

const (
	// maxBody bounds one request body: a basket is a list of small objects, not a file.
	maxBody = 1 << 20
	// defaultSearchLimit is what /api/search uses when the client sends no limit.
	defaultSearchLimit = 20
	// maxSearchLimit mirrors the CLIs' --limit ceiling.
	maxSearchLimit = 100
	// maxRequestTimeout caps one request end to end, well above the per-chain budget so
	// the six chains still get their own deadline before the handler gives up.
	maxRequestTimeout = 30 * time.Second
)

// diaCartAPI is the slice of dia.Client the cart hand-off needs, so handler tests run
// offline. It is never the search client: only DIA has a working retailer cart.
type diaCartAPI interface {
	SetSessionID(id string)
	SessionID() string
	AddItem(ctx context.Context, objectID string, qty int) error
	Cart(ctx context.Context) (dia.Cart, error)
}

// cartStore is where the anonymous DIA session lives between requests.
type cartStore interface {
	Load() (string, error)
	Save(sessionID string) error
}

// options is everything the server needs; tests inject stubs and a fixed clock.
type options struct {
	chains       []aggregate.Chain
	cacheTTL     time.Duration
	now          func() time.Time
	chainTimeout time.Duration
	candidates   int
	cart         diaCartAPI
	cartStore    cartStore
	assets       fs.FS
}

// server serves the front end and the JSON API over the packages the CLIs already use.
type server struct {
	chains       []aggregate.Chain
	storeNames   []string
	cache        *cache
	chainTimeout time.Duration
	requestLimit time.Duration
	candidates   int
	cart         diaCartAPI
	cartStore    cartStore
	assets       fs.FS
}

func newServer(o options) *server {
	if o.chainTimeout <= 0 {
		o.chainTimeout = 20 * time.Second
	}
	if o.candidates < 1 {
		o.candidates = basket.DefaultProposals
	}
	names := make([]string, len(o.chains))
	for i, c := range o.chains {
		names[i] = c.Store
	}
	return &server{
		chains:       o.chains,
		storeNames:   names,
		cache:        newCache(o.cacheTTL, o.now),
		chainTimeout: o.chainTimeout,
		requestLimit: maxRequestTimeout,
		candidates:   o.candidates,
		cart:         o.cart,
		cartStore:    o.cartStore,
		assets:       o.assets,
	}
}

// handler wires the routes. Method patterns make ServeMux answer 405 for a wrong verb.
func (s *server) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/caps", s.handleCaps)
	mux.HandleFunc("POST /api/search", s.handleSearch)
	mux.HandleFunc("POST /api/candidates", s.handleCandidates)
	mux.HandleFunc("POST /api/basket", s.handleBasket)
	mux.HandleFunc("POST /api/cart/dia", s.handleDIACart)
	if s.assets != nil {
		// Only the three embedded paths are served: a catch-all GET would shadow the
		// 405 that ServeMux answers when /api/search is asked with the wrong verb.
		files := http.FileServerFS(s.assets)
		for _, path := range []string{"/{$}", "/styles.css", "/app.js", "/favicon.svg"} {
			mux.Handle("GET "+path, files)
		}
	}
	return mux
}

// capsDoc is the /api/caps document: the capability table, unchanged.
type capsDoc struct {
	Chains []capabilities.Capability `json:"chains"`
}

func (s *server) handleCaps(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, capsDoc{Chains: capabilities.All()})
}

// searchRequest is /api/search's body: a term, how many hits per chain, and which chains
// to ask. An empty Stores asks every chain, so the CLIs' shape still works.
type searchRequest struct {
	Term   string   `json:"term"`
	Limit  int      `json:"limit"`
	Stores []string `json:"stores"`
}

// handleSearch fans one term out to the six chains and returns the aggregate document the
// CLI prints, so a per-chain failure arrives as its own code and never voids the rest.
func (s *server) handleSearch(w http.ResponseWriter, r *http.Request) {
	var req searchRequest
	if !decodeBody(w, r, &req) {
		return
	}
	req.Term = strings.TrimSpace(req.Term)
	if req.Term == "" {
		writeError(w, http.StatusBadRequest, "term is empty")
		return
	}
	if req.Limit == 0 {
		req.Limit = defaultSearchLimit
	}
	if req.Limit < 1 || req.Limit > maxSearchLimit {
		writeError(w, http.StatusBadRequest,
			fmt.Sprintf("limit must be between 1 and %d, got %d", maxSearchLimit, req.Limit))
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), s.requestLimit)
	defer cancel()
	chains, err := s.selectChains(req.Stores)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	res := aggregate.Find(ctx, req.Term, req.Limit, s.chainTimeout, s.cachedChains("search", fresh(r), chains))
	writeJSON(w, http.StatusOK, res)
}

// selectChains narrows the fan-out to the chains the shopper marked, in the server's own
// order. No list means every chain. A name this server does not serve is the caller's
// mistake: asking the remaining chains anyway would answer a question nobody asked, and
// would spend their quota doing it.
func (s *server) selectChains(stores []string) ([]aggregate.Chain, error) {
	if len(stores) == 0 {
		return s.chains, nil
	}
	want := make(map[string]bool, len(stores))
	for _, name := range stores {
		name = strings.ToLower(strings.TrimSpace(name))
		if !slices.Contains(s.storeNames, name) {
			return nil, fmt.Errorf("unknown store %q: this server serves %s", name, strings.Join(s.storeNames, ", "))
		}
		want[name] = true
	}
	out := make([]aggregate.Chain, 0, len(want))
	for _, c := range s.chains {
		if want[c.Store] {
			out = append(out, c)
		}
	}
	return out, nil
}

// handleCandidates proposes scored products per line and chain, so the user can pin the
// product of one chain without pinning the whole line.
func (s *server) handleCandidates(w http.ResponseWriter, r *http.Request) {
	req, ok := s.decodeBasket(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), s.requestLimit)
	defer cancel()
	res := basket.Candidates(ctx, req, s.cachedChains("candidates", fresh(r), s.chains), s.candidates, s.chainTimeout)
	writeJSON(w, http.StatusOK, res)
}

// handleBasket prices the user's own list of goods in every chain and returns the basket
// document unchanged: integer cents, coverage, missing and stale lines, and the ranking.
func (s *server) handleBasket(w http.ResponseWriter, r *http.Request) {
	req, ok := s.decodeBasket(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), s.requestLimit)
	defer cancel()
	res := basket.Price(ctx, req, s.cachedChains("basket", fresh(r), s.chains), basket.DefaultCandidates, s.chainTimeout)
	writeJSON(w, http.StatusOK, res)
}

// decodeBasket reads and validates a basket body. Every rejection here is the user's
// input, so it is a 400 with the package's own message.
func (s *server) decodeBasket(w http.ResponseWriter, r *http.Request) (basket.Request, bool) {
	r.Body = http.MaxBytesReader(w, r.Body, maxBody)
	req, err := basket.Decode(r.Body, s.storeNames)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return basket.Request{}, false
	}
	return req, true
}

// cartRequest is /api/cart/dia's body: the lines to put in DIA's real cart.
type cartRequest struct {
	Lines []struct {
		ID       string `json:"id"`
		Quantity int    `json:"quantity"`
	} `json:"lines"`
}

// handleDIACart is the delivery step, and only for DIA: it sets each line in the real
// anonymous cart and reads the cart back. It never checks out and never orders.
//
// A session DIA already forgot is not an error here: the first AddItem starts a new
// anonymous cart, exactly like `dia cart add`.
func (s *server) handleDIACart(w http.ResponseWriter, r *http.Request) {
	if s.cart == nil || s.cartStore == nil {
		writeError(w, http.StatusNotFound, "this server has no dia cart")
		return
	}
	var req cartRequest
	if !decodeBody(w, r, &req) {
		return
	}
	if len(req.Lines) == 0 {
		writeError(w, http.StatusBadRequest, "lines is empty")
		return
	}
	for i, l := range req.Lines {
		if strings.TrimSpace(l.ID) == "" {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("line %d: id is empty", i+1))
			return
		}
		if l.Quantity < 1 {
			writeError(w, http.StatusBadRequest,
				fmt.Sprintf("line %d: quantity must be at least 1, got %d", i+1, l.Quantity))
			return
		}
	}

	ctx, cancel := context.WithTimeout(r.Context(), s.requestLimit)
	defer cancel()

	session, err := s.cartStore.Load()
	switch {
	case err == nil:
		s.cart.SetSessionID(session)
	case errors.Is(err, dia.ErrNoCart):
		// No cart yet is the normal first add.
	default:
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	for _, l := range req.Lines {
		if err := s.cart.AddItem(ctx, strings.TrimSpace(l.ID), l.Quantity); err != nil {
			writeChainError(w, "dia", err)
			return
		}
	}
	if id := s.cart.SessionID(); id != "" {
		if err := s.cartStore.Save(id); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	cart, err := s.cart.Cart(ctx)
	if err != nil {
		writeChainError(w, "dia", err)
		return
	}
	writeJSON(w, http.StatusOK, cart)
}

// errDoc is the shape of an input error: one sentence, no store involved.
type errDoc struct {
	Error string `json:"error"`
}

// chainErrDoc is a chain failure in the CLIs' own envelope, so the front end switches on
// one set of codes whether it read them from a search or from the cart hand-off.
type chainErrDoc struct {
	Store string                `json:"store"`
	Error *aggregate.StoreError `json:"error"`
}

// writeChainError reports a chain failure with the stable code aggregate.Classify gives
// it (blocked, quota, timeout, network, parse, unknown).
func writeChainError(w http.ResponseWriter, store string, err error) {
	writeJSON(w, http.StatusBadGateway, chainErrDoc{Store: store, Error: aggregate.Classify(err)})
}

// decodeBody reads one JSON body, rejecting a trailing second document.
func decodeBody(w http.ResponseWriter, r *http.Request, dst any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxBody)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return false
	}
	if err := dec.Decode(new(json.RawMessage)); err != io.EOF {
		writeError(w, http.StatusBadRequest, "body carries more than one JSON document")
		return false
	}
	return true
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, errDoc{Error: msg})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		// The status line is already sent; nothing left to tell the client.
		return
	}
}
