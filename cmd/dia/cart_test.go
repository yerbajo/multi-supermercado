package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"multisuper/internal/dia"
)

// sampleCart is what dia.es answers for `dia cart get` after adding 2 x 504P6.
func sampleCart() dia.Cart {
	return dia.Cart{
		ID:    "ff08514b-e36a-4a04-abde-6723adb75b97",
		Total: ptr(9.96),
		Lines: []dia.Line{{
			ObjectID: "504P6", Name: "Leche semidesnatada Dia Láctea pack 6 x 1 L", Quantity: 2,
			UnitPrice: ptr(4.98), LineTotal: ptr(9.96),
		}},
	}
}

const sampleCartJSON = `{"id":"ff08514b-e36a-4a04-abde-6723adb75b97","total":9.96,"lines":[{"objectId":"504P6","name":"Leche semidesnatada Dia Láctea pack 6 x 1 L","quantity":2,"unitPrice":4.98,"lineTotal":9.96}]}`

// stubCart keeps the cart command tests offline.
type stubCart struct {
	session string
	cart    dia.Cart
	err     error
	added   []string
	updated []string
	removed []string
	emptied int
}

func (s *stubCart) SetSessionID(id string) { s.session = id }

func (s *stubCart) SessionID() string { return s.session }

func (s *stubCart) Cart(context.Context) (dia.Cart, error) { return s.cart, s.err }

func (s *stubCart) AddItem(_ context.Context, objectID string, qty int) error {
	s.added = append(s.added, fmt.Sprintf("%s x%d", objectID, qty))
	// Without a stored session the store issues one, as the real endpoint does.
	if s.err == nil && s.session == "" {
		s.session = "session-new"
	}
	return s.err
}

func (s *stubCart) UpdateItem(_ context.Context, objectID string, qty int) error {
	s.updated = append(s.updated, fmt.Sprintf("%s x%d", objectID, qty))
	return s.err
}

func (s *stubCart) RemoveItem(_ context.Context, objectID string) error {
	s.removed = append(s.removed, objectID)
	return s.err
}

