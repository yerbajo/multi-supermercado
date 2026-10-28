// Command web serves the aggregator's front end and the small JSON API it needs, over
// the same Go packages the CLIs use: the six-chain fan-out with per-chain typed errors,
// the virtual basket with exact cents, and the only working retailer cart (DIA).
//
// One 60 s TTL cache per chain and endpoint keeps a repeated search from costing six
// network passes twice: Carrefour's quota is 3 per minute and Alcampo re-arms its WAF.
//
// Nothing here invents a price or a store: a chain that failed keeps its code, and the
// front end shows it as its own state instead of a number (S13).
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"multisuper/internal/aggregate"
	"multisuper/internal/alcampo"
	"multisuper/internal/aldi"
	"multisuper/internal/carrefour"
	"multisuper/internal/contract"
	"multisuper/internal/dia"
	"multisuper/internal/lidl"
	"multisuper/internal/mas"
	"multisuper/web"
)

// searchFunc adapts a chain client onto the one method the aggregate needs: mas takes a
// page and aldi a region, the rest match aggregate.Searcher directly.
type searchFunc func(ctx context.Context, term string, limit int) (contract.Result, error)

func (f searchFunc) Search(ctx context.Context, term string, limit int) (contract.Result, error) {
	return f(ctx, term, limit)
}

// chains returns the real clients, one per store, in the order the table prints them.
func chains() []aggregate.Chain {
	return []aggregate.Chain{
		{Store: "alcampo", Searcher: searchFunc(func(ctx context.Context, term string, limit int) (contract.Result, error) {
			return alcampo.New().Search(ctx, term, limit)
		})},
		{Store: "aldi", Searcher: searchFunc(func(ctx context.Context, term string, limit int) (contract.Result, error) {
			return aldi.NewClient().Search(ctx, aldi.DefaultRegion, term, limit)
		})},
		{Store: "carrefour", Searcher: searchFunc(func(ctx context.Context, term string, limit int) (contract.Result, error) {
			return carrefour.NewClient().Search(ctx, term, limit)
		})},
		{Store: "dia", Searcher: searchFunc(func(ctx context.Context, term string, limit int) (contract.Result, error) {
			return dia.NewClient().Search(ctx, term, limit)
		})},
		{Store: "lidl", Searcher: searchFunc(func(ctx context.Context, term string, limit int) (contract.Result, error) {
			return lidl.New().Search(ctx, term, limit)
		})},
		{Store: "mas", Searcher: searchFunc(func(ctx context.Context, term string, limit int) (contract.Result, error) {
			return mas.New().Search(ctx, term, 1, limit)
		})},
	}
}

// diaCart wires the only working retailer cart: one anonymous session kept in the user
// config dir, exactly like `dia cart`.
func diaCart() (diaCartAPI, cartStore, error) {
	path, err := dia.DefaultCartPath()
	if err != nil {
		return nil, nil, err
	}
	return dia.NewClient(), dia.CartFile{Path: path}, nil
}

func main() {
	addr := flag.String("addr", "127.0.0.1:8099", "address to listen on")
	timeout := flag.Duration("timeout", 20*time.Second, "budget per chain")
	ttl := flag.Duration("cache-ttl", defaultCacheTTL, "how long one chain's answer is reused")
	flag.Parse()

	cart, store, err := diaCart()
	if err != nil {
		// The search and the basket still work; only the DIA hand-off is unavailable.
		log.Printf("web: dia cart disabled: %v", err)
		cart, store = nil, nil
	}

	srv := newServer(options{
		chains:       chains(),
		cacheTTL:     *ttl,
		chainTimeout: *timeout,
		cart:         cart,
		cartStore:    store,
		assets:       web.Files,
	})

	httpServer := &http.Server{
		Addr:              *addr,
		Handler:           srv.handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	fmt.Fprintf(os.Stderr, "multisuper web on http://%s\n", *addr)
	if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("web: %v", err)
	}
}
