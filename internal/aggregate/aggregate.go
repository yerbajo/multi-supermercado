// Package aggregate fans one query out to every supermarket chain's client in parallel
// and returns one partial result per chain with a machine-readable error, so the web app
// never orchestrates six processes or parses prose to learn what failed.
//
// There is deliberately no cache here: this process lives for one query, so the TTL
// cache the web app needs belongs in the future server that wraps it (mejoras-web.md §9).
package aggregate

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"multisuper/internal/contract"
)

// Stable failure codes the web app switches on. It never parses the message prose.
const (
	// CodeBlocked means the chain refused the request (403, CloudFront/Akamai deny, 401).
	CodeBlocked = "blocked"
	// CodeQuota means the request was refused for rate limiting (429, Alcampo's WAF 202).
	CodeQuota = "quota"
	// CodeTimeout means the chain did not answer inside the per-chain budget.
	CodeTimeout = "timeout"
	// CodeNetwork means the request failed at the transport level (DNS, refused, reset).
	CodeNetwork = "network"
	// CodeParse means the chain answered with something this CLI cannot decode.
	CodeParse = "parse"
	// CodeUnknown is a failure none of the codes above describes.
	CodeUnknown = "unknown"
)

// StoreError is one chain's failure. Retryable is true only for quota and timeout: a
// block, a parse change or a network error is not fixed by asking again.
type StoreError struct {
	Code      string `json:"code"`
	Status    int    `json:"status,omitempty"`
	Retryable bool   `json:"retryable"`
	Message   string `json:"message"`
}

// StoreResult is one chain's answer: its hits, or why it has none. A failed chain keeps
// an empty hits array, never the partial hits of an incomplete search.
type StoreResult struct {
	Store     string         `json:"store"`
	Count     int            `json:"count"`
	ElapsedMs int64          `json:"elapsedMs"`
	Error     *StoreError    `json:"error,omitempty"`
	Hits      []contract.Hit `json:"hits"`
}

// Result is the single document `find` prints: the sum of the chains' counts, and one
// entry per chain queried, in the order the chains were given.
type Result struct {
	Query   string        `json:"query"`
	Count   int           `json:"count"`
	Results []StoreResult `json:"results"`
}

// Encode writes the JSON the web app consumes: indented, one document, trailing newline.
func Encode(w io.Writer, r Result) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}

// StatusError is a chain failure carrying the HTTP status it got, so Classify maps it
// onto a stable code without reading the message. Chain clients may wrap it; tests and
// future clients use it to be unambiguous.
type StatusError struct {
	Status int
	Msg    string
}

func (e *StatusError) Error() string {
	if e.Msg != "" {
		return e.Msg
	}
	return "HTTP " + strconv.Itoa(e.Status)
}

// reStatus reads the status the current chain clients embed in their message
// ("HTTP 403:", "status 429"), so a chain needs no code change to be classified.
var reStatus = regexp.MustCompile(`\b(?:HTTP|status) (\d{3})\b`)

// Classify maps a chain failure onto the stable codes the web app switches on.
func Classify(err error) *StoreError {
	out := &StoreError{Code: CodeUnknown, Message: err.Error()}

	var status *StatusError
	if errors.As(err, &status) {
		out.Status = status.Status
	} else if m := reStatus.FindStringSubmatch(err.Error()); m != nil {
		out.Status, _ = strconv.Atoi(m[1])
	}

	switch {
	case isTimeout(err):
		out.Code = CodeTimeout
	case out.Status != 0:
		out.Code = codeForStatus(out.Status, err.Error())
	case isParse(err):
		out.Code = CodeParse
	case isNetwork(err):
		out.Code = CodeNetwork
	default:
		out.Code = codeForText(err.Error())
	}
	out.Retryable = out.Code == CodeQuota || out.Code == CodeTimeout
	return out
}

func codeForStatus(status int, msg string) string {
	switch {
	case status == http.StatusTooManyRequests:
		return CodeQuota
	// Alcampo answers its AWS WAF challenge with 202; that is a throttle, not success.
	case status == http.StatusAccepted && strings.Contains(strings.ToLower(msg), "waf"):
		return CodeQuota
	case status == http.StatusUnauthorized,
		status == http.StatusForbidden,
		status == http.StatusUnavailableForLegalReasons:
		return CodeBlocked
	}
	// A 5xx is none of the codes above: the chain answered, so it is not a network
	// failure, and no retry the client can schedule fixes it. Status and message say it.
	return CodeUnknown
}

func codeForText(msg string) string {
	s := strings.ToLower(msg)
	switch {
	case containsAny(s, "cloudfront", "akamai", "aws waf", "captcha", "access denied", "forbidden"):
		return CodeBlocked
	case containsAny(s, "rate limit", "quota", "too many requests"):
		return CodeQuota
	case containsAny(s, "no such host", "connection refused", "connection reset",
		"network is unreachable", "dial tcp", "tls handshake"):
		return CodeNetwork
	case containsAny(s, "invalid", "json", "decode", "unmarshal", "unexpected end"):
		return CodeParse
	}
	return CodeUnknown
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

func isTimeout(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return true
	}
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

func isParse(err error) bool {
	if errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	var syntax *json.SyntaxError
	var typ *json.UnmarshalTypeError
	return errors.As(err, &syntax) || errors.As(err, &typ)
}

func isNetwork(err error) bool {
	var uerr *url.Error
	return errors.As(err, &uerr)
}

// Searcher is the one method of every chain client this package needs.
type Searcher interface {
	Search(ctx context.Context, term string, limit int) (contract.Result, error)
}

// Chain binds a store name to the client that searches it.
type Chain struct {
	Store    string
	Searcher Searcher
}

// Find queries every chain in parallel, one request each, and returns one result per
// chain in the order given. A chain that fails or overruns timeout is reported in its
// own entry and never cancels the others: a partial answer beats no answer.
//
// The deadline is enforced through ctx, which every chain client hands to net/http.
func Find(ctx context.Context, query string, limit int, timeout time.Duration, chains []Chain) Result {
	res := Result{Query: query, Results: make([]StoreResult, len(chains))}

	var wg sync.WaitGroup
	for i, c := range chains {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// One context per chain: one chain's deadline must not take the rest down.
			cctx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()

			start := time.Now()
			got, err := c.Searcher.Search(cctx, query, limit)
			out := StoreResult{
				Store:     c.Store,
				ElapsedMs: time.Since(start).Milliseconds(),
				Hits:      []contract.Hit{},
			}
			if err != nil {
				out.Error = Classify(err)
			} else if got.Hits != nil {
				out.Hits = got.Hits
				out.Count = len(got.Hits)
			}
			res.Results[i] = out
		}()
	}
	wg.Wait()

	for _, r := range res.Results {
		res.Count += r.Count
	}
	return res
}

// ExitCode is 1 only when every chain failed, so a caller can tell "the search ran and
// found nothing" from "no chain could be asked".
func ExitCode(r Result) int {
	for _, sr := range r.Results {
		if sr.Error == nil {
			return 0
		}
	}
	return 1
}