func (s *stubCart) EmptyCart(context.Context) error {
	s.emptied++
	return s.err
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
		return "", dia.ErrNoCart
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

// The first add starts the anonymous session (DIA issues it) and remembers it, because
// that session cookie is the whole cart identity.
func TestCartAddStartsASessionAndPersistsIt(t *testing.T) {
	client := &stubCart{cart: sampleCart()}
	store := &stubStore{}
	var stdout, stderr strings.Builder

	if err := run([]string{"cart", "add", "504P6", "2", "--json"}, &stdout, &stderr, cartDeps(client, store)); err != nil {
		t.Fatalf("run: %v", err)
	}
	if fmt.Sprint(client.added) != "[504P6 x2]" {
		t.Errorf("adds = %v, want [504P6 x2]", client.added)
	}
	if len(store.saved) != 1 || store.saved[0] != "session-new" {
		t.Errorf("saved = %v, want [session-new]", store.saved)
	}
	if got, want := oneDocument(t, stdout.String()), oneDocument(t, sampleCartJSON); got != want {
		t.Errorf("stdout = %s, want %s", got, want)
	}
	if stderr.Len() != 0 {
		t.Errorf("stderr = %q, want empty", stderr.String())
	}
}

// A stored session is reused: one cart per machine, never a new one per invocation.
func TestCartAddReusesTheStoredSession(t *testing.T) {
	client := &stubCart{cart: sampleCart()}
	store := &stubStore{id: "session-old"}
	var stdout strings.Builder

	if err := run([]string{"cart", "add", "504P6", "2"}, &stdout, &strings.Builder{}, cartDeps(client, store)); err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(client.added) != 1 || client.session != "session-old" {
		t.Fatalf("adds = %v session = %q", client.added, client.session)
	}
	if !strings.Contains(stdout.String(), "504P6") {
		t.Errorf("table lost the added line:\n%s", stdout.String())
	}
}

// `update <object_id> <qty>` sets the line to that quantity through the PUT call.
func TestCartUpdateSetsTheQuantity(t *testing.T) {
	client := &stubCart{cart: sampleCart()}
	store := &stubStore{id: "session-old"}
	var stdout strings.Builder

	if err := run([]string{"cart", "update", "504P6", "3", "--json"}, &stdout, &strings.Builder{}, cartDeps(client, store)); err != nil {
		t.Fatalf("run: %v", err)
	}
	if fmt.Sprint(client.updated) != "[504P6 x3]" {
		t.Errorf("updates = %v, want [504P6 x3]", client.updated)
	}
	if got, want := oneDocument(t, stdout.String()), oneDocument(t, sampleCartJSON); got != want {
		t.Errorf("stdout = %s, want %s", got, want)
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
		{"add without arguments", []string{"cart", "add"}, "cart add needs <object_id> <qty>"},
		{"add without quantity", []string{"cart", "add", "504P6"}, "cart add needs <object_id> <qty>"},
		{"add with extra argument", []string{"cart", "add", "504P6", "2", "3"}, "cart add needs <object_id> <qty>"},
		{"quantity not a number", []string{"cart", "add", "504P6", "dos"}, `invalid quantity "dos"`},
		{"quantity zero", []string{"cart", "add", "504P6", "0"}, "quantity must be 1 or more"},
		{"negative quantity reads as a flag", []string{"cart", "add", "504P6", "-3"}, "flag provided but not defined"},
		{"get with argument", []string{"cart", "get", "504P6"}, "cart get takes no arguments"},
		{"clear with argument", []string{"cart", "clear", "504P6"}, "cart clear takes no arguments"},
		{"update without arguments", []string{"cart", "update"}, "cart update needs <object_id> <qty>"},
		{"update with a bad quantity", []string{"cart", "update", "504P6", "0"}, "quantity must be 1 or more"},
		{"remove without object id", []string{"cart", "remove"}, "cart remove needs <object_id>"},
		{"remove with extra argument", []string{"cart", "remove", "504P6", "2"}, "cart remove needs <object_id>"},
		{"unknown flag", []string{"cart", "get", "--nope"}, "flag provided but not defined"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := &stubCart{cart: sampleCart()}
			store := &stubStore{id: "session-old"}
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
			if len(client.added)+len(client.updated)+len(client.removed)+client.emptied != 0 {
				t.Fatalf("store was called: %+v", client)
			}
			if len(store.saved) != 0 || store.forgets != 0 {
				t.Fatalf("cart file was written: %+v", store)
			}
		})
	}
}

// `cart get` on a machine without a session says so instead of starting a cart.
func TestCartGetWithoutAStoredSession(t *testing.T) {
	client := &stubCart{cart: sampleCart()}
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
	if len(store.saved) != 0 {
		t.Fatalf("cart file was written: %v", store.saved)
	}
}

// `--json` prints one document and nothing else.
func TestCartGetPrintsOneJSONDocument(t *testing.T) {
	client := &stubCart{cart: sampleCart()}
	store := &stubStore{id: "session-old"}
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
}

// An expired session cookie is a 302 from DIA: the file is forgotten and the user is
// pointed at `cart add`, which starts a fresh anonymous cart.
func TestCartExpiredSessionIsForgottenAndExplained(t *testing.T) {
	client := &stubCart{err: dia.ErrCartExpired}
	store := &stubStore{id: "session-old"}
	var stdout strings.Builder

	err := run([]string{"cart", "get"}, &stdout, &strings.Builder{}, cartDeps(client, store))
	if err == nil || !strings.Contains(err.Error(), "dia cart add <object_id> <qty>") {
		t.Fatalf("err = %v, want a pointer to dia cart add", err)
	}
	if code := exitCode(err); code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if store.forgets != 1 {
		t.Errorf("forgets = %d, want the dead session dropped", store.forgets)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want nothing", stdout.String())
	}
}

