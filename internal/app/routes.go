package app

import (
	"io/fs"
	"net/http"
	"strings"
)

// Routes registers every route and wraps the mux in the standard middleware chain.
func (a *App) Routes() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /{$}", a.handleHome)
	mux.HandleFunc("POST /games", a.handleGameCreate)
	mux.HandleFunc("GET /games/{id}", a.handleGameShow)
	mux.HandleFunc("POST /games/{id}/moves", a.handleMoveCreate)

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
