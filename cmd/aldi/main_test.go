package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"testing"

	"multisuper/internal/contract"
)

func ptr(f float64) *float64 { return &f }

// fake answers like the aldi client, so the command tests stay offline.
type fake struct {
	res    contract.Result
	err    error
	region string
	term   string
	limit  int
}

func (f *fake) Search(_ context.Context, region, term string, limit int) (contract.Result, error) {
	f.region, f.term, f.limit = region, term, limit
	return f.res, f.err
}

func sample() contract.Result {
	return contract.Result{
		Store: "aldi", Query: "leche", Count: 1,
		Hits: []contract.Hit{{
			Store: "aldi", ID: "80000", Name: "Leche entera", Brand: "MILSANI®",
			Price: ptr(0.96), PricePerUnit: 0.96, Unit: "L", Currency: "EUR",
			Available: contract.Bool(true), Category: "Lácteos y huevos > Leche y bebidas vegetales",
			URL:   "https://www.aldi.es/producto/leche-entera-80000.html",
			Image: "https://s7g10.scene7.com/is/image/aldinord/800_pack",
		}},
	}
}

// The web app parses these exact keys; one JSON document and nothing else on stdout.
func TestSearchJSONIsTheContractEnvelopeAndNothingElse(t *testing.T) {
	var stdout, stderr bytes.Buffer
	f := &fake{res: sample()}
	if code := run([]string{"search", "leche", "--json"}, &stdout, &stderr, f); code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, stderr.String())
	}
	if stderr.Len() != 0 {
		t.Errorf("stderr = %q, want empty", stderr.String())
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(stdout.Bytes(), &raw); err != nil {
		t.Fatalf("stdout is not one JSON document: %v\n%s", err, stdout.String())
	}
	for _, key := range []string{"store", "query", "count", "hits"} {
		if _, ok := raw[key]; !ok {
			t.Errorf("envelope is missing %q:\n%s", key, stdout.String())
		}
	}
	var hits []map[string]json.RawMessage
	if err := json.Unmarshal(raw["hits"], &hits); err != nil || len(hits) != 1 {
		t.Fatalf("hits = %s (%v)", raw["hits"], err)
	}
	for _, key := range []string{"store", "id", "name", "price", "pricePerUnit", "unit", "currency", "available"} {
		if _, ok := hits[0][key]; !ok {
			t.Errorf("hit is missing %q:\n%s", key, stdout.String())
		}
	}
	var res contract.Result
	if err := json.Unmarshal(stdout.Bytes(), &res); err != nil {
		t.Fatalf("output does not decode into contract.Result: %v", err)
	}
	if res.Store != "aldi" || res.Hits[0].ID != "80000" {
		t.Errorf("res = %+v", res)
	}
}

// Default output is a readable table, not JSON.
func TestSearchTableIsTheDefault(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{"search", "leche"}, &stdout, &stderr, &fake{res: sample()}); code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, stderr.String())
	}
	out := stdout.String()
	if strings.HasPrefix(strings.TrimSpace(out), "{") {
		t.Errorf("default output should be a table:\n%s", out)
	}
	for _, want := range []string{"ID", "NAME", "PRICE", "80000", "Leche entera", "0.96", "0.96 €/L"} {
		if !strings.Contains(out, want) {
			t.Errorf("table is missing %q:\n%s", want, out)
		}
	}
}

// S8: a chain that publishes no stock state leaves `available` out of the JSON, and the
// table renders that unknown as "-": never as "no", which would read as sold out.
func TestTableMarksUnknownAvailability(t *testing.T) {
	var res contract.Result
	if err := json.Unmarshal([]byte(`{"store":"aldi","query":"x","count":1,"hits":[{"store":"aldi","id":"1","name":"A","price":0.96,"currency":"EUR"}]}`), &res); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := run([]string{"search", "x"}, &stdout, &stderr, &fake{res: res}); code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, stderr.String())
	}
	if !regexp.MustCompile(`(?m)^1\s+A\s+0\.96 €\s+-\s+-$`).MatchString(stdout.String()) {
		t.Errorf("unknown stock must print - and not claim a state:\n%s", stdout.String())
	}
}

// A hit the catalogue publishes without a price must not print 0.00 €.
func TestTableMarksAnUnpublishedPrice(t *testing.T) {
	res := contract.Result{Store: "aldi", Query: "cogollos", Count: 1,
		Hits: []contract.Hit{{Store: "aldi", ID: "998600", Name: "Cogollos", Currency: "EUR", Available: contract.Bool(true)}}}
	var stdout, stderr bytes.Buffer
	if code := run([]string{"search", "cogollos"}, &stdout, &stderr, &fake{res: res}); code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, stderr.String())
	}
	if !regexp.MustCompile(`(?m)^998600\s+Cogollos\s+-\s+-\s+yes$`).MatchString(stdout.String()) {
		t.Errorf("unpublished price must print - in the PRICE column:\n%s", stdout.String())
	}
	if strings.Contains(stdout.String(), "0.00") {
		t.Errorf("unknown price must not render as 0.00:\n%s", stdout.String())
	}
}

