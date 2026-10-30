package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strconv"
	"text/tabwriter"

	"multisuper/internal/dia"
)

const cartUsage = `dia cart keeps your own anonymous cart on dia.es.
It never places an order: there is no checkout here.

usage: dia cart add <object_id> <qty> [--json]
       dia cart get [--json]
       dia cart update <object_id> <qty> [--json]
       dia cart remove <object_id> [--json]
       dia cart clear [--json]

flags:
  --json  print the cart JSON document instead of a table

object_id is the product id DIA publishes as object_id in search results.

add sets that line to qty; update changes it; remove drops the line; clear empties
the whole cart. The cart session DIA issues is kept in dia-cart.json under the user
config dir (0600). A cart starts on the first "add"; "get" on a machine without one
is an error.

exit status: 0 success, 1 fetch or business error, 2 usage error.`

// cartAPI is the slice of dia.Client the cart command needs, so tests run offline.
type cartAPI interface {
	SessionID() string
	SetSessionID(id string)
	Cart(ctx context.Context) (dia.Cart, error)
	AddItem(ctx context.Context, objectID string, qty int) error
	UpdateItem(ctx context.Context, objectID string, qty int) error
	RemoveItem(ctx context.Context, objectID string) error
	EmptyCart(ctx context.Context) error
}

// cartStore is the local file holding the anonymous session cookie.
type cartStore interface {
	Load() (string, error)
	Save(sessionID string) error
	Forget() error
}

// cleared is what `dia cart clear --json` prints:
//
//	{"cleared":true}
//
// cleared is false when nothing was stored, or when DIA had already forgotten the
// session. The session cookie itself is never printed.
type cleared struct {
	Cleared bool `json:"cleared"`
}

func runCart(args []string, stdout, stderr io.Writer, client cartAPI, store cartStore) error {
	cartFlag := flag.NewFlagSet("dia cart", flag.ContinueOnError)
	cartFlag.SetOutput(stderr)
	cartFlag.Usage = func() { fmt.Fprintln(stderr, cartUsage) }
	asJSON := cartFlag.Bool("json", false, "print the cart JSON document")
	// Flags before or after the positionals, as in `dia search`.
	var words []string
	for {
		if err := cartFlag.Parse(args); err != nil {
			return usageError{err}
		}
		if cartFlag.NArg() == 0 {
			break
		}
		words = append(words, cartFlag.Arg(0))
		args = cartFlag.Args()[1:]
	}
	if len(words) == 0 {
		fmt.Fprintln(stderr, cartUsage)
		return usageError{errors.New("missing cart action: add <object_id> <qty>, get, update <object_id> <qty>, remove <object_id> or clear")}
	}

	action, rest := words[0], words[1:]
	ctx := context.Background()
	switch action {
	case "add":
		objectID, qty, err := objectAndQuantity(rest, "cart add needs <object_id> <qty>")
		if err != nil {
			return err
		}
		// Only an explicit add starts a cart: without a stored session the client
		// sends no cookie and DIA issues a fresh one.
		session, err := storedSession(store, true)
		if err != nil {
			return err
		}
		client.SetSessionID(session)
		if err := client.AddItem(ctx, objectID, qty); err != nil {
			return cartFetchError(err, store)
		}
		if err := store.Save(client.SessionID()); err != nil {
			return err
		}
		return printCart(ctx, stdout, client, store, *asJSON)
	case "update":
		objectID, qty, err := objectAndQuantity(rest, "cart update needs <object_id> <qty>")
		if err != nil {
			return err
		}
		session, err := storedSession(store, false)
		if err != nil {
			return err
		}
		client.SetSessionID(session)
		if err := client.UpdateItem(ctx, objectID, qty); err != nil {
			return cartFetchError(err, store)
		}
		if err := store.Save(client.SessionID()); err != nil {
			return err
		}
		return printCart(ctx, stdout, client, store, *asJSON)
	case "remove":
		if len(rest) != 1 {
			return usageError{errors.New("cart remove needs <object_id>")}
		}
		session, err := storedSession(store, false)
		if err != nil {
			return err
		}
		client.SetSessionID(session)
		if err := client.RemoveItem(ctx, rest[0]); err != nil {
			return cartFetchError(err, store)
		}
		if err := store.Save(client.SessionID()); err != nil {
			return err
		}
		return printCart(ctx, stdout, client, store, *asJSON)
	case "get":
		if len(rest) != 0 {
			return usageError{errors.New("cart get takes no arguments")}
		}
		session, err := storedSession(store, false)
		if err != nil {
			return err
		}
		client.SetSessionID(session)
		return printCart(ctx, stdout, client, store, *asJSON)
	case "clear":
		if len(rest) != 0 {
			return usageError{errors.New("cart clear takes no arguments")}
		}
		return runCartClear(ctx, stdout, client, store, *asJSON)
	default:
		fmt.Fprintln(stderr, cartUsage)
		return usageError{fmt.Errorf("unknown cart action %q", action)}
	}
}

