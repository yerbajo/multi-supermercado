package match

import (
	"testing"

	"multisuper/internal/contract"
)

func hit(id, name string) contract.Hit {
	return contract.Hit{Store: "stub", ID: id, Name: name}
}

// names renames the hits so a failed order is readable.
func names(hits []contract.Hit) []string {
	out := make([]string, len(hits))
	for i, h := range hits {
		out[i] = h.Name
	}
	return out
}

// The documented normalisation: lowercase, accents stripped, whitespace collapsed.
func TestNormaliseStripsAccentsAndCollapsesWhitespace(t *testing.T) {
	tests := []struct{ in, want string }{
		{"Leche  ENTERA", "leche entera"},
		{"  Leche\tentera\n", "leche entera"},
		{"Día Láctea", "dia lactea"},
		{"FIN CARRÉ Chocolate", "fin carre chocolate"},
		// ® is punctuation: Normalise keeps it, Tokens splits on it.
		{"SILVERCREST® Molde para pan", "silvercrest® molde para pan"},
		{"Piña YOGUR griego", "pina yogur griego"},
	}
	for _, tt := range tests {
		if got := Normalise(tt.in); got != tt.want {
			t.Errorf("Normalise(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// Tokens split on punctuation and on the letter/digit boundary and drop the function
// words, so «pan de molde» is two tokens and not three.
func TestTokensSplitSizesAndDropFunctionWords(t *testing.T) {
	tests := []struct {
		in   string
		want []string
	}{
		{"pack 6 x 1 L", []string{"pack", "6", "x", "1", "l"}},
		{"LINDT 100gr", []string{"lindt", "100", "gr"}},
		{"Leche entera (1,5 L)", []string{"leche", "entera", "1", "5", "l"}},
		{"pan de molde", []string{"pan", "molde"}},
		{"JOHNSON & JOHNSON Enjuague bucal", []string{"johnson", "enjuague", "bucal"}},
	}
	for _, tt := range tests {
		got := Tokens(tt.in)
		if len(got) != len(tt.want) {
			t.Errorf("Tokens(%q) = %v, want %v", tt.in, got, tt.want)
			continue
		}
		for _, w := range tt.want {
			if !got[w] {
				t.Errorf("Tokens(%q) = %v, missing %q", tt.in, got, w)
			}
		}
	}
}

// Real names from the live binary: two words of two, one of two, none of two.
func TestCoverageCountsTheQueryTokensInTheName(t *testing.T) {
	tests := []struct {
		query   string
		name    string
		covered int
		total   int
	}{
		{"leche entera", "Leche entera Dia Láctea pack 6 x 1 L", 2, 2},
		{"leche entera", "Leche fresca entera", 2, 2},
		{"leche entera", "FIN CARRÉ Chocolate", 0, 2}, // no token of «leche entera» is in it
		{"leche entera", "Chocolate leche LINDT 100gr", 1, 2},
		{"leche entera", "JOHNSON & JOHNSON Enjuague bucal", 0, 2},
		// The function word does not count on either side: two tokens, both covered.
		{"pan de molde", "LA CESTERA Pan de molde 12 cereales", 2, 2},
		{"pan de molde", "Pan de hamburguesa 6 ud", 1, 2},
		// A single-word query is 1 of 1 or 0 of 1.
		{"leche", "Leche entera PASCUAL botella 1,5l", 1, 1},
		{"leche", "SILVERCREST Molde para pan", 0, 1},
		// A query of only function words has no tokens: nothing to cover.
		{"de la", "Leche entera", 0, 0},
		{"", "Leche entera", 0, 0},
	}
	for _, tt := range tests {
		covered, total := Coverage(tt.query, tt.name)
		if covered != tt.covered || total != tt.total {
			t.Errorf("Coverage(%q, %q) = %d/%d, want %d/%d",
				tt.query, tt.name, covered, total, tt.covered, tt.total)
		}
	}
}

// The ranker is the whole point: two of two, then one of two, then none — and equal
// coverage keeps the chain's order, so the ranking never invents one.
func TestRankOrdersByCoverageAndIsStable(t *testing.T) {
	hits := []contract.Hit{
		hit("1", "FIN CARRÉ Chocolate"),
		hit("2", "JOHNSON & JOHNSON Enjuague bucal"),
		hit("3", "Leche entera Dia Láctea"),
		hit("4", "Chocolate leche LINDT 100gr"),
		hit("5", "MILBONA Nata"),
		hit("6", "Leche fresca entera"),
		hit("7", "REALVALLE Lacón al horno"),
	}

	got := names(Rank("leche entera", hits))
	want := []string{
		"Leche entera Dia Láctea",     // 2/2, first of its tier in the original order
		"Leche fresca entera",         // 2/2
		"Chocolate leche LINDT 100gr", // 1/2
		"FIN CARRÉ Chocolate",         // 0/2, before the next zero because it came first
		"JOHNSON & JOHNSON Enjuague bucal",
		"MILBONA Nata",
		"REALVALLE Lacón al horno",
	}
	if len(got) != len(want) {
		t.Fatalf("rank returned %d hits, want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("ranked order = %v, want %v", got, want)
		}
	}
}

// Rank never drops a hit and never mutates the chain's slice.
func TestRankKeepsEveryHitAndLeavesTheInputAlone(t *testing.T) {
	hits := []contract.Hit{hit("1", "Nata"), hit("2", "Leche entera")}
	got := Rank("leche entera", hits)

	if len(got) != 2 {
		t.Fatalf("rank returned %d hits, want both", len(got))
	}
	if got[0].ID != "2" {
		t.Fatalf("ranked order = %v, want the covering hit first", names(got))
	}
	if hits[0].ID != "1" {
		t.Fatalf("Rank reordered the caller's slice: %v", names(hits))
	}
}

// A chain whose whole answer covers nothing keeps its own order: junk is better than an
// empty answer when the chain simply has nothing relevant.
func TestRankKeepsTheOriginalOrderWhenNothingCovers(t *testing.T) {
	hits := []contract.Hit{
		hit("1", "FIN CARRÉ Chocolate"),
		hit("2", "JOHNSON & JOHNSON Enjuague bucal"),
		hit("3", "MILBONA Nata"),
	}
	got := names(Rank("leche entera", hits))
	for i := range hits {
		if got[i] != hits[i].Name {
			t.Fatalf("ranked order = %v, want the original order untouched", got)
		}
	}
}

// «leche de soja» must also be asked as «bebida de soja»: the catalogue may not call a
// plant drink «leche». Every plant of the family is covered.
func TestVariantsAddsTheCatalogueFormOfPlantDrinks(t *testing.T) {
	for _, plant := range []string{"soja", "avena", "arroz", "almendra", "coco", "avellana"} {
		query := "leche de " + plant
		got := Variants(query)
		want := []string{query, "bebida de " + plant}
		if len(got) != len(want) {
			t.Fatalf("Variants(%q) = %v, want %v", query, got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("Variants(%q) = %v, want %v", query, got, want)
			}
		}
	}
}

// Everything outside the family is left alone, and the expansion never chained: the
// catalogue form is already the catalogue form, so asking it again would loop.
func TestVariantsLeavesEveryOtherQueryAlone(t *testing.T) {
	for _, query := range []string{
		"leche entera",
		"leche",
		"bebida de soja",
		"bebida soja con calcio IFA",
		"soja texturizada",
		"leche de vaca",
		"",
	} {
		if got := Variants(query); len(got) != 1 || got[0] != query {
			t.Errorf("Variants(%q) = %v, want just the original", query, got)
		}
	}
}

// A hit fully covering the catalogue form outranks one covering a single word of the
// form the shopper typed, and the order stays stable inside a tier.
func TestRankVariantsUsesTheBestCoverageOfAnyVariant(t *testing.T) {
	hits := []contract.Hit{
		hit("1", "Bebida de arroz sin azucar"),
		hit("2", "Salsa de soja KIKKOMAN 150ml"),
		hit("3", "Bebida soja con calcio IFA ELIGES 1l"),
		hit("4", "Soja texturizada 500g"),
	}

	got := names(RankVariants([]string{"leche de soja", "bebida de soja"}, hits))
	want := []string{
		"Bebida soja con calcio IFA ELIGES 1l", // 2/2 of «bebida de soja»
		"Bebida de arroz sin azucar",           // 1/2, first of its tier
		"Salsa de soja KIKKOMAN 150ml",         // 1/2
		"Soja texturizada 500g",                // 1/2
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("ranked order = %v, want %v", got, want)
		}
	}
}

// One word still ranks: the names carrying it come first, in the order they had.
func TestRankWithASingleWord(t *testing.T) {
	hits := []contract.Hit{
		hit("1", "SILVERCREST Molde para pan"),
		hit("2", "Leche entera PASCUAL botella 1,5l"),
		hit("3", "Leche fresca entera"),
	}
	got := names(Rank("leche", hits))
	want := []string{"Leche entera PASCUAL botella 1,5l", "Leche fresca entera", "SILVERCREST Molde para pan"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("ranked order = %v, want %v", got, want)
		}
	}
}
