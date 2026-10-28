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

	"multisuper/internal/mas"
)

const cartUsage = `mas cart keeps your own anonymous guest cart on supermercadosmas.com.
It never places an order: there is no checkout here.

usage: mas cart add <sku> <qty> [--json]
       mas cart get [--json]
       mas cart remove <sku> [--json]
       mas cart clear [--json]

flags:
  --json  print the cart JSON document instead of a table

The cart id lives in mas-cart.json under the user config dir (0600). A cart is
created on the first "add"; "get" on a machine without one is an error.

exit status: 0 success, 1 fetch or business error, 2 usage error.`

// cartAPI is the slice of mas.Client the cart command needs, so tests run offline.
type cartAPI interface {
	CreateCart(ctx context.Context) (string, error)
	AddItem(ctx context.Context, cartID, sku string, qty int) (mas.Cart, error)
	Cart(ctx context.Context, cartID string) (mas.Cart, error)
	RemoveItem(ctx context.Context, cartID, sku string) (mas.Cart, error)
}

// cartStore is the local file holding the guest cart id.
type cartStore interface {
	Load() (string, error)
	Save(cartID string) error
	Forget() error
}

// cleared is what `mas cart clear --json` prints:
//
//	{"cartId":"<id>","cleared":true}
//
// cartId is omitted when nothing was stored, and cleared is false when the store had
// already forgotten the cart.
type cleared struct {
	CartID  string `json:"cartId,omitempty"`
	Cleared bool   `json:"cleared"`
}

func runCart(args []string, stdout, stderr io.Writer, client cartAPI, store cartStore) error {
	cartFlag := flag.NewFlagSet("mas cart", flag.ContinueOnError)
	cartFlag.SetOutput(stderr)
	cartFlag.Usage = func() { fmt.Fprintln(stderr, cartUsage) }
	asJSON := cartFlag.Bool("json", false, "print the cart JSON document")
	// Flags before or after the positionals, as in `mas search`.
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
		return usageError{errors.New("missing cart action: add <sku> <qty>, get, remove <sku> or clear")}
	}

	action, rest := words[0], words[1:]
	switch action {
	case "add":
		if len(rest) != 2 {
			return usageError{errors.New("cart add needs <sku> <qty>")}
		}
		qty, err := strconv.Atoi(rest[1])
		if err != nil {
			return usageError{fmt.Errorf("invalid quantity %q: want a whole number", rest[1])}
		}
		if qty < 1 {
			return usageError{fmt.Errorf("quantity must be 1 or more, got %d", qty)}
		}
		cartID, err := store.Load()
		switch {
		case errors.Is(err, mas.ErrNoCart): // Only an explicit add creates a cart.
			if cartID, err = client.CreateCart(context.Background()); err != nil {
				return err
			}
			if err := store.Save(cartID); err != nil {
				return err
			}
		case err != nil:
			return err
		}
		cart, err := client.AddItem(context.Background(), cartID, rest[0], qty)
		if err != nil {
			return cartFetchError(err, cartID)
		}
		return printCart(stdout, cart, *asJSON)
	case "get":
		if len(rest) != 0 {
			return usageError{errors.New("cart get takes no arguments")}
		}
		cartID, err := storedCart(store)
		if err != nil {
			return err
		}
		cart, err := client.Cart(context.Background(), cartID)
		if err != nil {
			return cartFetchError(err, cartID)
		}
		return printCart(stdout, cart, *asJSON)
	case "remove":
		if len(rest) != 1 {
			return usageError{errors.New("cart remove needs <sku>")}
		}
		cartID, err := storedCart(store)
		if err != nil {
			return err
		}
		cart, err := client.RemoveItem(context.Background(), cartID, rest[0])
		if err != nil {
			return cartFetchError(err, cartID)
		}
		return printCart(stdout, cart, *asJSON)
	case "clear":
		if len(rest) != 0 {
			return usageError{errors.New("cart clear takes no arguments")}
		}
		return runCartClear(stdout, client, store, *asJSON)
	default:
		fmt.Fprintln(stderr, cartUsage)
		return usageError{fmt.Errorf("unknown cart action %q", action)}
	}
}

// runCartClear empties what the stored cart still holds and forgets the id. A cart the
// store no longer knows is already clear, so it is forgotten too, never an error.
func runCartClear(stdout io.Writer, client cartAPI, store cartStore, asJSON bool) error {
	ctx := context.Background()
	cartID, err := store.Load()
	if errors.Is(err, mas.ErrNoCart) {
		return printCleared(stdout, cleared{Cleared: false}, "no cart yet\n", asJSON)
	}
	if err != nil {
		return err
	}
	cart, err := client.Cart(ctx, cartID)
	if errors.Is(err, mas.ErrCartNotFound) {
		if err := store.Forget(); err != nil {
			return err
		}
		return printCleared(stdout, cleared{CartID: cartID, Cleared: false}, fmt.Sprintf("cart %s no longer exists on the store; forgot it\n", cartID), asJSON)
	}
	if err != nil {
		return err
	}
	for _, line := range cart.Lines {
		if _, err := client.RemoveItem(ctx, cartID, line.SKU); err != nil {
			return cartFetchError(err, cartID)
		}
	}
	if err := store.Forget(); err != nil {
		return err
	}
	return printCleared(stdout, cleared{CartID: cartID, Cleared: true}, fmt.Sprintf("cart %s cleared\n", cartID), asJSON)
}

// storedCart loads the persisted id, reporting the missing case in plain words.
func storedCart(store cartStore) (string, error) {
	cartID, err := store.Load()
	if errors.Is(err, mas.ErrNoCart) {
		return "", errors.New(`no cart yet: run mas cart add <sku> <qty> first`)
	}
	return cartID, err
}

// cartFetchError keeps a stale id actionable instead of a dead end.
func cartFetchError(err error, cartID string) error {
	if errors.Is(err, mas.ErrCartNotFound) {
		return fmt.Errorf("%w: cart %s is gone; run mas cart clear to forget it", err, cartID)
	}
	return err
}

func printCleared(stdout io.Writer, doc cleared, message string, asJSON bool) error {
	if !asJSON {
		fmt.Fprint(stdout, message)
		return nil
	}
	return encodeJSON(stdout, doc)
}

func printCart(stdout io.Writer, cart mas.Cart, asJSON bool) error {
	if asJSON {
		return encodeJSON(stdout, cart)
	}
	tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	total := "-"
	if cart.Total != nil {
		total = fmt.Sprintf("%.2f %s", *cart.Total, cart.Currency)
	}
	fmt.Fprintf(tw, "CART\t%s\n", cart.ID)
	fmt.Fprintf(tw, "TOTAL\t%s\n", total)
	fmt.Fprintln(tw, "SKU\tNAME\tQTY\tUNIT PRICE\tLINE TOTAL")
	for _, line := range cart.Lines {
		unitPrice, lineTotal := "-", "-"
		if line.UnitPrice != nil {
			unitPrice = fmt.Sprintf("%.2f %s", *line.UnitPrice, cart.Currency)
		}
		if line.LineTotal != nil {
			lineTotal = fmt.Sprintf("%.2f %s", *line.LineTotal, cart.Currency)
		}
		fmt.Fprintf(tw, "%s\t%s\t%g\t%s\t%s\n", line.SKU, line.Name, line.Quantity, unitPrice, lineTotal)
	}
	return tw.Flush()
}

// encodeJSON prints one indented document and nothing else.
func encodeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
