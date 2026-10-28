// Command aldi searches the ALDI España (aldi.es) catalogue and prints the shared
// contract.Result shape, so the aggregator parses one format for every chain.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"

	"multisuper/internal/aldi"
	"multisuper/internal/contract"
)

const usage = `aldi searches the ALDI España (aldi.es) catalogue.

usage:
  aldi search <term> [--limit N] [--region pen|can|bal] [--json]

flags:
  --limit N   maximum number of hits, 1-100 (default 20)
  --region R  catalogue whose prices are shown: pen (peninsula, default),
              can (Canarias) or bal (Baleares)
  --json      print the shared contract JSON envelope instead of a table

ALDI does not sell online: the prices are the published catalogue prices for the
chosen region, and fresh produce is listed without a price at all.`

// searcher is the slice of aldi.Client the command needs, so tests run offline.
type searcher interface {
	Search(ctx context.Context, region, term string, limit int) (contract.Result, error)
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr, aldi.NewClient()))
}

func run(args []string, stdout, stderr io.Writer, s searcher) int {
	if len(args) > 0 {
		switch args[0] {
		case "-h", "--help", "help":
			fmt.Fprintln(stdout, usage)
			return 0
		case "search":
			return runSearch(args[1:], stdout, stderr, s)
		}
	}
	fmt.Fprintln(stderr, usage)
	return 2
}

func runSearch(args []string, stdout, stderr io.Writer, s searcher) int {
	fs := flag.NewFlagSet("aldi search", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprintln(stderr, usage) }
	limit := fs.Int("limit", aldi.DefaultLimit, "maximum number of hits (1-100)")
	region := fs.String("region", aldi.DefaultRegion, "catalogue region: pen, can or bal")
	asJSON := fs.Bool("json", false, "print the contract.Result JSON instead of a table")

	// Parse in a loop so flags are accepted after the search term too: Go's flag
	// package stops at the first positional argument.
	var words []string
	for {
		if err := fs.Parse(args); err != nil {
			return 2
		}
		if fs.NArg() == 0 {
			break
		}
		words = append(words, fs.Arg(0))
		args = fs.Args()[1:]
	}
	term := strings.TrimSpace(strings.Join(words, " "))
	if term == "" {
		fs.Usage()
		return 2
	}
	// S3: a --limit outside 1..100 is a usage error, never silently clamped.
	if *limit < 1 || *limit > aldi.MaxLimit {
		fmt.Fprintf(stderr, "--limit must be between 1 and %d, got %d\n", aldi.MaxLimit, *limit)
		return 2
	}
	if !aldi.ValidRegion(*region) {
		fmt.Fprintf(stderr, "aldi: --region must be pen, can or bal, not %q\n", *region)
		return 2
	}

	res, err := s.Search(context.Background(), *region, term, *limit)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}

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
	fmt.Fprintln(tw, "ID\tNAME\tPRICE\t€/UNIT\tIN STOCK")
	for _, h := range res.Hits {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n",
			h.ID, h.Name, price(h), perUnit(h), stock(h.Available))
	}
	return tw.Flush()
}

// price prints "-" when the catalogue publishes none: 0.00 € would read as free.
func price(h contract.Hit) string {
	if h.Price == 0 {
		return "-"
	}
	return fmt.Sprintf("%.2f €", h.Price)
}

func perUnit(h contract.Hit) string {
	if h.PricePerUnit == 0 || h.Unit == "" {
		return "-"
	}
	return fmt.Sprintf("%.2f €/%s", h.PricePerUnit, h.Unit)
}

func stock(available bool) string {
	if available {
		return "yes"
	}
	return "no"
}
