// Package aldi searches the ALDI España (aldi.es) catalogue and normalises its
// products into contract.Hit, so the aggregator compares chains with one shape.
//
// Discovery (2026-10): aldi.es is a Next.js app, but its data route
// /_next/data/<buildId>/<page>.json is rewritten by the CDN to <page>.html, so the
// catalogue is not reachable the documented Next.js way. The site's own text search
// runs client-side against Algolia, and the search-only application id and key it
// uses are published in the front-end bundle (chunk 40895). That public endpoint is
// what Search talks to; no buildId is needed at runtime.
package aldi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"multisuper/internal/contract"
)

const (
	// DefaultLimit is how many hits a search returns unless --limit says otherwise.
	DefaultLimit = 20
	// MaxLimit caps --limit, matching the Algolia index page size the site itself uses.
	MaxLimit = 100
	// DefaultRegion is the peninsula catalogue, the region aldi.es serves by default.
	DefaultRegion = "pen"

	// searchAppID and searchKey are the public search-only credentials the site's own
	// front-end sends (aldi.es chunk 40895). They are not secrets, but they rotate with
	// the bundle: a 403 means they changed, see the error text of ErrSearchKey.
	searchAppID = "L9KNU74IO7"
	searchKey   = "83df5acd172c42ab174afa4583232b5d"
	// searchHost serves the Algolia search API for searchAppID.
	searchHost = "https://" + searchAppID + "-dsn.algolia.net"

	// The index holds one catalogue per region: an_prd_es_es_<region>_products2.
	indexTemplate = "/1/indexes/an_prd_es_es_%s_products2/query"
	// productURL is where a hit's productSlug is published.
	productURL = "https://www.aldi.es/producto/%s.html"

	userAgent   = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36"
	httpTimeout = 20 * time.Second
	maxAttempts = 3
	// maxRetryWait caps a server-supplied Retry-After, which is otherwise untrusted.
	maxRetryWait = 10 * time.Second
)

var (
	// ErrUnavailable reports that Algolia kept answering 429/503 after all retries.
	ErrUnavailable = errors.New("aldi search unavailable after retries")
	// ErrRegion reports a --region outside the catalogues ALDI publishes.
	ErrRegion = errors.New("aldi: unknown region, want pen, can or bal")
	// ErrSearchKey reports a rejected public search key, which means ALDI rotated the
	// credentials embedded in its front-end bundle.
	ErrSearchKey = errors.New("aldi: the public Algolia search key in the aldi.es bundle was rejected; it has probably rotated")
)

// regionIndex maps --region onto the Algolia index holding that catalogue.
func regionIndex(region string) (string, error) {
	switch region {
	case "pen", "can", "bal":
		return fmt.Sprintf(indexTemplate, region), nil
	}
	return "", fmt.Errorf("%w: %q", ErrRegion, region)
}

// ValidRegion reports whether region names a catalogue ALDI publishes.
func ValidRegion(region string) bool {
	_, err := regionIndex(region)
	return err == nil
}

// query is the Algolia search body the site's front-end sends.
type query struct {
	Query       string `json:"query"`
	HitsPerPage int    `json:"hitsPerPage"`
}

// Client talks to the Algolia index behind the aldi.es search box.
type Client struct {
	// BaseURL is the Algolia application host; tests point it at a fake server.
	BaseURL string
	HTTP    *http.Client
	// Sleep waits between retries; tests replace it to keep Retry-After honoured
	// without slowing the suite.
	Sleep func(context.Context, time.Duration) error
}

// NewClient returns a client with the browser headers aldi.es expects.
func NewClient() *Client {
	return &Client{
		BaseURL: searchHost,
		HTTP:    &http.Client{Timeout: httpTimeout},
		Sleep:   sleepContext,
	}
}

// hit is the part of an Algolia product record we use. ALDI publishes no cart and no
// stock count: isAvailable is as far as availability goes, and fresh produce carries
// no price at all.
type hit struct {
	ObjectID   string `json:"objectID"`
	Name       string `json:"name"`
	Brand      string `json:"brandName"`
	Slug       string `json:"productSlug"`
	SalesUnit  string `json:"salesUnit"`
	Available  bool   `json:"isAvailable"`
	Categories struct {
		Lvl0 []string `json:"lvl0"`
		Lvl1 []string `json:"lvl1"`
	} `json:"hierarchicalCategories"`
	Assets []struct {
		Type string `json:"type"`
		URL  string `json:"url"`
	} `json:"assets"`
	CurrentPrice *struct {
		// A nil priceValue means the catalogue publishes no price (fresh produce).
		PriceValue *float64 `json:"priceValue"`
		BasePrice  []struct {
			Value float64 `json:"basePriceValue"`
			Scale string  `json:"basePriceScale"`
		} `json:"basePrice"`
	} `json:"currentPrice"`
}

