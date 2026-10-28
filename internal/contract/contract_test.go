package contract

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// The aggregator web app parses these exact keys; renaming one silently breaks it.
func TestEncodeEmitsTheFieldNamesTheWebAppConsumes(t *testing.T) {
	var buf bytes.Buffer
	err := Encode(&buf, Result{
		Store: "x",
		Query: "q",
		Count: 1,
		Hits: []Hit{{
			Store: "x", ID: "1", Name: "n", Price: 1.5,
			PricePerUnit: 2, Unit: "kg", Currency: "EUR", Available: true,
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
	for _, key := range []string{`"pricePerUnit"`, `"unit"`, `"brand"`, `"category"`, `"url"`, `"image"`} {
		if strings.Contains(buf.String(), key) {
			t.Errorf("empty %s should be omitted:\n%s", key, buf.String())
		}
	}
}
