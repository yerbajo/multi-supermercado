// Package lidl reads the anonymous search API behind lidl.es.
package lidl

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strconv"
	"strings"
	"time"

	"multisuper/internal/contract"
)

const (
	// store is the store name every hit from this chain carries (S7).
	store = "lidl"
	// baseURL is the Spanish storefront; its SPA reads the search API under /q/api.
	baseURL    = "https://www.lidl.es"
	searchPath = "/q/api/search"
	// userAgent is the Chrome UA the storefront is served to.
	userAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36"
	// apiVersion is the search contract version the SPA ships. The endpoint answers
	// HTTP 401 with an empty body when the `version` query parameter is missing; the
	// value itself is not validated (any value is accepted).
	apiVersion = "2.1.0"
	// maxLimit is the largest fetchsize the CLI exposes (the API itself allows 1000).
	maxLimit    = 100
	maxAttempts = 3
	httpTimeout = 20 * time.Second
	// maxRetryWait caps Retry-After so a hostile header cannot stall the CLI.
	maxRetryWait = 5 * time.Second
)

// Client talks to the lidl.es search API.
type Client struct {
	BaseURL string
	HTTP    *http.Client
	// Sleep waits between retries; tests replace it so Retry-After is honoured
	// without slowing the suite.
	Sleep func(context.Context, time.Duration) error
}

// New returns a client for www.lidl.es. The cookie jar keeps the session the
// storefront may hand out; the search API answers without any cookie today.
func New() *Client {
	jar, _ := cookiejar.New(nil) // never fails with nil options
	return &Client{
		BaseURL: baseURL,
		HTTP:    &http.Client{Timeout: httpTimeout, Jar: jar},
		Sleep:   sleepContext,
	}
}

// Search runs one catalogue search and returns it in the shared envelope.
func (c *Client) Search(ctx context.Context, term string, limit int) (contract.Result, error) {
	res := contract.Result{Store: store, Query: term, Hits: []contract.Hit{}}
	if limit < 1 {
		limit = 1
	}
	if limit > maxLimit {
		limit = maxLimit
	}

	q := url.Values{
		"q":          {term},
		"locale":     {"es_ES"},
		"assortment": {"ES"},
		"version":    {apiVersion},
		"fetchsize":  {strconv.Itoa(limit)},
		"offset":     {"0"},
	}
	raw, err := c.get(ctx, searchPath+"?"+q.Encode())
	if err != nil {
		return res, err
	}

	var resp searchResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return res, fmt.Errorf("lidl: invalid search response: %w", err)
	}
	for _, it := range resp.Items {
		if len(res.Hits) == limit {
			break
		}
		if hit, ok := it.hit(); ok {
			res.Hits = append(res.Hits, hit)
		}
	}
	res.Count = len(res.Hits)
	return res, nil
}

// get sends one search request, retrying 429/503 up to maxAttempts in total.
func (c *Client) get(ctx context.Context, pathAndQuery string) ([]byte, error) {
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(c.BaseURL, "/")+pathAndQuery, nil)
		if err != nil {
			return nil, fmt.Errorf("lidl: building request: %w", err)
		}
		req.Header.Set("Accept", "application/mindshift.search+json;version=2")
		req.Header.Set("Accept-Language", "es-ES,es;q=0.9")
		req.Header.Set("Referer", strings.TrimSuffix(c.BaseURL, "/")+"/")
		req.Header.Set("User-Agent", userAgent)

		resp, err := c.HTTP.Do(req)
		if err != nil {
			return nil, fmt.Errorf("lidl: request failed: %w", err)
		}
		raw, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if readErr != nil {
			return nil, fmt.Errorf("lidl: reading response: %w", readErr)
		}

		switch resp.StatusCode {
		case http.StatusOK:
			return raw, nil
		case http.StatusTooManyRequests, http.StatusServiceUnavailable:
			lastErr = fmt.Errorf("lidl: HTTP %d after %d attempts", resp.StatusCode, attempt)
			if attempt == maxAttempts {
				break
			}
			if err := c.Sleep(ctx, retryDelay(resp.Header.Get("Retry-After"), attempt)); err != nil {
				return nil, err
			}
		case http.StatusUnauthorized:
			return nil, fmt.Errorf("lidl: HTTP 401: the search API refused the request; it needs the `version` query parameter and may have changed its contract")
		default:
			return nil, fmt.Errorf("lidl: HTTP %d: %s", resp.StatusCode, snippet(raw))
		}
	}
	return nil, lastErr
}

// retryDelay honours Retry-After (seconds), otherwise backs off 1s, 2s, and caps the
// wait at maxRetryWait.
func retryDelay(header string, attempt int) time.Duration {
	d := time.Duration(1<<(attempt-1)) * time.Second
	if secs, err := strconv.Atoi(strings.TrimSpace(header)); err == nil && secs >= 0 {
		d = time.Duration(secs) * time.Second
	}
	if d > maxRetryWait {
		d = maxRetryWait
	}
	return d
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
		return fmt.Errorf("lidl: waiting to retry: %w", ctx.Err())
	}
}

func snippet(raw []byte) string {
	s := strings.TrimSpace(string(raw))
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	if s == "" {
		return "(empty body)"
	}
	return s
}
