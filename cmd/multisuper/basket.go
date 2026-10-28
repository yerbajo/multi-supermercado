package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"multisuper/internal/aggregate"
	"multisuper/internal/basket"
)

const basketUsage = `multisuper basket prices one shopping basket in every supermarket chain, and
multisuper basket candidates proposes scored products so the user can pin a choice.

usage: multisuper basket -f <basket.json|-> [--limit N] [--timeout D] [--only store1,store2] [--json]
       multisuper basket candidates -f <basket.json|-> [--candidates N] [--timeout D] [--only ...] [--json]

flags (basket):
  -f PATH       basket JSON file, "-" reads it from stdin (required)
  --limit N     candidates scored per item and chain, 1-100 (default 10)
  --timeout D   budget per chain, e.g. 20s (default 20s)
  --only S,...  price only these chains: alcampo, aldi, carrefour, dia, lidl, mas
  --json        print the basket JSON document instead of a table

flags (basket candidates):
  -f PATH         basket JSON file, "-" reads it from stdin (required)
  --candidates N  candidates offered per item and chain, 1-10 (default 3)
  --timeout D     budget per chain, e.g. 20s (default 20s)
  --only S,...    ask only these chains
  --json          print the candidates JSON document instead of a table

The basket file is the user's own list of goods, with two kinds of entry, mixed freely:

  {"items":[
    {"query":"leche entera","quantity":2},
    {"line":"huevos","query":"huevos","quantity":2},
    {"line":"huevos","store":"dia","id":"305802","name":"Huevos ... Dia 6 unidades","quantity":2},
    {"line":"huevos","store":"mas","id":"116867","name":"Huevos frescos 12 ud","quantity":2}]}

A query is matched by name in every chain. A store/id/name entry is a product the user
chose from "basket candidates": it is confirmed by its exact id in that chain, at the price
the chain publishes now, and never by matching another product. If the id is not in that
chain's answer today the chain reports the line as stale and leaves the ranking — a wrong
product is worse than no total. A chain that failed keeps its error, which is not the same
answer as stale.

line says which basket line an entry belongs to, so one line can hold a different chosen
product per chain:

  - an entry with a query and no line is its own line, named after the query;
  - a chosen entry always needs a line, and every entry of a line shares one quantity;
  - one line holds at most one chosen product per chain and at most one query;
  - the query is the fallback for the chains the user has not chosen a product in.

A basket written without line keys behaves exactly as before.

The ranking holds only the chains that cover the whole basket, cheapest first.

exit status: 0 at least one chain covers the whole basket, 1 no chain does (or every
             chain failed), 2 usage error or an invalid basket.`

