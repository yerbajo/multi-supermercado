package contract

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// ptr is a published price: non-nil means the store published that number.
func ptr(f float64) *float64 { return &f }

func compact(t *testing.T, s string) string {
	t.Helper()
	var buf bytes.Buffer
	if err := json.Compact(&buf, []byte(s)); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, s)
	}
	return buf.String()
}

// The aggregator web app parses these exact keys; renaming one silently breaks it.
func TestEncodeEmitsTheFieldNamesTheWebAppConsumes(t *testing.T) {
	var buf bytes.Buffer
	err := Encode(&buf, Result{
		Store: "x",
		Query: "q",
		Count: 1,
		Hits: []Hit{{
			Store: "x", ID: "1", Name: "n", Price: ptr(1.5),
			PricePerUnit: 2, Unit: "kg", Currency: "EUR", Available: Bool(true),
		}},
	})
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}

	for _, key := range []string{
		`"store"`, `"query"`, `"count"`, `"hits"`,
		`"id"`, `"name"`, `"price"`, `"pricePerUnit"`, `"unit"`, `"currency"`, `"available"`,
	} {
		if !strings.Contains(buf.String(), key) {
			t.Errorf("encoded result is missing %s:\n%s", key, buf.String())
		}
	}

	// One JSON document, indented, so a CLI cannot leak a second value into stdout.
	var back Result
	if err := json.Unmarshal(buf.Bytes(), &back); err != nil {
		t.Fatalf("output is not a single JSON document: %v\n%s", err, buf.String())
	}
	if back.Hits[0].PricePerUnit != 2 || back.Hits[0].Unit != "kg" {
		t.Errorf("round trip lost the unit price: %+v", back.Hits[0])
	}
}

// Optional fields must disappear entirely rather than serialise as zero values.
func TestEncodeOmitsUnknownOptionalFields(t *testing.T) {
	var buf bytes.Buffer
	if err := Encode(&buf, Result{Store: "x", Query: "q", Hits: []Hit{{Store: "x", ID: "1", Name: "n"}}}); err != nil {
		t.Fatalf("Encode: %v", err)
	}
	for _, key := range []string{`"price"`, `"currency"`, `"pricePerUnit"`, `"unit"`, `"brand"`, `"category"`, `"url"`, `"image"`} {
		if strings.Contains(buf.String(), key) {
			t.Errorf("empty %s should be omitted:\n%s", key, buf.String())
		}
	}
}

// S8: an absent price means "the store publishes no price", never 0,00 €. Both keys
// vanish so a consumer cannot mistake an unknown price for a free product.
func TestEncodeOmitsAnUnpublishedPrice(t *testing.T) {
	var buf bytes.Buffer
	err := Encode(&buf, Result{Store: "x", Query: "q", Count: 1, Hits: []Hit{{
		Store: "x", ID: "1", Name: "n", Available: Bool(true),
	}}})
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	want := `{"store":"x","query":"q","count":1,"hits":[{"store":"x","id":"1","name":"n","available":true}]}`
	if got := compact(t, buf.String()); got != want {
		t.Fatalf("json mismatch\n got: %s\nwant: %s", got, want)
	}
}

// S8: a chain that publishes no stock state leaves the key out. An absent `available`
// means "unknown"; it must never be serialised as a false the web app would read as
// "out of stock".
func TestEncodeOmitsAnUnknownAvailability(t *testing.T) {
	var buf bytes.Buffer
	err := Encode(&buf, Result{Store: "x", Query: "q", Count: 1, Hits: []Hit{{
		Store: "x", ID: "1", Name: "n", Price: ptr(2.75), Currency: "EUR",
	}}})
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	want := `{"store":"x","query":"q","count":1,"hits":[{"store":"x","id":"1","name":"n","price":2.75,"currency":"EUR"}]}`
	if got := compact(t, buf.String()); got != want {
		t.Fatalf("json mismatch\n got: %s\nwant: %s", got, want)
	}
}

// A published price stays a plain number: no wrapper object and no second presence
// field, so the web app keeps reading `hit.price`.
func TestEncodeWritesAPublishedPriceAsANumber(t *testing.T) {
	var buf bytes.Buffer
	err := Encode(&buf, Result{Store: "x", Query: "q", Count: 1, Hits: []Hit{{
		Store: "x", ID: "1", Name: "n", Price: ptr(2.75), Currency: "EUR", Available: Bool(false),
	}}})
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	want := `{"store":"x","query":"q","count":1,"hits":[{"store":"x","id":"1","name":"n","price":2.75,"currency":"EUR","available":false}]}`
	if got := compact(t, buf.String()); got != want {
		t.Fatalf("json mismatch\n got: %s\nwant: %s", got, want)
	}
}
