package dia

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// The anonymous cart, as the Android app calls it: IApiService.java:360 reads the
// cart's details, :405 adds an item (POST), :408 updates its quantity (PUT),
// :189 removes one line and :195 empties the whole cart. Nothing here logs in,
// checks out or places an order.
const (
	cartDetailsPath = "/api/v1/checkout-back/cart/reduced"
	cartItemPath    = "/api/v1/cart/current/item"
	cartEmptyPath   = "/api/v1/cart/current/empty-cart"
)

// ErrNoCart means no cart session is stored yet: `dia cart get` says so instead of
// starting a cart behind the user's back.
var ErrNoCart = errors.New("no cart yet")

// ErrCartExpired means DIA no longer knows the stored session. It answers with a 302
// to the site and a cookie deletion, not with a 404.
var ErrCartExpired = errors.New("cart session expired")

// Cart is the stable JSON document `dia cart get --json` prints:
//
//	{"id":"ff08514b-e36a-4a04-abde-6723adb75b97","total":9.96,
//	 "lines":[{"objectId":"504P6","name":"Leche semidesnatada Dia Láctea pack 6 x 1 L",
//	           "quantity":2,"unitPrice":4.98,"lineTotal":9.96}]}
//
// id is DIA's cart_id; it is omitted for a cart DIA answers without one. total is the
// cart summary DIA publishes for the items, without shipping: this is not checkout.
// unitPrice, lineTotal and total are omitted when DIA publishes no price, so a missing
// key never reads as 0,00 € (S8). currency appears only if DIA ever publishes one: its
// cart payload carries no currency, and assuming EUR would be inventing a datum. lines
// is always present, as [] for an empty cart.
type Cart struct {
	ID       string   `json:"id,omitempty"`
	Currency string   `json:"currency,omitempty"`
	Total    *float64 `json:"total,omitempty"`
	Lines    []Line   `json:"lines"`
}

// Line is one cart line as DIA publishes it: price is the unit price and raw_price the
// line total (the app renders raw_price as the row total, CartItemsAdapter.java:331).
type Line struct {
	ObjectID  string   `json:"objectId"`
	Name      string   `json:"name"`
	Quantity  int      `json:"quantity"`
	UnitPrice *float64 `json:"unitPrice,omitempty"`
	LineTotal *float64 `json:"lineTotal,omitempty"`
}

// SessionID returns the anonymous cart session cookie, empty when there is none yet.
func (c *Client) SessionID() string { return c.session }

// SetSessionID makes the client send a stored session cookie on every cart call.
func (c *Client) SetSessionID(id string) { c.session = id }

// AddItem puts objectID in the cart holding qty units. DIA sets the line to that
// quantity rather than adding to it, and answers 204 with no body: read the cart back
// with Cart to see the result.
func (c *Client) AddItem(ctx context.Context, objectID string, qty int) error {
	_, err := c.cartCall(ctx, http.MethodPost, cartItemPath, itemAddRequest{Quantity: qty, SKUID: objectID})
	return err
}

// UpdateItem sets the quantity of a line already in the cart (PUT on the same path).
func (c *Client) UpdateItem(ctx context.Context, objectID string, qty int) error {
	_, err := c.cartCall(ctx, http.MethodPut, cartItemPath, itemAddRequest{Quantity: qty, SKUID: objectID})
	return err
}

// RemoveItem drops one line. There is no body: the sku travels in the path.
func (c *Client) RemoveItem(ctx context.Context, objectID string) error {
	_, err := c.cartCall(ctx, http.MethodDelete, cartItemPath+"/"+objectID, nil)
	return err
}

// EmptyCart empties the cart server-side, keeping the same session and cart id.
func (c *Client) EmptyCart(ctx context.Context) error {
	_, err := c.cartCall(ctx, http.MethodDelete, cartEmptyPath, nil)
	return err
}

