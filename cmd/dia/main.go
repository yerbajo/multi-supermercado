// Command dia searches the DIA (dia.es) catalog and prints the shared
// contract.Result shape, so the aggregator parses one format for every chain.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"text/tabwriter"

	"multisuper/internal/contract"
	"multisuper/internal/dia"
)

// searcher is the slice of dia.Client the command needs, so tests run offline.
type searcher interface {
	Search(ctx context.Context, term string, limit int) (contract.Result, error)
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr, dia.NewClient()))
}

func run(args []string, stdout, stderr io.Writer, s searcher) int {
	fs := flag.NewFlagSet("dia", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprintln(stderr, "usage: dia search <term> [--limit N] [--json]")
	}
	limit := fs.Int("limit", dia.DefaultLimit, "maximum number of hits (1-100)")
	asJSON := fs.Bool("json", false, "print the contract.Result JSON instead of a table")

	if len(args) == 0 || args[0] != "search" {
		fs.Usage()
		return 2
	}
	// Parse flags after the subcommand, so `dia search leche --limit 3` works.
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	term := fs.Arg(0)
	// Go's flag package stops at the first positional, so pick up flags that
	// follow the term.
	for rest := fs.Args(); len(rest) > 1; rest = fs.Args() {
		if err := fs.Parse(rest[1:]); err != nil {
			return 2
		}
	}
	if term == "" {
		fs.Usage()
		return 2
	}

	res, err := s.Search(context.Background(), term, *limit)
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
		fmt.Fprintf(tw, "%s\t%s\t%.2f\t%s\t%s\n",
			h.ID, h.Name, h.Price, perUnit(h), stock(h.Available))
	}
	return tw.Flush()
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
