package app

import "net/http"

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
	root.Handle("GET /static/", cacheForever(http.FileServerFS(a.staticFS)))
	root.Handle("/", a.middleware(mux))

	return root
}

// cacheForever marks embedded assets immutable; URLs carry ?v=<version> to bust.
func cacheForever(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		next.ServeHTTP(w, r)
	})
}
