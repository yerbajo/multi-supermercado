package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"multisuper/internal/mas"
)

// sampleCart is what the store answers after `mas cart add 114042 2`.
func sampleCart() mas.Cart {
	return mas.Cart{
		ID:       "cart1",
		Currency: "EUR",
		Total:    ptr(5.78),
		Lines: []mas.Line{{
			SKU: "114042", Name: "Leche condensada LA LECHERA 370 gr", Quantity: 2,
			UnitPrice: ptr(2.89), LineTotal: ptr(5.78),
		}},
	}
}

const sampleCartJSON = `{"id":"cart1","currency":"EUR","total":5.78,"lines":[{"sku":"114042","name":"Leche condensada LA LECHERA 370 gr","quantity":2,"unitPrice":2.89,"lineTotal":5.78}]}`

// stubCart keeps the cart tests offline.
type stubCart struct {
	id      string
	cart    mas.Cart
	err     error
	created int
	adds    []string
	reads   []string
	removes []string
}

func (s *stubCart) CreateCart(context.Context) (string, error) {
	s.created++
	return s.id, nil
}

func (s *stubCart) AddItem(_ context.Context, cartID, sku string, qty int) (mas.Cart, error) {
	s.adds = append(s.adds, fmt.Sprintf("%s %s x%d", cartID, sku, qty))
	return s.cart, s.err
}

func (s *stubCart) Cart(_ context.Context, cartID string) (mas.Cart, error) {
	s.reads = append(s.reads, cartID)
	return s.cart, s.err
}

func (s *stubCart) RemoveItem(_ context.Context, cartID, sku string) (mas.Cart, error) {
	s.removes = append(s.removes, cartID+" "+sku)
	return s.cart, s.err
}

// stubStore is the injectable cart file: no test touches $HOME.
type stubStore struct {
	id      string
	loadErr error
	saved   []string
	forgets int
}

func (s *stubStore) Load() (string, error) {
	if s.loadErr != nil {
		return "", s.loadErr
	}
	if s.id == "" {
		return "", mas.ErrNoCart
	}
	return s.id, nil
}

func (s *stubStore) Save(id string) error {
	s.saved = append(s.saved, id)
	s.id = id
	return nil
}

func (s *stubStore) Forget() error {
	s.forgets++
	s.id = ""
	return nil
}

func cartDeps(c *stubCart, s *stubStore) deps {
	return deps{search: &stubSearcher{}, cart: c, store: s}
}

// oneDocument fails unless out holds exactly one JSON document and returns it compacted.
func oneDocument(t *testing.T, out string) string {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(out))
	var v any
	if err := dec.Decode(&v); err != nil {
		t.Fatalf("stdout is not JSON: %v\n%s", err, out)
	}
	var extra any
	if err := dec.Decode(&extra); err == nil {
		t.Fatalf("stdout carries more than one document:\n%s", out)
	}
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(raw)
}

// `cart add` creates a guest cart on demand and remembers its id.
func TestCartAddCreatesAndPersistsTheCart(t *testing.T) {
	client := &stubCart{id: "cartNew", cart: sampleCart()}
	store := &stubStore{}
	var stdout, stderr strings.Builder

	if err := run([]string{"cart", "add", "114042", "2", "--json"}, &stdout, &stderr, cartDeps(client, store)); err != nil {
		t.Fatalf("run: %v", err)
	}
	if client.created != 1 {
		t.Errorf("CreateCart calls = %d, want 1", client.created)
	}
	if len(store.saved) != 1 || store.saved[0] != "cartNew" {
		t.Errorf("saved = %v, want [cartNew]", store.saved)
	}
	if got, want := fmt.Sprint(client.adds), "[cartNew 114042 x2]"; got != want {
		t.Errorf("adds = %s, want %s", got, want)
	}
	if got, want := oneDocument(t, stdout.String()), oneDocument(t, sampleCartJSON); got != want {
		t.Errorf("stdout = %s, want %s", got, want)
	}
	if stderr.Len() != 0 {
		t.Errorf("stderr = %q, want empty", stderr.String())
	}
}

