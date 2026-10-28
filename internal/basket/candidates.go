package basket

import (
	"context"
	"encoding/json"
	"io"
	"sync"
	"time"

	"multisuper/internal/aggregate"
	"multisuper/internal/contract"
)

// Candidate is one product a chain proposes for a line: the id the user pastes back into a
// pinned entry, the name, the price published now, how sure the match is, and whether it is
// the product the line already holds in that chain.
type Candidate struct {
	ID        string  `json:"id"`
	Name      string  `json:"name"`
	UnitCents int64   `json:"unitCents"`
	Score     float64 `json:"score"`
	Current   bool    `json:"current,omitempty"`
}

// ItemCandidates is one basket line as a proposal: each chain's best n candidates for it.
// Line is always present here, even for a plain automatic entry, because a candidate list is
// read per line and per chain: it is the key the web groups the user's choices by.
type ItemCandidates struct {
	Line       string                 `json:"line,omitempty"`
	Query      string                 `json:"query"`
	Quantity   int                    `json:"quantity"`
	Candidates map[string][]Candidate `json:"candidates"`
}

// ChainAnswer is one chain's part in a candidate run: how long it took and, when it
// failed, why it proposed nothing. A silent absence would read as "nothing to offer".
type ChainAnswer struct {
	Store     string                `json:"store"`
	ElapsedMs int64                 `json:"elapsedMs"`
	Error     *aggregate.StoreError `json:"error,omitempty"`
}

// CandidatesResult is the document `multisuper basket candidates` prints.
type CandidatesResult struct {
	Items  []ItemCandidates `json:"items"`
	Chains []ChainAnswer    `json:"chains"`
}

// EncodeCandidates writes the proposal document: indented, one document, trailing newline.
func EncodeCandidates(w io.Writer, r CandidatesResult) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}

// Candidates asks every chain for its best n candidates per line, scored by the same
// function and ordered the same way as the basket's own pick, so a user who takes the
// first candidate gets exactly what the automatic basket would have bought (L7).
//
// A line that already holds a pinned entry in a chain proposes that chain's own product
// first, marked as the current choice, so the user sees what the line holds today and can
// change it. A chain without a pin falls back to the line's query, and a line with neither
// has nothing to propose there.
func Candidates(ctx context.Context, req Request, chains []aggregate.Chain, n int, timeout time.Duration) CandidatesResult {
	if n < 1 {
		n = DefaultProposals
	}
	// Decode rejected every broken line group before this runs.
	lines, _ := req.lines()
	res := CandidatesResult{
		Items:  make([]ItemCandidates, len(lines)),
		Chains: make([]ChainAnswer, len(chains)),
	}
	for i, l := range lines {
		res.Items[i] = ItemCandidates{
			Line:       l.id,
			Query:      l.query,
			Quantity:   l.quantity,
			Candidates: make(map[string][]Candidate),
		}
	}

	type answer struct {
		cands   map[int][]Candidate
		err     *aggregate.StoreError
		elapsed int64
	}
	answers := make([]answer, len(chains))

	var wg sync.WaitGroup
	for i, c := range chains {
		wg.Add(1)
		go func() {
			defer wg.Done()
			start := time.Now()
			cctx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()
			a := answer{cands: make(map[int][]Candidate, len(lines))}
			defer func() {
				a.elapsed = time.Since(start).Milliseconds()
				answers[i] = a
			}()
			for j, l := range lines {
				// The chain proposes around the product the line holds there, and around
				// the line's query where it holds none.
				term, pin, limit := l.query, "", DefaultCandidates
				if p, ok := l.pinned[c.Store]; ok {
					term, pin, limit = p.Name, p.ID, PinnedLimit
				}
				if term == "" {
					continue
				}
				got, err := c.Searcher.Search(cctx, term, limit)
				if err != nil {
					// One failed search drops the chain: a partial proposal would hide
					// that this chain has nothing to say about the other lines.
					a.err = aggregate.Classify(err)
					return
				}
				top := currentFirst(rank(term, got.Hits), pin)
				if len(top) > n {
					top = top[:n]
				}
				a.cands[j] = top
			}
		}()
	}
	wg.Wait()

	for i, c := range chains {
		a := answers[i]
		res.Chains[i] = ChainAnswer{Store: c.Store, ElapsedMs: a.elapsed, Error: a.err}
		if a.err != nil {
			continue
		}
		for j, cands := range a.cands {
			if cands == nil {
				cands = []Candidate{}
			}
			res.Items[j].Candidates[c.Store] = cands
		}
	}
	return res
}

// currentFirst puts the product the line already holds in this chain at the head of the
// list, marked as the current choice, and leaves the order of the rest alone.
func currentFirst(cands []Candidate, id string) []Candidate {
	for k, c := range cands {
		if c.ID != id {
			continue
		}
		c.Current = true
		out := make([]Candidate, 0, len(cands))
		out = append(out, c)
		out = append(out, cands[:k]...)
		return append(out, cands[k+1:]...)
	}
	return cands
}

// pinnedOf confirms the id the user chose in the chain's live answer, and takes the price
// published now. A matching id without a price cannot join a total, so it is not
// confirmed; another product is never a substitute for the user's choice.
func pinnedOf(hits []contract.Hit, id string, quantity int) (Resolved, bool) {
	for _, h := range hits {
		if h.ID != id || h.Price == nil {
			continue
		}
		cents := Cents(*h.Price)
		return Resolved{
			ID:        h.ID,
			Name:      h.Name,
			UnitCents: cents,
			LineCents: cents * int64(quantity),
		}, true
	}
	return Resolved{}, false
}
