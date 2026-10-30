// Package basket prices one virtual shopping basket in every supermarket chain: the
// user's own list of goods, resolved to one product per chain, so the user learns what
// that same basket would cost in each chain and what would be missing there (S10/L6).
//
// The basket is ours, not a retailer's: no chain cart is created, and no cross-chain
// product identity is assumed. Only Carrefour publishes an EAN, so each item is matched
// by name with the score documented in score.go, and a weak match is reported missing
// rather than tied to whatever product happened to be in the first results.
//
// One basket line may hold a different chosen product per chain (T32): pinning the eggs
// at DIA, MAS and ALDI is one line, not three, so those chains can still be compared.
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

// Item is one entry of the basket file. An automatic entry is a query the machine matches
// in every chain; a pinned entry is the product the user chose from a candidate list
// (L7), and it is only ever resolved by its exact id in its own chain. Line names the
// basket line the entry belongs to: an automatic entry with no line is its own line, and
// a pinned entry always needs one, so one line can hold one chosen product per chain.
type Item struct {
	Line     string `json:"line,omitempty"`
	Query    string `json:"query,omitempty"`
	Store    string `json:"store,omitempty"`
	ID       string `json:"id,omitempty"`
	Name     string `json:"name,omitempty"`
	Quantity int    `json:"quantity"`
}

// Pinned reports whether the user chose the product instead of asking for a match.
func (it Item) Pinned() bool { return it.Store != "" || it.ID != "" || it.Name != "" }

// label is the name a chain reports for a line in its missing and stale lists: the query
// of an automatic entry, the chosen product's name for a pinned one.
func (it Item) label() string {
	if it.Pinned() {
		return it.Name
	}
	return it.Query
}

// line is one thing the user wants, however many entries the file needs to say it: one
// quantity, at most one automatic entry (the fallback for the chains with no pin) and at
// most one pinned entry per chain. Resolving per line and chain is what makes the same
// goods comparable: each chain prices its own product for the line.
type line struct {
	id       string
	query    string
	name     string
	quantity int
	pinned   map[string]Item
	entries  int
}

// label is the name this line carries into a chain's missing and stale lists: its query,
// or the chosen product when the line holds exactly one, or the line id — a line pinned in
// several chains has no single product name to report.
func (l line) label() string {
	if l.query != "" {
		return l.query
	}
	if l.entries == 1 {
		return l.name
	}
	return l.id
}

// showID reports whether the document has to print the line id. A single automatic entry
// whose query is the whole line is already fully described by that query, so leaving its
// id out keeps the answer to a basket written without lines byte for byte unchanged.
func (l line) showID() bool { return !(l.entries == 1 && l.query == l.id) }

// lines groups the basket entries by line, in order of first appearance. Every invariant
// of a line group is enforced here, so Decode can reject a broken basket and Price never
// has to guess what two conflicting entries for one line might mean.
func (r Request) lines() ([]line, error) {
	byID := make(map[string]*line, len(r.Items))
	order := make([]string, 0, len(r.Items))
	for _, it := range r.Items {
		id := it.Line
		if id == "" {
			id = it.Query
		}
		l, ok := byID[id]
		if !ok {
			l = &line{id: id, pinned: make(map[string]Item)}
			byID[id] = l
			order = append(order, id)
		}
		if l.quantity != 0 && it.Quantity != l.quantity {
			return nil, fmt.Errorf("line %q: quantity %d does not match the %d of its other entries",
				id, it.Quantity, l.quantity)
		}
		l.quantity = it.Quantity
		l.entries++
		if it.Pinned() {
			if _, dup := l.pinned[it.Store]; dup {
				return nil, fmt.Errorf("line %q: two entries for store %q, a line holds one product per chain", id, it.Store)
			}
			l.pinned[it.Store] = it
			if l.name == "" {
				l.name = it.Name
			}
			continue
		}
		if l.query != "" {
			return nil, fmt.Errorf("line %q: more than one automatic entry, a line has one query", id)
		}
		l.query = it.Query
		l.name = it.Query
	}
	out := make([]line, len(order))
	for i, id := range order {
		out[i] = *byID[id]
	}
	return out, nil
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

// ItemResult is one basket line as every chain answered it: the line id and quantity, the
// automatic query when the line has one, and one resolved product per chain — which is the
// chain's own pinned choice, or its best match for the query. Missing names the chains
// that answered and do not sell the line; Stale names the chains where the pinned id could
// not be confirmed today, which is not the same thing: the product exists for the user, it
// just is not on the shelf this run can see. A chain that failed is in neither list, it is
// in Chains with its error.
type ItemResult struct {
	Line     string              `json:"line,omitempty"`
	Query    string              `json:"query,omitempty"`
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

// normalize trims the text fields and enforces the two entry shapes: at least one item, a
// whole quantity of at least one, and per item either a query to match or a complete
// store/id/name the user chose — never both, never half of one. A pinned entry also needs
// the line it belongs to, and the line group itself is validated by lines.
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
		it.Line = strings.TrimSpace(it.Line)
		it.Query = strings.TrimSpace(it.Query)
		it.Store = strings.TrimSpace(it.Store)
		it.ID = strings.TrimSpace(it.ID)
		it.Name = strings.TrimSpace(it.Name)
		switch {
		case it.Pinned() && it.Query != "":
			return fmt.Errorf("item %d: give either query or store/id/name, not both", i+1)
		case it.Pinned():
			if it.Line == "" {
				return fmt.Errorf("item %d (%s): a pinned entry needs a line, so one line can hold one product per chain",
					i+1, it.Name)
			}
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
		default:
			// An automatic entry with no line of its own is the line its query names.
			if it.Line == "" {
				it.Line = it.Query
			}
		}
		if it.Quantity < 1 {
			return fmt.Errorf("item %d (%s): quantity must be a whole number of at least 1, got %d",
				i+1, it.label(), it.Quantity)
		}
	}
	_, err := r.lines()
	return err
}

