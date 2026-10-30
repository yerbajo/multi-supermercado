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
// live region, a label on the search field and on every chain switch, and reduced motion
// respected. S17 removed the postal step, so its field is gone, not hidden.
func TestAccessibilityStatics(t *testing.T) {
	html := read(t, "index.html")
	js := read(t, "app.js")
	css := read(t, "styles.css")
	for what, check := range map[string][2]string{
		"live region":    {html, `aria-live="polite"`},
		"search label":   {html, `<label for="q">`},
		"chain switch":   {js, `aria-label="Buscar en `},
		"remove label":   {js, `aria-label="Quitar `},
		"reduced motion": {css, "prefers-reduced-motion: reduce"},
		"visible focus":  {css, ":focus-visible"},
		"skip link":      {html, `class="skip"`},
		"retry offer":    {js, `data-retry=`},
		"dia zone claim": {html, "no es tu zona"},
	} {
		if !strings.Contains(check[0], check[1]) {
			t.Errorf("%s: missing %q", what, check[1])
		}
	}
	for _, gone := range []string{`id="cp"`, "código postal", "cp-status", "postal-form"} {
		if strings.Contains(html, gone) || strings.Contains(js, gone) {
			t.Errorf("the page still has a postal-code step: %q", gone)
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
	// Every rendered chain goes through it: the store cards, the search heads, the basket
	// sections and the comparison rows all call logo().
	for _, call := range []string{"logo(cap.store)", "logo(r.store)", "logo(store)", "logo(chain.store)"} {
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
		"stale pin":     "Sin confirmar",
		"no basket":     "no resuelve ninguna línea",
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

// TestTheCardsAreTheSearchSelector pins S17's mechanism: the card's switch decides which
// chains the page asks. The counter is how many chains the next search will ask, a chain
// the table cannot search has no switch and says why, and an empty selection stops the
// search instead of quietly asking all six.
func TestTheCardsAreTheSearchSelector(t *testing.T) {
	js := read(t, "app.js")

	pick := fnBody(t, js, "function picked(")
	for _, want := range []string{"canSearch(store)", "!state.off.has(store)", "chainOrder()"} {
		if !strings.Contains(pick, want) {
			t.Errorf("picked() is not the one list of chains to ask: missing %q", want)
		}
	}
	guard := fnBody(t, js, "function canSearch(")
	if !strings.Contains(guard, "cap.search !== false") {
		t.Error("canSearch ignores the capability table, so a chain that cannot search could still be asked")
	}

	counter := fnBody(t, js, "function updatePicked(")
	for _, want := range []string{"picked().length", "chainOrder().length", `" supermercados"`} {
		if !strings.Contains(counter, want) {
			t.Errorf("the counter is missing %q", want)
		}
	}

	// The switch is the choice, and it is live.
	for _, want := range []string{"state.off.add(store)", "state.off.delete(store)", "updatePicked()"} {
		if !strings.Contains(js, want) {
			t.Errorf("the switch does not move the selection: missing %q", want)
		}
	}

	card := fnBody(t, js, "function shopCard(")
	for what, want := range map[string]string{
		"an accessible switch":  `aria-label="Buscar en ${name}"`,
		"the table as the gate": "canSearch(cap.store)",
		"a stated reason":       "Su API no permite buscar",
		"the mark on the left": `<span class="shop-logo">${logo(cap.store)}</span>
      <span class="shop-body">`,
	} {
		if !strings.Contains(card, want) {
			t.Errorf("%s: shopCard is missing %q", what, want)
		}
	}

	// The old store-picker prose is gone, not renamed: there is no store to pick without a
	// postal code, and no card promises one.
	for _, gone := range []string{"PICKABLE", "cadenas permiten elegir tienda", "Sin tienda que elegir", "Usar esta tienda"} {
		if strings.Contains(js, gone) {
			t.Errorf("app.js still carries the store picker's copy: %q", gone)
		}
	}

	search := fnBody(t, js, "async function runSearch(")
	for _, want := range []string{"const stores = picked()", "stores: stores", "Marca al menos un supermercado"} {
		if !strings.Contains(search, want) {
			t.Errorf("the search does not ask only the marked chains: missing %q", want)
		}
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
		"{ term: page.term, limit: limit, stores: [store] }",
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

// TestBasketIsGroupedByChain pins S14: the basket is read per supermarket. One section per
// chain that answered carries that chain's mark, its name and the subtotal the payload
// already summed, and inside it the lines that chain resolves — the same basket line
// legitimately appears in several sections, each with its own product and price.
func TestBasketIsGroupedByChain(t *testing.T) {
	js := read(t, "app.js")

	sections := fnBody(t, js, "function sectionsFor(")
	if !strings.Contains(sections, "!c.error") {
		t.Error("the basket builds a section for a chain that did not answer: that is a failure line, not a section")
	}
	section := fnBody(t, js, "function chainSection(")
	for what, want := range map[string]string{
		"a section keyed by chain": "dataset.chain",
		"the chain's mark":         "logo(store)",
		"the chain's name":         "nameOf(store)",
		"the payload's subtotal":   "money(chain.totalCents)",
		"only its own lines":       "line.store === store",
		"a stated empty section":   "no resuelve ninguna línea",
	} {
		if !strings.Contains(section, want) {
			t.Errorf("%s: chainSection is missing %q", what, want)
		}
	}
	// The subtotal is the payload's: the section never adds a line price up in JS.
	if strings.Contains(section, "lineCents") {
		t.Error("chainSection sums line prices instead of using chains[].totalCents")
	}

	// A line element carries the line and the chain, and a line lives in exactly one
	// section: the one of the supermarket it was added from (S15).
	line := fnBody(t, js, "function sectionLine(")
	for what, want := range map[string]string{
		"line identity":   `data-line="${esc(line.key)}"`,
		"chain identity":  `data-store="${esc(store)}"`,
		"its own product": "r ? r.name : line.name",
		"its own price":   "sectionNote(line, r)",
		"the stepper":     "stepperHTML(line)",
	} {
		if !strings.Contains(line, want) {
			t.Errorf("%s: sectionLine is missing %q", what, want)
		}
	}
	// The line price is that chain's lineCents, never the unit price times the quantity.
	note := fnBody(t, js, "function sectionNote(")
	if !strings.Contains(note, "r.lineCents") {
		t.Error("a section line does not show its own chain's line price")
	}
	if strings.Contains(note, "r.unitCents *") || strings.Contains(note, "line.quantity *") {
		t.Error("the line price is recomputed from the unit price")
	}
	// Missing and stale lines are not priced section lines: the chain that cannot serve one
	// does not charge for it, and the line says so in its own section.
	if strings.Contains(line, "it.missing") || strings.Contains(line, "it.stale") {
		t.Error("a section lists a line the chain does not resolve")
	}

	// S15: no «Elegir producto en X» inside a section. It existed only to move a line into
	// another chain, which is exactly the parallel basket the user forbade.
	for _, gone := range []string{
		`class="chooser"`, `<details`, "Elegir producto en ", "choiceFor(", "choice-state",
		"function candidates", "/api/candidates", "candidateItem(", "ensureCandidates(",
	} {
		if strings.Contains(js, gone) {
			t.Errorf("app.js still offers a per-chain chooser inside the sections: %q", gone)
		}
	}

	// A quantity change moves the section's subtotal in place: the payload's cents, never a
	// sum in JS, and no rebuild that would steal the focus of the stepper.
	sync := fnBody(t, js, "function syncLines(")
	for want, what := range map[string]string{
		`".bc-sub"`:               "the section subtotal update",
		"money(chain.totalCents)": "the subtotal from the payload",
	} {
		if !strings.Contains(sync, want) {
			t.Errorf("%s is missing %q", what, want)
		}
	}

	// A chain that did not answer keeps one compact line inside the basket too, with the
	// retry the comparison has.
	panel := fnBody(t, js, "function renderLines(")
	for want, what := range map[string]string{
		`failBody(failed, "basket")`: "the compact failure line in the basket",
		`"fail-line"`:                "the compact line's class",
	} {
		if !strings.Contains(panel, want) {
			t.Errorf("%s is missing %q", what, want)
		}
	}
}

// TestEveryLineIsBoundToItsChain pins S15's first half: the page never sends an automatic
// line. Every entry carries the store, id and name of the product the shopper added from
// that chain's own row, so no chain resolves a line it was not given.
func TestEveryLineIsBoundToItsChain(t *testing.T) {
	js, html := read(t, "app.js"), read(t, "index.html")

	items := fnBody(t, js, "function basketItems(")
	for what, want := range map[string]string{
		"the line id":  "line: line.key",
		"the chain":    "store: line.store",
		"the product":  "id: line.id",
		"the name":     "name: line.name",
		"the quantity": "quantity: line.quantity",
	} {
		if !strings.Contains(items, want) {
			t.Errorf("%s: basketItems is missing %q", what, want)
		}
	}
	// No automatic entry: no query, and no entry without a store.
	for _, gone := range []string{"query:", "pins"} {
		if strings.Contains(items, gone) {
			t.Errorf("basketItems still sends an automatic entry: %q", gone)
		}
	}

	// A product enters the basket only from a search row, which knows its chain; the free
	// text field that could not know one is gone, not hidden.
	if !strings.Contains(js, `addLine(pin.dataset.pin, pin.dataset.id, pin.dataset.name)`) {
		t.Error("the pin button does not add the product with its chain, id and name")
	}
	for _, gone := range []string{"add-form", "add-term", "data-add", "Añadir «"} {
		if strings.Contains(js, gone) || strings.Contains(html, gone) {
			t.Errorf("the page still offers a chain-less add field: %q", gone)
		}
	}
	// A wrong pin must be removable, or the basket cannot be corrected.
	if !strings.Contains(js, `data-drop="${esc(line.key)}"`) {
		t.Error("a basket line has no way out")
	}
	if !strings.Contains(js, `closest("[data-drop]")`) {
		t.Error("the remove control has no listener, so it is a dead button")
	}
}

// TestComparisonIsOneBasketPerChain pins S15's second half: no chain holds «the whole
// basket», so the comparison stops ranking full coverage and prints each chain's own
// basket — how many of the shopper's items it holds and what it costs.
func TestComparisonIsOneBasketPerChain(t *testing.T) {
	js, html := read(t, "app.js"), read(t, "index.html")

	row := fnBody(t, js, "function comparisonRow(")
	for what, want := range map[string]string{
		"the chain's mark": "logo(chain.store)",
		"its item count":   "countOf(lines.length)",
		"its own total":    "money(chain.totalCents)",
	} {
		if !strings.Contains(row, want) {
			t.Errorf("%s: comparisonRow is missing %q", what, want)
		}
	}
	for _, gone := range []string{"delta", "coverage", "ranking", "is-best"} {
		if strings.Contains(row, gone) {
			t.Errorf("a comparison row still ranks baskets against each other: %q", gone)
		}
	}

	// The copy that read as a cross-chain ranking is gone from both files.
	for _, gone := range []string{
		"la más barata", "cubre las ", "cesta completa", "coverage", "cobertura",
		"ranking", "is-best", "down-sub", "Sin total hoy",
	} {
		if strings.Contains(js, gone) || strings.Contains(html, gone) {
			t.Errorf("the page still claims a cheapest basket for a basket that spans chains: %q", gone)
		}
	}

	// One shape for the count: «2 artículos».
	for _, want := range []string{"function countOf(", `" artículo"`, `" artículos"`} {
		if !strings.Contains(js, want) {
			t.Errorf("the page cannot say how many items one chain's basket holds, missing %q", want)
		}
	}

	// A chain that holds none of the shopper's items contributes no row: six empty baskets
	// would be noise, and a line only ever belongs to its own chain (S15).
	if !strings.Contains(js, "owned.has(c.store)") {
		t.Error("the comparison lists chains that hold none of the shopper's items")
	}
}

// TestPinIsABasketIconButton pins the second half of S14: «elegir en <cadena>» is an
// icon-only button with an inline basket mark, named for a screen reader by both the
// product and the chain, with a mouse title and a 40 px hit area.
func TestPinIsABasketIconButton(t *testing.T) {
	js, css := read(t, "app.js"), read(t, "styles.css")
	row := fnBody(t, js, "function hitRow(")
	for what, want := range map[string]string{
		"icon button":     `class="btn btn-quiet btn-icon"`,
		"product in name": "h.name",
		"chain in name":   `a la cesta de " + name`,
		"accessible name": `aria-label="${esc(label)}"`,
		"mouse title":     `title="${esc(label)}"`,
		"the same action": `data-pin="${esc(store)}"`,
	} {
		if !strings.Contains(row, want) {
			t.Errorf("%s: hitRow is missing %q", what, want)
		}
	}

	// The button holds the mark and nothing else: an icon-only control whose only name is
	// its accessible one. The old text label is gone, not hidden.
	if !regexp.MustCompile(`<button[^>]*data-pin[^>]*>\$\{ICON\.basket\}</button>`).MatchString(row) {
		t.Error("the pin button carries a visible text label instead of only the basket icon")
	}
	if strings.Contains(js, ">Elegir en ") {
		t.Error("app.js still renders the old text label «Elegir en …»")
	}

	// One inline <svg> in currentColor: no file, no URL, no icon font, no raster.
	icon := fnBody(t, js, "const ICON = {")
	if !strings.Contains(icon, "basket: '<svg") {
		t.Error("the basket mark is not an inline <svg>")
	}
	if !strings.Contains(icon, "currentColor") {
		t.Error("the basket mark does not inherit the chain accent through currentColor")
	}
	for _, bad := range []string{"url(", "<img", "<use", "font-face"} {
		if strings.Contains(icon, bad) {
			t.Errorf("the icon references %q instead of drawing itself", bad)
		}
	}

	// An 18 px mark inside a 40 x 40 hit area, centred.
	at := strings.Index(css, ".btn-icon{")
	if at < 0 {
		t.Fatal("styles.css has no .btn-icon rule")
	}
	rule := css[at:]
	rule = rule[:strings.Index(rule, "}")]
	for _, want := range []string{"width:40px", "height:40px", "place-items:center"} {
		if !strings.Contains(rule, want) {
			t.Errorf("the icon button is missing %q", want)
		}
	}
}
