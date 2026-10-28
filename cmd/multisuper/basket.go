package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"

	"multisuper/internal/aggregate"
	"multisuper/internal/basket"
)

const basketUsage = `multisuper basket prices one shopping basket in every supermarket chain.

usage: multisuper basket -f <basket.json|-> [--limit N] [--timeout D] [--only store1,store2] [--json]

flags:
  -f PATH       basket JSON file, "-" reads it from stdin (required)
  --limit N     candidates scored per item and chain, 1-100 (default 10)
  --timeout D   budget per chain, e.g. 20s (default 20s)
  --only S,...  price only these chains: alcampo, aldi, carrefour, dia, lidl, mas
  --json        print the basket JSON document instead of a table

The basket file is the user's own list of goods:

  {"items":[{"query":"leche entera","quantity":2},{"query":"pan de molde","quantity":1}]}

Each item is searched in each chain and matched by name: the same basket is priced in
integer cents per chain, with the items that chain does not sell. The ranking holds only
the chains that cover the whole basket, cheapest first. A product with no published price
counts as missing, and a weak name match is a miss, never a random product.

exit status: 0 at least one chain covers the whole basket, 1 no chain does (or every
             chain failed), 2 usage error or an invalid basket.`

// runBasket prices the basket in -f and prints one document or one table.
func runBasket(args []string, stdout, stderr io.Writer, all []aggregate.Chain, stdin io.Reader) int {
	fs := flag.NewFlagSet("multisuper basket", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprintln(stderr, basketUsage) }
	file := fs.String("f", "", "basket JSON file, - for stdin")
	limit := fs.Int("limit", basket.DefaultCandidates, "candidates scored per item and chain (1-100)")
	timeout := fs.Duration("timeout", defaultTimeout, "budget per chain")
	only := fs.String("only", "", "comma-separated chains to query")
	asJSON := fs.Bool("json", false, "print the basket JSON document")

	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		fs.Usage()
		fmt.Fprintf(stderr, "multisuper: unexpected argument %q\n", fs.Arg(0))
		return 2
	}
	if *file == "" {
		fs.Usage()
		fmt.Fprintln(stderr, "multisuper: missing -f <basket.json>, use -f - to read the basket from stdin")
		return 2
	}
	// Out-of-range flags are usage errors, never silently clamped (S3).
	if *limit < 1 || *limit > maxLimit {
		fmt.Fprintf(stderr, "--limit must be between 1 and %d, got %d\n", maxLimit, *limit)
		return 2
	}
	if *timeout <= 0 {
		fmt.Fprintf(stderr, "--timeout must be greater than 0, got %s\n", *timeout)
		return 2
	}
	selected, err := selectChains(all, *only)
	if err != nil {
		fmt.Fprintf(stderr, "%s\n", err)
		return 2
	}

	req, err := readBasket(*file, stdin)
	if err != nil {
		fmt.Fprintf(stderr, "multisuper basket: %s\n", err)
		return 2
	}

	res := basket.Price(context.Background(), req, selected, *limit, *timeout)
	if *asJSON {
		if err := basket.Encode(stdout, res); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
	} else if err := writeBasketTable(stdout, res); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if len(res.Ranking) == 0 {
		return 1
	}
	return 0
}

// readBasket opens the basket file, or reads stdin for "-".
func readBasket(path string, stdin io.Reader) (basket.Request, error) {
	if path == "-" {
		return basket.Decode(stdin)
	}
	f, err := os.Open(path)
	if err != nil {
		return basket.Request{}, err
	}
	defer f.Close()
	return basket.Decode(f)
}

// writeBasketTable prints one row per chain, then what each chain is missing, then the
// cheapest chain that covers the whole basket.
func writeBasketTable(w io.Writer, res basket.Result) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "STORE\tESTADO\tCOBERTURA\tTOTAL\tFALTAN\tMS")
	for _, c := range res.Chains {
		estado, coverage, total := "ok", c.Coverage, money(c.TotalCents)
		if c.Error != nil {
			// A chain that failed has no comparable price: "-", never 0,00 €.
			estado, coverage, total = c.Error.Code, "-", "-"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%d\t%d\n",
			c.Store, estado, coverage, total, len(c.Missing), c.ElapsedMs)
	}
	if err := tw.Flush(); err != nil {
		return err
	}

	for _, c := range res.Chains {
		if len(c.Missing) > 0 {
			fmt.Fprintf(w, "faltan en %s: %s\n", c.Store, strings.Join(c.Missing, ", "))
		}
	}
	if len(res.Ranking) == 0 {
		fmt.Fprintln(w, "ninguna cadena cubre la cesta completa")
		return nil
	}
	fmt.Fprintf(w, "más barata con la cesta completa: %s %s\n",
		res.Ranking[0].Store, money(res.Ranking[0].TotalCents))
	return nil
}

// money prints integer cents exactly, without converting back to a float.
func money(cents int64) string {
	return fmt.Sprintf("%d.%02d €", cents/100, cents%100)
}
