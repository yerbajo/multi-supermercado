package mas

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Shapes copied from the live endpoint (2026-10-05): createEmptyCart answers the
// opaque id as a plain String, addProductsToCart answers the whole cart under `cart`
// plus `user_errors`, and items live under itemsV2.
const (
	createResp = `{"data":{"createEmptyCart":"IZ21gstLNlX7napKoUxFDL9O1D2jJQMM"}}`

	oneLineCart = `{"id":"IZ21gstLNlX7napKoUxFDL9O1D2jJQMM","total_quantity":2,"itemsV2":{"total_count":1,"items":[{"uid":"OTQy","quantity":2,"product":{"sku":"114042","name":"Leche condensada LA LECHERA 370 gr"},"prices":{"price":{"value":2.89,"currency":"EUR"},"row_total":{"value":5.78,"currency":"EUR"}}}]},"prices":{"grand_total":{"value":5.78,"currency":"EUR"}}}`

	emptyCart = `{"id":"IZ21gstLNlX7napKoUxFDL9O1D2jJQMM","total_quantity":0,"itemsV2":{"total_count":0,"items":[]},"prices":{"grand_total":{"value":0,"currency":"EUR"}}}`

	// The exact business error the live store answers for every SKU: Magento reports
	// failures inside `data`, so a client that ignores user_errors reports success.
	addUserErrorResp = `{"data":{"addProductsToCart":{"cart":` + emptyCart + `,"user_errors":[{"code":"UNDEFINED","message":"La cantidad solicitada no est\u00e1 disponible"}]}}}`

	// Live message for an id the store does not know.
	cartNotFoundResp = `{"errors":[{"message":"No se pudo encontrar un carrito con el ID \"bogusCartId123\"","path":["cart"]}],"data":{"cart":null}}`
)

// cartStub is a GraphQL endpoint that routes on the operation the body carries.
type cartStub struct {
	t       *testing.T
	bodies  []map[string]any
	replies map[string]string
}

func (s *cartStub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.t.Errorf("method = %s, want POST", r.Method)
	}
	if r.URL.Path != "/graphql/" {
		// Without the trailing slash the store answers 301 and Go drops the POST body.
		s.t.Errorf("path = %q, want /graphql/", r.URL.Path)
	}
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		s.t.Errorf("decoding body: %v", err)
	}
	s.bodies = append(s.bodies, body)
	query, _ := body["query"].(string)
	for marker, reply := range s.replies {
		if strings.Contains(query, marker) {
			w.Write([]byte(reply))
			return
		}
	}
	s.t.Errorf("no reply for query %q", query)
}

func newCartClient(t *testing.T, replies map[string]string) (*Client, *cartStub) {
	t.Helper()
	stub := &cartStub{t: t, replies: replies}
	srv := httptest.NewServer(stub)
	t.Cleanup(srv.Close)
	c := New()
	c.BaseURL = srv.URL
	return c, stub
}

func TestCreateCartReturnsTheOpaqueStoreID(t *testing.T) {
	c, _ := newCartClient(t, map[string]string{"createEmptyCart": createResp})
	id, err := c.CreateCart(context.Background())
	if err != nil {
		t.Fatalf("CreateCart: %v", err)
	}
	if id != "IZ21gstLNlX7napKoUxFDL9O1D2jJQMM" {
		t.Fatalf("cart id = %q", id)
	}
}

// The cart document is the stable JSON `mas cart get --json` prints: id, lines with
// sku/name/quantity, unit price and line total when published, and the cart total.
func TestAddItemReadsTheCartBack(t *testing.T) {
	c, _ := newCartClient(t, map[string]string{
		"createEmptyCart":   createResp,
		"addProductsToCart": `{"data":{"addProductsToCart":{"cart":` + oneLineCart + `,"user_errors":[]}}}`,
		"cart(cart_id":      `{"data":{"cart":` + oneLineCart + `}}`,
	})
	id, err := c.CreateCart(context.Background())
	if err != nil {
		t.Fatalf("CreateCart: %v", err)
	}
	added, err := c.AddItem(context.Background(), id, "114042", 2)
	if err != nil {
		t.Fatalf("AddItem: %v", err)
	}
	read, err := c.Cart(context.Background(), id)
	if err != nil {
		t.Fatalf("Cart: %v", err)
	}

	want := `{"id":"IZ21gstLNlX7napKoUxFDL9O1D2jJQMM","currency":"EUR","total":5.78,"lines":[{"sku":"114042","name":"Leche condensada LA LECHERA 370 gr","quantity":2,"unitPrice":2.89,"lineTotal":5.78}]}`
	for name, cart := range map[string]Cart{"add": added, "get": read} {
		raw, err := json.Marshal(cart)
		if err != nil {
			t.Fatalf("%s: marshal: %v", name, err)
		}
		if got := string(raw); got != want {
			t.Errorf("%s: cart json mismatch\n got: %s\nwant: %s", name, got, want)
		}
	}
}

// An empty cart still prints a document: lines is [], never null.
func TestEmptyCartSerialisesLinesAsAnArray(t *testing.T) {
	c, _ := newCartClient(t, map[string]string{"cart(cart_id": `{"data":{"cart":` + emptyCart + `}}`})
	cart, err := c.Cart(context.Background(), "IZ21gstLNlX7napKoUxFDL9O1D2jJQMM")
	if err != nil {
		t.Fatalf("Cart: %v", err)
	}
	raw, _ := json.Marshal(cart)
	want := `{"id":"IZ21gstLNlX7napKoUxFDL9O1D2jJQMM","currency":"EUR","total":0,"lines":[]}`
	if string(raw) != want {
		t.Fatalf("cart json = %s, want %s", raw, want)
	}
}

