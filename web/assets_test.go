package web

import (
	"io/fs"
	"regexp"
	"strings"
	"testing"
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
		"dia zone claim":  {js, "Zona del servidor (28041) hasta que se fije"},
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
