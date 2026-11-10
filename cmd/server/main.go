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
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/alexedwards/scs/v2"
	"golang.org/x/sync/errgroup"

	reference "github.com/andygeiss/baseline-reference/v5"
	"github.com/andygeiss/baseline-reference/v5/internal/anthropic"
	"github.com/andygeiss/baseline-reference/v5/internal/app"
	"github.com/andygeiss/baseline-reference/v5/internal/auth"
	"github.com/andygeiss/baseline-reference/v5/internal/echo"
	"github.com/andygeiss/baseline-reference/v5/internal/logmail"
	"github.com/andygeiss/baseline-reference/v5/internal/smtpmail"
	"github.com/andygeiss/baseline-reference/v5/internal/store"
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
	// Only the token's SHA-256 reaches the store, like every other credential
	// this app keeps: a copy of the database, or of a snapshot, then holds no
	// session anybody can use.
	sessions.HashTokenInStore = true
	sessionStore := store.NewSessions(db, sessions.Lifetime)
	sessions.Store = sessionStore

	// Built once: it costs a real argon2id run, and the login path needs it on
	// every attempt against a name nobody has.
	dummyHash, err := auth.DummyHash()
	if err != nil {
		return fmt.Errorf("building the dummy password hash: %w", err)
	}

	// The only place that names both sides. Constructing an adapter sends
	// nothing: boot validates what is local — flags, files, this database — and
	// stops there, so a model being down is a broken mention rather than a
	// process that will not start (patterns/go-http-client.md).
	var assistant app.Assistant = echo.New()
	if cfg.Assistant == "anthropic" {
		assistant = anthropic.New(string(cfg.AnthropicKey)) // the conversion is the only way past Secret
	}
	// The same shape for mail: the adapter that needs nothing is the default,
	// so the whole password-reset flow can be walked with an empty environment.
	// Constructing either one connects to nothing.
	var mailer Mailer = logmail.New(logger)
	if cfg.Mailer == "smtp" {
		mailer = smtpmail.New(cfg.SMTPAddr, cfg.SMTPFrom, cfg.SMTPUser, string(cfg.SMTPPassword), cfg.BaseURL.Hostname())
	}

	resets := store.NewResets(db)
	outbox := store.NewOutbox(db)

	// Built before the app because the app holds gctx: it is what ends the work
	// a request starts and does not wait for. errgroup cancels it as Wait
	// returns, whichever goroutine below ended the process, so that work is
	// always already stopping by the time this function waits for it.
	g, gctx := errgroup.WithContext(ctx)

	a, err := app.New(app.Options{
		Logger:      logger,
		Users:       store.NewUsers(db),
		Rooms:       store.NewRooms(db),
		Messages:    store.NewMessages(db),
		Attachments: store.NewAttachments(db),
		Resets:      resets,
		Tokens:      store.NewTokens(db),
		Sessions:    sessions,
		Assistant:   assistant,
		TemplatesFS: templatesFS,
		StaticFS:    staticFS,
		Version:     ver,
		Location:    cfg.Location,
		BaseURL:     cfg.BaseURL,
		DummyHash:   dummyHash,
		InviteCode:  string(cfg.InviteCode),
		Stopping:    gctx,
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
	g.Go(func() error { return serve(gctx, srv) })
	g.Go(func() error { return serve(gctx, opsSrv) })
	g.Go(func() error { return sweepSessions(gctx, logger, sessionStore) })
	g.Go(func() error { return sweepResets(gctx, logger, resets) })
	g.Go(func() error { return sendMail(gctx, logger, outbox, mailer) })
	g.Go(func() error { return snapshot(gctx, logger, db, cfg.DBPath) })
	g.Go(func() error { <-gctx.Done(); logger.Info("shutting down"); return nil })
	logger.Info("started", "version", ver, "config", cfg)

	err = g.Wait() // the listeners are down, and gctx with them
	// Now the work they started. srv.Shutdown does not wait for it — those
	// goroutines are not in-flight requests — so without this line the process
	// exits mid-reply and nothing logs a fault. gctx is already cancelled here,
	// so a wedged reply is cancelled rather than waited out
	// (patterns/go-background-work.md).
	a.Wait()
	return err // the deferred db.Close() runs after this line, not before
}

// sweepSessions deletes expired session rows every few minutes. This only
// reclaims disk: an expired session stops working the moment it expires,
// because the store's Find refuses to return it.
func sweepSessions(ctx context.Context, logger *slog.Logger, sessions *store.Sessions) error {
	return every(ctx, 5*time.Minute, func() {
		switch err := sessions.DeleteExpired(ctx); {
		case errors.Is(err, context.Canceled):
			// Shutting down mid-sweep is not a fault. Logging it as one puts an
			// ERROR line in every orderly stop that lands on this.
		case err != nil:
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
		dir := filepath.Dir(dbPath)
		dst := filepath.Join(dir, "snapshot-"+time.Now().UTC().Format(time.DateOnly)+".db")
		// One fixed temporary name, never dst itself: VACUUM INTO refuses a
		// file that exists, which is every start after the day's first, and a
		// stop mid-copy would leave half a database under the name a restore
		// reads. Fixed, so that a copy a kill left behind is cleared by the
		// next run whatever day that is.
		tmp := filepath.Join(dir, "snapshot.tmp")
		if !filepath.IsAbs(tmp) {
			tmp = "./" + tmp // a leading "file:" would make it a URI (go-sqlite.md rule 10)
		}
		if err := os.Remove(tmp); err != nil && !errors.Is(err, fs.ErrNotExist) {
			logger.Error("clearing an unfinished snapshot", "path", tmp, "err", err)
			return
		}
		// The read pool, never the write pool: this statement only reads, so
		// running it on the single write connection would starve writes for its
		// whole duration.
		if _, err := db.Read.ExecContext(ctx, "VACUUM INTO ?", tmp); err != nil {
			if !errors.Is(err, context.Canceled) { // shutdown mid-snapshot is not a fault
				logger.Error("writing snapshot", "path", tmp, "err", err)
			}
			return
		}
		if err := os.Rename(tmp, dst); err != nil {
			logger.Error("naming the snapshot", "path", dst, "err", err)
			return
		}
		logger.Info("snapshot written", "path", dst)
		// Only after a copy that succeeded: a failing VACUUM must not eat the
		// older files it was meant to add to.
		if err := pruneSnapshots(filepath.Dir(dst), keptSnapshots); err != nil {
			logger.Error("pruning old snapshots", "err", err)
		}
	}
	// every runs write at boot, so a fresh deployment is never a day away from
	// its first copy.
	return every(ctx, 24*time.Hour, write)
}

// keptSnapshots is how many snapshot files stay beside the database. Each one
// holds everyone deleted since it was written, so this number is how long a
// deleted account can outlive its delete on this machine, and the README names
// it (patterns/go-data-deletion.md).
const keptSnapshots = 3

// pruneSnapshots removes every snapshot in dir but the newest keep.
//
// It deletes files, so it deletes only what snapshot wrote: a regular file
// named snapshot-<date>.db whose date parses. The dates sort as text, and
// os.ReadDir returns names sorted, so the oldest come first.
func pruneSnapshots(dir string, keep int) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("listing %s: %w", dir, err)
	}
	var ours []string
	for _, e := range entries {
		day, ok := strings.CutPrefix(e.Name(), "snapshot-")
		if !ok {
			continue
		}
		if day, ok = strings.CutSuffix(day, ".db"); !ok || !e.Type().IsRegular() {
			continue
		}
		if _, err := time.Parse(time.DateOnly, day); err != nil {
			continue
		}
		ours = append(ours, e.Name())
	}
	var errs []error
	for len(ours) > keep {
		if err := os.Remove(filepath.Join(dir, ours[0])); err != nil {
			errs = append(errs, err)
		}
		ours = ours[1:]
	}
	return errors.Join(errs...)
}

// every runs do now, then on a ticker until ctx is canceled — an owned
// lifecycle, so the goroutine stops when the process does.
//
// The call before the loop is the load-bearing part: time.NewTicker does not
// fire at zero, so a process restarted more often than d would otherwise never
// run do at all (patterns/go-http-server.md). That is every binary under
// development, and every service that deploys more often than it cleans up.
func every(ctx context.Context, d time.Duration, do func()) error {
	do()
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
//
// It returns when Shutdown has, never when ListenAndServe does: that returns the
// moment Shutdown is called, with requests still in flight
// (patterns/go-http-server.md).
func serve(ctx context.Context, srv *http.Server) error {
	drained := make(chan error, 1)
	stop := context.AfterFunc(ctx, func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		drained <- srv.Shutdown(shutdownCtx)
	})
	if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		stop()
		return fmt.Errorf("serving %s: %w", srv.Addr, err)
	}
	return <-drained
}