// A stored id is reused: one cart per machine, never a new one per invocation.
func TestCartAddReusesTheStoredCart(t *testing.T) {
	client := &stubCart{id: "cartNew", cart: sampleCart()}
	store := &stubStore{id: "cartOld"}
	var stdout strings.Builder

	if err := run([]string{"cart", "add", "114042", "2"}, &stdout, &strings.Builder{}, cartDeps(client, store)); err != nil {
		t.Fatalf("run: %v", err)
	}
	if client.created != 0 {
		t.Errorf("CreateCart calls = %d, want 0", client.created)
	}
	if len(store.saved) != 0 {
		t.Errorf("saved = %v, want nothing", store.saved)
	}
	if got, want := fmt.Sprint(client.adds), "[cartOld 114042 x2]"; got != want {
		t.Errorf("adds = %s, want %s", got, want)
	}
	if !strings.Contains(stdout.String(), "114042") {
		t.Errorf("table lost the added line:\n%s", stdout.String())
	}
}

// Usage errors exit 2 and never reach the store.
func TestCartRejectsBadInput(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"missing action", []string{"cart"}, "missing cart action"},
		{"unknown action", []string{"cart", "checkout"}, `unknown cart action "checkout"`},
		{"add without arguments", []string{"cart", "add"}, "cart add needs <sku> <qty>"},
		{"add without quantity", []string{"cart", "add", "114042"}, "cart add needs <sku> <qty>"},
		{"add with extra argument", []string{"cart", "add", "114042", "2", "3"}, "cart add needs <sku> <qty>"},
		{"quantity not a number", []string{"cart", "add", "114042", "dos"}, `invalid quantity "dos"`},
		{"quantity zero", []string{"cart", "add", "114042", "0"}, "quantity must be 1 or more"},
		{"negative quantity reads as a flag", []string{"cart", "add", "114042", "-3"}, "flag provided but not defined"},
		{"get with argument", []string{"cart", "get", "cart1"}, "cart get takes no arguments"},
		{"clear with argument", []string{"cart", "clear", "cart1"}, "cart clear takes no arguments"},
		{"remove without sku", []string{"cart", "remove"}, "cart remove needs <sku>"},
		{"unknown flag", []string{"cart", "get", "--nope"}, "flag provided but not defined"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := &stubCart{id: "cartNew", cart: sampleCart()}
			store := &stubStore{id: "cartOld"}
			var stdout, stderr strings.Builder
			err := run(tt.args, &stdout, &stderr, cartDeps(client, store))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want it to mention %q", err, tt.want)
			}
			if code := exitCode(err); code != 2 {
				t.Fatalf("exit code = %d, want 2", code)
			}
			if stdout.Len() != 0 {
				t.Fatalf("stdout = %q, want nothing on error", stdout.String())
			}
			if client.created != 0 || len(client.adds) != 0 || len(client.reads) != 0 || len(client.removes) != 0 {
				t.Fatalf("store was called: %+v", client)
			}
			if len(store.saved) != 0 || store.forgets != 0 {
				t.Fatalf("cart file was written: %+v", store)
			}
		})
	}
}

// `cart get` on a machine that never created a cart says so instead of creating one.
func TestCartGetWithoutAStoredCart(t *testing.T) {
	client := &stubCart{id: "cartNew", cart: sampleCart()}
	store := &stubStore{}
	var stdout strings.Builder

	err := run([]string{"cart", "get", "--json"}, &stdout, &strings.Builder{}, cartDeps(client, store))
	if err == nil || !strings.Contains(err.Error(), "no cart yet") {
		t.Fatalf("err = %v, want a no-cart-yet message", err)
	}
	if code := exitCode(err); code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want nothing", stdout.String())
	}
	if client.created != 0 || len(client.reads) != 0 {
		t.Fatalf("store was called: %+v", client)
	}
}