func TestFlagsReachTheClient(t *testing.T) {
	var stdout, stderr bytes.Buffer
	f := &fake{res: sample()}
	if code := run([]string{"search", "leche entera", "--limit", "3", "--region", "can"}, &stdout, &stderr, f); code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, stderr.String())
	}
	if f.term != "leche entera" || f.limit != 3 || f.region != "can" {
		t.Errorf("client got term=%q limit=%d region=%q", f.term, f.limit, f.region)
	}
}

func TestRegionDefaultsToPeninsula(t *testing.T) {
	var stdout, stderr bytes.Buffer
	f := &fake{res: sample()}
	if code := run([]string{"search", "leche"}, &stdout, &stderr, f); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if f.region != "pen" {
		t.Errorf("default region = %q, want pen", f.region)
	}
}

func TestUsageErrorsExitTwo(t *testing.T) {
	cases := map[string][]string{
		"no command":      {},
		"unknown command": {"buscar", "leche"},
		"missing term":    {"search"},
		"unknown region":  {"search", "leche", "--region", "esp"},
		"bad flag":        {"search", "leche", "--nope"},
	}
	for name, args := range cases {
		var stdout, stderr bytes.Buffer
		f := &fake{res: sample()}
		if code := run(args, &stdout, &stderr, f); code != 2 {
			t.Errorf("%s: exit = %d, want 2", name, code)
		}
		if stdout.Len() != 0 {
			t.Errorf("%s: stdout = %q, want empty", name, stdout.String())
		}
		if f.term != "" {
			t.Errorf("%s: must not query, got term %q", name, f.term)
		}
	}
}

// S3: an out-of-range --limit is rejected, never silently clamped.
func TestLimitOutOfRangeIsRejected(t *testing.T) {
	for _, limit := range []string{"0", "-1", "101", "1000"} {
		var stdout, stderr bytes.Buffer
		f := &fake{res: sample()}
		if code := run([]string{"search", "leche", "--limit", limit, "--json"}, &stdout, &stderr, f); code != 2 {
			t.Errorf("--limit %s: exit = %d, want 2", limit, code)
		}
		if stdout.Len() != 0 {
			t.Errorf("--limit %s: stdout = %q, want empty", limit, stdout.String())
		}
		if f.term != "" || f.limit != 0 {
			t.Errorf("--limit %s: must not query, got term %q limit %d", limit, f.term, f.limit)
		}
		want := "--limit must be between 1 and 100, got " + limit
		if got := stderr.String(); !strings.Contains(got, want) {
			t.Errorf("--limit %s: stderr = %q, want it to contain %q", limit, got, want)
		}
	}
}

func TestFetchErrorsExitOneAndKeepStdoutClean(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"search", "leche", "--json"}, &stdout, &stderr, &fake{err: errors.New("aldi: search status 503")})
	if code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout = %q, want empty", stdout.String())
	}
	if !strings.Contains(stderr.String(), "503") {
		t.Errorf("stderr = %q, want the cause", stderr.String())
	}
}

// S3: -h and --help print the usage on stdout and exit 0, in any position. Left to the
// flag package they return flag.ErrHelp, which this CLI reads as a usage error.
func TestHelpInAnyPositionExitsZeroWithUsageOnStdout(t *testing.T) {
	for _, args := range [][]string{
		{"--help"},
		{"-h"},
		{"search", "--help"},
		{"search", "-h"},
		{"search", "leche", "--help"},
		{"search", "leche", "-h"},
	} {
		f := &fake{}
		var stdout, stderr bytes.Buffer
		if code := run(args, &stdout, &stderr, f); code != 0 {
			t.Errorf("%v: exit = %d, want 0 (stderr %q)", args, code, stderr.String())
		}
		if !strings.Contains(stdout.String(), "usage: aldi search") {
			t.Errorf("%v: stdout = %q, want the usage", args, stdout.String())
		}
		if stderr.Len() != 0 {
			t.Errorf("%v: stderr = %q, want empty", args, stderr.String())
		}
		if f.term != "" {
			t.Errorf("%v: searcher was called with term=%q", args, f.term)
		}
	}
}

func TestHelpIsRecognised(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{"help"}, &stdout, &stderr, &fake{}); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	for _, want := range []string{"aldi search", "--region", "--limit", "--json"} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("help is missing %q:\n%s", want, stdout.String())
		}
	}
}
