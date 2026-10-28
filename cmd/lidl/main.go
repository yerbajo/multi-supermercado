// Command lidl searches the lidl.es catalog and prints the shared contract.Result
// shape, so every chain's CLI answers the aggregator with the same document.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"text/tabwriter"

	"multisuper/internal/contract"
	"multisuper/internal/lidl"
)

const usage = `lidl searches the lidl.es catalog.

usage: lidl search <term> [--limit N] [--json]

flags:
  --limit N  maximum number of hits, 1-100 (default 20)
  --json     print the shared contract JSON envelope instead of a table

exit status: 0 success, 1 fetch or parse error, 2 usage error.`

// searcher is the slice of lidl.Client the command needs, so tests run offline.
type searcher interface {
	Search(ctx context.Context, term string, limit int) (contract.Result, error)
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr, lidl.New()))
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
	fs := flag.NewFlagSet("lidl", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprintln(stderr, usage) }

	limit := fs.Int("limit", 20, "maximum number of hits (1-100)")
	asJSON := fs.Bool("json", false, "print the contract.Result JSON instead of a table")

	if len(args) == 0 || args[0] != "search" {
		fs.Usage()
		return 2
	}
	// Parse flags after the subcommand, then keep parsing after the term, so
	// `lidl search leche entera --limit 3` works (flag.Parse stops at the first word).
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
	if *limit < 1 || *limit > 100 {
		fmt.Fprintf(stderr, "--limit must be between 1 and 100, got %d\n", *limit)
		return 2
	}

	res, err := s.Search(context.Background(), term, *limit)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}

	// S8: Lidl publishes no price for some articles. The hit carries no price and the
	// gap is declared here, never inside the JSON document.
	if unpriced := unpricedHits(res); unpriced > 0 {
		fmt.Fprintf(stderr, "lidl: %d of %d hits have no published price on lidl.es; their price is left empty\n",
			unpriced, res.Count)
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

func unpricedHits(res contract.Result) int {
	n := 0
	for _, h := range res.Hits {
		if h.Price == nil {
			n++
		}
	}
	return n
}

func writeTable(w io.Writer, res contract.Result) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tNAME\tPRICE\t€/UNIT")
	for _, h := range res.Hits {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", h.ID, h.Name, price(h), perUnit(h))
	}
	return tw.Flush()
}

// price prints "-" when Lidl publishes none: 0.00 would read as free.
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
