package app

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSecureHeaders pins the four values a page carries, so an edit to the
// middleware cannot quietly drop one of them. It asks one page: a middleware
// that sent them on that request alone would pass it
// (patterns/security-headers.md, Testing).
func TestSecureHeaders(t *testing.T) {
	t.Parallel()
	ta := newTestApp(t)

	res, _ := ta.do(t, http.MethodGet, "/", nil, nil)

	// The policy is written out a second time here, not read from csp:
	// compared with the constant, the header passes whatever the constant
	// says. Whoever changes the policy changes it twice, and a tightening
	// pass that drops data: without knowing its cost, or a loosening one that
	// adds a host or widens default-src, fails here
	// (patterns/security-headers.md). script-src is the room map's.
	const policy = "default-src 'self'; script-src 'self' 'wasm-unsafe-eval'; img-src 'self' data:; " +
		"frame-ancestors 'none'; base-uri 'none'; form-action 'self'"
	want := map[string]string{
		"Content-Security-Policy":   policy,
		"Strict-Transport-Security": "max-age=31536000",
		"X-Content-Type-Options":    "nosniff",
		"Referrer-Policy":           "same-origin",
	}
	for name, value := range want {
		if got := res.Header.Get(name); got != value {
			t.Errorf("%s = %q, want %q", name, got, value)
		}
	}
	// One line of each, or a second policy could say something else.
	for name := range want {
		if n := len(res.Header.Values(name)); n != 1 {
			t.Errorf("%d %s headers, want 1", n, name)
		}
	}

	// The two keywords that undo the policy, matched with both quotes and
	// case-folded, since a browser reads 'UNSAFE-EVAL' as 'unsafe-eval'.
	// 'wasm-unsafe-eval' passes only because the pattern's leading quote
	// sits where its "wasm-" is.
	// Both copies: the constant, and the one above.
	for _, banned := range []string{"'unsafe-eval'", "'unsafe-inline'"} {
		if strings.Contains(strings.ToLower(csp+policy), banned) {
			t.Errorf("CSP carries %s", banned)
		}
	}
}

// TestEverySubmitButtonCarriesAnID: a swap removes the button that was pressed —
// the chat region replaces itself, and a boosted 422 replaces the body — and
// htmx puts focus back only on an element it finds by id. Without one focus
// falls to <body>, and the next Space or Enter does nothing
// (patterns/htmx-server-rendering.md).
func TestEverySubmitButtonCarriesAnID(t *testing.T) {
	t.Parallel()
	for name, page := range templateSources(t) {
		for n, line := range strings.Split(page, "\n") {
			if strings.Contains(line, "<button") && strings.Contains(line, `type="submit"`) && !strings.Contains(line, ` id="`) {
				t.Errorf("%s:%d: a submit button with no id: %s", name, n+1, strings.TrimSpace(line))
			}
		}
	}
}

// TestNoTemplateAsksHtmxToEvaluateAnything: hx-on, a js: value and an
// hx-trigger filter are all strings htmx evaluates as script, which the policy
// forbids — the page would break under it, quietly (stack/htmx.md).
func TestNoTemplateAsksHtmxToEvaluateAnything(t *testing.T) {
	t.Parallel()
	for name, page := range templateSources(t) {
		for n, line := range strings.Split(page, "\n") {
			_, trigger, hasTrigger := strings.Cut(line, `hx-trigger="`)
			trigger, _, _ = strings.Cut(trigger, `"`)
			switch {
			case strings.Contains(line, "hx-on"):
				t.Errorf("%s:%d: hx-on", name, n+1)
			case strings.Contains(line, `="js:`), strings.Contains(line, `"javascript:`):
				t.Errorf("%s:%d: a js: value", name, n+1)
			case hasTrigger && strings.Contains(trigger, "["):
				t.Errorf("%s:%d: an hx-trigger filter: %q", name, n+1, trigger)
			}
		}
	}
}

// templateSources reads every template as text, by file name.
func templateSources(t *testing.T) map[string]string {
	t.Helper()
	names, err := filepath.Glob("../../web/templates/*.html")
	if err != nil || len(names) == 0 {
		t.Fatalf("listing the templates: %v, %v", names, err)
	}
	pages := map[string]string{}
	for _, name := range names {
		b, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		pages[filepath.Base(name)] = string(b)
	}
	return pages
}
