// Command server wires configuration, dependencies, and the HTTP servers.
package main

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/http/pprof"
	"os"
	"os/signal"
	"runtime/debug"
	"syscall"
	"time"

	reference "github.com/andygeiss/baseline-reference"
	"github.com/andygeiss/baseline-reference/internal/app"
	"github.com/andygeiss/baseline-reference/internal/store"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	var cfg struct {
		port, opsPort, dbPath, logLevel, env string
	}
	flag.StringVar(&cfg.port, "port", cmp.Or(os.Getenv("PORT"), "8080"), "app listen port (localhost)")
	flag.StringVar(&cfg.opsPort, "ops-port", cmp.Or(os.Getenv("OPS_PORT"), "6060"), "ops listen port (localhost)")
	flag.StringVar(&cfg.dbPath, "db", cmp.Or(os.Getenv("DATABASE_URL"), "app.db"), "SQLite file path")
	flag.StringVar(&cfg.logLevel, "log-level", cmp.Or(os.Getenv("LOG_LEVEL"), "info"), "debug|info|warn|error")
	flag.StringVar(&cfg.env, "env", cmp.Or(os.Getenv("ENV"), "dev"), "dev|prod")
	flag.Parse()

	var level slog.Level
	if err := level.UnmarshalText([]byte(cfg.logLevel)); err != nil {
		return fmt.Errorf("parsing log level: %w", err)
	}
	opts := &slog.HandlerOptions{Level: level}
	var handler slog.Handler = slog.NewTextHandler(os.Stdout, opts)
	if cfg.env == "prod" {
		handler = slog.NewJSONHandler(os.Stdout, opts)
	}
	logger := slog.New(handler)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	db, err := store.Open(ctx, cfg.dbPath)
	if err != nil {
		return fmt.Errorf("opening database: %w", err)
	}
	defer db.Close()

	templatesFS, err := fs.Sub(reference.TemplatesFS, "web/templates")
	if err != nil {
		return fmt.Errorf("templates fs: %w", err)
	}
	staticFS, err := fs.Sub(reference.StaticFS, "web")
	if err != nil {
		return fmt.Errorf("static fs: %w", err)
	}

	a, err := app.New(logger, store.NewGames(db), templatesFS, staticFS, version())
	if err != nil {
		return fmt.Errorf("building app: %w", err)
	}

	srv := &http.Server{
		Addr:              net.JoinHostPort("127.0.0.1", cfg.port),
		Handler:           a.Routes(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       2 * time.Minute,
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelError),
	}
	opsSrv := &http.Server{
		Addr:              net.JoinHostPort("127.0.0.1", cfg.opsPort),
		Handler:           opsHandler(db),
		ReadHeaderTimeout: 5 * time.Second,
	}

	serveErr := make(chan error, 2)
	go func() { serveErr <- srv.ListenAndServe() }()
	go func() { serveErr <- opsSrv.ListenAndServe() }()
	logger.Info("started", "addr", srv.Addr, "ops", opsSrv.Addr, "version", version(), "env", cfg.env)

	select {
	case <-ctx.Done():
		logger.Info("shutting down")
	case err := <-serveErr:
		if !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("serving: %w", err)
		}
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutting down app server: %w", err)
	}
	if err := opsSrv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutting down ops server: %w", err)
	}
	return nil
}

// opsHandler serves /healthz and pprof on the localhost-only ops listener.
func opsHandler(db *store.DB) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), time.Second)
		defer cancel()
		status, code := "ok", http.StatusOK
		if err := db.Read.PingContext(ctx); err != nil {
			status, code = "degraded: "+err.Error(), http.StatusServiceUnavailable
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		json.NewEncoder(w).Encode(map[string]string{"status": status, "version": version()})
	})
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	return mux
}

// version returns the VCS revision embedded by the toolchain.
func version() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "unknown"
	}
	var rev, modified string
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.modified":
			if s.Value == "true" {
				modified = "-dirty"
			}
		}
	}
	if rev == "" {
		return "unknown"
	}
	return rev[:min(12, len(rev))] + modified
}
