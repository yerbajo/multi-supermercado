// Package carrefour reads Carrefour prices from Superfudis, the declared third-party
// source for this chain (S9): carrefour.es answers Cloudflare 403 on every catalogue
// and API path, so the price is *reported by Superfudis*, not published by Carrefour.
package carrefour

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"multisuper/internal/contract"
)

const (
	// DefaultBaseURL is the Superfudis compare-basket host, a free-tier Render app that
	// sleeps when idle.
	DefaultBaseURL = "https://supercomparador-i9tw.onrender.com"
	// DefaultLimit is how many hits a search returns unless --limit says otherwise.
	DefaultLimit = 20
	// MaxLimit caps --limit so one query never returns an unbounded list.
	MaxLimit = 100

	compareBasketPath = "/api/v1/compare-basket"
	// store is the store name every hit carries (S7) and retailer the only chain this
	// binary asks the comparator about.
	store    = "carrefour"
	retailer = "Carrefour"

	// httpTimeout clears Superfudis' cold start: the sleeping host was observed taking
	// 8.7s to answer, so a shorter timeout would report a false failure.
	httpTimeout = 45 * time.Second
	// maxAttempts is 2 because the quota is 3 requests/minute: a 429 gets one retry and
	// no aggressive loop.
	maxAttempts = 2
	// maxRetryWait bounds a Retry-After; a longer request is reported instead of waiting
	// or retrying earlier than the server asked.
	maxRetryWait = 30 * time.Second
	// retryFallback is the wait when 429 carries no usable Retry-After.
	retryFallback = 10 * time.Second
)

// ErrQuota reports that Superfudis kept answering 429 after the single allowed retry.
var ErrQuota = fmt.Errorf("carrefour: Superfudis rate limit reached: its quota is 3 requests/minute, try again later")

// Client queries the Superfudis compare-basket API.
type Client struct {
	BaseURL string
	HTTP    *http.Client
	// Sleep waits between retries; tests replace it so Retry-After is honoured without
	// slowing the suite.
	Sleep func(context.Context, time.Duration) error
}

// NewClient returns a client with the cold-start timeout Superfudis needs.
func NewClient() *Client {
	return &Client{
		BaseURL: DefaultBaseURL,
		HTTP:    &http.Client{Timeout: httpTimeout},
		Sleep:   sleepContext,
	}
}

// Search asks Superfudis for the Carrefour offer of one term. It sends exactly one item
// in one request: the API takes 10 items per request and 3 requests per minute and its
// terms forbid bulk extraction, so there is no paging and no batching here.
func (c *Client) Search(ctx context.Context, term string, limit int) (contract.Result, error) {
	res := contract.Result{Store: store, Query: term, Hits: []contract.Hit{}}

	raw, err := c.post(ctx, term)
	if err != nil {
		return res, err
	}

	var payload compareBasket
	if err := json.Unmarshal(raw, &payload); err != nil {
		return res, fmt.Errorf("carrefour: invalid compare-basket response: %w", err)
	}
	if payload.Status != "ok" {
		return res, fmt.Errorf("carrefour: compare-basket answered status %q, want \"ok\"", payload.Status)
	}

	index := 0
	for _, item := range payload.Items {
		for _, o := range item.Offers {
			if len(res.Hits) == limit {
				break
			}
			res.Hits = append(res.Hits, o.hit(offerID(o, index)))
			index++
		}
	}
	res.Count = len(res.Hits)
	return res, nil
}

// basketRequest is the exact body Superfudis expects: one item, one retailer.
type basketRequest struct {
	Items     []basketRequestItem `json:"items"`
	Retailers []string            `json:"retailers"`
}

type basketRequestItem struct {
	Query    string `json:"query"`
	Quantity int    `json:"quantity"`
}

// post sends the single search request, retrying a 429 at most once.
func (c *Client) post(ctx context.Context, term string) ([]byte, error) {
	payload, err := json.Marshal(basketRequest{
		Items:     []basketRequestItem{{Query: term, Quantity: 1}},
		Retailers: []string{retailer},
	})
	if err != nil {
		return nil, fmt.Errorf("carrefour: build request body: %w", err)
	}
	endpoint := strings.TrimSuffix(c.BaseURL, "/") + compareBasketPath

	for attempt := 1; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
		if err != nil {
			return nil, fmt.Errorf("carrefour: build request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json")

		resp, err := c.HTTP.Do(req)
		if err != nil {
			return nil, fmt.Errorf("carrefour: compare-basket request failed (timeout %s; Superfudis is a free-tier host that can sleep): %w", httpTimeout, err)
		}
		raw, readErr := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		resp.Body.Close()
		if readErr != nil {
			return nil, fmt.Errorf("carrefour: read compare-basket response: %w", readErr)
		}

		if resp.StatusCode == http.StatusTooManyRequests {
			if attempt == maxAttempts {
				return nil, ErrQuota
			}
			wait, ok := retryWait(resp.Header.Get("Retry-After"))
			if !ok {
				return nil, fmt.Errorf("%w (Superfudis asked to wait %s before retrying)", ErrQuota, wait)
			}
			if err := c.Sleep(ctx, wait); err != nil {
				return nil, err
			}
			continue
		}
		if resp.StatusCode < 200 || resp.StatusCode > 299 {
			return nil, fmt.Errorf("carrefour: compare-basket status %s: %s", resp.Status, snippet(raw))
		}
		return raw, nil
	}
}

// retryWait returns the pause a 429 asks for and whether a retry fits the budget. It
// never waits less than Retry-After says.
func retryWait(header string) (time.Duration, bool) {
	wait := retryFallback
	if secs, err := strconv.Atoi(strings.TrimSpace(header)); err == nil && secs >= 0 {
		wait = time.Duration(secs) * time.Second
	}
	return wait, wait <= maxRetryWait
}

func sleepContext(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("carrefour: waiting to retry: %w", ctx.Err())
	}
}

func snippet(raw []byte) string {
	s := strings.TrimSpace(string(raw))
	if s == "" {
		return "(empty body)"
	}
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}
