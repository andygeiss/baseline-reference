package app

import (
	"net/http"
	"strings"
	"testing"
)

// TestSecureHeaders pins the whole header contract, so an edit to the
// middleware cannot quietly drop one of them.
func TestSecureHeaders(t *testing.T) {
	t.Parallel()
	ta := newTestApp(t)

	res, _ := ta.do(t, http.MethodGet, "/", nil, nil)

	want := map[string]string{
		"Content-Security-Policy":   csp,
		"Strict-Transport-Security": "max-age=31536000",
		"X-Content-Type-Options":    "nosniff",
		"Referrer-Policy":           "same-origin",
	}
	for name, value := range want {
		if got := res.Header.Get(name); got != value {
			t.Errorf("%s = %q, want %q", name, got, value)
		}
	}

	// Unlike the loop above, these are not tautologies: comparing the header
	// against the csp constant proves only that it was sent. Each directive
	// below is one a future "tidy the policy" edit would drop without knowing
	// the cost — starting with the mask icons.
	for _, directive := range []string{
		"img-src 'self' data:",
		"frame-ancestors 'none'",
		"base-uri 'none'",
		"form-action 'self'",
	} {
		if !strings.Contains(csp, directive) {
			t.Errorf("CSP lost %q", directive)
		}
	}
}
