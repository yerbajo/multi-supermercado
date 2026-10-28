package carrefour

import (
	"encoding/json"
	"strings"
	"testing"

	"multisuper/internal/contract"
)

func ptr[T any](v T) *T { return &v }

// S8: the €/unit only ever comes from the sizes Superfudis publishes
// (unit_size_value/unit_size_unit, volume_total, n_units), never from the name.
func TestSizeUsesPublishedNumbersOnly(t *testing.T) {
	tests := []struct {
		name  string
		offer offer
		// want is the sold amount in the returned unit; 0 means "no unit price".
		want float64
		unit string
	}{
		{
			name:  "millilitres of a bottle",
			offer: offer{Name: "Leche 2,2 l.", NUnits: ptr(1.0), VolumeTotal: ptr(2200.0), UnitSizeValue: ptr(2200.0), UnitSizeUnit: ptr("ml")},
			want:  2.2, unit: "L",
		},
		{
			name:  "centilitres",
			offer: offer{UnitSizeValue: ptr(33.0), UnitSizeUnit: ptr("cl")},
			want:  0.33, unit: "L",
		},
		{
			name:  "litres",
			offer: offer{UnitSizeValue: ptr(1.5), UnitSizeUnit: ptr("l")},
			want:  1.5, unit: "L",
		},
		{
			name:  "grams",
			offer: offer{Name: "Azúcar 750 g", UnitSizeValue: ptr(750.0), UnitSizeUnit: ptr("g")},
			want:  0.75, unit: "kg",
		},
		{
			name:  "grams spelled gr",
			offer: offer{UnitSizeValue: ptr(500.0), UnitSizeUnit: ptr("gr")},
			want:  0.5, unit: "kg",
		},
		{
			name:  "kilos",
			offer: offer{UnitSizeValue: ptr(1.0), UnitSizeUnit: ptr("kg")},
			want:  1.0, unit: "kg",
		},
		{
			name:  "pack multiplies the published unit size",
			offer: offer{Name: "Pack 2x200 g", NUnits: ptr(2.0), UnitSizeValue: ptr(200.0), UnitSizeUnit: ptr("g")},
			want:  0.4, unit: "kg",
		},
		{
			name:  "volume_total wins over the unit size when both are published",
			offer: offer{NUnits: ptr(3.0), VolumeTotal: ptr(3000.0), UnitSizeValue: ptr(1000.0), UnitSizeUnit: ptr("ml")},
			want:  3.0, unit: "L",
		},
		{
			name:  "no published unit leaves both fields empty",
			offer: offer{Name: "Leche entera 2,2 l.", NUnits: ptr(1.0)},
			want:  0, unit: "",
		},
		{
			name:  "unknown unit is never guessed",
			offer: offer{Name: "Papel 8 rollos", NUnits: ptr(8.0), UnitSizeValue: ptr(8.0), UnitSizeUnit: ptr("unidad")},
			want:  0, unit: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, unit := size(tt.offer)
			if got != tt.want || unit != tt.unit {
				t.Fatalf("size() = %v %q, want %v %q", got, unit, tt.want, tt.unit)
			}
		})
	}
}

// The hit keeps the published price and leaves the unit price empty when the amount
// is unknown, instead of estimating one (S8).
func TestHitLeavesUnitPriceEmptyWithoutPublishedSize(t *testing.T) {
	h := offer{Name: "Arroz redondo 2x200 g", EAN: "8411495116732", PriceEUR: ptr(2.69), NUnits: ptr(2.0)}.hit("8411495116732")
	if h.Price == nil || *h.Price != 2.69 || h.Currency != "EUR" {
		t.Fatalf("hit = %+v", h)
	}
	if h.PricePerUnit != 0 || h.Unit != "" {
		t.Fatalf("unit price = %v %q, want it left empty", h.PricePerUnit, h.Unit)
	}
}

// The offer carries the barcode Superfudis publishes; the aggregator matches the same
// product across chains with it, so it is copied as published and never derived.
func TestHitCarriesThePublishedEAN(t *testing.T) {
	o := offer{
		Name: "Leche entera Central Lechera Asturiana botella 2,2 l.", EAN: "8410297110979",
		NUnits: ptr(1.0), UnitSizeValue: ptr(2200.0), UnitSizeUnit: ptr("ml"),
		PriceEUR: ptr(2.75), MatchType: "exact_ean",
	}
	got := marshal(t, o.hit(offerID(o, 0)))
	for _, key := range []string{`"ean":"8410297110979"`, `"size":"2200 ml"`} {
		if !strings.Contains(got, key) {
			t.Errorf("hit is missing %s:\n%s", key, got)
		}
	}
}

// Size is text built from the numbers Superfudis publishes (unit_size_value,
// unit_size_unit, n_units), never parsed out of the product name.
func TestSizeTextIsBuiltFromPublishedNumbers(t *testing.T) {
	tests := []struct {
		name  string
		offer offer
		want  string
	}{
		{
			name:  "single bottle in millilitres",
			offer: offer{Name: "Leche 2,2 l.", NUnits: ptr(1.0), UnitSizeValue: ptr(2200.0), UnitSizeUnit: ptr("ml")},
			want:  "2200 ml",
		},
		{
			name:  "pack multiplies nothing but shows the pack",
			offer: offer{Name: "Leche pack 6 x 1 L", NUnits: ptr(6.0), UnitSizeValue: ptr(1000.0), UnitSizeUnit: ptr("ml")},
			want:  "6 x 1000 ml",
		},
		{
			name:  "weight in grams keeps the published unit",
			offer: offer{Name: "Azúcar 370 gr", UnitSizeValue: ptr(370.0), UnitSizeUnit: ptr("gr")},
			want:  "370 gr",
		},
		{
			name:  "decimal comma in the published value",
			offer: offer{UnitSizeValue: ptr(1.5), UnitSizeUnit: ptr("l")},
			want:  "1,5 l",
		},
		{
			name:  "the name is never read",
			offer: offer{Name: "Leche entera 2,2 l.", NUnits: ptr(1.0)},
			want:  "",
		},
		{
			name:  "a value without a unit is not a size",
			offer: offer{UnitSizeValue: ptr(2200.0)},
			want:  "",
		},
		{
			name:  "a unit without a value is not a size",
			offer: offer{UnitSizeUnit: ptr("ml")},
			want:  "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sizeText(tt.offer); got != tt.want {
				t.Fatalf("sizeText() = %q, want %q", got, tt.want)
			}
		})
	}
}

// An offer Superfudis publishes no barcode and no size for leaves both keys out: the
// aggregator must not read an absent identifier as a real one.
func TestHitOmitsIdentifiersSuperfudisDidNotPublish(t *testing.T) {
	o := offer{Name: "Leche entera 2,2 l.", MatchType: "fuzzy", PriceEUR: ptr(2.75)}
	got := marshal(t, o.hit(offerID(o, 1)))
	for _, key := range []string{`"ean"`, `"size"`} {
		if strings.Contains(got, key) {
			t.Errorf("hit carries %s though Superfudis published none:\n%s", key, got)
		}
	}
}

// marshal serialises one hit so the assertions read the JSON the web app receives.
func marshal(t *testing.T, h contract.Hit) string {
	t.Helper()
	raw, err := json.Marshal(h)
	if err != nil {
		t.Fatalf("marshal hit: %v", err)
	}
	return string(raw)
}