// objectAndQuantity reads the <object_id> <qty> pair, rejecting anything else.
func objectAndQuantity(rest []string, usage string) (string, int, error) {
	if len(rest) != 2 {
		return "", 0, usageError{errors.New(usage)}
	}
	qty, err := strconv.Atoi(rest[1])
	if err != nil {
		return "", 0, usageError{fmt.Errorf("invalid quantity %q: want a whole number", rest[1])}
	}
	if qty < 1 {
		return "", 0, usageError{fmt.Errorf("quantity must be 1 or more, got %d", qty)}
	}
	return rest[0], qty, nil
}

// runCartClear empties the cart server-side and forgets the session. A session DIA no
// longer knows is already clear, so it is forgotten too, never an error.
func runCartClear(ctx context.Context, stdout io.Writer, client cartAPI, store cartStore, asJSON bool) error {
	session, err := storedSession(store, true)
	if err != nil {
		return err
	}
	if session == "" {
		return printCleared(stdout, cleared{Cleared: false}, "no cart yet\n", asJSON)
	}
	client.SetSessionID(session)
	switch err := client.EmptyCart(ctx); {
	case errors.Is(err, dia.ErrCartExpired):
		if err := store.Forget(); err != nil {
			return err
		}
		return printCleared(stdout, cleared{Cleared: false}, "the cart no longer exists on DIA; forgot it\n", asJSON)
	case err != nil:
		return err
	}
	if err := store.Forget(); err != nil {
		return err
	}
	return printCleared(stdout, cleared{Cleared: true}, "cart cleared\n", asJSON)
}

// storedSession loads the persisted session. A missing one is only allowed for `add`,
// which starts a cart; every other action says how to start one.
func storedSession(store cartStore, allowMissing bool) (string, error) {
	session, err := store.Load()
	if errors.Is(err, dia.ErrNoCart) {
		if allowMissing {
			return "", nil
		}
		return "", errors.New("no cart yet: run dia cart add <object_id> <qty> first")
	}
	return session, err
}

// cartFetchError keeps an expired session actionable: the dead cookie is dropped, so
// the next add starts a fresh cart instead of dead-ending on the old one.
func cartFetchError(err error, store cartStore) error {
	if !errors.Is(err, dia.ErrCartExpired) {
		return err
	}
	if ferr := store.Forget(); ferr != nil {
		return ferr
	}
	return fmt.Errorf("the stored cart is gone; run dia cart add <object_id> <qty> to start a new one")
}

func printCleared(stdout io.Writer, doc cleared, message string, asJSON bool) error {
	if !asJSON {
		fmt.Fprint(stdout, message)
		return nil
	}
	return encodeJSON(stdout, doc)
}

// printCart reads the cart back after a mutation (DIA answers those with 204) and prints it.
func printCart(ctx context.Context, stdout io.Writer, client cartAPI, store cartStore, asJSON bool) error {
	cart, err := client.Cart(ctx)
	if err != nil {
		return cartFetchError(err, store)
	}
	if asJSON {
		return encodeJSON(stdout, cart)
	}
	tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	total := "-"
	if cart.Total != nil {
		total = fmt.Sprintf("%.2f", *cart.Total)
	}
	fmt.Fprintf(tw, "CART\t%s\n", cart.ID)
	fmt.Fprintf(tw, "TOTAL\t%s\n", total)
	fmt.Fprintln(tw, "OBJECT ID\tNAME\tQTY\tUNIT PRICE\tLINE TOTAL")
	for _, line := range cart.Lines {
		unitPrice, lineTotal := "-", "-"
		if line.UnitPrice != nil {
			unitPrice = fmt.Sprintf("%.2f", *line.UnitPrice)
		}
		if line.LineTotal != nil {
			lineTotal = fmt.Sprintf("%.2f", *line.LineTotal)
		}
		fmt.Fprintf(tw, "%s\t%s\t%d\t%s\t%s\n", line.ObjectID, line.Name, line.Quantity, unitPrice, lineTotal)
	}
	return tw.Flush()
}

// encodeJSON prints one indented document and nothing else.
func encodeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
