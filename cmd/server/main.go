// Command server wires configuration, dependencies, and the HTTP servers.
package main

import (
	"cmp"
	"context"
	"flag"
	"fmt"
	"io/fs"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"os"
	"os/signal"
	"runtime/debug"
	"syscall"
	"time"

	"golang.org/x/sync/errgroup"

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
		host, port, opsPort, dbPath, logLevel, env string
	}
	flag.StringVar(&cfg.host, "host", cmp.Or(os.Getenv("HOST"), "127.0.0.1"), "bind address (the proxy is the public listener)")
	flag.StringVar(&cfg.port, "port", cmp.Or(os.Getenv("PORT"), "8080"), "app listen port")
	flag.StringVar(&cfg.opsPort, "ops-port", cmp.Or(os.Getenv("OPS_PORT"), "6060"), "ops listen port (localhost)")
	flag.StringVar(&cfg.dbPath, "db", cmp.Or(os.Getenv("DATABASE_URL"), "app.db"), "SQLite file path")
	flag.StringVar(&cfg.logLevel, "log-level", cmp.Or(os.Getenv("LOG_LEVEL"), "info"), "debug|info|warn|error")
	flag.Parse()
	cfg.env = cmp.Or(os.Getenv("ENV"), "dev") // environment only, per the ops env contract

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

	// Go's built-in mime table lacks .webmanifest, and the system mime files it
	// merges on Unix vary by host — without this line, so does the served type.
	// The error is impossible for these valid literals.
	_ = mime.AddExtensionType(".webmanifest", "application/manifest+json")

	ver := version() // read once at boot; the ops handler and the asset cache-buster share it

	a, err := app.New(logger, store.NewGames(db), templatesFS, staticFS, ver)
	if err != nil {
		return fmt.Errorf("building app: %w", err)
	}

	srv := &http.Server{
		Addr:              net.JoinHostPort(cfg.host, cfg.port),
		Handler:           a.Routes(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       2 * time.Minute,
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelError),
	}
	opsSrv := &http.Server{
		Addr:              net.JoinHostPort("127.0.0.1", cfg.opsPort),
		Handler:           app.OpsHandler(db.Read, ver),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       2 * time.Minute,
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelError),
		// WriteTimeout does not cap long profiles: profile?seconds=30 writes
		// nothing until profiling ends, but net/http/pprof extends its own write
		// deadline to WriteTimeout + seconds on every seconds-based handler.
	}

	// Both listeners run under the signal context via errgroup — every goroutine
	// has an owned lifecycle; a failing listener shuts the other down gracefully.
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error { return serve(gctx, srv) })
	g.Go(func() error { return serve(gctx, opsSrv) })
	g.Go(func() error { <-gctx.Done(); logger.Info("shutting down"); return nil })
	logger.Info("started", "addr", srv.Addr, "ops", opsSrv.Addr, "version", ver, "env", cfg.env)
	return g.Wait()
}

// serve runs srv until ctx is canceled, then shuts it down gracefully so
// in-flight requests finish.
func serve(ctx context.Context, srv *http.Server) error {
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	case err := <-errc:
		return fmt.Errorf("serving %s: %w", srv.Addr, err)
	}
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