// runBasket prices the basket in -f and prints one document or one table, or proposes
// candidates when the first argument is "candidates".
func runBasket(args []string, stdout, stderr io.Writer, all []aggregate.Chain, stdin io.Reader) int {
	if len(args) > 0 && args[0] == "candidates" {
		return runCandidates(args[1:], stdout, stderr, all, stdin)
	}
	fs := flag.NewFlagSet("multisuper basket", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprintln(stderr, basketUsage) }
	file := fs.String("f", "", "basket JSON file, - for stdin")
	limit := fs.Int("limit", basket.DefaultCandidates, "candidates scored per item and chain (1-100)")
	timeout := fs.Duration("timeout", defaultTimeout, "budget per chain")
	only := fs.String("only", "", "comma-separated chains to query")
	asJSON := fs.Bool("json", false, "print the basket JSON document")

	if !parseFlags(fs, args, stderr) {
		return 2
	}
	selected, ok := validateBasketRun(fs, stderr, all, *file, *only, *timeout)
	if !ok {
		return 2
	}
	// Out-of-range flags are usage errors, never silently clamped (S3).
	if *limit < 1 || *limit > maxLimit {
		fmt.Fprintf(stderr, "--limit must be between 1 and %d, got %d\n", maxLimit, *limit)
		return 2
	}

	req, err := readBasket(*file, stdin, selected)
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

// parseFlags parses args and rejects a stray positional argument.
func parseFlags(fs *flag.FlagSet, args []string, stderr io.Writer) bool {
	if err := fs.Parse(args); err != nil {
		return false
	}
	if fs.NArg() > 0 {
		fs.Usage()
		fmt.Fprintf(stderr, "multisuper: unexpected argument %q\n", fs.Arg(0))
		return false
	}
	return true
}

// validateBasketRun is the validation both basket forms share: the required -f, the
// timeout and the chain selection. Each command keeps its own limit flag, so the bounds
// message names the flag the user actually typed.
func validateBasketRun(fs *flag.FlagSet, stderr io.Writer, all []aggregate.Chain, file, only string, timeout time.Duration) ([]aggregate.Chain, bool) {
	if file == "" {
		fs.Usage()
		fmt.Fprintln(stderr, "multisuper: missing -f <basket.json>, use -f - to read the basket from stdin")
		return nil, false
	}
	if timeout <= 0 {
		fmt.Fprintf(stderr, "--timeout must be greater than 0, got %s\n", timeout)
		return nil, false
	}
	selected, err := selectChains(all, only)
	if err != nil {
		fmt.Fprintf(stderr, "%s\n", err)
		return nil, false
	}
	return selected, true
}

// runCandidates proposes scored candidates per item and chain, so the user can pin the
// product instead of trusting the automatic match (L7).
func runCandidates(args []string, stdout, stderr io.Writer, all []aggregate.Chain, stdin io.Reader) int {
	fs := flag.NewFlagSet("multisuper basket candidates", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprintln(stderr, basketUsage) }
	file := fs.String("f", "", "basket JSON file, - for stdin")
	n := fs.Int("candidates", basket.DefaultProposals, "candidates offered per item and chain (1-10)")
	timeout := fs.Duration("timeout", defaultTimeout, "budget per chain")
	only := fs.String("only", "", "comma-separated chains to query")
	asJSON := fs.Bool("json", false, "print the candidates JSON document")

	if !parseFlags(fs, args, stderr) {
		return 2
	}
	selected, ok := validateBasketRun(fs, stderr, all, *file, *only, *timeout)
	if !ok {
		return 2
	}
	if *n < 1 || *n > basket.MaxProposals {
		fmt.Fprintf(stderr, "--candidates must be between 1 and %d, got %d\n", basket.MaxProposals, *n)
		return 2
	}
	req, err := readBasket(*file, stdin, selected)
	if err != nil {
		fmt.Fprintf(stderr, "multisuper basket candidates: %s\n", err)
		return 2
	}

	res := basket.Candidates(context.Background(), req, selected, *n, *timeout)
	if *asJSON {
		if err := basket.EncodeCandidates(stdout, res); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		return 0
	}
	if err := writeCandidatesTable(stdout, res); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

// readBasket opens the basket file, or reads stdin for "-". chains is what is being
// priced: a pinned line naming any other chain is a usage error, not a silent miss.
func readBasket(path string, stdin io.Reader, chains []aggregate.Chain) (basket.Request, error) {
	stores := make([]string, len(chains))
	for i, c := range chains {
		stores[i] = c.Store
	}
	if path == "-" {
		return basket.Decode(stdin, stores)
	}
	f, err := os.Open(path)
	if err != nil {
		return basket.Request{}, err
	}
	defer f.Close()
	return basket.Decode(f, stores)
}

// writeBasketTable prints one row per chain, then what each chain is missing or cannot
// confirm, then the cheapest chain that covers the whole basket.
func writeBasketTable(w io.Writer, res basket.Result) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "STORE\tESTADO\tCOBERTURA\tTOTAL\tFALTAN\tSTALE\tMS")
	for _, c := range res.Chains {
		estado, coverage, total := "ok", c.Coverage, money(c.TotalCents)
		if c.Error != nil {
			// A chain that failed has no comparable price: "-", never 0,00 €.
			estado, coverage, total = c.Error.Code, "-", "-"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%d\t%d\t%d\n",
			c.Store, estado, coverage, total, len(c.Missing), len(c.Stale), c.ElapsedMs)
	}
	if err := tw.Flush(); err != nil {
		return err
	}

	for _, c := range res.Chains {
		if len(c.Missing) > 0 {
			fmt.Fprintf(w, "faltan en %s: %s\n", c.Store, strings.Join(c.Missing, ", "))
		}
		if len(c.Stale) > 0 {
			fmt.Fprintf(w, "sin confirmar en %s: %s\n", c.Store, strings.Join(c.Stale, ", "))
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

// writeCandidatesTable lists the proposals in chain order, so the user can copy one id
// into a pinned line. Score 1.00 means "shares every word with the query", not "correct".
func writeCandidatesTable(w io.Writer, res basket.CandidatesResult) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ITEM\tSTORE\tPUNTOS\tPRECIO\tPRODUCTO")
	for _, it := range res.Items {
		for _, c := range res.Chains {
			for _, cand := range it.Candidates[c.Store] {
				name := cand.Name
				if cand.Current {
					name += " (actual)"
				}
				fmt.Fprintf(tw, "%s\t%s\t%.2f\t%s\t%s\n",
					it.Line, c.Store, cand.Score, money(cand.UnitCents), name)
			}
		}
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	for _, c := range res.Chains {
		if c.Error != nil {
			fmt.Fprintf(w, "%s no propone candidatos: %s\n", c.Store, c.Error.Message)
		}
	}
	return nil
}

// money prints integer cents exactly, without converting back to a float.
func money(cents int64) string {
	return fmt.Sprintf("%d.%02d €", cents/100, cents%100)
}
