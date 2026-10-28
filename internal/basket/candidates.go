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

// Candidate is one product a chain proposes for an item: the id the user pastes back into
// a pinned line, the name, the price published now, and how sure the match is.
type Candidate struct {
	ID        string  `json:"id"`
	Name      string  `json:"name"`
	UnitCents int64   `json:"unitCents"`
	Score     float64 `json:"score"`
}

// ItemCandidates is one basket line as a proposal: each chain's best n candidates for it.
type ItemCandidates struct {
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

// Candidates asks every chain for its best n candidates per item, scored by the same
// function and ordered the same way as the basket's own pick, so a user who takes the
// first candidate gets exactly what the automatic basket would have bought (L7).
//
// A pinned item has no query to search, so it is left out: the command rejects such a
// basket instead of guessing what the name might mean.
func Candidates(ctx context.Context, req Request, chains []aggregate.Chain, n int, timeout time.Duration) CandidatesResult {
	if n < 1 {
		n = DefaultProposals
	}
	res := CandidatesResult{
		Items:  make([]ItemCandidates, len(req.Items)),
		Chains: make([]ChainAnswer, len(chains)),
	}
	for i, it := range req.Items {
		res.Items[i] = ItemCandidates{
			Query:      it.Query,
			Quantity:   it.Quantity,
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
			a := answer{cands: make(map[int][]Candidate, len(req.Items))}
			defer func() {
				a.elapsed = time.Since(start).Milliseconds()
				answers[i] = a
			}()
			for j, it := range req.Items {
				if it.Pinned() {
					continue
				}
				got, err := c.Searcher.Search(cctx, it.Query, DefaultCandidates)
				if err != nil {
					// One failed search drops the chain: a partial proposal would hide
					// that this chain has nothing to say about the other items.
					a.err = aggregate.Classify(err)
					return
				}
				top := rank(it.Query, got.Hits)
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

// pinnedOf confirms the id the user chose in the chain's live answer, and takes the price
// published now. A matching id without a price cannot join a total, so it is not
// confirmed; another product is never a substitute for the user's choice.
func pinnedOf(hits []contract.Hit, it Item) (Resolved, bool) {
	for _, h := range hits {
		if h.ID != it.ID || h.Price == nil {
			continue
		}
		cents := Cents(*h.Price)
		return Resolved{
			ID:        h.ID,
			Name:      h.Name,
			UnitCents: cents,
			LineCents: cents * int64(it.Quantity),
		}, true
	}
	return Resolved{}, false
}
