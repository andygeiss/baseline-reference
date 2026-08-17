package app

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// TestEveryRouteNamesAnAccessClass guards the zero value. guard panics on it at
// boot, and this names the row that did it instead of leaving a stack trace.
func TestEveryRouteNamesAnAccessClass(t *testing.T) {
	t.Parallel()
	for _, rt := range newTestApp(t).routes() {
		switch rt.access {
		case public, pageAuth, apiAuth:
		default:
			t.Errorf("route %q names no access class", rt.pattern)
		}
	}
}

// TestOnlyTheWayInAndOutIsPublic pins the public surface. Making a route public
// is a real decision, so it fails here until somebody writes it down.
func TestOnlyTheWayInAndOutIsPublic(t *testing.T) {
	t.Parallel()
	want := map[string]bool{
		"GET /{$}":       true,
		"GET /login":     true,
		"POST /login":    true,
		"GET /register":  true,
		"POST /register": true,
		"POST /logout":   true,
	}

	found := 0
	for _, rt := range newTestApp(t).routes() {
		switch {
		case rt.access == public && !want[rt.pattern]:
			t.Errorf("%q is public and this list does not say it should be", rt.pattern)
		case rt.access != public && want[rt.pattern]:
			t.Errorf("%q is in the public list but its class is not public", rt.pattern)
		case rt.access == public:
			found++
		}
	}
	if found != len(want) {
		t.Errorf("found %d public routes, want %d — one in the list is gone", found, len(want))
	}
}

// TestPrivateRoutesTurnAwayAnAnonymousRequest walks the route table rather than a
// hand-kept list of paths, so a route added without a guard fails here instead of
// serving.
func TestPrivateRoutesTurnAwayAnAnonymousRequest(t *testing.T) {
	t.Parallel()
	ta := newTestApp(t) // nobody is signed in

	for _, rt := range ta.routes() {
		if rt.access == public {
			continue
		}
		method, path := concrete(rt.pattern)
		t.Run(method+" "+path, func(t *testing.T) {
			var form url.Values
			if method == http.MethodPost {
				form = url.Values{} // non-nil, so it goes as a form POST
			}
			res, _ := ta.do(t, method, path, form, nil)

			// A browser is sent to the sign-in page; a program is told 401,
			// because 200 of HTML reads as success to anything checking only
			// the status.
			want := http.StatusSeeOther
			if rt.access == apiAuth {
				want = http.StatusUnauthorized
			}
			if res.StatusCode != want {
				t.Errorf("anonymous %s %s = %d, want %d", method, path, res.StatusCode, want)
			}
		})
	}
}

// concrete turns a route pattern into a request line: "GET /rooms/{slug}" becomes
// GET /rooms/x. The wildcard values do not matter — no handler behind a private
// route runs in these tests.
func concrete(pattern string) (method, path string) {
	method, path, _ = strings.Cut(pattern, " ")
	if path == "/{$}" {
		return method, "/"
	}
	for {
		open := strings.Index(path, "{")
		if open < 0 {
			return method, path
		}
		end := strings.Index(path[open:], "}")
		path = path[:open] + "x" + path[open+end+1:]
	}
}
