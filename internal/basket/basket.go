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

// PinnedLimit is the wider page a pinned id is looked for in. A pinned line is confirmed
// by finding the user's exact id in the chain's live answer, and ten results can miss it.
const PinnedLimit = 20

// DefaultProposals and MaxProposals bound the candidate list a human reads: three fits a
// terminal, ten is the most any chain's page can offer without becoming noise.
const (
	DefaultProposals = 3
	MaxProposals     = 10
)

// Item is one line the user wrote. An automatic line is a query the machine matches in
// every chain; a pinned line is the product the user chose from a candidate list (L7),
// and it is only ever resolved by its exact id in its own chain.
type Item struct {
	Query    string `json:"query,omitempty"`
	Store    string `json:"store,omitempty"`
	ID       string `json:"id,omitempty"`
	Name     string `json:"name,omitempty"`
	Quantity int    `json:"quantity"`
}

// Pinned reports whether the user chose the product instead of asking for a match.
func (it Item) Pinned() bool { return it.Store != "" || it.ID != "" || it.Name != "" }

// label is the name a chain reports for the line in its missing and stale lists: the
// query of an automatic item, the chosen product's name for a pinned one.
func (it Item) label() string {
	if it.Pinned() {
		return it.Name
	}
	return it.Query
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
// that answered and do not sell the item; Stale names the chains where the pinned id
// could not be confirmed today, which is not the same thing: the product exists for the
// user, it just is not on the shelf this run can see. A chain that failed is in neither
// list, it is in Chains with its error.
type ItemResult struct {
	Query    string              `json:"query,omitempty"`
	Store    string              `json:"store,omitempty"`
	ID       string              `json:"id,omitempty"`
	Name     string              `json:"name,omitempty"`
	Quantity int                 `json:"quantity"`
	Resolved map[string]Resolved `json:"resolved"`
	Missing  []string            `json:"missing"`
	Stale    []string            `json:"stale"`
}

// ChainResult is one chain's candidate basket. Coverage is resolved/total, empty when
// the chain failed; a failed chain has no total, because a partial sum is not a price.
type ChainResult struct {
	Store      string                `json:"store"`
	TotalCents int64                 `json:"totalCents"`
	Coverage   string                `json:"coverage,omitempty"`
	ElapsedMs  int64                 `json:"elapsedMs"`
	Missing    []string              `json:"missing"`
	Stale      []string              `json:"stale"`
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
// command maps it onto exit 2. stores is the list of chain keys being priced: a pinned
// line naming any other store could never resolve.
func Decode(r io.Reader, stores []string) (Request, error) {
	dec := json.NewDecoder(r)
	dec.DisallowUnknownFields()
	var req Request
	if err := dec.Decode(&req); err != nil {
		return Request{}, fmt.Errorf("basket is not valid JSON: %w", err)
	}
	if err := dec.Decode(new(json.RawMessage)); err != io.EOF {
		return Request{}, errors.New("basket carries more than one JSON document")
	}
	if err := req.normalize(stores); err != nil {
		return Request{}, err
	}
	return req, nil
}

// normalize trims the text fields and enforces the two item shapes: at least one item, a
// whole quantity of at least one, and per item either a query to match or a complete
// store/id/name the user chose — never both, never half of one.
func (r *Request) normalize(stores []string) error {
	if len(r.Items) == 0 {
		return errors.New("basket has no items")
	}
	known := make(map[string]bool, len(stores))
	for _, s := range stores {
		known[s] = true
	}
	for i := range r.Items {
		it := &r.Items[i]
		it.Query = strings.TrimSpace(it.Query)
		it.Store = strings.TrimSpace(it.Store)
		it.ID = strings.TrimSpace(it.ID)
		it.Name = strings.TrimSpace(it.Name)
		switch {
		case it.Pinned() && it.Query != "":
			return fmt.Errorf("item %d: give either query or store/id/name, not both", i+1)
		case it.Pinned():
			if !known[it.Store] {
				return fmt.Errorf("item %d: store %q is not one of: %s", i+1, it.Store, strings.Join(stores, ", "))
			}
			if it.ID == "" {
				return fmt.Errorf("item %d (%s): id is empty", i+1, it.Name)
			}
			if it.Name == "" {
				return fmt.Errorf("item %d: name is empty", i+1)
			}
		case it.Query == "":
			return fmt.Errorf("item %d: query is empty", i+1)
		}
		if it.Quantity < 1 {
			return fmt.Errorf("item %d (%s): quantity must be a whole number of at least 1, got %d",
				i+1, it.label(), it.Quantity)
		}
	}
	return nil
}

// Price resolves every item in every chain and prices the basket in each one. Chains
// are asked in parallel, one budget each, exactly like aggregate.Find: a slow or failing
// chain is reported in its own entry and never voids the others.
//
// A pinned item is only ever resolved by its exact id in the chain the user chose: the
// other chains cannot serve that choice, so it is missing there, and if the id is not in
// the chosen chain's answer today that chain reports it stale and leaves the ranking. No
// fallback to a name match, ever: a wrong product is worse than no total.
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
			Store:    it.Store,
			ID:       it.ID,
			Name:     it.Name,
			Quantity: it.Quantity,
			Resolved: make(map[string]Resolved),
			Missing:  []string{},
			Stale:    []string{},
		}
	}

	type answer struct {
		resolved map[int]Resolved
		stale    map[int]bool
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
			a := answer{resolved: make(map[int]Resolved, len(req.Items)), stale: make(map[int]bool)}
			defer func() {
				a.elapsed = time.Since(start).Milliseconds()
				answers[i] = a
			}()
			for j, it := range req.Items {
				if it.Pinned() {
					if it.Store != c.Store {
						continue
					}
					got, err := c.Searcher.Search(cctx, it.Name, PinnedLimit)
					if err != nil {
						a.err = aggregate.Classify(err)
						return
					}
					r, ok := pinnedOf(got.Hits, it)
					if !ok {
						a.stale[j] = true
						continue
					}
					a.resolved[j] = r
					continue
				}
				got, err := c.Searcher.Search(cctx, it.Query, candidates)
				if err != nil {
					// A chain that failed mid-basket keeps no partial result: half a
					// basket priced from half a catalog is not a price.
					a.err = aggregate.Classify(err)
					return
				}
				h, ok := best(it.Query, got.Hits)
				if !ok {
					continue
				}
				a.resolved[j] = Resolved{
					ID:        h.ID,
					Name:      h.Name,
					UnitCents: h.UnitCents,
					LineCents: h.UnitCents * int64(it.Quantity),
				}
			}
		}()
	}
	wg.Wait()

	full := make([]int, 0, len(chains))
	for i, c := range chains {
		a := answers[i]
		cr := ChainResult{Store: c.Store, ElapsedMs: a.elapsed, Missing: []string{}, Stale: []string{}}
		if a.err != nil {
			cr.Error = a.err
			res.Chains[i] = cr
			continue
		}
		for j := range req.Items {
			if a.stale[j] {
				cr.Stale = append(cr.Stale, req.Items[j].label())
				res.Items[j].Stale = append(res.Items[j].Stale, c.Store)
				continue
			}
			r, ok := a.resolved[j]
			if !ok {
				cr.Missing = append(cr.Missing, req.Items[j].label())
				res.Items[j].Missing = append(res.Items[j].Missing, c.Store)
				continue
			}
			res.Items[j].Resolved[c.Store] = r
			cr.TotalCents += r.LineCents
		}
		cr.Coverage = fmt.Sprintf("%d/%d", len(a.resolved), len(req.Items))
		res.Chains[i] = cr
		if len(cr.Missing) == 0 && len(cr.Stale) == 0 {
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