// `--json` prints one document and nothing else.
func TestCartGetPrintsOneJSONDocument(t *testing.T) {
	client := &stubCart{cart: sampleCart()}
	store := &stubStore{id: "cart1"}
	var stdout, stderr strings.Builder

	if err := run([]string{"cart", "get", "--json"}, &stdout, &stderr, cartDeps(client, store)); err != nil {
		t.Fatalf("run: %v", err)
	}
	if got, want := oneDocument(t, stdout.String()), oneDocument(t, sampleCartJSON); got != want {
		t.Errorf("stdout = %s, want %s", got, want)
	}
	if stderr.Len() != 0 {
		t.Errorf("stderr = %q, want empty", stderr.String())
	}
	if fmt.Sprint(client.reads) != "[cart1]" {
		t.Errorf("reads = %v, want [cart1]", client.reads)
	}
}

// A stale id on `get` is a fetch error (exit 1) and points at `cart clear`.
func TestCartGetOnAStaleCartPointsAtClear(t *testing.T) {
	client := &stubCart{err: mas.ErrCartNotFound}
	store := &stubStore{id: "cart1"}
	err := run([]string{"cart", "get"}, &strings.Builder{}, &strings.Builder{}, cartDeps(client, store))
	if err == nil || !strings.Contains(err.Error(), "mas cart clear") {
		t.Fatalf("err = %v, want it to point at mas cart clear", err)
	}
	if code := exitCode(err); code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
}

// `cart remove <sku>` resolves the sku in the client and prints the remaining cart.
func TestCartRemovePrintsTheRemainingCart(t *testing.T) {
	client := &stubCart{cart: mas.Cart{ID: "cart1", Currency: "EUR", Total: ptr(0), Lines: []mas.Line{}}}
	store := &stubStore{id: "cart1"}
	var stdout strings.Builder

	if err := run([]string{"cart", "remove", "114042", "--json"}, &stdout, &strings.Builder{}, cartDeps(client, store)); err != nil {
		t.Fatalf("run: %v", err)
	}
	if fmt.Sprint(client.removes) != "[cart1 114042]" {
		t.Errorf("removes = %v, want [cart1 114042]", client.removes)
	}
	want := `{"id":"cart1","currency":"EUR","total":0,"lines":[]}`
	if got, want := oneDocument(t, stdout.String()), oneDocument(t, want); got != want {
		t.Errorf("stdout = %s, want %s", got, want)
	}
}

// Clearing an empty cart forgets the id and exits 0.
func TestCartClearEmptiesTheCart(t *testing.T) {
	client := &stubCart{cart: mas.Cart{ID: "cart1", Currency: "EUR", Lines: []mas.Line{}}}
	store := &stubStore{id: "cart1"}
	var stdout strings.Builder

	if err := run([]string{"cart", "clear"}, &stdout, &strings.Builder{}, cartDeps(client, store)); err != nil {
		t.Fatalf("run: %v", err)
	}
	if store.forgets != 1 {
		t.Errorf("forgets = %d, want 1", store.forgets)
	}
	if len(client.removes) != 0 {
		t.Errorf("removes = %v, want none for an empty cart", client.removes)
	}
	if !strings.Contains(stdout.String(), "cleared") {
		t.Errorf("stdout = %q, want a cleared message", stdout.String())
	}
}

// Clearing a cart still holding lines removes them from the store, then forgets the id.
func TestCartClearRemovesTheLinesItFinds(t *testing.T) {
	client := &stubCart{cart: sampleCart()}
	store := &stubStore{id: "cart1"}

	if err := run([]string{"cart", "clear"}, &strings.Builder{}, &strings.Builder{}, cartDeps(client, store)); err != nil {
		t.Fatalf("run: %v", err)
	}
	if fmt.Sprint(client.removes) != "[cart1 114042]" {
		t.Errorf("removes = %v, want [cart1 114042]", client.removes)
	}
	if store.forgets != 1 {
		t.Errorf("forgets = %d, want 1", store.forgets)
	}
}

