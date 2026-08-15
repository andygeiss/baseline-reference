// Command server wires configuration, dependencies, and the HTTP servers.
package main

import (
	"context"
	"errors"
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
	// Config is parsed and validated before anything opens a file or binds a
	// port: a bad value costs one line on stderr, not a half-started process.
	cfg, err := parseConfig(os.Args[1:], os.Stderr)
	switch {
	case err == nil:
	case errors.Is(err, flag.ErrHelp):
		return // -h: usage already printed, exit 0
	case errors.Is(err, errUsage):
		os.Exit(2) // the FlagSet already said what was wrong
	default:
		fmt.Fprintf(os.Stderr, "server: %v\n", err)
		os.Exit(2)
	}

	if err := run(cfg); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(cfg Config) error {
	opts := &slog.HandlerOptions{Level: cfg.LogLevel}
	var handler slog.Handler = slog.NewTextHandler(os.Stdout, opts)
	if cfg.Env == "prod" {
		handler = slog.NewJSONHandler(os.Stdout, opts)
	}
	logger := slog.New(handler)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	db, err := store.Open(ctx, cfg.DBPath)
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

	a, err := app.New(logger, store.NewTasks(db), templatesFS, staticFS, ver)
	if err != nil {
		return fmt.Errorf("building app: %w", err)
	}

	srv := &http.Server{
		Addr:              net.JoinHostPort(cfg.Host, cfg.Port),
		Handler:           a.Routes(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       2 * time.Minute,
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelError),
	}
	opsSrv := &http.Server{
		Addr:              net.JoinHostPort("127.0.0.1", cfg.OpsPort),
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
	logger.Info("started", "version", ver, "config", cfg)
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
