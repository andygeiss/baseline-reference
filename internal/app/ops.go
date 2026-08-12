package app

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"net/http/pprof"
	"time"
)

// OpsHandler serves /healthz and /debug/pprof on the localhost ops listener.
// The health ping uses the read pool: a ping queued behind the write pool's
// single connection times out during any long write — a healthy app flapping 503.
func OpsHandler(readDB *sql.DB, version string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), time.Second)
		defer cancel()
		status, state := http.StatusOK, "ok"
		if err := readDB.PingContext(ctx); err != nil {
			status, state = http.StatusServiceUnavailable, "unavailable"
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		fmt.Fprintf(w, `{"status":%q,"version":%q}`, state, version)
	})
	// Registered explicitly — the blank net/http/pprof import registers on
	// http.DefaultServeMux, which this app never serves. The patterns break the
	// app mux's routing rules on purpose: method-less because Symbol answers
	// both GET and POST, a subtree match because that is how Index dispatches
	// the named runtime profiles (heap, goroutine, …).
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	return mux
}