// `cart remove <object_id>` drops the line and prints what is left.
func TestCartRemovePrintsTheRemainingCart(t *testing.T) {
	client := &stubCart{cart: dia.Cart{ID: "cart1", Total: ptr(0), Lines: []dia.Line{}}}
	store := &stubStore{id: "session-old"}
	var stdout strings.Builder

	if err := run([]string{"cart", "remove", "504P6", "--json"}, &stdout, &strings.Builder{}, cartDeps(client, store)); err != nil {
		t.Fatalf("run: %v", err)
	}
	if fmt.Sprint(client.removed) != "[504P6]" {
		t.Errorf("removed = %v, want [504P6]", client.removed)
	}
	want := `{"id":"cart1","total":0,"lines":[]}`
	if got, want := oneDocument(t, stdout.String()), oneDocument(t, want); got != want {
		t.Errorf("stdout = %s, want %s", got, want)
	}
}

// Clearing uses DIA's empty-cart call and forgets the local session.
func TestCartClearEmptiesTheCartAndForgetsTheSession(t *testing.T) {
	client := &stubCart{}
	store := &stubStore{id: "session-old"}
	var stdout strings.Builder

	if err := run([]string{"cart", "clear", "--json"}, &stdout, &strings.Builder{}, cartDeps(client, store)); err != nil {
		t.Fatalf("run: %v", err)
	}
	if client.emptied != 1 {
		t.Errorf("emptied = %d, want 1", client.emptied)
	}
	if store.forgets != 1 {
		t.Errorf("forgets = %d, want 1", store.forgets)
	}
	if got := oneDocument(t, stdout.String()); got != `{"cleared":true}` {
		t.Errorf("stdout = %s, want {\"cleared\":true}", got)
	}
}

// Nothing stored: `clear` is a no-op that exits 0.
func TestCartClearWithNoStoredSession(t *testing.T) {
	client := &stubCart{}
	store := &stubStore{}
	var stdout, stderr strings.Builder

	if err := run([]string{"cart", "clear", "--json"}, &stdout, &stderr, cartDeps(client, store)); err != nil {
		t.Fatalf("run: %v", err)
	}
	if client.emptied != 0 || store.forgets != 0 {
		t.Fatalf("store was called: %+v", client)
	}
	if got := oneDocument(t, stdout.String()); got != `{"cleared":false}` {
		t.Errorf("stdout = %s, want {\"cleared\":false}", got)
	}
}

// Flags work before the positionals too, as in `dia search`.
func TestCartAcceptsFlagsBeforePositionals(t *testing.T) {
	client := &stubCart{cart: sampleCart()}
	store := &stubStore{}
	var stdout strings.Builder

	if err := run([]string{"cart", "add", "--json", "504P6", "2"}, &stdout, &strings.Builder{}, cartDeps(client, store)); err != nil {
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
		{"cart", "add", "504P6", "2", "-h"},
	} {
		var stdout, stderr strings.Builder
		client := &stubCart{}
		store := &stubStore{}
		if err := run(args, &stdout, &stderr, cartDeps(client, store)); err != nil {
			t.Errorf("%v: err = %v, want nil", args, err)
		}
		if !strings.Contains(stdout.String(), "dia cart add <object_id> <qty>") {
			t.Errorf("%v: stdout = %q, want the cart usage", args, stdout.String())
		}
		if stderr.Len() != 0 {
			t.Errorf("%v: stderr = %q, want empty", args, stderr.String())
		}
		if len(client.added) != 0 {
			t.Errorf("%v: store was called", args)
		}
	}
}

// A business error (DIA rejects the add) exits 1 and prints nothing on stdout.
func TestCartBusinessErrorExitsOne(t *testing.T) {
	client := &stubCart{err: errors.New("dia: cart 206 VALIDATION_ERROR: only 6 in stock")}
	store := &stubStore{id: "session-old"}
	var stdout strings.Builder

	err := run([]string{"cart", "add", "504P6", "9999", "--json"}, &stdout, &strings.Builder{}, cartDeps(client, store))
	if err == nil || !strings.Contains(err.Error(), "only 6 in stock") {
		t.Fatalf("err = %v, want the store's rejection", err)
	}
	if code := exitCode(err); code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want nothing", stdout.String())
	}
}
