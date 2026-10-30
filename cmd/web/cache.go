package main

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"multisuper/internal/aggregate"
	"multisuper/internal/contract"
)

// defaultCacheTTL is how long one chain's answer is reused. Carrefour's quota is 3
// requests per minute and Alcampo re-arms its WAF after a burst, so a second identical
// search inside a minute must cost zero network passes.
const defaultCacheTTL = 60 * time.Second

// cache is a tiny in-memory TTL cache. ttl and now are injected so a test drives the
// clock instead of sleeping for a minute.
type cache struct {
	mu      sync.Mutex
	ttl     time.Duration
	now     func() time.Time
	entries map[string]cacheEntry
}

type cacheEntry struct {
	res     contract.Result
	err     error
	expires time.Time
}

func newCache(ttl time.Duration, now func() time.Time) *cache {
	if now == nil {
		now = time.Now
	}
	return &cache{ttl: ttl, now: now, entries: make(map[string]cacheEntry)}
}

// get returns a live entry, dropping it when it has expired.
func (c *cache) get(key string) (contract.Result, error, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok {
		return contract.Result{}, nil, false
	}
	if !c.now().Before(e.expires) {
		delete(c.entries, key)
		return contract.Result{}, nil, false
	}
	return e.res, e.err, true
}

// put stores one answer. A disabled cache (ttl <= 0) stores nothing rather than
// pretending every answer is instantly stale.
func (c *cache) put(key string, res contract.Result, err error) {
	if c.ttl <= 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	// Keep the map bounded: prune the expired entries before adding to a large one.
	if len(c.entries) > 512 {
		now := c.now()
		for k, e := range c.entries {
			if !now.Before(e.expires) {
				delete(c.entries, k)
			}
		}
	}
	c.entries[key] = cacheEntry{res: res, err: err, expires: c.now().Add(c.ttl)}
}

// cachedChains puts the cache in front of every chain's searcher for one endpoint. The
// key is endpoint + chain + term (+ limit, so a shorter page is never served a longer
// one). A failure is cached too: Carrefour's quota is 3/min and Alcampo re-arms its WAF,
// so asking the same thing again inside the TTL must cost nothing, failed or not. A
// request that means to retry says so with ?fresh=1 and reads past the cache.
//
// chains is the fan-out for this request: a search asks only the chains the shopper
// marked, and a chain left out never gets a cache entry either.
func (s *server) cachedChains(endpoint string, fresh bool, chains []aggregate.Chain) []aggregate.Chain {
	out := make([]aggregate.Chain, len(chains))
	for i, c := range chains {
		out[i] = aggregate.Chain{
			Store: c.Store,
			Searcher: cachedSearcher{
				endpoint: endpoint, store: c.Store, inner: c.Searcher, cache: s.cache, fresh: fresh,
			},
		}
	}
	return out
}

// cachedSearcher is one chain behind the TTL cache, still an aggregate.Searcher.
type cachedSearcher struct {
	endpoint string
	store    string
	inner    aggregate.Searcher
	cache    *cache
	// fresh skips the read side, so «reintentar» is honest inside the TTL.
	fresh bool
}

func (c cachedSearcher) Search(ctx context.Context, term string, limit int) (contract.Result, error) {
	key := strings.Join([]string{c.endpoint, c.store, term, strconv.Itoa(limit)}, "\x00")
	if !c.fresh {
		if res, err, ok := c.cache.get(key); ok {
			return res, err
		}
	}
	res, err := c.inner.Search(ctx, term, limit)
	c.cache.put(key, res, err)
	return res, err
}

// fresh reports whether this request asked to read past the cache.
func fresh(r *http.Request) bool {
	return r.URL.Query().Has("fresh")
}
