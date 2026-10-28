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
// font, no remote script, no network URL of any kind. Every src/href must resolve to one
// of the three embedded files.
func TestNoExternalReferences(t *testing.T) {
	files := []string{"index.html", "styles.css", "app.js"}
	forbidden := []string{"http://", "https://", "//cdn", "@import", "@font-face", "fonts.googleapis", "url("}
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
		"state as text":   {js, "Sin respuesta"},
		"retry offer":     {js, "no disponible ahora"},
		"postal no claim": {html, "no filtra precios"},
	} {
		if !strings.Contains(check[0], check[1]) {
			t.Errorf("%s: missing %q", what, check[1])
		}
	}
}
