package basket

import (
	"math"
	"sort"
	"strings"
	"unicode"

	"multisuper/internal/contract"
)

// matchThreshold is the documented cut below which a candidate counts as "not found".
// A candidate that shares one of two query tokens scores 0.5 and is rejected, so
// «leche entera» is never matched to «Chocolate leche LINDT 100gr»; a candidate that
// shares every query token scores 1 and is accepted. Matching the wrong product is
// worse than reporting the item missing, because the total would be a lie.
const matchThreshold = 0.6

// score is the matching function, and this is its definition:
//
//	score = clamp01(overlap + sizeBonus - unitPenalty), a candidate matches at >= 0.6
//
//	overlap     = |query tokens ∩ candidate tokens| / |query tokens|
//	sizeBonus   = 0.1 per number in the query that also appears in the candidate, max 0.2
//	unitPenalty = 0.3 when both sides name a measure (kg, L, g, ml, cl, ud) and differ
//
// Text is normalised first (lowercase, accents stripped, whitespace collapsed) and
// tokenised on punctuation and on the letter/digit boundary, so "pack 6 x 1 L" is
// {pack,6,x,1,l} and "100gr" is {100,gr}: the size in a name is compared, not guessed.
// Function words (de, del, la, el, los, las, con, y, en) are dropped, so «pan de molde»
// does not half-match «Pan de hamburguesa».
//
// The candidate list is ordered by score, then by price, then by id, so the same ten
// results always produce the same pick.
func score(query, name string) float64 {
	q, c := tokens(query), tokens(name)
	if len(q) == 0 {
		return 0
	}

	common, bonus := 0, 0.0
	for tok := range q {
		if !c[tok] {
			continue
		}
		common++
		if isNumber(tok) {
			bonus += 0.1
		}
	}

	s := float64(common)/float64(len(q)) + math.Min(bonus, 0.2)
	if qu, cu := unit(q), unit(c); qu != "" && cu != "" && qu != cu {
		s -= 0.3
	}
	return math.Max(0, math.Min(1, s))
}

// foldAccents is the only normalisation the chains' names need: they print the same
// word with or without its accent, and a missing accent must not cost a token.
var foldAccents = strings.NewReplacer(
	"á", "a", "à", "a", "ä", "a", "â", "a", "ã", "a",
	"é", "e", "è", "e", "ë", "e", "ê", "e",
	"í", "i", "ì", "i", "ï", "i", "î", "i",
	"ó", "o", "ò", "o", "ö", "o", "ô", "o", "õ", "o",
	"ú", "u", "ù", "u", "ü", "u", "û", "u",
	"ñ", "n", "ç", "c",
)

// normalize lowercases, strips accents and collapses whitespace.
func normalize(s string) string {
	return strings.Join(strings.Fields(foldAccents.Replace(strings.ToLower(s))), " ")
}

// stopwords are the function words that carry no product identity. Without them
// «pan de molde» would share two of three tokens with «Pan de hamburguesa» and match.
var stopwords = map[string]bool{"de": true, "del": true, "la": true, "el": true,
	"los": true, "las": true, "con": true, "y": true, "en": true}

// tokens is the set of normalized words and numbers in s, without stopwords.
func tokens(s string) map[string]bool {
	rs := []rune(normalize(s))
	out := make(map[string]bool, len(rs))
	start, prevDigit := -1, false
	flush := func(end int) {
		if start < 0 {
			return
		}
		if tok := string(rs[start:end]); !stopwords[tok] {
			out[tok] = true
		}
		start = -1
	}
	for i, r := range rs {
		digit := unicode.IsDigit(r)
		if !digit && !unicode.IsLetter(r) {
			flush(i)
			continue
		}
		if start < 0 {
			start, prevDigit = i, digit
			continue
		}
		if digit != prevDigit {
			flush(i)
			start, prevDigit = i, digit
		}
	}
	flush(len(rs))
	return out
}

// unitTokens are the measure words the six chains print inside a product name.
var unitTokens = map[string]bool{
	"kg": true, "g": true, "gr": true, "mg": true,
	"l": true, "ml": true, "cl": true, "ud": true, "uds": true,
}

// unitCanon maps the ways a chain spells a measure onto one token, so «1 L» and
// «1 litro» agree and «1 L» against «1 kg» contradicts.
func unitCanon(t string) string {
	switch t {
	case "litro", "litros":
		return "l"
	case "kilo", "kilos", "kilogramo", "kilogramos":
		return "kg"
	case "gramo", "gramos":
		return "g"
	case "mililitro", "mililitros":
		return "ml"
	case "unidad", "unidades":
		return "ud"
	}
	if unitTokens[t] {
		return t
	}
	return ""
}

// unit is the measure a token set names, empty when it names none. The sort makes it
// deterministic when a name carries two measures.
func unit(set map[string]bool) string {
	found := make([]string, 0, len(set))
	for tok := range set {
		if u := unitCanon(tok); u != "" {
			found = append(found, u)
		}
	}
	if len(found) == 0 {
		return ""
	}
	sort.Strings(found)
	return found[0]
}

func isNumber(t string) bool {
	for _, r := range t {
		if !unicode.IsDigit(r) {
			return false
		}
	}
	return t != ""
}

// Cents converts a published price into integer cents once, at the boundary; every
// total after this point is int64 arithmetic, so 0.1 + 0.2 is 30 cents and not
// 0.30000000000000004 €. Half a cent rounds away from zero, the usual money rule.
func Cents(price float64) int64 {
	return int64(math.Round(price * 100))
}

// best picks one candidate for query: the highest score above the threshold, the
// cheapest among equals, the lowest id among those. A candidate without a published
// price is skipped, because it cannot join a comparable total.
func best(query string, hits []contract.Hit) (contract.Hit, int64, bool) {
	var (
		pick  contract.Hit
		cents int64
		top   float64
		found bool
	)
	for _, h := range hits {
		if h.Price == nil {
			continue
		}
		c := Cents(*h.Price)
		s := score(query, h.Name)
		if s < matchThreshold {
			continue
		}
		better := !found || s > top ||
			(s == top && (c < cents || (c == cents && h.ID < pick.ID)))
		if better {
			pick, cents, top, found = h, c, s, true
		}
	}
	return pick, cents, found
}
