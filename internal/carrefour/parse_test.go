package carrefour

import "testing"

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
	h := offer{Name: "Arroz redondo 2x200 g", EAN: "8411495116732", PriceEUR: 2.69, NUnits: ptr(2.0)}.hit("8411495116732")
	if h.Price != 2.69 || h.Currency != "EUR" {
		t.Fatalf("hit = %+v", h)
	}
	if h.PricePerUnit != 0 || h.Unit != "" {
		t.Fatalf("unit price = %v %q, want it left empty", h.PricePerUnit, h.Unit)
	}
}
