// Command dia searches the DIA (dia.es) catalog and prints the shared
// contract.Result shape, so the aggregator parses one format for every chain.
// It also keeps your own anonymous cart, never an order.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"

	"multisuper/internal/contract"
	"multisuper/internal/dia"
)

const usage = `dia searches the DIA (dia.es) catalog and keeps an anonymous guest cart.

usage: dia search <term> [--limit N] [--json]
       dia cart add <object_id> <qty> [--json]
       dia cart get [--json]
       dia cart update <object_id> <qty> [--json]
       dia cart remove <object_id> [--json]
       dia cart clear [--json]

flags:
  --limit N  maximum number of hits, 1-100 (default 20)
  --json     print the JSON document instead of a table

The cart never places an order: see "dia cart --help".

exit status: 0 success, 1 fetch or parse error, 2 usage error.`

// searcher is the slice of dia.Client the search command needs, so tests run offline.
type searcher interface {
	Search(ctx context.Context, term string, limit int) (contract.Result, error)
}

// deps is everything run reaches for, so tests inject stubs and stay offline.
type deps struct {
	search searcher
	cart   cartAPI
	store  cartStore
}

// usageError marks a bad invocation: exit 2, distinct from a fetch or parse error.
type usageError struct{ error }

// exitCode maps an error onto the exit codes S3 declares for every binary.
func exitCode(err error) int {
	var usage usageError
	if errors.As(err, &usage) {
		return 2
	}
	return 1
}

func main() {
	client := dia.NewClient()
	path, err := dia.DefaultCartPath()
	if err != nil {
		fmt.Fprintln(os.Stderr, "dia:", err)
		os.Exit(1)
	}
	d := deps{search: client, cart: client, store: dia.CartFile{Path: path}}
	// Errors are printed once, bare, as this binary always did.
	if err := run(os.Args[1:], os.Stdout, os.Stderr, d); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(exitCode(err))
	}
}

func run(args []string, stdout, stderr io.Writer, d deps) error {
	// S3: asking for help is not an error, in any position. Left to the flag package
	// it returns flag.ErrHelp through run, which would exit 1.
	for _, arg := range args {
		switch arg {
		case "-h", "--h", "-help", "--help":
			if len(args) > 0 && args[0] == "cart" {
				fmt.Fprintln(stdout, cartUsage)
				return nil
			}
			fmt.Fprintln(stdout, usage)
			return nil
		}
	}
	if len(args) == 0 {
		fmt.Fprintln(stderr, usage)
		return usageError{errors.New("missing command")}
	}
	switch args[0] {
	case "search":
		return runSearch(args[1:], stdout, stderr, d.search)
	case "cart":
		return runCart(args[1:], stdout, stderr, d.cart, d.store)
	case "help":
		fmt.Fprintln(stdout, usage)
		return nil
	default:
		// Keep the pre-existing behaviour: an unknown command prints the search usage.
		fmt.Fprintln(stderr, usage)
		return usageError{fmt.Errorf("unknown command %q", args[0])}
	}
}

func runSearch(args []string, stdout, stderr io.Writer, s searcher) error {
	fs := flag.NewFlagSet("dia search", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprintln(stderr, usage) }
	limit := fs.Int("limit", dia.DefaultLimit, "maximum number of hits (1-100)")
	asJSON := fs.Bool("json", false, "print the contract.Result JSON instead of a table")

	// Parse flags after the subcommand, so `dia search leche --limit 3` works, and
	// collect every positional: the term is the whole phrase (S3).
	var words []string
	for rest := args; ; {
		if err := fs.Parse(rest); err != nil {
			return usageError{err}
		}
		if fs.NArg() == 0 {
			break
		}
		words = append(words, fs.Arg(0))
		rest = fs.Args()[1:]
	}
	term := strings.TrimSpace(strings.Join(words, " "))
	if term == "" {
		fs.Usage()
		return usageError{errors.New("missing search term")}
	}
	// S3: a --limit outside 1..100 is a usage error, never silently clamped.
	if *limit < 1 || *limit > dia.MaxLimit {
		return usageError{fmt.Errorf("--limit must be between 1 and %d, got %d", dia.MaxLimit, *limit)}
	}

	res, err := s.Search(context.Background(), term, *limit)
	if err != nil {
		return err
	}
	if *asJSON {
		return contract.Encode(stdout, res)
	}
	return writeTable(stdout, res)
}

func writeTable(w io.Writer, res contract.Result) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tNAME\tPRICE\t€/UNIT\tIN STOCK")
	for _, h := range res.Hits {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n",
			h.ID, h.Name, price(h), perUnit(h), stock(h.Available))
	}
	return tw.Flush()
}

// price prints "-" when the store publishes none: 0.00 would read as free.
func price(h contract.Hit) string {
	if h.Price == nil {
		return "-"
	}
	return fmt.Sprintf("%.2f", *h.Price)
}

func perUnit(h contract.Hit) string {
	if h.PricePerUnit == 0 || h.Unit == "" {
		return "-"
	}
	return fmt.Sprintf("%.2f €/%s", h.PricePerUnit, h.Unit)
}

// stock prints "-" when the chain publishes no stock state: unknown is not "no" (S8).
func stock(available *bool) string {
	if available == nil {
		return "-"
	}
	if *available {
		return "yes"
	}
	return "no"
}
