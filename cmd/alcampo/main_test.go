package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"multisuper/internal/alcampo"
	"multisuper/internal/contract"
)

// Real (trimmed) capture of the storefront search answer for "leche".
const lecheBody = `{"productGroups":[{"type":"personalized","decoratedProducts":[
{"productId":"176cfd5e","retailerProductId":"54180","type":"REGULAR","name":"AUCHAN Leche semidesnatada de vaca 6 x 1l Producto Alcampo.","brand":"PRODUCTO ALCAMPO","packSizeDescription":"6000ml","price":{"amount":"5.28","currency":"EUR"},"unitPrice":{"price":{"amount":"0.88","currency":"EUR"},"unit":"fop.price.per.litre","unitName":"PER_LITRE"},"available":true,"images":[{"src":"https://img/1.jpg"}]},
{"productId":"6ab4700b","retailerProductId":"54178","type":"REGULAR","name":"AUCHAN Leche entera de vaca 6 x 1 l Producto Alcampo.","brand":"PRODUCTO ALCAMPO","packSizeDescription":"6000ml","price":{"amount":"5.76","currency":"EUR"},"unitPrice":{"price":{"amount":"0.96","currency":"EUR"},"unit":"fop.price.per.litre","unitName":"PER_LITRE"},"available":false,"images":[{"src":"https://img/2.jpg"}]}
]}],"metadata":{},"additionalPageInfo":{},"missedPromotions":[]}`

const storefrontPage = `{"session":{"metadata":{"assetVersion":"2.0.0-test"},"csrf":{"token":"tok-123"}}}`

// liveClient points a real alcampo client at a fake storefront.
func liveClient(t *testing.T, status int) *alcampo.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			_, _ = w.Write([]byte(storefrontPage))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(lecheBody))
	}))
	t.Cleanup(srv.Close)
	c := alcampo.New()
	c.BaseURL = srv.URL
	c.Sleep = func(context.Context, time.Duration) error { return nil }
	return c
}

type stubSearcher struct {
	err error
}

func (s stubSearcher) Search(context.Context, string, int) (contract.Result, error) {
	return contract.Result{}, s.err
}

func TestSearchJSONEnvelope(t *testing.T) {
	var out, errOut strings.Builder
	if code := run([]string{"search", "leche", "--limit", "2", "--json"}, &out, &errOut, liveClient(t, http.StatusOK)); code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, errOut.String())
	}

	var got contract.Result
	dec := json.NewDecoder(strings.NewReader(out.String()))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&got); err != nil {
		t.Fatalf("stdout is not the JSON envelope: %v\n%s", err, out.String())
	}
	if dec.More() {
		t.Fatalf("stdout has trailing content:\n%s", out.String())
	}
	if got.Store != "alcampo" || got.Query != "leche" || got.Count != 2 || len(got.Hits) != 2 {
		t.Fatalf("envelope = %+v", got)
	}
	if got.Hits[0].ID != "54180" || got.Hits[0].Price == nil || *got.Hits[0].Price != 5.28 || got.Hits[0].Unit != "L" || got.Hits[0].PricePerUnit != 0.88 {
		t.Fatalf("first hit = %+v", got.Hits[0])
	}
	if got.Hits[1].Available == nil || *got.Hits[1].Available {
		t.Fatalf("second hit should be unavailable: %+v", got.Hits[1])
	}
	if errOut.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", errOut.String())
	}
}

func TestSearchTableIsDefault(t *testing.T) {
	var out, errOut strings.Builder
	if code := run([]string{"search", "leche"}, &out, &errOut, liveClient(t, http.StatusOK)); code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, errOut.String())
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("table has %d lines, want 3:\n%s", len(lines), out.String())
	}
	if !strings.HasPrefix(lines[0], "ID") || !strings.Contains(lines[1], "54180") || !strings.Contains(lines[1], "0.88 EUR/L") {
		t.Fatalf("unexpected table:\n%s", out.String())
	}
}

// S8: an unpublished price prints "-" in the PRICE column instead of 0,00 €.
func TestTableMarksAnUnpublishedPrice(t *testing.T) {
	res := contract.Result{Store: "alcampo", Query: "x", Count: 1, Hits: []contract.Hit{{
		Store: "alcampo", ID: "1", Name: "Plátano", Available: contract.Bool(true),
	}}}
	var out strings.Builder
	if err := printTable(&out, res); err != nil {
		t.Fatalf("printTable: %v", err)
	}
	row := strings.Split(strings.TrimSpace(out.String()), "\n")[1]
	if !regexp.MustCompile(`^1\s+Plátano\s+-\s+-$`).MatchString(row) {
		t.Fatalf("row = %q, want - for price and unit price", row)
	}
	if strings.Contains(row, "0.00") {
		t.Fatalf("unknown price must not render as 0.00: %q", row)
	}
}

func TestFetchErrorExitsOne(t *testing.T) {
	var out, errOut strings.Builder
	if code := run([]string{"search", "leche", "--json"}, &out, &errOut, liveClient(t, http.StatusInternalServerError)); code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if out.Len() != 0 {
		t.Fatalf("stdout = %q, want empty on error", out.String())
	}
	if !strings.Contains(errOut.String(), "500") {
		t.Fatalf("stderr = %q, want the HTTP status", errOut.String())
	}

	out.Reset()
	errOut.Reset()
	if code := run([]string{"search", "leche"}, &out, &errOut, stubSearcher{err: errors.New("boom")}); code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if !strings.Contains(errOut.String(), "boom") {
		t.Fatalf("stderr = %q, want the search error", errOut.String())
	}
}

func TestUsageErrorsExitTwo(t *testing.T) {
	tests := map[string][]string{
		"no command": {},
		"unknown":    {"nope"},
		"no term":    {"search"},
		"limit low":  {"search", "leche", "--limit", "0"},
		"limit high": {"search", "leche", "--limit", "101"},
		"bad flag":   {"search", "leche", "--nope"},
	}
	for name, args := range tests {
		t.Run(name, func(t *testing.T) {
			var out, errOut strings.Builder
			if code := run(args, &out, &errOut, stubSearcher{}); code != 2 {
				t.Fatalf("exit = %d, want 2 (stderr %q)", code, errOut.String())
			}
			if out.Len() != 0 {
				t.Fatalf("stdout = %q, want empty", out.String())
			}
			if errOut.Len() == 0 {
				t.Fatal("stderr is empty, want a usage message")
			}
		})
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
		var out, errOut strings.Builder
		if code := run(args, &out, &errOut, stubSearcher{}); code != 0 {
			t.Errorf("%v: exit = %d, want 0 (stderr %q)", args, code, errOut.String())
		}
		if !strings.Contains(out.String(), "usage: alcampo search") {
			t.Errorf("%v: stdout = %q, want the usage", args, out.String())
		}
		if errOut.Len() != 0 {
			t.Errorf("%v: stderr = %q, want empty", args, errOut.String())
		}
	}
}

func TestHelpExitsZero(t *testing.T) {
	var out, errOut strings.Builder
	if code := run([]string{"--help"}, &out, &errOut, stubSearcher{}); code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	if !strings.Contains(out.String(), "alcampo search") {
		t.Fatalf("stdout = %q, want usage on stdout", out.String())
	}
}
