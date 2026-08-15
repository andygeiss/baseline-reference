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
	"path/filepath"
	"runtime/debug"
	"syscall"
	"time"

	"github.com/alexedwards/scs/v2"
	"golang.org/x/sync/errgroup"

	reference "github.com/andygeiss/baseline-reference/v3"
	"github.com/andygeiss/baseline-reference/v3/internal/app"
	"github.com/andygeiss/baseline-reference/v3/internal/auth"
	"github.com/andygeiss/baseline-reference/v3/internal/store"
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

	sessions := scs.New()
	sessions.Lifetime = 12 * time.Hour
	sessions.IdleTimeout = 2 * time.Hour
	// All three are set explicitly: scs defaults HttpOnly to true and SameSite
	// to Lax, but Secure to false. Do not "simplify" these away.
	sessions.Cookie.HttpOnly = true
	sessions.Cookie.SameSite = http.SameSiteLaxMode
	// Secure follows the deployment, not a constant. This binary only ever
	// speaks plain HTTP — TLS terminates in the proxy in front of it — so a
	// cookie marked Secure in dev is a cookie no client sends back, and the app
	// cannot be exercised over the HTTP it actually serves. ENV is set by the
	// deployment, which is the thing that knows whether TLS is in front.
	sessions.Cookie.Secure = cfg.Env == "prod"
	sessionStore := store.NewSessions(db)
	sessions.Store = sessionStore

	// Built once: it costs a real argon2id run, and the login path needs it on
	// every attempt against a name nobody has.
	dummyHash, err := auth.DummyHash()
	if err != nil {
		return fmt.Errorf("building the dummy password hash: %w", err)
	}

	a, err := app.New(app.Options{
		Logger:      logger,
		Users:       store.NewUsers(db),
		Rooms:       store.NewRooms(db),
		Messages:    store.NewMessages(db),
		Tokens:      store.NewTokens(db),
		Sessions:    sessions,
		TemplatesFS: templatesFS,
		StaticFS:    staticFS,
		Version:     ver,
		DummyHash:   dummyHash,
		InviteCode:  cfg.InviteCode,
	})
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
	g.Go(func() error { return sweepSessions(gctx, logger, sessionStore) })
	g.Go(func() error { return snapshot(gctx, logger, db, cfg.DBPath) })
	g.Go(func() error { <-gctx.Done(); logger.Info("shutting down"); return nil })
	logger.Info("started", "version", ver, "config", cfg)
	return g.Wait()
}

// sweepSessions deletes expired session rows every few minutes. This only
// reclaims disk: an expired session stops working the moment it expires,
// because the store's Find refuses to return it.
func sweepSessions(ctx context.Context, logger *slog.Logger, sessions *store.Sessions) error {
	return every(ctx, 5*time.Minute, func() {
		if err := sessions.DeleteExpired(ctx); err != nil {
			logger.Error("sweeping sessions", "err", err)
		}
	})
}

// snapshot answers the question patterns/go-sqlite.md makes every project
// answer: if the server disappears right now, what have you lost? This app's
// answer is "up to a day", so it writes a consistent copy beside the database
// once at boot and once a day after that.
//
// Getting that copy off the machine is a separate job with its own credentials,
// and it belongs to the deployment — the snapshot alone shares a disk with the
// thing it protects.
func snapshot(ctx context.Context, logger *slog.Logger, db *store.DB, dbPath string) error {
	write := func() {
		// The snapshot goes beside the database, built from that file's own
		// directory: every other path may be read-only, and VACUUM INTO
		// resolves a relative path against the process's working directory,
		// not the database's.
		dst := filepath.Join(filepath.Dir(dbPath), "snapshot-"+time.Now().UTC().Format("2006-01-02")+".db")
		if err := os.Remove(dst); err != nil && !errors.Is(err, os.ErrNotExist) {
			logger.Error("clearing yesterday's snapshot", "path", dst, "err", err)
			return
		}
		// The read pool, never the write pool: this statement only reads, so
		// running it on the single write connection would starve writes for its
		// whole duration.
		if _, err := db.Read.ExecContext(ctx, "VACUUM INTO ?", dst); err != nil {
			logger.Error("writing snapshot", "path", dst, "err", err)
			return
		}
		logger.Info("snapshot written", "path", dst)
	}
	write() // at boot, so a fresh deployment is never a day away from its first copy
	return every(ctx, 24*time.Hour, write)
}

// every runs do on a ticker until ctx is canceled — an owned lifecycle, so the
// goroutine stops when the process does.
func every(ctx context.Context, d time.Duration, do func()) error {
	ticker := time.NewTicker(d)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			do()
		}
	}
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

// version returns the build version the toolchain embedded — the tag when HEAD
// sits on one, a pseudo-version otherwise, "+dirty" when the tree was modified.
// One string for three jobs: the boot log line, the /healthz field, and the
// static-asset cache-buster (operations/web-application.md).
func version() string {
	info, ok := debug.ReadBuildInfo() // nil outside module mode — reading it would panic at boot
	if !ok {
		return "unknown"
	}
	if v := info.Main.Version; v != "" && v != "(devel)" {
		return v // go install @version, or VCS-derived since Go 1.24
	}
	return "unknown" // "(devel)" means no VCS metadata — vcs.* is absent too
}
