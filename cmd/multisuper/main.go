// Command multisuper searches every supermarket chain at once and prints one partial
// result per chain. A caller learns what each chain answered and, for the chains that
// failed, a machine-readable code and status instead of prose.
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
	"multisuper/internal/alcampo"
	"multisuper/internal/aldi"
	"multisuper/internal/carrefour"
	"multisuper/internal/contract"
	"multisuper/internal/dia"
	"multisuper/internal/lidl"
	"multisuper/internal/mas"
)

const usage = `multisuper searches every supermarket chain at once.

usage: multisuper find <term> [--limit N] [--timeout D] [--only store1,store2] [--json]

flags:
  --limit N     maximum hits per chain, 1-100 (default 20)
  --timeout D   budget per chain, e.g. 20s (default 20s)
  --only S,...  query only these chains: alcampo, aldi, carrefour, dia, lidl, mas
  --json        print the aggregate JSON document instead of a table

The chains are queried in parallel and a failing chain never voids the others: it is
reported with a stable code (blocked, quota, timeout, network, parse, unknown), the HTTP
status when there is one, and whether retrying is worth it.

Hit prices are published by each chain as-is; there is no cross-chain merging (that
needs the EAN) and no winner is picked.

exit status: 0 at least one chain answered (even with no hits), 1 every chain failed,
             2 usage error.`

const (
	defaultLimit   = 20
	maxLimit       = 100
	defaultTimeout = 20 * time.Second
)

// searchFunc adapts a chain client method onto the one method the aggregate needs: mas
// takes a page and aldi a region, the rest match Searcher directly.
type searchFunc func(ctx context.Context, term string, limit int) (contract.Result, error)

func (f searchFunc) Search(ctx context.Context, term string, limit int) (contract.Result, error) {
	return f(ctx, term, limit)
}

// chains returns the real clients, one per store, in the order the table prints them.
// There is no cache: this process serves one query, so the TTL cache the web app needs
// belongs in the server that wraps this binary (mejoras-web.md §9).
func chains() []aggregate.Chain {
	return []aggregate.Chain{
		{Store: "alcampo", Searcher: searchFunc(func(ctx context.Context, term string, limit int) (contract.Result, error) {
			return alcampo.New().Search(ctx, term, limit)
		})},
		{Store: "aldi", Searcher: searchFunc(func(ctx context.Context, term string, limit int) (contract.Result, error) {
			return aldi.NewClient().Search(ctx, aldi.DefaultRegion, term, limit)
		})},
		{Store: "carrefour", Searcher: searchFunc(func(ctx context.Context, term string, limit int) (contract.Result, error) {
			return carrefour.NewClient().Search(ctx, term, limit)
		})},
		{Store: "dia", Searcher: searchFunc(func(ctx context.Context, term string, limit int) (contract.Result, error) {
			return dia.NewClient().Search(ctx, term, limit)
		})},
		{Store: "lidl", Searcher: searchFunc(func(ctx context.Context, term string, limit int) (contract.Result, error) {
			return lidl.New().Search(ctx, term, limit)
		})},
		{Store: "mas", Searcher: searchFunc(func(ctx context.Context, term string, limit int) (contract.Result, error) {
			return mas.New().Search(ctx, term, 1, limit)
		})},
	}
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr, chains()))
}

func run(args []string, stdout, stderr io.Writer, all []aggregate.Chain) int {
	// Asking for help is not an error, in any position: the sibling binaries' convention.
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
	case "find":
		return runFind(args[1:], stdout, stderr, all)
	case "help":
		fmt.Fprintln(stdout, usage)
		return 0
	default:
		fmt.Fprintln(stderr, usage)
		fmt.Fprintf(stderr, "multisuper: unknown command %q\n", args[0])
		return 2
	}
}

func runFind(args []string, stdout, stderr io.Writer, all []aggregate.Chain) int {
	fs := flag.NewFlagSet("multisuper find", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprintln(stderr, usage) }
	limit := fs.Int("limit", defaultLimit, "maximum hits per chain (1-100)")
	timeout := fs.Duration("timeout", defaultTimeout, "budget per chain")
	only := fs.String("only", "", "comma-separated chains to query")
	asJSON := fs.Bool("json", false, "print the aggregate JSON document")

	// Parse in a loop so flags are accepted after the search term too: the flag package
	// stops at the first positional argument.
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
		fmt.Fprintln(stderr, `multisuper: missing search term, e.g. multisuper find leche`)
		return 2
	}
	// A --limit outside 1..100 is a usage error, never silently clamped.
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

	res := aggregate.Find(context.Background(), term, *limit, *timeout, selected)
	if *asJSON {
		if err := aggregate.Encode(stdout, res); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		return aggregate.ExitCode(res)
	}
	if err := writeTable(stdout, res); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return aggregate.ExitCode(res)
}

// selectChains keeps the chains named by --only, in table order, and rejects an unknown
// name so a typo never looks like a chain that failed.
func selectChains(all []aggregate.Chain, only string) ([]aggregate.Chain, error) {
	only = strings.TrimSpace(only)
	if only == "" {
		return all, nil
	}
	known := make(map[string]bool, len(all))
	names := make([]string, len(all))
	for i, c := range all {
		known[c.Store] = true
		names[i] = c.Store
	}
	want := make(map[string]bool, len(all))
	for _, name := range strings.Split(only, ",") {
		name = strings.TrimSpace(name)
		if !known[name] {
			return nil, fmt.Errorf("--only: unknown store %q, known: %s", name, strings.Join(names, ", "))
		}
		want[name] = true
	}
	selected := make([]aggregate.Chain, 0, len(want))
	for _, c := range all {
		if want[c.Store] {
			selected = append(selected, c)
		}
	}
	return selected, nil
}

func writeTable(w io.Writer, res aggregate.Result) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "STORE\tESTADO\tHITS\tMEJOR €/UNIDAD\tMS")
	for _, r := range res.Results {
		fmt.Fprintf(tw, "%s\t%s\t%d\t%s\t%d\n", r.Store, estado(r), r.Count, bestUnit(r.Hits), r.ElapsedMs)
	}
	return tw.Flush()
}

// estado is "ok" or the stable failure code the JSON carries.
func estado(r aggregate.StoreResult) string {
	if r.Error == nil {
		return "ok"
	}
	return r.Error.Code
}

// bestUnit prints the cheapest published €/unit among the chain's hits, with its unit so
// kg and L are never read as the same measure. "-" when the chain publishes no unit
// price: the table never invents one (S8), and the unit price is a hint, not a ranking.
func bestUnit(hits []contract.Hit) string {
	best, unit := 0.0, ""
	for _, h := range hits {
		if h.PricePerUnit == 0 || h.Unit == "" {
			continue
		}
		if unit == "" || h.PricePerUnit < best {
			best, unit = h.PricePerUnit, h.Unit
		}
	}
	if unit == "" {
		return "-"
	}
	return fmt.Sprintf("%.2f €/%s", best, unit)
}