// Price resolves every line in every chain and prices the basket in each one. Chains are
// asked in parallel, one budget each, exactly like aggregate.Find: a slow or failing chain
// is reported in its own entry and never voids the others.
//
// Resolution is per line and chain: where the user chose a product in that chain, the
// chain's own pinned entry wins, and the line's automatic entry is the fallback for the
// chains the user did not choose in. A pinned entry is only ever resolved by its exact id
// in the chain the user chose: if that id is not in the chain's answer today the line is
// stale there and the chain leaves the ranking. A stale pin never falls back to a name
// match: a wrong product is worse than no total.
//
// No cache and no cross-chain identity: this process serves one basket, and the only
// published identifier the chains share (the EAN) is missing from five of them.
func Price(ctx context.Context, req Request, chains []aggregate.Chain, candidates int, timeout time.Duration) Result {
	if candidates < 1 {
		candidates = DefaultCandidates
	}
	// Decode rejected every broken line group before this runs.
	lines, _ := req.lines()
	res := Result{
		Items:   make([]ItemResult, len(lines)),
		Chains:  make([]ChainResult, len(chains)),
		Ranking: []Rank{},
	}
	for i, l := range lines {
		ir := ItemResult{
			Query:    l.query,
			Quantity: l.quantity,
			Resolved: make(map[string]Resolved),
			Missing:  []string{},
			Stale:    []string{},
		}
		if l.showID() {
			ir.Line = l.id
		}
		res.Items[i] = ir
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
			a := answer{resolved: make(map[int]Resolved, len(lines)), stale: make(map[int]bool)}
			defer func() {
				a.elapsed = time.Since(start).Milliseconds()
				answers[i] = a
			}()
			for j, l := range lines {
				if p, ok := l.pinned[c.Store]; ok {
					got, err := c.Searcher.Search(cctx, p.Name, PinnedLimit)
					if err != nil {
						a.err = aggregate.Classify(err)
						return
					}
					r, ok := pinnedOf(got.Hits, p.ID, l.quantity)
					if !ok {
						a.stale[j] = true
						continue
					}
					a.resolved[j] = r
					continue
				}
				if l.query == "" {
					// Chosen in other chains only: there is nothing to match here.
					continue
				}
				got, err := c.Searcher.Search(cctx, l.query, candidates)
				if err != nil {
					// A chain that failed mid-basket keeps no partial result: half a
					// basket priced from half a catalog is not a price.
					a.err = aggregate.Classify(err)
					return
				}
				h, ok := best(l.query, got.Hits)
				if !ok {
					continue
				}
				a.resolved[j] = Resolved{
					ID:        h.ID,
					Name:      h.Name,
					UnitCents: h.UnitCents,
					LineCents: h.UnitCents * int64(l.quantity),
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
		for j := range lines {
			if a.stale[j] {
				cr.Stale = append(cr.Stale, lines[j].label())
				res.Items[j].Stale = append(res.Items[j].Stale, c.Store)
				continue
			}
			r, ok := a.resolved[j]
			if !ok {
				cr.Missing = append(cr.Missing, lines[j].label())
				res.Items[j].Missing = append(res.Items[j].Missing, c.Store)
				continue
			}
			res.Items[j].Resolved[c.Store] = r
			cr.TotalCents += r.LineCents
		}
		// Coverage counts lines, so a basket whose lines are pinned to different chains
		// still reads 3/3 in each of them.
		cr.Coverage = fmt.Sprintf("%d/%d", len(a.resolved), len(lines))
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
