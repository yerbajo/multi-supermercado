// Package alcampo searches the Alcampo online catalogue (compraonline.alcampo.es) and
// maps its products onto the shared contract envelope.
//
// The storefront is an Ocado Smart Platform site: the product listing the web
// frontend itself calls lives at /api/webproductpagews/v6/product-pages/search. Every
// request must carry the CSRF token and asset version embedded in the storefront HTML,
// which this client fetches once per search.
package alcampo

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"multisuper/internal/contract"
)

// store is the store name every hit from this chain carries.
const store = "alcampo"

// baseURL is the Alcampo storefront; product pages live at <baseURL>/products/<id>/details.
const baseURL = "https://www.compraonline.alcampo.es"

const (
	searchPath  = "/api/webproductpagews/v6/product-pages/search"
	userAgent   = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36"
	maxAttempts = 3
	httpTimeout = 20 * time.Second
	// maxRetryWait caps the honoured Retry-After, so a CLI invocation never hangs on a
	// store asking for minutes.
	maxRetryWait = 10 * time.Second
	// maxPageSize is the page size the storefront frontend requests.
	maxPageSize = 300
)

// Client talks to the Alcampo storefront.
type Client struct {
	BaseURL string
	HTTP    *http.Client
	// Sleep waits between retries; tests replace it to keep Retry-After honoured
	// without slowing the suite.
	Sleep func(context.Context, time.Duration) error
}

// New returns a client for compraonline.alcampo.es with a cookie jar, because the
// storefront ties the CSRF token to the visitor session it hands out with the HTML.
func New() *Client {
	jar, _ := cookiejar.New(nil)
	return &Client{
		BaseURL: baseURL,
		HTTP:    &http.Client{Timeout: httpTimeout, Jar: jar},
		Sleep:   sleepContext,
	}
}

type response struct {
	ProductGroups []struct {
		DecoratedProducts []product `json:"decoratedProducts"`
	} `json:"productGroups"`
}

// Search runs one catalogue search and returns it in the shared envelope.
func (c *Client) Search(ctx context.Context, term string, limit int) (contract.Result, error) {
	res := contract.Result{Store: store, Query: term, Hits: []contract.Hit{}}

	token, version, err := c.session(ctx)
	if err != nil {
		return res, err
	}

	query := url.Values{}
	query.Set("q", term)
	query.Set("tag", "web")
	query.Set("includeAdditionalPageInfo", "true")
	query.Set("maxProductsToDecorate", strconv.Itoa(limit))
	query.Set("maxPageSize", strconv.Itoa(maxPageSize))
	endpoint := strings.TrimSuffix(c.BaseURL, "/") + searchPath + "?" + query.Encode()

	raw, err := c.get(ctx, endpoint, token, version)
	if err != nil {
		return res, err
	}

	var resp response
	if err := json.Unmarshal(raw, &resp); err != nil {
		return res, fmt.Errorf("invalid search response: %w", err)
	}
	for _, group := range resp.ProductGroups {
		for _, p := range group.DecoratedProducts {
			if len(res.Hits) == limit {
				break
			}
			res.Hits = append(res.Hits, p.hit())
		}
	}
	res.Count = len(res.Hits)
	return res, nil
}

var (
	reCSRF    = regexp.MustCompile(`"csrf":\{"token":"([^"]+)"`)
	reVersion = regexp.MustCompile(`"assetVersion":"([^"]+)"`)
)

// session reads the CSRF token and asset version the storefront embeds in its HTML.
// Without them the API answers 403 "ecom-csrf-failure".
func (c *Client) session(ctx context.Context) (token, version string, err error) {
	raw, err := c.get(ctx, strings.TrimSuffix(c.BaseURL, "/")+"/", "", "")
	if err != nil {
		return "", "", fmt.Errorf("fetching storefront session: %w", err)
	}
	page := string(raw)
	if m := reCSRF.FindStringSubmatch(page); m != nil {
		token = m[1]
	}
	if m := reVersion.FindStringSubmatch(page); m != nil {
		version = m[1]
	}
	if token == "" {
		return "", "", fmt.Errorf("storefront page carries no csrf token")
	}
	return token, version, nil
}

// get sends one storefront request, retrying 429/503 up to maxAttempts in total.
func (c *Client) get(ctx context.Context, endpoint, token, version string) ([]byte, error) {
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return nil, fmt.Errorf("building request: %w", err)
		}
		req.Header.Set("User-Agent", userAgent)
		req.Header.Set("Accept", "application/json")
		req.Header.Set("Client-Route-Id", "SEARCH")
		req.Header.Set("Ecom-Request-Source", "web")
		if version != "" {
			req.Header.Set("Ecom-Request-Source-Version", version)
		}
		if token != "" {
			req.Header.Set("X-Csrf-Token", token)
		}

		resp, err := c.HTTP.Do(req)
		if err != nil {
			return nil, fmt.Errorf("request failed: %w", err)
		}
		raw, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if readErr != nil {
			return nil, fmt.Errorf("reading response: %w", readErr)
		}

		switch resp.StatusCode {
		case http.StatusOK:
			return raw, nil
		case http.StatusAccepted:
			// AWS WAF answers with a JavaScript challenge that no stdlib client can solve.
			return nil, fmt.Errorf("storefront served an AWS WAF challenge (HTTP 202, %s)", wafAction(resp))
		case http.StatusTooManyRequests, http.StatusServiceUnavailable:
			lastErr = fmt.Errorf("HTTP %d after %d attempts", resp.StatusCode, attempt)
			if attempt == maxAttempts {
				break
			}
			if err := c.Sleep(ctx, retryDelay(resp.Header.Get("Retry-After"), attempt)); err != nil {
				return nil, err
			}
		default:
			return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, snippet(raw))
		}
	}
	return nil, lastErr
}

func wafAction(resp *http.Response) string {
	if action := resp.Header.Get("X-Amzn-Waf-Action"); action != "" {
		return action
	}
	return "no challenge header"
}

// retryDelay honours Retry-After (seconds) and otherwise backs off 1s, 2s.
func retryDelay(header string, attempt int) time.Duration {
	d := time.Duration(1<<(attempt-1)) * time.Second
	if secs, err := strconv.Atoi(strings.TrimSpace(header)); err == nil && secs >= 0 {
		d = time.Duration(secs) * time.Second
	}
	if d > maxRetryWait {
		return maxRetryWait
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
		return fmt.Errorf("waiting to retry: %w", ctx.Err())
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
