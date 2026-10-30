package basket

import (
	"testing"

	"multisuper/internal/contract"
)

func ptr(f float64) *float64 { return &f }

// The documented normalisation: lowercase, accents stripped, whitespace collapsed.
func TestNormaliseStripsAccentsAndCollapsesWhitespace(t *testing.T) {
	tests := []struct{ in, want string }{
		{"Leche  ENTERA", "leche entera"},
		{"  Leche\tentera\n", "leche entera"},
		{"Día Láctea", "dia lactea"},
		{"Chocolate leche LINDT 100gr", "chocolate leche lindt 100gr"},
		{"Piña YOGUR griego", "pina yogur griego"},
	}
	for _, tt := range tests {
		if got := normalize(tt.in); got != tt.want {
			t.Errorf("normalize(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// Tokens split on punctuation and on the letter/digit boundary, so a sold size becomes
// the numbers and the unit it is written with.
func TestTokensSplitSizesAndUnits(t *testing.T) {
	tests := []struct {
		in   string
		want []string
	}{
		{"pack 6 x 1 L", []string{"pack", "6", "x", "1", "l"}},
		{"LINDT 100gr", []string{"lindt", "100", "gr"}},
		{"Leche entera (1,5 L)", []string{"leche", "entera", "1", "5", "l"}},
	}
	for _, tt := range tests {
		got := tokens(tt.in)
		if len(got) != len(tt.want) {
			t.Errorf("tokens(%q) = %v, want %v", tt.in, got, tt.want)
			continue
		}
		for _, w := range tt.want {
			if !got[w] {
				t.Errorf("tokens(%q) = %v, missing %q", tt.in, got, w)
			}
		}
	}
}

// The threshold is pinned by real names from the six chains: the same product matches,
// a different product that shares one word does not.
func TestScoreMatchesRealProductNames(t *testing.T) {
	tests := []struct {
		query string
		name  string
		want  bool
	}{
		// same product, the chain spells it differently
		{"leche entera", "Leche entera Dia Láctea pack 6 x 1 L", true},
		{"leche entera", "Leche entera Milbona 1 l", true},
		{"leche entera", "Leche entera Pilos 1 l", true},
		{"leche entera", "Leche entera Alipende 1 L", true},
		{"leche entera", "Leche entera Carrefour 1 L", true},
		{"leche entera", "Leche entera Pascual 1 L", true},
		{"leche entera 1 l", "Leche entera Dia Láctea pack 6 x 1 L", true},
		{"pan de molde", "Pan de molde blanco 460 g", true},
		{"pan de molde", "Pan molde integral 500 g", true},
		{"huevos", "Huevos frescos clase M 12 ud", true},
		// one shared word is not the product
		{"leche entera", "Leche condensada LA LECHERA 370 gr", false},
		{"leche entera", "Chocolate leche LINDT 100gr", false},
		{"leche entera", "Bebida de avena Alpro 1 L", false},
		{"pan de molde", "Pan de hamburguesa 6 ud", false},
		// the unit in the candidate contradicts the unit in the query
		{"leche entera 1 kg", "Leche entera Dia Láctea pack 6 x 1 L", false},
	}
	for _, tt := range tests {
		got := score(tt.query, tt.name) >= matchThreshold
		if got != tt.want {
			t.Errorf("score(%q, %q) = %.2f, match = %v, want %v",
				tt.query, tt.name, score(tt.query, tt.name), got, tt.want)
		}
	}
}

// Pin the number, not just the verdict: only "leche" of the two query tokens is in the
// chocolate name, and 0.5 is below the documented 0.6 threshold.
func TestScoreIsTheDocumentedFormula(t *testing.T) {
	if got := score("leche entera", "Chocolate leche LINDT 100gr"); got != 0.5 {
		t.Errorf("score = %v, want 0.5 (1 of 2 query tokens)", got)
	}
	if got := score("leche entera", "Leche entera Dia Láctea pack 6 x 1 L"); got != 1 {
		t.Errorf("score = %v, want 1 (both tokens)", got)
	}
	// 3 of 4 tokens (0.75) + 0.1 for the matching number - 0.3 for the contradicting unit.
	if got := score("leche entera 1 kg", "Leche entera Dia Láctea pack 6 x 1 L"); got > 0.56 || got < 0.54 {
		t.Errorf("score = %v, want ~0.55", got)
	}
}

// The pick is deterministic: best score first, then the cheapest, then the lowest id.
func TestBestPicksTheCheapestOfTheHighestScore(t *testing.T) {
	hits := []contract.Hit{
		{ID: "c", Name: "Leche entera Carrefour 1 L", Price: ptr(1.20)},
		{ID: "b", Name: "Leche entera Carrefour 1 L", Price: ptr(0.99)},
		{ID: "a", Name: "Leche entera Carrefour 1 L", Price: ptr(0.99)},
		{ID: "z", Name: "Leche condensada LA LECHERA 370 gr", Price: ptr(0.10)},
	}
	got, ok := best("leche entera", hits)
	if !ok {
		t.Fatal("no candidate matched")
	}
	if got.ID != "a" || got.UnitCents != 99 {
		t.Fatalf("got %q at %d cents, want a at 99", got.ID, got.UnitCents)
	}
}

// A candidate that publishes no price cannot join a comparable total, so it is not a
// candidate at all.
func TestBestSkipsCandidatesWithoutAPublishedPrice(t *testing.T) {
	hits := []contract.Hit{
		{ID: "free", Name: "Leche entera gratis"},
		{ID: "paid", Name: "Leche entera Carrefour 1 L", Price: ptr(1.05)},
	}
	got, ok := best("leche entera", hits)
	if !ok || got.ID != "paid" || got.UnitCents != 105 {
		t.Fatalf("got %q %d %v, want paid 105 true", got.ID, got.UnitCents, ok)
	}
	if _, ok := best("leche entera", []contract.Hit{{ID: "free", Name: "Leche entera gratis"}}); ok {
		t.Fatal("a hit without a price was treated as a match")
	}
}

// Money crosses into integer cents once: no float can survive into the arithmetic.
func TestCentsRoundsHalfAwayFromZero(t *testing.T) {
	tests := []struct {
		price float64
		want  int64
	}{
		{2.89, 289},
		{0.1, 10},
		{0.2, 20},
		{0.1 + 0.2, 30},
		{1.15, 115},
		{0.005, 1},
		{0, 0},
	}
	for _, tt := range tests {
		if got := Cents(tt.price); got != tt.want {
			t.Errorf("Cents(%v) = %d, want %d", tt.price, got, tt.want)
		}
	}
}
