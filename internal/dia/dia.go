// Package dia talks to the DIA (dia.es) catalog search API and normalises its
// products into contract.Hit, so the aggregator compares chains with one shape.
package dia

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"multisuper/internal/contract"
)

const (
	// DefaultBaseURL is the site every relative product path is resolved against.
	DefaultBaseURL = "https://www.dia.es"
	// DefaultLimit is how many hits a search returns unless --limit says otherwise.
	DefaultLimit = 20
	// MaxLimit caps --limit so one query never returns an unbounded list.
	MaxLimit = 100

	searchPath = "/api/v1/search-back/search/reduced"

	maxAttempts = 3
	// maxRetryWait caps a server-supplied Retry-After, which is otherwise untrusted.
	maxRetryWait = 10 * time.Second
	// requestTimeout bounds one attempt, handshake included.
	requestTimeout = 20 * time.Second
)

// platformHeaders are the DIA Android app's platform headers. The search endpoint
// only answers requests that look like the app, and it answers plain TLS: measured
// 5/5 HTTP 200 with these exact values and curl. They are not secrets and carry no
// account data; the provenance is odd/research/dia-android-api.md §5.1.
var platformHeaders = map[string]string{
	"User-Agent":                "okhttp/4.12.0",
	"Accept":                    "application/json",
	"Accept-Language":           "es-ES",
	"X-Id-Application-From":     "8",
	"X-Mob-Platform-From":       "OneApp-Android",
	"order_source":              "app",
	"x-avoid-seo-redirections":  "true",
	"X-Mob-Os-Version":          "14",
	"X-Mob-Application-Version": "6.18.1",
	"X-Mob-Manufacturer":        "Google",
	"X-Mob-Deviceid":            "3f2a9c1be7d54a80",
	"X-Retail-Visitor-Id":       "20250702180000000000000000000001",
	"X-Device-Trace-Id":         "8f14e45fceea167a",
	"X-App-Supports-L1-As-L2":   "true",
}

// ErrUnavailable reports that dia.es kept answering 429/503 after all retries.
var ErrUnavailable = errors.New("dia search unavailable after retries")

// Client queries the DIA search API. The zero value is not usable; use NewClient.
type Client struct {
	BaseURL string
	HTTP    *http.Client
}

// NewClient returns a client with the app's headers and the timeout dia.es expects.
func NewClient() *Client {
	return &Client{BaseURL: DefaultBaseURL, HTTP: &http.Client{Timeout: requestTimeout}}
}

// Search returns at most limit hits for term. An unknown term is not an error:
// the API answers with an empty search_items list. A limit outside 1..MaxLimit is
// rejected, never clamped, so the CLI can report it as a usage error (S3).
func (c *Client) Search(ctx context.Context, term string, limit int) (contract.Result, error) {
	if limit < 1 || limit > MaxLimit {
		return contract.Result{}, fmt.Errorf("dia: limit must be between 1 and %d, got %d", MaxLimit, limit)
	}
	body, err := c.get(ctx, strings.TrimSuffix(c.BaseURL, "/")+searchPath+"?q="+url.QueryEscape(term)+"&page=0")
	if err != nil {
		return contract.Result{}, err
	}

	var payload struct {
		SearchItems []struct {
			Brand    string `json:"brand"`
			Name     string `json:"display_name"`
			Image    string `json:"image"`
			Category string `json:"l2_category_description"`
			ObjectID string `json:"object_id"`
			Stock    int    `json:"units_in_stock"`
			URL      string `json:"url"`
			Prices   *struct {
				Currency    string `json:"currency"`
				MeasureUnit string `json:"measure_unit"`
				// A nil price means DIA published a prices object without a price (S8).
				Price        *float64 `json:"price"`
				PricePerUnit float64  `json:"price_per_unit"`
			} `json:"prices"`
		} `json:"search_items"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return contract.Result{}, fmt.Errorf("dia: decode search response: %w", err)
	}

	base := strings.TrimSuffix(c.BaseURL, "/")
	hits := make([]contract.Hit, 0, len(payload.SearchItems))
	for _, it := range payload.SearchItems {
		hit := contract.Hit{
			Store:     "dia",
			ID:        it.ObjectID,
			Name:      it.Name,
			Brand:     it.Brand,
			Available: it.Stock > 0,
			Category:  it.Category,
			URL:       absolute(base, it.URL),
			Image:     absolute(base, it.Image),
		}
		if p := it.Prices; p != nil {
			hit.Price = p.Price
			hit.PricePerUnit = p.PricePerUnit
			hit.Unit = unit(p.MeasureUnit)
			if p.Price != nil {
				hit.Currency = p.Currency
			}
		}
		hits = append(hits, hit)
	}
	if len(hits) > limit {
		hits = hits[:limit]
	}

	return contract.Result{Store: "dia", Query: term, Count: len(hits), Hits: hits}, nil
}

// unit maps DIA's measure_unit onto the contract vocabulary; anything else stays empty.
func unit(measureUnit string) string {
	switch strings.ToUpper(measureUnit) {
	case "KILO":
		return "kg"
	case "LITRO":
		return "L"
	case "UNIDAD":
		return "unit"
	default:
		return ""
	}
}

func absolute(base, path string) string {
	if path == "" || strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://") {
		return path
	}
	return base + "/" + strings.TrimPrefix(path, "/")
}

// newSearchRequest builds one attempt: the /reduced endpoint with the app's headers.
func newSearchRequest(ctx context.Context, endpoint string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("dia: build request: %w", err)
	}
	for k, v := range platformHeaders {
		req.Header.Set(k, v)
	}
	return req, nil
}

// get performs the request, retrying 429/503 up to maxAttempts times.
func (c *Client) get(ctx context.Context, endpoint string) ([]byte, error) {
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		req, err := newSearchRequest(ctx, endpoint)
		if err != nil {
			return nil, err
		}

		resp, err := c.HTTP.Do(req)
		if err != nil {
			return nil, fmt.Errorf("dia: search request: %w", err)
		}
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		resp.Body.Close()

		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusServiceUnavailable {
			if attempt == maxAttempts {
				return nil, fmt.Errorf("%w (last status %s)", ErrUnavailable, resp.Status)
			}
			wait := retryAfter(resp, attempt)
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(wait):
			}
			lastErr = fmt.Errorf("%s", resp.Status)
			continue
		}
		if resp.StatusCode < 200 || resp.StatusCode > 299 {
			return nil, fmt.Errorf("dia: search status %s", resp.Status)
		}
		if readErr != nil {
			return nil, fmt.Errorf("dia: read response: %w", readErr)
		}
		return body, nil
	}
	return nil, fmt.Errorf("%w: %v", ErrUnavailable, lastErr)
}

// retryAfter honours the Retry-After header in seconds, else backs off exponentially.
func retryAfter(resp *http.Response, attempt int) time.Duration {
	if secs, err := strconv.Atoi(strings.TrimSpace(resp.Header.Get("Retry-After"))); err == nil && secs >= 0 {
		wait := time.Duration(secs) * time.Second
		if wait > maxRetryWait {
			return maxRetryWait
		}
		return wait
	}
	wait := time.Duration(attempt) * 500 * time.Millisecond
	if wait > maxRetryWait {
		return maxRetryWait
	}
	return wait
}
