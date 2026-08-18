package app

import (
	"fmt"
	"io/fs"
	"net/http"
	"strings"
)

// access says who may reach a route. The zero value is deliberately not a
// class: a row that names none panics at boot instead of quietly becoming
// public.
type access int

const (
	_        access = iota
	public          // no credential needed
	pageAuth        // a session or a token; a browser without one gets the sign-in page
	apiAuth         // a session or a token; a program without one gets 401 and JSON
)

// route is one row of the table below. The access class sits between the
// pattern and the handler because every row is a positional literal: a route
// that names no class does not compile, so a private route cannot become public
// by somebody forgetting a wrapper.
type route struct {
	pattern string
	access  access
	handler http.HandlerFunc
}

// routes is every route this app answers. Registration walks this table, so a
// route that is not in it does not exist.
//
// Every mutation is a POST: a plain form can only GET or POST, and every action
// here must work with htmx switched off.
func (a *App) routes() []route {
	return []route{
		// Public: the two ways in, and the way out.
		{"GET /{$}", public, func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "/rooms", http.StatusSeeOther)
		}},
		{"GET /login", public, a.handleLoginForm},
		{"POST /login", public, a.handleLogin},
		{"GET /register", public, a.handleRegisterForm},
		{"POST /register", public, a.handleRegister},
		{"POST /logout", public, a.handleLogout},

		// Getting back in. Public because somebody who cannot sign in is the
		// only person who ever needs these, and every one of them answers the
		// same whether the account exists or not.
		{"GET /reset", public, a.handleResetForm},
		{"POST /reset", public, a.handleReset},
		{"GET /reset/confirm", public, a.handleResetConfirmForm},
		{"POST /reset/confirm", public, a.handleResetConfirm},

		// The pages. pageAuth takes either credential — a browser session or a
		// machine token — so the CLI reaches the same routes.
		{"GET /rooms", pageAuth, a.handleRoomList},
		{"POST /rooms", pageAuth, a.handleRoomCreate},
		{"GET /rooms/new", pageAuth, a.handleRoomNew},
		{"GET /rooms/{slug}", pageAuth, a.handleRoomShow},
		{"GET /rooms/{slug}/messages", pageAuth, a.handleMessagePoll},
		{"POST /rooms/{slug}/messages", pageAuth, a.handleMessagePost},
		{"GET /rooms/{slug}/older", pageAuth, a.handleMessageOlder},
		// Attachments are served by a handler, never by a file server: a file
		// server names no reader and types a file by its extension, which is
		// whatever the sender called it (patterns/go-file-uploads.md).
		{"GET /rooms/{slug}/files/{id}", pageAuth, a.handleFileShow},
		{"POST /rooms/{slug}/files/{id}/delete", pageAuth, a.handleFileDelete},
		{"GET /profile", pageAuth, a.handleProfile},
		{"POST /profile/email", pageAuth, a.handleEmailSet},
		{"POST /profile/tokens", pageAuth, a.handleTokenCreate},
		{"POST /profile/tokens/{id}/delete", pageAuth, a.handleTokenDelete},

		// Deleting the account is two requests on purpose: the page asks for
		// the name and the password, and the POST checks both. An hx-confirm
		// would be a dialog htmx draws, and htmx is not what this may depend
		// on (patterns/go-data-deletion.md).
		{"GET /account/delete", pageAuth, a.handleAccountDeleteForm},
		{"POST /account/delete", pageAuth, a.handleAccountDelete},

		// The JSON surface, for programs. A separate surface rather than a
		// second representation of the pages: a command-line client cannot
		// render HTML, and an API has no forms, no redirects, and no flash
		// messages to render. It gets its own class because 303 to a sign-in
		// page is not an answer a program can act on.
		{"GET /api/me", apiAuth, a.handleAPIMe},
		{"GET /api/rooms", apiAuth, a.handleAPIRoomList},
		{"POST /api/rooms", apiAuth, a.handleAPIRoomCreate},
		{"GET /api/rooms/{slug}/messages", apiAuth, a.handleAPIMessageList},
		{"POST /api/rooms/{slug}/messages", apiAuth, a.handleAPIMessageCreate},
	}
}

// Routes registers every route in the table and wraps the mux in the standard
// middleware chain.
func (a *App) Routes() http.Handler {
	mux := http.NewServeMux()
	for _, rt := range a.routes() {
		mux.Handle(rt.pattern, a.guard(rt.access, rt.handler))
	}

	// Static sits outside the middleware chain: CSRF and the body cap have
	// nothing to check on a bodiless GET, and FileServerFS sets the right
	// Content-Type on its own. CSP and HSTS still cover the HTML documents
	// that reference the assets.
	root := http.NewServeMux()
	root.Handle("GET /static/", staticAssets(a.staticFS))
	root.Handle("/", a.middleware(mux))

	return root
}

// guard wraps a handler in the middleware its access class calls for. An
// unknown class panics, and it panics at boot rather than on a request: it is a
// programming error, and answering it with "public" is the one wrong answer
// available.
func (a *App) guard(c access, h http.HandlerFunc) http.Handler {
	switch c {
	case public:
		return h
	case pageAuth:
		return a.requireAuth(h)
	case apiAuth:
		return a.requireAPIAuth(h)
	default:
		panic(fmt.Sprintf("route access class %d is not one of public, pageAuth, apiAuth", c))
	}
}

// staticAssets serves the embedded files as immutable: they cannot change
// within a deployed binary, and every URL carries ?v=<version> to bust the
// cache on the next deploy. Directory URLs are 404: the file server would
// otherwise answer them with a browsable index of the embedded tree — a page
// nobody links to, cached for a year.
func staticAssets(fsys fs.FS) http.Handler {
	files := http.FileServerFS(fsys)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		files.ServeHTTP(w, r)
	})
}
