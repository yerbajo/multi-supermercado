// Package basket prices one virtual shopping basket in every supermarket chain: the
// user's own list of goods, resolved to one product per chain, so the user learns what
// that same basket would cost in each chain and what would be missing there (S10/L6).
//
// The basket is ours, not a retailer's: no chain cart is created, and no cross-chain
// product identity is assumed. Only Carrefour publishes an EAN, so each item is matched
// by name with the score documented in score.go, and a weak match is reported missing
// rather than tied to whatever product happened to be in the first results.
package basket

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"time"

	"multisuper/internal/aggregate"
)

// DefaultCandidates is how many results per item and chain are scored. Ten is enough
// to contain the same product in every chain and small enough to stay one page.
const DefaultCandidates = 10

// Item is one line the user wrote: what they want and how many.
type Item struct {
	Query    string `json:"query"`
	Quantity int    `json:"quantity"`
}

// Request is the basket file: at least one item, one search per item per chain.
type Request struct {
	Items []Item `json:"items"`
}

// Resolved is the product one chain would put in the basket for an item.
type Resolved struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	UnitCents int64  `json:"unitCents"`
	LineCents int64  `json:"lineCents"`
}

// ItemResult is one basket line as every chain answered it. Missing names the chains
// that answered and do not sell the item; a chain that failed is not missing there, it
// is in Chains with its error.
type ItemResult struct {
	Query    string              `json:"query"`
	Quantity int                 `json:"quantity"`
	Resolved map[string]Resolved `json:"resolved"`
	Missing  []string            `json:"missing"`
}

// ChainResult is one chain's candidate basket. Coverage is resolved/total, empty when
// the chain failed; a failed chain has no total, because a partial sum is not a price.
type ChainResult struct {
	Store      string                `json:"store"`
	TotalCents int64                 `json:"totalCents"`
	Coverage   string                `json:"coverage,omitempty"`
	ElapsedMs  int64                 `json:"elapsedMs"`
	Missing    []string              `json:"missing"`
	Error      *aggregate.StoreError `json:"error,omitempty"`
}

// Rank is a chain that can fill the whole basket, cheapest first.
type Rank struct {
	Store      string `json:"store"`
	TotalCents int64  `json:"totalCents"`
}

// Result is the single document `multisuper basket` prints. Ranking holds only the
// chains with full coverage and no error; it is empty when no chain can fill the
// basket, which is an answer and not a failure to compute one.
type Result struct {
	Items   []ItemResult  `json:"items"`
	Chains  []ChainResult `json:"chains"`
	Ranking []Rank        `json:"ranking"`
}

// Encode writes the JSON the web app consumes: indented, one document, trailing newline.
func Encode(w io.Writer, r Result) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}

// Decode reads and validates a basket. Every problem here is the user's input, so the
// command maps it onto exit 2.
func Decode(r io.Reader) (Request, error) {
	dec := json.NewDecoder(r)
	dec.DisallowUnknownFields()
	var req Request
	if err := dec.Decode(&req); err != nil {
		return Request{}, fmt.Errorf("basket is not valid JSON: %w", err)
	}
	if err := dec.Decode(new(json.RawMessage)); err != io.EOF {
		return Request{}, errors.New("basket carries more than one JSON document")
	}
	if err := req.validate(); err != nil {
		return Request{}, err
	}
	for i := range req.Items {
		req.Items[i].Query = strings.TrimSpace(req.Items[i].Query)
	}
	return req, nil
}

// validate is the rule the basket file must satisfy: at least one item, a query, and a
// whole quantity of at least one.
func (r Request) validate() error {
	if len(r.Items) == 0 {
		return errors.New("basket has no items")
	}
	for i, it := range r.Items {
		if strings.TrimSpace(it.Query) == "" {
			return fmt.Errorf("item %d: query is empty", i+1)
		}
		if it.Quantity < 1 {
			return fmt.Errorf("item %d (%s): quantity must be a whole number of at least 1, got %d",
				i+1, it.Query, it.Quantity)
		}
	}
	return nil
}

// Price resolves every item in every chain and prices the basket in each one. Chains
// are asked in parallel, one budget each, exactly like aggregate.Find: a slow or failing
// chain is reported in its own entry and never voids the others.
//
// No cache and no cross-chain identity: this process serves one basket, and the only
// published identifier the chains share (the EAN) is missing from five of them.
func Price(ctx context.Context, req Request, chains []aggregate.Chain, candidates int, timeout time.Duration) Result {
	if candidates < 1 {
		candidates = DefaultCandidates
	}
	res := Result{
		Items:   make([]ItemResult, len(req.Items)),
		Chains:  make([]ChainResult, len(chains)),
		Ranking: []Rank{},
	}
	for i, it := range req.Items {
		res.Items[i] = ItemResult{
			Query:    it.Query,
			Quantity: it.Quantity,
			Resolved: make(map[string]Resolved),
			Missing:  []string{},
		}
	}

	type answer struct {
		resolved map[int]Resolved
		err      *aggregate.StoreError
		elapsed  int64
	}
	answers := make([]answer, len(chains))

	var wg sync.WaitGroup
	for i, c := range chains {
		wg.Add(1)
		go func() {
			defer wg.Done()
			start := time.Now()
			// One context per chain: one chain's deadline must not take the rest down.
			cctx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()
			a := answer{resolved: make(map[int]Resolved, len(req.Items))}
			defer func() {
				a.elapsed = time.Since(start).Milliseconds()
				answers[i] = a
			}()
			for j, it := range req.Items {
				got, err := c.Searcher.Search(cctx, it.Query, candidates)
				if err != nil {
					// A chain that failed mid-basket keeps no partial result: half a
					// basket priced from half a catalog is not a price.
					a.err = aggregate.Classify(err)
					return
				}
				h, cents, ok := best(it.Query, got.Hits)
				if !ok {
					continue
				}
				a.resolved[j] = Resolved{
					ID:        h.ID,
					Name:      h.Name,
					UnitCents: cents,
					LineCents: cents * int64(it.Quantity),
				}
			}
		}()
	}
	wg.Wait()

	full := make([]int, 0, len(chains))
	for i, c := range chains {
		a := answers[i]
		cr := ChainResult{Store: c.Store, ElapsedMs: a.elapsed, Missing: []string{}}
		if a.err != nil {
			cr.Error = a.err
			res.Chains[i] = cr
			continue
		}
		for j := range req.Items {
			r, ok := a.resolved[j]
			if !ok {
				cr.Missing = append(cr.Missing, req.Items[j].Query)
				res.Items[j].Missing = append(res.Items[j].Missing, c.Store)
				continue
			}
			res.Items[j].Resolved[c.Store] = r
			cr.TotalCents += r.LineCents
		}
		cr.Coverage = fmt.Sprintf("%d/%d", len(a.resolved), len(req.Items))
		res.Chains[i] = cr
		if len(cr.Missing) == 0 {
			full = append(full, i)
		}
	}

	// Cheapest first, and a tie keeps the table order so the ranking is stable.
	sort.SliceStable(full, func(x, y int) bool {
		return res.Chains[full[x]].TotalCents < res.Chains[full[y]].TotalCents
	})
	for _, i := range full {
		res.Ranking = append(res.Ranking, Rank{Store: res.Chains[i].Store, TotalCents: res.Chains[i].TotalCents})
	}
	return res
}
