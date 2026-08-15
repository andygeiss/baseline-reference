package app

import (
	"io/fs"
	"net/http"
	"strings"
)

// Routes registers every route and wraps the mux in the standard middleware
// chain. Every mutation is a POST: a plain form can only GET or POST, and every
// action here must work with htmx switched off.
func (a *App) Routes() http.Handler {
	mux := http.NewServeMux()

	// Public: the two ways in, and the way out.
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/rooms", http.StatusSeeOther)
	})
	mux.HandleFunc("GET /login", a.handleLoginForm)
	mux.HandleFunc("POST /login", a.handleLogin)
	mux.HandleFunc("GET /register", a.handleRegisterForm)
	mux.HandleFunc("POST /register", a.handleRegister)
	mux.HandleFunc("POST /logout", a.handleLogout)

	// Everything else needs a user. requireAuth takes either credential — a
	// browser session or a machine token — so the CLI reaches the same routes.
	private := func(h http.HandlerFunc) http.Handler { return a.requireAuth(h) }
	mux.Handle("GET /rooms", private(a.handleRoomList))
	mux.Handle("POST /rooms", private(a.handleRoomCreate))
	mux.Handle("GET /rooms/new", private(a.handleRoomNew))
	mux.Handle("GET /rooms/{slug}", private(a.handleRoomShow))
	mux.Handle("GET /rooms/{slug}/messages", private(a.handleMessagePoll))
	mux.Handle("POST /rooms/{slug}/messages", private(a.handleMessagePost))
	mux.Handle("GET /profile", private(a.handleProfile))
	mux.Handle("POST /profile/tokens", private(a.handleTokenCreate))
	mux.Handle("POST /profile/tokens/{id}/delete", private(a.handleTokenDelete))

	// The JSON surface, for programs. A separate surface rather than a second
	// representation of the pages: a command-line client cannot render HTML,
	// and an API has no forms, no redirects, and no flash messages to render.
	// It is guarded by its own middleware, because 303 to a sign-in page is not
	// an answer a program can act on.
	api := func(h http.HandlerFunc) http.Handler { return a.requireAPIAuth(h) }
	mux.Handle("GET /api/me", api(a.handleAPIMe))
	mux.Handle("GET /api/rooms", api(a.handleAPIRoomList))
	mux.Handle("POST /api/rooms", api(a.handleAPIRoomCreate))
	mux.Handle("GET /api/rooms/{slug}/messages", api(a.handleAPIMessageList))
	mux.Handle("POST /api/rooms/{slug}/messages", api(a.handleAPIMessageCreate))

	// Static sits outside the middleware chain: CSRF and the body cap have
	// nothing to check on a bodiless GET, and FileServerFS sets the right
	// Content-Type on its own. CSP and HSTS still cover the HTML documents
	// that reference the assets.
	root := http.NewServeMux()
	root.Handle("GET /static/", staticAssets(a.staticFS))
	root.Handle("/", a.middleware(mux))

	return root
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
