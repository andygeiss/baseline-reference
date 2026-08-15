package app

import (
	"net/http"
	"time"
)

// middleware is the one canonical chain, outermost → innermost. No session
// middleware: the app has no users (recorded deviation).
func (a *App) middleware(mux http.Handler) http.Handler {
	csrf := http.NewCrossOriginProtection()
	h := http.MaxBytesHandler(mux, 1<<20)
	h = csrf.Handler(h)
	return a.logRequests(a.recoverPanic(a.secureHeaders(h)))
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

func (a *App) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		a.logger.Info("request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.status,
			"duration", time.Since(start),
		)
	})
}

func (a *App) recoverPanic(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				a.logger.Error("panic", "path", r.URL.Path, "panic", rec)
				http.Error(w, "Sorry, something went wrong.", http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// csp is the whole policy, built once. A policy assembled per request is a
// policy that can differ per request, which is a bug.
//
// img-src allows data: so the mask icons in app.css can paint. A CSS image is
// an image request, and 'self' never matches data: — under default-src alone
// the browser refuses every mask. Such an image runs no script and reaches no
// third party, so allowing it gives an attacker nothing new.
//
// base-uri and form-action have no default-src fallback, so they are written
// out: the first stops an injected <base> from re-pointing every root-relative
// URL on the page, the second stops an injected form from posting off-origin.
const csp = "default-src 'self'; " +
	"img-src 'self' data:; " +
	"frame-ancestors 'none'; " +
	"base-uri 'none'; " +
	"form-action 'self'"

func (a *App) secureHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", csp)
		h.Set("Strict-Transport-Security", "max-age=31536000")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "same-origin")
		next.ServeHTTP(w, r)
	})
}