// Magento answers business failures inside `data.user_errors`, not as an HTTP error:
// ignoring them would report a cart that never received the product as success.
func TestAddItemTurnsUserErrorsIntoAnError(t *testing.T) {
	c, _ := newCartClient(t, map[string]string{"addProductsToCart": addUserErrorResp})
	_, err := c.AddItem(context.Background(), "IZ21gstLNlX7napKoUxFDL9O1D2jJQMM", "114042", 2)
	if err == nil {
		t.Fatal("AddItem succeeded, want the user_errors surfaced as an error")
	}
	for _, want := range []string{"UNDEFINED", "La cantidad solicitada no est", "114042"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err = %v, want it to mention %q", err, want)
		}
	}
}

// Pins the mutation body: the exact query, the variables, the trailing slash on the
// endpoint and itemsV2 (Cart has no `items` field in this schema).
func TestAddItemPinsTheMutationShape(t *testing.T) {
	c, stub := newCartClient(t, map[string]string{
		"addProductsToCart": `{"data":{"addProductsToCart":{"cart":` + oneLineCart + `,"user_errors":[]}}}`,
	})
	if _, err := c.AddItem(context.Background(), "cartABC", "114042", 2); err != nil {
		t.Fatalf("AddItem: %v", err)
	}
	if len(stub.bodies) != 1 {
		t.Fatalf("requests = %d, want 1", len(stub.bodies))
	}
	body := stub.bodies[0]
	if got, want := body["query"], addItemMutation; got != want {
		t.Errorf("query mismatch\n got: %v\nwant: %s", got, want)
	}
	if !strings.Contains(addItemMutation, "itemsV2") {
		t.Errorf("add mutation must read itemsV2, got %s", addItemMutation)
	}
	vars, _ := json.Marshal(body["variables"])
	want := `{"c":"cartABC","i":[{"quantity":2,"sku":"114042"}]}`
	if string(vars) != want {
		t.Errorf("variables = %s, want %s", vars, want)
	}
}

// Magento answers a cart id it does not know with a business error, so the CLI can tell
// "stale id" apart from a network failure and forget the stored id instead of failing.
func TestCartNotFoundIsDistinguishable(t *testing.T) {
	c, _ := newCartClient(t, map[string]string{"cart(cart_id": cartNotFoundResp})
	_, err := c.Cart(context.Background(), "bogusCartId123")
	if !errors.Is(err, ErrCartNotFound) {
		t.Fatalf("err = %v, want ErrCartNotFound", err)
	}
}

// `cart remove <sku>` has no by-sku mutation: the client reads the cart to resolve the
// sku to its cart item uid, then removes that uid.
func TestRemoveItemResolvesTheSKUToItsUID(t *testing.T) {
	c, stub := newCartClient(t, map[string]string{
		"cart(cart_id":       `{"data":{"cart":` + oneLineCart + `}}`,
		"removeItemFromCart": `{"data":{"removeItemFromCart":{"cart":` + emptyCart + `}}}`,
	})
	cart, err := c.RemoveItem(context.Background(), "IZ21gstLNlX7napKoUxFDL9O1D2jJQMM", "114042")
	if err != nil {
		t.Fatalf("RemoveItem: %v", err)
	}
	if len(cart.Lines) != 0 {
		t.Fatalf("lines = %d, want 0", len(cart.Lines))
	}
	if len(stub.bodies) != 2 {
		t.Fatalf("requests = %d, want 2 (read, then remove)", len(stub.bodies))
	}
	var vars map[string]any
	raw, _ := json.Marshal(stub.bodies[1]["variables"])
	json.Unmarshal(raw, &vars)
	if vars["u"] != "OTQy" || vars["c"] != "IZ21gstLNlX7napKoUxFDL9O1D2jJQMM" {
		t.Fatalf("remove variables = %s, want the uid OTQy for cart IZ21...", raw)
	}
}

func TestRemoveItemRejectsASKUTheCartDoesNotHold(t *testing.T) {
	c, _ := newCartClient(t, map[string]string{"cart(cart_id": `{"data":{"cart":` + oneLineCart + `}}`})
	_, err := c.RemoveItem(context.Background(), "IZ21gstLNlX7napKoUxFDL9O1D2jJQMM", "000000")
	if err == nil || !strings.Contains(err.Error(), "000000") {
		t.Fatalf("err = %v, want it to name the sku", err)
	}
}

// One file, one cart id, 0600, in the directory the CLI injects.
func TestCartFileRoundTrip(t *testing.T) {
	f := CartFile{Path: filepath.Join(t.TempDir(), "multisuper", "mas-cart.json")}
	if _, err := f.Load(); !errors.Is(err, ErrNoCart) {
		t.Fatalf("Load on a missing file = %v, want ErrNoCart", err)
	}
	if err := f.Save("cartABC"); err != nil {
		t.Fatalf("Save: %v", err)
	}
	info, err := os.Stat(f.Path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("permissions = %o, want 600", perm)
	}
	id, err := f.Load()
	if err != nil || id != "cartABC" {
		t.Fatalf("Load = %q, %v", id, err)
	}
	if err := f.Forget(); err != nil {
		t.Fatalf("Forget: %v", err)
	}
	if _, err := f.Load(); !errors.Is(err, ErrNoCart) {
		t.Fatalf("Load after Forget = %v, want ErrNoCart", err)
	}
	// Forgetting a cart that is already gone is success, not an error.
	if err := f.Forget(); err != nil {
		t.Fatalf("Forget twice: %v", err)
	}
}

func TestCartFileRejectsGarbage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mas-cart.json")
	if err := os.WriteFile(path, []byte("not json"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := (CartFile{Path: path}).Load(); err == nil || errors.Is(err, ErrNoCart) {
		t.Fatalf("Load on garbage = %v, want a parse error", err)
	}
}
