// Command mas searches the supermercadosmas.com catalog and prints the shared
// supermarket JSON envelope, the same shape the dia binary prints.
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
	"multisuper/internal/mas"
)

const usage = `mas searches the supermercadosmas.com catalog.

usage:
  mas search <term> [--limit N] [--page N] [--json]

flags:
  --limit N  products per page, 1-100 (default 20)
  --page N   result page, 1 or more (default 1)
  --json     print the shared contract JSON envelope instead of a table

exit status: 0 success, 1 fetch or parse error, 2 usage error.`

// searcher is the slice of mas.Client the command needs, so tests run offline.
type searcher interface {
	Search(ctx context.Context, term string, page, limit int) (contract.Result, error)
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
	if err := run(os.Args[1:], os.Stdout, os.Stderr, mas.New()); err != nil {
		fmt.Fprintln(os.Stderr, "mas:", err)
		os.Exit(exitCode(err))
	}
}

func run(args []string, stdout, stderr io.Writer, s searcher) error {
	if len(args) == 0 {
		fmt.Fprintln(stderr, usage)
		return usageError{errors.New("missing command")}
	}
	switch args[0] {
	case "search":
		return runSearch(args[1:], stdout, stderr, s)
	case "-h", "--help", "help":
		fmt.Fprintln(stdout, usage)
		return nil
	default:
		fmt.Fprintln(stderr, usage)
		return usageError{fmt.Errorf("unknown command %q", args[0])}
	}
}

func runSearch(args []string, stdout, stderr io.Writer, s searcher) error {
	// S3: asking for help is not an error, in any position. Left to the flag package
	// it returns flag.ErrHelp through run, which would exit 1.
	for _, arg := range args {
		switch arg {
		case "-h", "--h", "-help", "--help":
			fmt.Fprintln(stdout, usage)
			return nil
		}
	}
	fs := flag.NewFlagSet("mas search", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprintln(stderr, usage) }
	limit := fs.Int("limit", 20, "products per page (1-100)")
	page := fs.Int("page", 1, "result page")
	asJSON := fs.Bool("json", false, "print the contract JSON envelope")
	// Parse in a loop so flags are accepted after the search term too
	// (flag.Parse alone stops at the first positional argument).
	var words []string
	for {
		if err := fs.Parse(args); err != nil {
			return usageError{err}
		}
		if fs.NArg() == 0 {
			break
		}
		words = append(words, fs.Arg(0))
		args = fs.Args()[1:]
	}

	term := strings.TrimSpace(strings.Join(words, " "))
	if term == "" {
		return usageError{errors.New(`missing search term, e.g. mas search leche`)}
	}
	if *limit < 1 || *limit > 100 {
		return usageError{fmt.Errorf("--limit must be between 1 and 100, got %d", *limit)}
	}
	if *page < 1 {
		return usageError{fmt.Errorf("--page must be 1 or more, got %d", *page)}
	}

	res, err := s.Search(context.Background(), term, *page, *limit)
	if err != nil {
		return err
	}
	if *asJSON {
		return contract.Encode(stdout, res)
	}
	return printTable(stdout, res)
}

func printTable(w io.Writer, res contract.Result) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tNAME\tPRICE\tUNIT PRICE")
	for _, h := range res.Hits {
		unitPrice := "-"
		if h.Unit != "" {
			unitPrice = fmt.Sprintf("%.2f %s/%s", h.PricePerUnit, h.Currency, h.Unit)
		}
		fmt.Fprintf(tw, "%s\t%s\t%.2f %s\t%s\n", h.ID, h.Name, h.Price, h.Currency, unitPrice)
	}
	return tw.Flush()
}
