package dia

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// readCartFixture returns a real `checkout-back/cart/reduced` payload captured from
// dia.es, with the categories tree (unused by the CLI) trimmed away.
func readCartFixture(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func newCartServer(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	c := NewClient()
	c.BaseURL = srv.URL
	return c
}

// The cart document is the stable JSON `dia cart get --json` prints: the cart id,
// the items subtotal DIA publishes as the cart summary, and one line per item with
// its object id, name, quantity, unit price and line total.
func TestCartMapsTheRecordedDetailsPayload(t *testing.T) {
	c := newCartServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != cartDetailsPath {
			t.Errorf("path = %q, want %q", r.URL.Path, cartDetailsPath)
		}
		w.Write(readCartFixture(t, "cart_details.json"))
	})

	cart, err := c.Cart(context.Background())
	if err != nil {
		t.Fatalf("Cart: %v", err)
	}
	got, err := json.Marshal(cart)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	const want = `{"id":"ff08514b-e36a-4a04-abde-6723adb75b97","total":9.96,"lines":[{"objectId":"504P6","name":"Leche semidesnatada Dia Láctea pack 6 x 1 L","quantity":2,"unitPrice":4.98,"lineTotal":9.96}]}`
	if string(got) != want {
		t.Fatalf("cart = %s\nwant  %s", got, want)
	}
}

// An empty cart is a valid cart: DIA publishes total 0, no items and keeps the id.
func TestCartReadsAnEmptyCart(t *testing.T) {
	c := newCartServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write(readCartFixture(t, "cart_empty.json"))
	})

	cart, err := c.Cart(context.Background())
	if err != nil {
		t.Fatalf("Cart: %v", err)
	}
	if cart.ID != "ff08514b-e36a-4a04-abde-6723adb75b97" || cart.Total == nil || *cart.Total != 0 {
		t.Fatalf("cart = %+v", cart)
	}
	if cart.Lines == nil || len(cart.Lines) != 0 {
		t.Fatalf("lines = %v, want an empty non-nil slice", cart.Lines)
	}
}

// recordingClient serves any cart call and records what reached the wire.
type recordingClient struct {
	t       *testing.T
	reply   func(w http.ResponseWriter)
	method  string
	path    string
	headers http.Header
	body    string
}

func (rc *recordingClient) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	rc.method, rc.path, rc.headers = r.Method, r.URL.Path, r.Header.Clone()
	raw, _ := io.ReadAll(r.Body)
	rc.body = string(raw)
	rc.reply(w)
}

func newRecordingClient(t *testing.T, reply func(w http.ResponseWriter)) (*Client, *recordingClient) {
	t.Helper()
	rc := &recordingClient{t: t, reply: reply}
	return newCartServer(t, rc.ServeHTTP), rc
}

func noContent(w http.ResponseWriter) { w.WriteHeader(http.StatusNoContent) }

// The add is the app's own call: POST the line as {quantity, sku_id} with the same
// platform headers the search uses and the anonymous session cookie.
func TestAddItemPinsTheRequestTheAppSends(t *testing.T) {
	c, rc := newRecordingClient(t, noContent)
	c.SetSessionID("5d8d8952-74b1-41fb-9aeb-9dea500142b1")

	if err := c.AddItem(context.Background(), "504P6", 2); err != nil {
		t.Fatalf("AddItem: %v", err)
	}
	if rc.method != http.MethodPost || rc.path != "/api/v1/cart/current/item" {
		t.Fatalf("%s %s, want POST /api/v1/cart/current/item", rc.method, rc.path)
	}
	if rc.body != `{"quantity":2,"sku_id":"504P6"}` {
		t.Fatalf("body = %s", rc.body)
	}
	for want, key := range map[string]string{
		"8":                "X-Id-Application-From",
		"OneApp-Android":   "X-Mob-Platform-From",
		"app":              "order_source",
		"true":             "x-avoid-seo-redirections",
		"okhttp/4.12.0":    "User-Agent",
		"application/json": "Content-Type",
	} {
		if got := rc.headers.Get(key); got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
	if got := rc.headers.Get("Cookie"); got != "session_id=5d8d8952-74b1-41fb-9aeb-9dea500142b1" {
		t.Errorf("Cookie = %q", got)
	}
}

// Updating the quantity is a PUT of the same body: DIA sets the line to that quantity.
func TestUpdateItemPinsTheRequest(t *testing.T) {
	c, rc := newRecordingClient(t, noContent)
	c.SetSessionID("session-1")

	if err := c.UpdateItem(context.Background(), "504P6", 3); err != nil {
		t.Fatalf("UpdateItem: %v", err)
	}
	if rc.method != http.MethodPut || rc.path != "/api/v1/cart/current/item" {
		t.Fatalf("%s %s, want PUT /api/v1/cart/current/item", rc.method, rc.path)
	}
	if rc.body != `{"quantity":3,"sku_id":"504P6"}` {
		t.Fatalf("body = %s", rc.body)
	}
}

// Removing one line names the sku in the path and sends no body.
func TestRemoveItemPinsTheRequest(t *testing.T) {
	c, rc := newRecordingClient(t, noContent)
	c.SetSessionID("session-1")

	if err := c.RemoveItem(context.Background(), "504P6"); err != nil {
		t.Fatalf("RemoveItem: %v", err)
	}
	if rc.method != http.MethodDelete || rc.path != "/api/v1/cart/current/item/504P6" {
		t.Fatalf("%s %s, want DELETE /api/v1/cart/current/item/504P6", rc.method, rc.path)
	}
	if rc.body != "" {
		t.Fatalf("body = %q, want none", rc.body)
	}
}

// Clearing is DIA's own empty-cart endpoint, never a loop of removes.
func TestEmptyCartPinsTheRequest(t *testing.T) {
	c, rc := newRecordingClient(t, noContent)
	c.SetSessionID("session-1")

	if err := c.EmptyCart(context.Background()); err != nil {
		t.Fatalf("EmptyCart: %v", err)
	}
	if rc.method != http.MethodDelete || rc.path != "/api/v1/cart/current/empty-cart" {
		t.Fatalf("%s %s, want DELETE /api/v1/cart/current/empty-cart", rc.method, rc.path)
	}
}

// The first call issues the anonymous session, and every later call must carry it:
// that cookie is the whole cart identity.
func TestSessionCookieIsAdoptedAndSentBack(t *testing.T) {
	var seen []string
	c := newCartServer(t, func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Header.Get("Cookie"))
		http.SetCookie(w, &http.Cookie{Name: "session_id", Value: "new-session", Path: "/"})
		w.WriteHeader(http.StatusNoContent)
	})

	if err := c.AddItem(context.Background(), "504P6", 1); err != nil {
		t.Fatalf("AddItem: %v", err)
	}
	if c.SessionID() != "new-session" {
		t.Fatalf("SessionID = %q, want the issued cookie", c.SessionID())
	}
	if err := c.AddItem(context.Background(), "504P6", 1); err != nil {
		t.Fatalf("AddItem: %v", err)
	}
	if want := []string{"", "session_id=new-session"}; strings.Join(seen, "|") != strings.Join(want, "|") {
		t.Fatalf("cookies seen = %v, want %v", seen, want)
	}
}

