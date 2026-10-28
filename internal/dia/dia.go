// Package dia talks to the DIA (dia.es) catalog search API and normalises its
// products into contract.Hit, so the aggregator compares chains with one shape.
package dia

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	utls "github.com/refraction-networking/utls"
	"golang.org/x/net/http2"

	"multisuper/internal/contract"
)

const (
	// DefaultBaseURL is the site every relative product path is resolved against.
	DefaultBaseURL = "https://www.dia.es"
	// DefaultLimit is how many hits a search returns unless --limit says otherwise.
	DefaultLimit = 20
	// MaxLimit caps --limit so one query never returns an unbounded list.
	MaxLimit = 100

	searchPath = "/api/v1/search-back/search"
	userAgent  = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36"

	maxAttempts = 3
	// maxRetryWait caps a server-supplied Retry-After, which is otherwise untrusted.
	maxRetryWait = 10 * time.Second
	// requestTimeout bounds one attempt, handshake included.
	requestTimeout = 20 * time.Second
)

// ErrUnavailable reports that dia.es kept answering 429/503 after all retries.
var ErrUnavailable = errors.New("dia search unavailable after retries")

// errFingerprint marks a failed Chrome-fingerprint handshake, the one failure
// that degrades to the standard transport. An HTTP status never does.
var errFingerprint = errors.New("dia: uTLS handshake failed")

// Client queries the DIA search API. The zero value is not usable; use NewClient.
type Client struct {
	BaseURL string
	HTTP    *http.Client
	// RootCAs overrides the roots dia.es is verified against; nil means the
	// system pool. Set it before the first request.
	RootCAs *x509.CertPool

	warn     io.Writer
	warnOnce sync.Once
}

// NewClient returns a client with the browser headers, the timeout and the TLS
// fingerprint dia.es expects: it sits behind Akamai, which answers 403 to Go's
// own ClientHello even when every header matches a browser.
func NewClient() *Client {
	c := &Client{BaseURL: DefaultBaseURL}
	c.HTTP = &http.Client{Timeout: requestTimeout, Transport: c.transport()}
	return c
}

// transport routes by scheme: https presents Chrome's handshake and keeps HTTP/2
// like a browser, plain http (a local mirror, the tests) keeps the standard stack.
type transport struct {
	fingerprint *http2.Transport
	plain       http.RoundTripper
}

func (t *transport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Scheme == "https" {
		return t.fingerprint.RoundTrip(req)
	}
	return t.plain.RoundTrip(req)
}

func (c *Client) transport() *transport {
	return &transport{
		fingerprint: &http2.Transport{DialTLSContext: func(ctx context.Context, network, addr string, _ *tls.Config) (net.Conn, error) {
			return c.dialTLS(ctx, network, addr)
		}},
		plain: &http.Transport{ForceAttemptHTTP2: true},
	}
}

// dialTLS opens addr and does Chrome's handshake over it; http2.Transport needs
// the negotiated protocol to be h2, which the preset already offers.
func (c *Client) dialTLS(ctx context.Context, network, addr string) (net.Conn, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	var d net.Dialer
	raw, err := d.DialContext(ctx, network, addr)
	if err != nil {
		return nil, err
	}
	conn := utls.UClient(raw, &utls.Config{ServerName: host, RootCAs: c.RootCAs}, utls.HelloChrome_Auto)
	if err := conn.HandshakeContext(ctx); err != nil {
		raw.Close()
		return nil, fmt.Errorf("%w: %v", errFingerprint, err)
	}
	if proto := conn.ConnectionState().NegotiatedProtocol; proto != http2.NextProtoTLS {
		raw.Close()
		return nil, fmt.Errorf("%w: server negotiated %q, not h2", errFingerprint, proto)
	}
	return conn, nil
}

// fallbackClient is the standard net/http stack. It only runs when the uTLS
// handshake fails, so a future uTLS/Go incompatibility degrades instead of
// breaking the CLI.
func (c *Client) fallbackClient() *http.Client {
	return &http.Client{
		Timeout:   requestTimeout,
		Transport: &http.Transport{ForceAttemptHTTP2: true, TLSClientConfig: &tls.Config{RootCAs: c.RootCAs}},
	}
}

// warnFallback reports the degradation once per client, not once per request.
func (c *Client) warnFallback(err error) {
	c.warnOnce.Do(func() {
		w := c.warn
		if w == nil {
			w = os.Stderr
		}
		fmt.Fprintf(w, "%v; falling back to the standard transport\n", err)
	})
}

// Search returns at most limit hits for term. An unknown term is not an error:
// the API answers with an empty search_items list. A limit outside 1..MaxLimit is
// rejected, never clamped, so the CLI can report it as a usage error (S3).
func (c *Client) Search(ctx context.Context, term string, limit int) (contract.Result, error) {
	if limit < 1 || limit > MaxLimit {
		return contract.Result{}, fmt.Errorf("dia: limit must be between 1 and %d, got %d", MaxLimit, limit)
	}
	body, err := c.get(ctx, strings.TrimSuffix(c.BaseURL, "/")+searchPath+"?q="+url.QueryEscape(term))
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
				Currency     string  `json:"currency"`
				MeasureUnit  string  `json:"measure_unit"`
				Price        float64 `json:"price"`
				PricePerUnit float64 `json:"price_per_unit"`
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
			hit.Currency = p.Currency
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

// newSearchRequest builds one attempt with the headers dia.es expects.
func newSearchRequest(ctx context.Context, endpoint string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("dia: build request: %w", err)
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/json")
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
		if err != nil && errors.Is(err, errFingerprint) {
			c.warnFallback(err)
			if retry, buildErr := newSearchRequest(ctx, endpoint); buildErr == nil {
				resp, err = c.fallbackClient().Do(retry)
			}
		}
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