// Search returns at most limit hits for term from the catalogue of region. A limit
// outside 1..MaxLimit is rejected, never clamped, so the CLI can report it as a
// usage error (S3).
func (c *Client) Search(ctx context.Context, region, term string, limit int) (contract.Result, error) {
	res := contract.Result{Store: "aldi", Query: term, Hits: []contract.Hit{}}
	if limit < 1 || limit > MaxLimit {
		return res, fmt.Errorf("aldi: limit must be between 1 and %d, got %d", MaxLimit, limit)
	}
	path, err := regionIndex(region)
	if err != nil {
		return res, err
	}

	body, err := json.Marshal(query{Query: term, HitsPerPage: limit})
	if err != nil {
		return res, fmt.Errorf("aldi: encoding query: %w", err)
	}
	raw, err := c.post(ctx, strings.TrimSuffix(c.BaseURL, "/")+path, body)
	if err != nil {
		return res, err
	}

	var payload struct {
		Hits []hit `json:"hits"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return res, fmt.Errorf("aldi: decode search response: %w", err)
	}
	for _, h := range payload.Hits {
		res.Hits = append(res.Hits, h.contractHit())
	}
	res.Count = len(res.Hits)
	if len(res.Hits) > limit {
		res.Hits = res.Hits[:limit]
		res.Count = limit
	}
	return res, nil
}

// contractHit normalises one catalogue record. An empty price stays empty (S8).
func (h hit) contractHit() contract.Hit {
	out := contract.Hit{
		Store:     "aldi",
		ID:        h.ObjectID,
		Name:      h.Name,
		Brand:     h.Brand,
		Available: h.Available,
	}
	if len(h.Categories.Lvl1) > 0 {
		out.Category = h.Categories.Lvl1[0]
	} else if len(h.Categories.Lvl0) > 0 {
		out.Category = h.Categories.Lvl0[0]
	}
	if h.Slug != "" {
		out.URL = fmt.Sprintf(productURL, h.Slug)
	}
	for _, a := range h.Assets {
		if a.Type == "primary" {
			out.Image = a.URL
			break
		}
	}
	if p := h.CurrentPrice; p != nil {
		out.Price = p.PriceValue
		if out.Price != nil {
			out.Currency = "EUR"
		}
		// A published unit price wins; the declared sales unit ("1 l unidad") is the
		// fallback, because most of the assortment lists no base price.
		if len(p.BasePrice) > 0 {
			out.PricePerUnit, out.Unit = publishedUnitPrice(p.BasePrice[0].Value, p.BasePrice[0].Scale)
		}
		if out.PricePerUnit == 0 && out.Price != nil && *out.Price > 0 {
			if size, unit := parseQuantity(h.SalesUnit); size > 0 {
				out.PricePerUnit = round(*out.Price/size, 2)
				out.Unit = unit
			}
		}
	}
	return out
}

// publishedUnitPrice maps ALDI's base price onto the contract vocabulary. Scales the
// contract cannot express ("100-g", whose value ALDI publishes inconsistently) are
// left empty rather than converted on a guess.
func publishedUnitPrice(value float64, scale string) (float64, string) {
	switch strings.ToLower(scale) {
	case "kg":
		return value, "kg"
	case "l":
		return value, "L"
	case "unidad":
		return value, "unit"
	}
	return 0, ""
}

// post sends the search request, retrying 429/503 up to maxAttempts in total.
func (c *Client) post(ctx context.Context, endpoint string, body []byte) ([]byte, error) {
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
		if err != nil {
			return nil, fmt.Errorf("aldi: build request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json")
		req.Header.Set("User-Agent", userAgent)
		req.Header.Set("X-Algolia-Application-Id", searchAppID)
		req.Header.Set("X-Algolia-API-Key", searchKey)

		resp, err := c.HTTP.Do(req)
		if err != nil {
			return nil, fmt.Errorf("aldi: search request: %w", err)
		}
		raw, readErr := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		resp.Body.Close()

		switch resp.StatusCode {
		case http.StatusOK:
			if readErr != nil {
				return nil, fmt.Errorf("aldi: read response: %w", readErr)
			}
			return raw, nil
		case http.StatusUnauthorized, http.StatusForbidden:
			return nil, fmt.Errorf("%w (status %s)", ErrSearchKey, resp.Status)
		case http.StatusTooManyRequests, http.StatusServiceUnavailable:
			lastErr = fmt.Errorf("status %s", resp.Status)
			if attempt == maxAttempts {
				break
			}
			if err := c.Sleep(ctx, retryAfter(resp, attempt)); err != nil {
				return nil, err
			}
		default:
			return nil, fmt.Errorf("aldi: search status %s", resp.Status)
		}
	}
	return nil, fmt.Errorf("%w (last %v)", ErrUnavailable, lastErr)
}

func sleepContext(ctx context.Context, d time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}

// retryAfter honours the Retry-After header in seconds, else backs off exponentially.
func retryAfter(resp *http.Response, attempt int) time.Duration {
	if secs, err := strconv.Atoi(strings.TrimSpace(resp.Header.Get("Retry-After"))); err == nil && secs >= 0 {
		return min(time.Duration(secs)*time.Second, maxRetryWait)
	}
	return min(time.Duration(attempt)*500*time.Millisecond, maxRetryWait)
}
