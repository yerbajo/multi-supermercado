// Command alcampo searches the Alcampo catalogue and prints the shared supermarket
// JSON envelope, the same shape the dia and mas binaries print.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"

	"multisuper/internal/alcampo"
	"multisuper/internal/contract"
)

const usage = `alcampo searches the compraonline.alcampo.es catalog.

usage: alcampo search <term> [--limit N] [--json]

flags:
  --limit N  products to return, 1-100 (default 20)
  --json     print the shared contract JSON envelope instead of a table

exit status: 0 success, 1 fetch or parse error, 2 usage error.`

// searcher is the one method this CLI needs; tests pass a stub.
type searcher interface {
	Search(ctx context.Context, term string, limit int) (contract.Result, error)
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr, alcampo.New()))
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
	if len(args) == 0 {
		fmt.Fprintln(stderr, usage)
		return 2
	}
	switch args[0] {
	case "search":
		return runSearch(args[1:], stdout, stderr, s)
	case "help":
		fmt.Fprintln(stdout, usage)
		return 0
	default:
		fmt.Fprintln(stderr, usage)
		fmt.Fprintf(stderr, "alcampo: unknown command %q\n", args[0])
		return 2
	}
}

func runSearch(args []string, stdout, stderr io.Writer, s searcher) int {
	fs := flag.NewFlagSet("alcampo search", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprintln(stderr, usage) }
	limit := fs.Int("limit", 20, "products to return (1-100)")
	asJSON := fs.Bool("json", false, "print the contract JSON envelope")
	// Parse in a loop so flags are accepted after the search term too
	// (flag.Parse alone stops at the first positional argument).
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
		fmt.Fprintln(stderr, usage)
		fmt.Fprintln(stderr, `alcampo: missing search term, e.g. alcampo search leche`)
		return 2
	}
	if *limit < 1 || *limit > 100 {
		fmt.Fprintf(stderr, "alcampo: --limit must be between 1 and 100, got %d\n", *limit)
		return 2
	}

	res, err := s.Search(context.Background(), term, *limit)
	if err != nil {
		fmt.Fprintln(stderr, "alcampo:", err)
		return 1
	}
	if *asJSON {
		if err := contract.Encode(stdout, res); err != nil {
			fmt.Fprintln(stderr, "alcampo:", err)
			return 1
		}
		return 0
	}
	if err := printTable(stdout, res); err != nil {
		fmt.Fprintln(stderr, "alcampo:", err)
		return 1
	}
	return 0
}

func printTable(w io.Writer, res contract.Result) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tNAME\tPRICE\tUNIT PRICE")
	for _, h := range res.Hits {
		price := "-"
		if h.Price != nil {
			price = fmt.Sprintf("%.2f %s", *h.Price, h.Currency)
		}
		unitPrice := "-"
		if h.Unit != "" {
			unitPrice = fmt.Sprintf("%.2f %s/%s", h.PricePerUnit, h.Currency, h.Unit)
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", h.ID, h.Name, price, unitPrice)
	}
	return tw.Flush()
}
