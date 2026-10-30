// Package match decides how much of a search query a product name covers, so search
// results can be ordered by relevance: names carrying every query word first, then names
// carrying some, and names carrying none last.
//
// The normalisation and the tokenisation below are a copy of internal/basket's score.go,
// on purpose: the basket keeps its own weighting and its own acceptance threshold, and
// this package is only about ordering results. The two must stay in agreement — a new
// accent, a new function word or a new rule for splitting a size belongs in both in the
// same change.
package match

import (
	"sort"
	"strings"
	"unicode"

	"multisuper/internal/contract"
)

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

// Normalise lowercases, strips accents and collapses whitespace.
func Normalise(s string) string {
	return strings.Join(strings.Fields(foldAccents.Replace(strings.ToLower(s))), " ")
}

// stopwords are the function words that carry no product identity. Without them
// «pan de molde» would count three tokens and a name carrying only one of them would look
// half relevant.
var stopwords = map[string]bool{"de": true, "del": true, "la": true, "el": true,
	"los": true, "las": true, "con": true, "y": true, "en": true}

// Tokens is the set of normalized words and numbers in s, without stopwords. It splits on
// punctuation and on the letter/digit boundary, so a sold size is compared, not guessed.
func Tokens(s string) map[string]bool {
	rs := []rune(Normalise(s))
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

// catalogueQuery is one rewrite from the words a shopper types to the words the shops are
// allowed to print. It only fires when the query also carries one of the family words: a
// rewrite on the bare word would drag «leche entera» into the plant-drink catalogue.
type catalogueQuery struct {
	typed  string
	prints string
	family []string
}

// catalogueQueries is the whole synonym table, and it is deliberately tiny. EU Regulation
// 1308/2013 reserves «leche» for dairy, so no catalogue may call a plant drink «leche»: it
// prints «bebida de soja». Shoppers keep typing the forbidden word. Only rewrites the law
// forces are here — a wrong expansion is worse than a missing one, so no brand maps and no
// fuzzy synonyms («nata»/«crema») — and a new family is one more row.
var catalogueQueries = []catalogueQuery{
	{typed: "leche", prints: "bebida", family: []string{"soja", "avena", "arroz", "almendra", "coco", "avellana"}},
}

// Variants returns the query as typed, followed by the catalogue form of it when the query
// hits one of the rewrites above: «leche de soja» → «bebida de soja». A query already in
// catalogue form is returned alone, so asking the variants never loops.
func Variants(query string) []string {
	words := strings.Fields(query)
	tokens := Tokens(query)
	out := []string{query}
	for _, q := range catalogueQueries {
		if !tokens[q.typed] || !containsAnyToken(tokens, q.family) {
			continue
		}
		rewritten := make([]string, len(words))
		changed := false
		for i, w := range words {
			if Normalise(w) == q.typed {
				rewritten[i] = q.prints
				changed = true
				continue
			}
			rewritten[i] = w
		}
		if changed {
			out = append(out, strings.Join(rewritten, " "))
		}
	}
	return out
}

func containsAnyToken(tokens map[string]bool, words []string) bool {
	for _, w := range words {
		if tokens[w] {
			return true
		}
	}
	return false
}

// Coverage is how many of the query's tokens appear in name, and how many tokens the
// query has. A query of nothing but function words has no tokens, so every candidate
// covers 0 of 0 and the caller keeps the chain's own order.
func Coverage(query, name string) (covered, total int) {
	q, c := Tokens(query), Tokens(name)
	for tok := range q {
		if c[tok] {
			covered++
		}
	}
	return covered, len(q)
}

// Rank returns hits ordered by how much of the query each name covers: full coverage
// first, then partial by descending coverage, then none. The order is stable, so hits
// with the same coverage keep the chain's own order — which also means an answer where
// nothing covers the query comes back untouched. Zero-coverage hits are kept, never
// dropped: a chain that has nothing relevant is still better asked than emptied.
func Rank(query string, hits []contract.Hit) []contract.Hit {
	return rankBy(hits, func(name string) int {
		covered, _ := Coverage(query, name)
		return covered
	})
}

// RankVariants is Rank for a query and its catalogue forms: each hit is scored by its best
// coverage of any variant, so a name carrying every word of «bebida de soja» outranks one
// carrying only «soja» from the form the shopper typed.
func RankVariants(variants []string, hits []contract.Hit) []contract.Hit {
	return rankBy(hits, func(name string) int {
		best := 0
		for _, v := range variants {
			if covered, _ := Coverage(v, name); covered > best {
				best = covered
			}
		}
		return best
	})
}

// rankBy stable-sorts hits by descending relevance without touching the caller's slice.
func rankBy(hits []contract.Hit, covered func(name string) int) []contract.Hit {
	type scored struct {
		hit     contract.Hit
		covered int
	}
	ranked := make([]scored, len(hits))
	for i, h := range hits {
		ranked[i] = scored{hit: h, covered: covered(h.Name)}
	}
	sort.SliceStable(ranked, func(i, j int) bool { return ranked[i].covered > ranked[j].covered })

	out := make([]contract.Hit, len(ranked))
	for i, r := range ranked {
		out[i] = r.hit
	}
	return out
}