// Cart reads the cart back, lines included.
func (c *Client) Cart(ctx context.Context) (Cart, error) {
	raw, err := c.cartCall(ctx, http.MethodGet, cartDetailsPath, nil)
	if err != nil {
		return Cart{}, err
	}

	var payload struct {
		CartData struct {
			CartID string `json:"cart_id"`
			Items  []struct {
				Name      string `json:"name"`
				Quantity  int    `json:"quantity"`
				SKUID     string `json:"sku_id"`
				PriceInfo *struct {
					Price    *float64 `json:"price"`
					RawPrice *float64 `json:"raw_price"`
				} `json:"price_info"`
			} `json:"items"`
		} `json:"cart_data"`
		CartSummary struct {
			TotalAmount *float64 `json:"total_amount"`
		} `json:"cart_summary"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return Cart{}, fmt.Errorf("dia: decode cart response: %w", err)
	}

	cart := Cart{
		ID:    payload.CartData.CartID,
		Total: payload.CartSummary.TotalAmount,
		Lines: make([]Line, 0, len(payload.CartData.Items)),
	}
	for _, it := range payload.CartData.Items {
		line := Line{ObjectID: it.SKUID, Name: it.Name, Quantity: it.Quantity}
		if p := it.PriceInfo; p != nil {
			line.UnitPrice, line.LineTotal = p.Price, p.RawPrice
		}
		cart.Lines = append(cart.Lines, line)
	}
	return cart, nil
}

// itemAddRequest is the app's ItemAddRequest (network/data/request/ItemAddRequest.java):
// quantity, the sku id as sku_id, and an optional position the cart does not need.
type itemAddRequest struct {
	Quantity int    `json:"quantity"`
	SKUID    string `json:"sku_id"`
}

// cartCall performs one cart request and returns its body (nil for the 204 answers).
// The anonymous session cookie is the whole cart identity, so it is adopted from the
// response when DIA issues or refreshes it, and dropped when DIA says it is dead.
func (c *Client) cartCall(ctx context.Context, method, path string, payload any) ([]byte, error) {
	var body io.Reader
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			return nil, fmt.Errorf("dia: encode cart request: %w", err)
		}
		body = bytes.NewReader(raw)
	}
	req, err := newRequest(ctx, method, strings.TrimSuffix(c.BaseURL, "/")+path, body)
	if err != nil {
		return nil, err
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.session != "" {
		req.AddCookie(&http.Cookie{Name: "session_id", Value: c.session})
	}

	// A stale session is answered with a 302 to the site; following it would silently
	// turn every cart call into the home page of an empty new session.
	client := *c.HTTP
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("dia: cart request: %w", err)
	}
	defer resp.Body.Close()
	raw, readErr := io.ReadAll(io.LimitReader(resp.Body, 8<<20))

	if resp.StatusCode == http.StatusFound {
		c.session = ""
		return nil, fmt.Errorf("%w (status %s)", ErrCartExpired, resp.Status)
	}
	for _, ck := range resp.Cookies() {
		if ck.Name == "session_id" && ck.Value != "" {
			c.session = ck.Value
		}
	}
	// 200 carries the cart and 204 answers a mutation; 206 is DIA's validation error.
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		return nil, cartStatusError(resp.StatusCode, raw)
	}
	if readErr != nil {
		return nil, fmt.Errorf("dia: read cart response: %w", readErr)
	}
	return raw, nil
}

// cartStatusError turns DIA's rejection envelope into a Go error. The store answers
// 206 with a message object for a validation failure and 400 with a message string for
// a bad quantity: {"code":206,"message":{"max_quantity_in_stock":6},"type":"VALIDATION_ERROR"}.
func cartStatusError(status int, raw []byte) error {
	var envelope struct {
		Code    int             `json:"code"`
		Message json.RawMessage `json:"message"`
		Type    string          `json:"type"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil || envelope.Code == 0 {
		return fmt.Errorf("dia: cart status %d", status)
	}
	return fmt.Errorf("dia: cart %d %s: %s", envelope.Code, envelope.Type, cartMessage(envelope.Message))
}

// cartMessage renders the two message shapes the cart endpoints use.
func cartMessage(raw json.RawMessage) string {
	var text string
	if json.Unmarshal(raw, &text) == nil && text != "" {
		return text
	}
	var detail struct {
		Unavailable        *bool `json:"unavailable"`
		MaxQuantityInStock *int  `json:"max_quantity_in_stock"`
	}
	if json.Unmarshal(raw, &detail) == nil {
		if detail.Unavailable != nil && *detail.Unavailable {
			return "product not available"
		}
		if detail.MaxQuantityInStock != nil {
			return fmt.Sprintf("only %d in stock", *detail.MaxQuantityInStock)
		}
	}
	return "the store rejected the request"
}
