package mas

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

// searchQuery is the Magento products query verified against the live storefront.
// price_range returns HTTP 500 and finalPrice does not exist on ProductPrices: only
// price.regularPrice.amount is requested. categories{name url_path} is answered as
// published: one product can carry every ancestor plus brand and seasonal buckets.
const searchQuery = `query($t:String!,$p:Int!,$n:Int!){products(search:$t,currentPage:$p,pageSize:$n){total_count items{name sku url_key stock_status categories{name url_path} price{regularPrice{amount{value currency}}}}}}`

const (
	graphqlPath = "/graphql/"
	userAgent   = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36"
	maxAttempts = 3
	httpTimeout = 20 * time.Second
)

// Client talks to the MAS Magento GraphQL endpoint.
type Client struct {
	BaseURL string
	HTTP    *http.Client
	// Sleep waits between retries; tests replace it to keep Retry-After honoured
	// without slowing the suite.
	Sleep func(context.Context, time.Duration) error
}

// New returns a client for www.supermercadosmas.com.
func New() *Client {
	return &Client{
		BaseURL: baseURL,
		HTTP:    &http.Client{Timeout: httpTimeout},
		Sleep:   sleepContext,
	}
}

type gqlResponse struct {
	Data struct {
		Products struct {
			TotalCount int          `json:"total_count"`
			Items      []gqlProduct `json:"items"`
		} `json:"products"`
	} `json:"data"`
	Errors []struct {
		Message string `json:"message"`
	} `json:"errors"`
}

// Search runs one catalog page query and returns it in the shared envelope.
func (c *Client) Search(ctx context.Context, term string, page, limit int) (contract.Result, error) {
	res := contract.Result{Store: store, Query: term, Hits: []contract.Hit{}}

	body, err := json.Marshal(map[string]any{
		"query":     searchQuery,
		"variables": map[string]any{"t": term, "p": page, "n": limit},
	})
	if err != nil {
		return res, fmt.Errorf("mas: encoding query: %w", err)
	}
	raw, err := c.post(ctx, body)
	if err != nil {
		return res, err
	}

	var resp gqlResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return res, fmt.Errorf("mas: invalid GraphQL response: %w", err)
	}
	if len(resp.Errors) > 0 {
		return res, fmt.Errorf("mas: graphql: %s", resp.Errors[0].Message)
	}
	for _, p := range resp.Data.Products.Items {
		res.Hits = append(res.Hits, p.hit())
	}
	res.Count = len(res.Hits)
	return res, nil
}

// post sends the GraphQL request, retrying 429/503 up to maxAttempts in total.
func (c *Client) post(ctx context.Context, body []byte) ([]byte, error) {
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSuffix(c.BaseURL, "/")+graphqlPath, bytes.NewReader(body))
		if err != nil {
			return nil, fmt.Errorf("mas: building request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json")
		req.Header.Set("User-Agent", userAgent)

		resp, err := c.HTTP.Do(req)
		if err != nil {
			return nil, fmt.Errorf("mas: request failed: %w", err)
		}
		raw, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if readErr != nil {
			return nil, fmt.Errorf("mas: reading response: %w", readErr)
		}

		switch resp.StatusCode {
		case http.StatusOK:
			return raw, nil
		case http.StatusTooManyRequests, http.StatusServiceUnavailable:
			lastErr = fmt.Errorf("mas: HTTP %d after %d attempts", resp.StatusCode, attempt)
			if attempt == maxAttempts {
				break
			}
			if err := c.Sleep(ctx, retryDelay(resp.Header.Get("Retry-After"), attempt)); err != nil {
				return nil, err
			}
		default:
			return nil, fmt.Errorf("mas: HTTP %d: %s", resp.StatusCode, snippet(raw))
		}
	}
	return nil, lastErr
}

// retryDelay honours Retry-After (seconds) and otherwise backs off 1s, 2s.
func retryDelay(header string, attempt int) time.Duration {
	if secs, err := strconv.Atoi(strings.TrimSpace(header)); err == nil && secs >= 0 {
		return time.Duration(secs) * time.Second
	}
	return time.Duration(1<<(attempt-1)) * time.Second
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
		return fmt.Errorf("mas: waiting to retry: %w", ctx.Err())
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