// A stale id on `clear` is not an error: forget it and say so.
func TestCartClearForgetsAStaleID(t *testing.T) {
	client := &stubCart{err: mas.ErrCartNotFound}
	store := &stubStore{id: "cart1"}
	var stdout strings.Builder

	if err := run([]string{"cart", "clear"}, &stdout, &strings.Builder{}, cartDeps(client, store)); err != nil {
		t.Fatalf("run: %v, want nil for a stale id", err)
	}
	if store.forgets != 1 {
		t.Errorf("forgets = %d, want 1", store.forgets)
	}
	if !strings.Contains(stdout.String(), "cart1") {
		t.Errorf("stdout = %q, want it to name the forgotten cart", stdout.String())
	}
}

// Nothing stored: `clear` is a no-op that exits 0.
func TestCartClearWithNoStoredCart(t *testing.T) {
	client := &stubCart{}
	store := &stubStore{}
	var stdout, stderr strings.Builder

	if err := run([]string{"cart", "clear", "--json"}, &stdout, &stderr, cartDeps(client, store)); err != nil {
		t.Fatalf("run: %v", err)
	}
	if store.forgets != 0 {
		t.Errorf("forgets = %d, want 0", store.forgets)
	}
	if got := oneDocument(t, stdout.String()); got != `{"cleared":false}` {
		t.Errorf("stdout = %s, want {\"cleared\":false}", got)
	}
}

// Flags work before the positionals too, as in `mas search`.
func TestCartAcceptsFlagsBeforePositionals(t *testing.T) {
	client := &stubCart{id: "cartNew", cart: sampleCart()}
	store := &stubStore{}
	var stdout strings.Builder

	if err := run([]string{"cart", "add", "--json", "114042", "2"}, &stdout, &strings.Builder{}, cartDeps(client, store)); err != nil {
		t.Fatalf("run: %v", err)
	}
	if got, want := oneDocument(t, stdout.String()), oneDocument(t, sampleCartJSON); got != want {
		t.Errorf("stdout = %s, want %s", got, want)
	}
}

// Help stays exit 0 on stdout in any position for the new subcommand too.
func TestCartHelpInAnyPosition(t *testing.T) {
	for _, args := range [][]string{
		{"cart", "--help"},
		{"cart", "-h"},
		{"cart", "add", "--help"},
		{"cart", "add", "114042", "2", "-h"},
	} {
		var stdout, stderr strings.Builder
		client := &stubCart{id: "cartNew"}
		store := &stubStore{}
		if err := run(args, &stdout, &stderr, cartDeps(client, store)); err != nil {
			t.Errorf("%v: err = %v, want nil", args, err)
		}
		if !strings.Contains(stdout.String(), "mas cart add <sku> <qty>") {
			t.Errorf("%v: stdout = %q, want the cart usage", args, stdout.String())
		}
		if stderr.Len() != 0 {
			t.Errorf("%v: stderr = %q, want empty", args, stderr.String())
		}
		if client.created != 0 || len(client.adds) != 0 {
			t.Errorf("%v: store was called", args)
		}
	}
}

// A business error (the store rejects the add) exits 1 and prints nothing on stdout.
func TestCartBusinessErrorExitsOne(t *testing.T) {
	client := &stubCart{err: errors.New(`mas: add 114042: UNDEFINED: La cantidad solicitada no está disponible`)}
	store := &stubStore{id: "cart1"}
	var stdout strings.Builder

	err := run([]string{"cart", "add", "114042", "2", "--json"}, &stdout, &strings.Builder{}, cartDeps(client, store))
	if err == nil || !strings.Contains(err.Error(), "La cantidad solicitada") {
		t.Fatalf("err = %v, want the business error", err)
	}
	if code := exitCode(err); code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want nothing", stdout.String())
	}
}

// The usage text must list the new subcommand, or nobody finds it.
func TestUsageDocumentsTheCart(t *testing.T) {
	for _, want := range []string{"mas cart add <sku> <qty>", "mas cart get", "mas cart clear", "never places an order"} {
		if !strings.Contains(usage, want) {
			t.Errorf("usage does not mention %q:\n%s", want, usage)
		}
	}
}