// DIA answers a cart it no longer knows with a 302 to the site and a cookie deletion,
// not with a 404. Following that redirect would silently start an empty cart.
func TestUnknownSessionIsNotFollowedAsARedirect(t *testing.T) {
	c := newCartServer(t, func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "session_id", Value: "gone", Expires: time.Unix(0, 0)})
		http.Redirect(w, r, "/", http.StatusFound)
	})
	c.SetSessionID("dead-session")

	if _, err := c.Cart(context.Background()); !errors.Is(err, ErrCartExpired) {
		t.Fatalf("err = %v, want ErrCartExpired", err)
	}
	if c.SessionID() != "" {
		t.Fatalf("SessionID = %q, want the dead session dropped", c.SessionID())
	}
}

// The real rejection envelope: HTTP 206 with a nested message object.
func TestUnavailableProductBecomesAnError(t *testing.T) {
	c := newCartServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusPartialContent)
		io.WriteString(w, `{"code":206,"message":{"unavailable":true},"type":"VALIDATION_ERROR"}`)
	})

	err := c.AddItem(context.Background(), "NOPE999", 1)
	if err == nil || !strings.Contains(err.Error(), "product not available") {
		t.Fatalf("err = %v, want the unavailable message", err)
	}
	if !strings.Contains(err.Error(), "206") || !strings.Contains(err.Error(), "VALIDATION_ERROR") {
		t.Fatalf("err = %v, want the status and type in it", err)
	}
}

// Over-stock adds come back with the number of units the store will accept.
func TestOverStockErrorNamesTheLimit(t *testing.T) {
	c := newCartServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusPartialContent)
		io.WriteString(w, `{"code":206,"message":{"max_quantity_in_stock":6},"type":"VALIDATION_ERROR"}`)
	})

	err := c.UpdateItem(context.Background(), "504P6", 20000)
	if err == nil || !strings.Contains(err.Error(), "only 6 in stock") {
		t.Fatalf("err = %v, want the stock limit", err)
	}
}

// A bad quantity is a plain HTTP 400 whose message is a string, not an object.
func TestBadQuantityErrorKeepsTheStoreMessage(t *testing.T) {
	c := newCartServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		io.WriteString(w, `{"code":400,"message":"Quantity cannot be less than 1 when updating","type":"ERROR"}`)
	})

	err := c.UpdateItem(context.Background(), "504P6", 0)
	if err == nil || !strings.Contains(err.Error(), "Quantity cannot be less than 1") {
		t.Fatalf("err = %v, want the store message", err)
	}
}

// A cart file injected under a temp dir: nothing here writes to $HOME.
func TestCartFileRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "multisuper", "dia-cart.json")
	file := CartFile{Path: path}

	if _, err := file.Load(); !errors.Is(err, ErrNoCart) {
		t.Fatalf("Load on a missing file = %v, want ErrNoCart", err)
	}
	if err := file.Save("5d8d8952-74b1-41fb-9aeb-9dea500142b1"); err != nil {
		t.Fatalf("Save: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("mode = %o, want 600", perm)
	}
	got, err := file.Load()
	if err != nil || got != "5d8d8952-74b1-41fb-9aeb-9dea500142b1" {
		t.Fatalf("Load = %q, %v", got, err)
	}
	if err := file.Forget(); err != nil {
		t.Fatalf("Forget: %v", err)
	}
	if _, err := file.Load(); !errors.Is(err, ErrNoCart) {
		t.Fatalf("Load after Forget = %v, want ErrNoCart", err)
	}
	if err := file.Forget(); err != nil {
		t.Fatalf("Forget on a missing file: %v", err)
	}
}
