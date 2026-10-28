package web

import (
	"io/fs"
	"regexp"
	"strings"
	"testing"

	"multisuper/internal/capabilities"
)

func read(t *testing.T, name string) string {
	t.Helper()
	raw, err := fs.ReadFile(Files, name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(raw)
}

// TestNoExternalReferences is the static guarantee of "self-contained": no CDN, no web
// font, no remote script, no raster tag, no network URL of any kind. Every src/href must
// resolve to one of the four embedded files.
func TestNoExternalReferences(t *testing.T) {
	files := []string{"index.html", "styles.css", "app.js"}
	forbidden := []string{"http://", "https://", "//cdn", "@import", "@font-face", "fonts.googleapis", "url(", "<img", "srcset=", "src=\"//", "href=\"//"}
	for _, name := range files {
		body := read(t, name)
		for _, bad := range forbidden {
			if strings.Contains(body, bad) {
				t.Errorf("%s references %q", name, bad)
			}
		}
	}

	// The favicon is an SVG: its xmlns is a namespace identifier the browser never
	// fetches, so only real loads and embeds are forbidden there.
	icon := read(t, "favicon.svg")
	for _, bad := range []string{"href=\"http", "src=\"http", "<script", "@import", "url("} {
		if strings.Contains(icon, bad) {
			t.Errorf("favicon.svg fetches %q", bad)
		}
	}

	html := read(t, "index.html")
	refs := regexp.MustCompile(`(?:src|href)="([^"]*)"`).FindAllStringSubmatch(html, -1)
	if len(refs) == 0 {
		t.Fatal("index.html references no local asset")
	}
	for _, ref := range refs {
		target := ref[1]
		if strings.HasPrefix(target, "#") {
			continue
		}
		if _, err := fs.Stat(Files, target); err != nil {
			t.Errorf("index.html references %q, which is not an embedded local file", target)
		}
	}
	if strings.Contains(html, "<script src=") {
		script := regexp.MustCompile(`<script src="([^"]*)"`).FindStringSubmatch(html)
		if script == nil || script[1] != "app.js" {
			t.Errorf("index.html loads a script that is not the local app.js: %v", script)
		}
	}
}

// TestNoFramework keeps the page framework-free: a browser runs it with no build step.
func TestNoFramework(t *testing.T) {
	body := read(t, "index.html") + read(t, "app.js")
	lower := strings.ToLower(body)
	for _, marker := range []string{"react", "vue.js", "angular", "jquery", "svelte", "preact", "cdn."} {
		if strings.Contains(lower, marker) {
			t.Errorf("the front end references %q", marker)
		}
	}
}

// TestAccessibilityStatics pins the promises that are visible in the source: a polite
// live region, a label on the postal field, and reduced motion respected.
func TestAccessibilityStatics(t *testing.T) {
	html := read(t, "index.html")
	js := read(t, "app.js")
	css := read(t, "styles.css")
	for what, check := range map[string][2]string{
		"live region":     {html, `aria-live="polite"`},
		"postal label":    {html, `<label for="cp">`},
		"search label":    {html, `<label for="q">`},
		"add label":       {html, `<label for="add-term">`},
		"select labels":   {js, `aria-label="Producto de la línea`},
		"reduced motion":  {css, "prefers-reduced-motion: reduce"},
		"visible focus":   {css, ":focus-visible"},
		"skip link":       {html, `class="skip"`},
		"retry offer":     {js, `data-retry=`},
		"postal no claim": {html, "no filtra precios"},
		"dia zone claim":  {html, "no es tu zona"},
	} {
		if !strings.Contains(check[0], check[1]) {
			t.Errorf("%s: missing %q", what, check[1])
		}
	}
}

// chainKeys reads the six chains out of the one table the front end keeps, so the logo
// assertions check reality instead of a copy of it that could drift.
func chainKeys(t *testing.T, js string) []string {
	t.Helper()
	start := strings.Index(js, "const CHAIN_NAMES = {")
	if start < 0 {
		t.Fatal("app.js has no CHAIN_NAMES table")
	}
	end := strings.Index(js[start:], "};")
	if end < 0 {
		t.Fatal("CHAIN_NAMES is not closed")
	}
	block := js[start : start+end]
	var keys []string
	for _, m := range regexp.MustCompile(`(\w+):\s*"([A-Z]+)"`).FindAllStringSubmatch(block, -1) {
		keys = append(keys, m[1])
	}
	if len(keys) != 6 {
		t.Fatalf("CHAIN_NAMES lists %d chains, want 6: %v", len(keys), keys)
	}
	return keys
}

// TestEveryChainHasAnInlineLogo pins T36's brand marks: one inline <symbol> per chain in
// the markup, drawn with shapes and text only — no file, no URL, no web font.
func TestEveryChainHasAnInlineLogo(t *testing.T) {
	html, js := read(t, "index.html"), read(t, "app.js")
	if !strings.Contains(html, `<svg class="sprite"`) {
		t.Fatal("index.html has no inline sprite to hold the brand marks")
	}
	for _, chain := range chainKeys(t, js) {
		marker := `<symbol id="logo-` + chain + `"`
		at := strings.Index(html, marker)
		if at < 0 {
			t.Errorf("index.html draws no logo for %s (want %s)", chain, marker)
			continue
		}
		block := html[at:]
		block = block[:strings.Index(block, "</symbol>")]
		if !strings.Contains(block, "<text") && !strings.Contains(block, "<path") && !strings.Contains(block, "<rect") {
			t.Errorf("logo-%s draws no shape", chain)
		}
	}
	// A brand mark is never a fetch: the sprite is inlined and the app references it by
	// fragment, which the reference test above already keeps local.
	for _, name := range []string{"index.html", "app.js"} {
		if strings.Contains(read(t, name), "<image") {
			t.Errorf("%s embeds a raster or external image element", name)
		}
	}
	if !strings.Contains(js, `<use href="#logo-`) {
		t.Error("app.js does not instantiate the sprite by local fragment")
	}
}

// TestLogosHaveAccessibleNames pins the other half: the mark is the chain's name for a
// screen reader wherever it replaces the word, so it needs role="img" plus aria-label.
func TestLogosHaveAccessibleNames(t *testing.T) {
	js := read(t, "app.js")
	start := strings.Index(js, "function logo(")
	if start < 0 {
		t.Fatal("app.js has no logo builder")
	}
	body := js[start:]
	body = body[:strings.Index(body, "\n}")]
	for _, want := range []string{`role="img"`, `aria-label="${esc(nameOf(store))}"`, `<use href="#logo-${esc(store)}"`} {
		if !strings.Contains(body, want) {
			t.Errorf("the logo builder is missing %s", want)
		}
	}
	// Every rendered chain goes through it: the store cards, the ranking, the recommendation
	// and the failure-free search heads all call logo().
	for _, call := range []string{"logo(cap.store)", "logo(r.store)", "logo(best.store)", "logo(chain.store)"} {
		if !strings.Contains(js, call) {
			t.Errorf("app.js never renders a brand mark with %s", call)
		}
	}
}

// TestFailureStatesAreIconAndText pins the four chain states and the compact failure line:
// a chain that did not answer is one line with its reason and a retry, never a row that
// looks like a competitor, and a total silence is a page-level empty state.
func TestFailureStatesAreIconAndText(t *testing.T) {
	js := read(t, "app.js")
	for what, want := range map[string]string{
		"complete":      "la más barata",
		"missing":       "Falta un artículo",
		"no quota":      "sin cuota",
		"no answer":     "sin respuesta",
		"failure line":  "cadenas no respondieron",
		"retry":         `data-retry="${retry}"`,
		"empty state":   "Hoy no respondió ninguna cadena",
		"failure place": `failBody(failed, "basket")`,
	} {
		if !strings.Contains(js, want) {
			t.Errorf("%s: app.js is missing %q", what, want)
		}
	}
	if strings.Contains(js, "ERROR_TEXT") {
		t.Error("app.js still carries the old per-row error prose")
	}
}

// fnBody is one function of app.js, from its signature to its closing brace: an
// assertion about the card cannot pass because the copy moved elsewhere in the file.
func fnBody(t *testing.T, js, fn string) string {
	t.Helper()
	at := strings.Index(js, fn)
	if at < 0 {
		t.Fatalf("app.js has no %s", strings.TrimSuffix(fn, "("))
	}
	body := js[at:]
	end := strings.Index(body, "\n}")
	if end < 0 {
		t.Fatalf("%s is not closed", fn)
	}
	return body[:end]
}

// modeChips reads the mode table out of app.js, so the assertion checks the real mapping
// instead of a copy of it.
func modeChips(t *testing.T, js string) map[string]string {
	t.Helper()
	start := strings.Index(js, "const MODE_CHIP = {")
	if start < 0 {
		t.Fatal("app.js has no MODE_CHIP table")
	}
	block := js[start:]
	end := strings.Index(block, "};")
	if end < 0 {
		t.Fatal("MODE_CHIP is not closed")
	}
	block = block[:end]
	out := map[string]string{}
	for _, m := range regexp.MustCompile(`(\w+):\s*"([^"]*)"`).FindAllStringSubmatch(block, -1) {
		out[m[1]] = m[2]
	}
	return out
}

// TestStoreCardCopyIsNotRepeated pins T37: one store card is the status header, the logo,
// the price-scope label, one mechanism sentence and one chip. Each capability field
// reaches the template exactly once — the table's label is the sentence, the mode is the
// chip, price_scoped is the header label — instead of three or four copies of it.
func TestStoreCardCopyIsNotRepeated(t *testing.T) {
	js, html := read(t, "app.js"), read(t, "index.html")
	card := fnBody(t, js, "function shopCard(")
	mech := fnBody(t, js, "function mechanism(")
	scope := fnBody(t, js, "function zoneShort(")

	for what, want := range map[string]int{
		"mechanism(zone)":      1, // the one mechanism sentence (the table's label)
		"MODE_CHIP[zone.mode]": 1, // the one chip, derived from the mode
		"zoneShort(zone)":      1, // the one price-scope label
		"zone.price_scoped":    1, // the chip's icon, never a fourth sentence
	} {
		if got := strings.Count(card, what); got != want {
			t.Errorf("shopCard uses %s %d times, want %d", what, got, want)
		}
	}
	if got := strings.Count(mech, "zone.label"); got != 1 {
		t.Errorf("mechanism reads zone.label %d times, want 1", got)
	}
	if got := strings.Count(scope, "zone.price_scoped"); got != 1 {
		t.Errorf("zoneShort reads zone.price_scoped %d times, want 1", got)
	}

	// The layers that said the same thing again are gone, not merely renamed.
	for _, gone := range []string{
		"zoneText(", "MODE_LABEL",
		"el precio no depende de la zona", "Esta cadena no tiene zona",
		"El precio depende de la región", "hasta que se fije", "precio por zona",
	} {
		if strings.Contains(js, gone) {
			t.Errorf("app.js still repeats the capability copy with %q", gone)
		}
	}

	// The four mode -> chip pairs, and no two modes sharing a chip.
	want := map[string]string{
		"warehouse": "Zona del servidor (28041)",
		"region":    "Precios por región",
		"pickup":    "Sin precio por tienda",
		"none":      "Sin zona",
	}
	chips := modeChips(t, js)
	if len(chips) != len(want) {
		t.Errorf("MODE_CHIP lists %d modes, want %d: %v", len(chips), len(want), chips)
	}
	for mode, text := range want {
		if chips[mode] != text {
			t.Errorf("mode %s chips %q, want %q", mode, chips[mode], text)
		}
	}
	seen := map[string]string{}
	for mode, text := range chips {
		if other, dup := seen[text]; dup {
			t.Errorf("modes %s and %s share the chip %q", other, mode, text)
		}
		seen[text] = mode
	}

	// No chain's zone label prose appears twice in the page the card is built from: the
	// capability table stays the only source of the mechanism sentence.
	for _, cap := range capabilities.All() {
		if n := strings.Count(js+html, cap.Zone.Label); n > 1 {
			t.Errorf("%s zone label appears %d times, a card says it once", cap.Store, n)
		}
	}
}

// TestStoreCardCounterIsNotAContradiction pins the other half of T37: the counter over the
// store cards names the capability («cadenas permiten elegir tienda»), not the chosen
// stores alone, so it cannot read as a contradiction of the «sin tienda que elegir» cards.
func TestStoreCardCounterIsNotAContradiction(t *testing.T) {
	js := read(t, "app.js")
	start := strings.Index(js, "function updatePicked(")
	if start < 0 {
		t.Fatal("app.js has no updatePicked")
	}
	body := js[start:]
	body = body[:strings.Index(body, "\n}")]
	for _, want := range []string{"cadenas permiten elegir tienda", "PICKABLE.has(s)", "elegidas"} {
		if !strings.Contains(body, want) {
			t.Errorf("the store counter is missing %q", want)
		}
	}
	if strings.Contains(js, `" tiendas elegidas"`) {
		t.Error("app.js still counts only the chosen stores")
	}
	// The number is live: ticking or clearing a store card moves the counter, and the same
	// choice takes the chain out of the ranking.
	if !strings.Contains(js, "state.chosen[box.dataset.chain] = box.checked") {
		t.Error("the store checkbox does not update the choice, so the counter cannot be live")
	}
	if !strings.Contains(js, "updatePicked()") {
		t.Error("nothing refreshes the store counter")
	}
}

// TestSearchPagesPerChain pins T39: the first ask is ten rows per chain, «ver más» re-asks
// the same query for a bigger page up to the ceiling instead of reordering anything, the
// appended rows are deduped by store+id, and one line under the results says whose order
// the shopper is reading.
func TestSearchPagesPerChain(t *testing.T) {
	js, html := read(t, "app.js"), read(t, "index.html")

	for what, want := range map[string]string{
		"first page":  "const PAGE = 10;",
		"first ask":   "term: term, limit: PAGE",
		"page cap":    "const PAGE_CAP = 100;",
		"bigger page": "Math.min(limit * 2, PAGE_CAP)",
	} {
		if !strings.Contains(js, want) {
			t.Errorf("%s: app.js is missing %q", what, want)
		}
	}

	// The block carries the control, and the control asks the chain for a bigger page of the
	// same term — never a different order.
	if !strings.Contains(fnBody(t, js, "function hitChain("), "appendHits(") {
		t.Error("the chain block never feeds its rows through appendHits")
	}
	rows := fnBody(t, js, "function appendHits(")
	for _, want := range []string{`data-more="${esc(r.store)}"`, "Ver más en "} {
		if !strings.Contains(rows, want) {
			t.Errorf("the chain block has no «ver más» control: missing %q", want)
		}
	}
	more := fnBody(t, js, "async function pageMore(")
	for _, want := range []string{
		"nextLimit(page.limit)",
		"{ term: page.term, limit: limit }",
		"appendHits(",
		"updateSearchStatus()",
	} {
		if !strings.Contains(more, want) {
			t.Errorf("«ver más» does not extend the same query: missing %q", want)
		}
	}
	if !strings.Contains(js, `closest("[data-more]")`) {
		t.Error("«ver más» has no click listener, so it is a dead button")
	}

	// Appended rows dedupe by the pair, not the id: every chain numbers its own catalogue.
	for _, want := range []string{
		`const hitKey = (store, hit) => store + "/" + hit.id;`,
		"if (seenHits.has(key)) continue;",
	} {
		if !strings.Contains(js, want) {
			t.Errorf("appended rows are not deduped by store+id: missing %q", want)
		}
	}

	// A chain that returns fewer rows than asked, or reaches the cap, loses the control and
	// says why, instead of leaving a button that does nothing.
	for _, want := range []string{"r.hits.length < page.limit", "Sin más resultados en "} {
		if !strings.Contains(js, want) {
			t.Errorf("the control is not retired with its reason: missing %q", want)
		}
	}

	// The previous block is still what it was: a pin button per row and a stated empty answer.
	for _, want := range []string{`data-pin="${esc(store)}"`, "Sin resultados para este término."} {
		if !strings.Contains(js, want) {
			t.Errorf("the search block lost its previous behaviour: missing %q", want)
		}
	}

	// One honest line under the results: the order is each store's, ours only lifts the hits
	// that carry the shopper's words.
	at := strings.Index(html, `id="hits-note"`)
	if at < 0 {
		t.Fatal("index.html has no honesty line under the results")
	}
	line := html[at:]
	if end := strings.Index(line, "</p>"); end > 0 {
		line = line[:end]
	}
	for _, want := range []string{
		"dentro de cada cadena",
		"solo ponemos delante los resultados que contienen tus palabras",
		"sin reordenar por marca ni por precio",
	} {
		if !strings.Contains(line, want) {
			t.Errorf("the honesty line does not say %q", want)
		}
	}
}