// version names this build for three jobs at once: the boot log line, the
// /healthz field, and the cache-buster on every immutable static asset. That
// last job sets the rule — two builds with different assets MUST NOT share a
// string — so this is the three-case reader from patterns/go-performance.md,
// not the shorter one cmd/gochat uses for -version. `unknown` would be a
// correct answer there and a stale stylesheet here.
var version = sync.OnceValue(func() string {
	info, ok := debug.ReadBuildInfo() // nil outside module mode — reading it would panic at boot
	if !ok {
		return bootID()
	}
	return resolveVersion(info)
})

// resolveVersion picks the name for a build. It takes the build info rather
// than reading it, because the three cases it decides between cannot all be
// produced by the toolchain running the test.
func resolveVersion(info *debug.BuildInfo) string {
	// A real released version: go install of a tagged module. A tag built from
	// an edited tree is not one — "v0.3.0+dirty" is the same string after every
	// further edit, the exact thing this function exists to avoid — so it falls
	// through to the boot-by-boot name below.
	if v := info.Main.Version; v != "" && v != "(devel)" && !strings.HasSuffix(v, "+dirty") {
		return v
	}

	var revision string
	var edited bool
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			revision = s.Value
		case "vcs.modified":
			edited = s.Value == "true"
		}
	}
	// A clean checkout names its assets by the commit they came from, so two
	// deployments of the same commit do not make anyone download them twice.
	if revision != "" && !edited {
		return revision[:min(len(revision), 12)]
	}
	return bootID()
}

// bootID names one run of an edited tree, which is the finest grain there is:
// the toolchain cannot tell two edits apart, but it can tell two starts apart.
func bootID() string {
	return strconv.FormatInt(time.Now().UnixMilli(), 36)
}
