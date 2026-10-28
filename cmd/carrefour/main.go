// Command carrefour searches Carrefour prices through Superfudis, the declared
// third-party source for this chain, and prints the shared contract.Result shape.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"text/tabwriter"

	"multisuper/internal/carrefour"
	"multisuper/internal/contract"
)

const usage = `carrefour searches Carrefour prices through Superfudis, the declared third-party source.

usage: carrefour search <term> [--limit N] [--json]

flags:
  --limit N  maximum number of hits, 1-100 (default 20)
  --json     print the shared contract JSON envelope instead of a table

exit status: 0 success, 1 fetch or parse error, 2 usage error.`

// provenanceNotice is S9's labelling: the price is reported by Superfudis, not published
// by Carrefour. It always goes to stderr, so --json stdout stays the bare envelope.
const provenanceNotice = "carrefour: prices are reported by Superfudis, not published by Carrefour"

// searcher is the slice of carrefour.Client the command needs, so tests run offline.
type searcher interface {
	Search(ctx context.Context, term string, limit int) (contract.Result, error)
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr, carrefour.NewClient()))
}

func run(args []string, stdout, stderr io.Writer, s searcher) int {
	// S3: asking for help is not an error, in any position. Left to the flag package
	// it returns flag.ErrHelp, which would be read as a usage error.
	for _, arg := range args {
		switch arg {
		case "-h", "--h", "-help", "--help":
			fmt.Fprintln(stdout, usage)
			return 0
		}
	}
	fs := flag.NewFlagSet("carrefour", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprintln(stderr, usage) }

	limit := fs.Int("limit", carrefour.DefaultLimit, "maximum number of hits (1-100)")
	asJSON := fs.Bool("json", false, "print the contract.Result JSON instead of a table")

	if len(args) == 0 || args[0] != "search" {
		fs.Usage()
		return 2
	}
	// Parse flags after the subcommand, then keep parsing after the term, so
	// `carrefour search leche entera --limit 3` works (flag.Parse stops at the first word).
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	words := fs.Args()[:0:0]
	for rest := fs.Args(); len(rest) > 0; rest = fs.Args() {
		words = append(words, rest[0])
		if err := fs.Parse(rest[1:]); err != nil {
			return 2
		}
	}
	term := ""
	for _, w := range words {
		if term != "" {
			term += " "
		}
		term += w
	}
	if term == "" {
		fs.Usage()
		return 2
	}
	// S3: an out-of-range limit is rejected, never silently clamped.
	if *limit < 1 || *limit > carrefour.MaxLimit {
		fmt.Fprintf(stderr, "--limit must be between 1 and %d, got %d\n", carrefour.MaxLimit, *limit)
		return 2
	}

	res, err := s.Search(context.Background(), term, *limit)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fmt.Fprintln(stderr, provenanceNotice)

	if *asJSON {
		if err := contract.Encode(stdout, res); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		return 0
	}
	if err := writeTable(stdout, res); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

func writeTable(w io.Writer, res contract.Result) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tNAME\tPRICE\t€/UNIT")
	for _, h := range res.Hits {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", h.ID, h.Name, price(h), perUnit(h))
	}
	return tw.Flush()
}

// price prints "-" when Superfudis reports none: 0.00 would read as free.
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
