// Package app is the HTTP edge: routing, middleware, handlers, rendering.
package app

import (
	"bytes"
	"context"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/alexedwards/scs/v2"

	"github.com/andygeiss/baseline-reference/v6/internal/domain"
)

// The ports: what this app needs from persistence, in its own words. Each one
// is defined here, beside the feature that uses it, not beside the store that
// happens to satisfy it.
type (
	UserStore interface {
		Add(ctx context.Context, u *domain.User) error
		ByName(ctx context.Context, name string) (domain.User, error)
		ByID(ctx context.Context, id string) (domain.User, error)
		// Rehash and Delete are statements a password allowed, so each names
		// the password counter its request loaded, version, and answers
		// domain.ErrStale when the row has moved on since: a refusal
		// (patterns/go-auth-sessions.md rule 1). Rehash writes a new hash of
		// the same password and moves nothing.
		Rehash(ctx context.Context, id, hash string, version int64) error
		SetEmail(ctx context.Context, id, email string) error
		Delete(ctx context.Context, id string, version int64) error
	}

	RoomStore interface {
		Add(ctx context.Context, r *domain.Room) error
		All(ctx context.Context) ([]domain.Room, error)
		BySlug(ctx context.Context, slug string) (domain.Room, error)
	}

	MessageStore interface {
		// blob holds the attachment's bytes when m.Attachment is set. Both are
		// written with the message or not at all.
		Add(ctx context.Context, m *domain.Message, blob []byte) error
		// Page walks backwards from a cursor; before == 0 starts at the newest.
		Page(ctx context.Context, roomID string, before int64, limit int) ([]domain.Message, error)
		Since(ctx context.Context, roomID string, since int64) ([]domain.Message, error)
	}

	// AttachmentStore is the file surface. Open takes no actor because an
	// attachment is readable by everyone who can read its room, and in this app
	// that is everyone signed in; Delete takes one because deleting is not
	// shared. The README says which rows are which.
	AttachmentStore interface {
		Open(ctx context.Context, id string) (domain.Attachment, io.ReadSeekCloser, error)
		Delete(ctx context.Context, uploaderID, id string) error
	}

	// ResetStore holds outstanding password-reset links. Add takes the mail
	// with them: the link and the message that carries it are one write.
	// Redeem is one write too: the link is spent, the hash set and the
	// counter moved, and the user's other links deleted together, and it
	// returns the user as the row then stands.
	ResetStore interface {
		Add(ctx context.Context, res *domain.Reset, mail domain.Mail) error
		Redeem(ctx context.Context, hash, passwordHash string, now time.Time) (domain.User, error)
	}

	TokenStore interface {
		// Add is a statement a password allowed too: version is the owner's
		// password counter as the request loaded it, and domain.ErrStale the
		// answer when the row has moved on.
		Add(ctx context.Context, t *domain.Token, version int64) error
		UserByHash(ctx context.Context, hash string) (domain.User, error)
		// Owner is UserByHash without marking the token used: the open
		// socket's recheck, which only reads (wasm-client.md rule 6).
		Owner(ctx context.Context, hash string) (domain.User, error)
		ByUser(ctx context.Context, userID string) ([]domain.Token, error)
		Delete(ctx context.Context, userID, id string) error
	}
)

// Options is everything main hands the app. A struct rather than nine
// positional arguments: the compiler stops naming them in the wrong order.
type Options struct {
	Logger      *slog.Logger
	Users       UserStore
	Rooms       RoomStore
	Messages    MessageStore
	Attachments AttachmentStore
	Resets      ResetStore
	Tokens      TokenStore
	Sessions    *scs.SessionManager
	TemplatesFS fs.FS
	StaticFS    fs.FS
	Version     string

	// Location is the one zone this app renders every time in. Every reader
	// sees the same clock, and every rendered time carries its abbreviation, so
	// a reader elsewhere can tell what they are looking at. Loaded and
	// validated at boot — see cmd/server/config.go.
	Location *time.Location

	// BaseURL is where this app answers from, and it is the only thing a link
	// in an outgoing email is ever built from. Never r.Host: that header is
	// whatever the client sent, and a reset link built from it mails a working
	// token to whoever asked for it (patterns/go-email.md).
	BaseURL *url.URL

	// DummyHash is verified against when the name is unknown, so a login takes
	// the same time either way.
	DummyHash string

	// InviteCode gates registration. Empty means anybody may register, which is
	// how the app runs with no credential file — see cmd/server/config.go.
	InviteCode string

	// Assistant answers when somebody mentions it. Never nil: the degenerate
	// mode in internal/echo is the default, so the whole loop runs with an
	// empty environment — see cmd/server/config.go.
	Assistant Assistant

	// Stopping is done when the process is going down, and it is what ends the
	// work a request starts and does not wait for. It is the errgroup's
	// context, not the signal context: errgroup cancels it as Wait returns,
	// whichever goroutine ended the process, so nothing is left running with
	// no way to be told to stop (patterns/go-background-work.md).
	Stopping context.Context
}

type App struct {
	logger      *slog.Logger
	users       UserStore
	rooms       RoomStore
	messages    MessageStore
	attachments AttachmentStore
	resets      ResetStore
	tokens      TokenStore
	sessions    *scs.SessionManager
	assistant   Assistant
	templates   map[string]*template.Template
	staticFS    fs.FS
	location    *time.Location
	baseURL     *url.URL
	dummyHash   string
	inviteCode  string
	limiter     *limiter
	presence    *hub

	// stopping ends detached work; running counts it so Wait can block on it.
	stopping context.Context
	running  sync.WaitGroup
}

// Wait blocks until every reply this app detached has been written or given up.
//
// main calls it after g.Wait(), because srv.Shutdown does not: those goroutines
// are not in-flight requests, so from the server's side the shutdown is already
// clean while the work is still going (patterns/go-background-work.md).
func (a *App) Wait() { a.running.Wait() }

// New parses the page templates once, failing the boot on any error. Version is
// the asset cache-buster: it reaches the pages as a template function, so no
// handler has to carry it in its view data.
//
// The page list comes from the directory rather than a list in this file. A
// hand-kept list is one a new page gets left out of, and the symptom is a 500
// from one route while every test of every other route stays green.
func New(o Options) (*App, error) {
	if o.Location == nil {
		return nil, fmt.Errorf("no time zone: every rendered time names one, so there is no default here")
	}
	if o.BaseURL == nil {
		return nil, fmt.Errorf("no base URL: an emailed link is built from it, never from the request")
	}
	if o.Stopping == nil {
		return nil, fmt.Errorf("no stopping context: detached work would have nothing to end it")
	}
	a := &App{
		logger:      o.Logger,
		users:       o.Users,
		rooms:       o.Rooms,
		messages:    o.Messages,
		attachments: o.Attachments,
		resets:      o.Resets,
		tokens:      o.Tokens,
		sessions:    o.Sessions,
		assistant:   o.Assistant,
		templates:   make(map[string]*template.Template),
		staticFS:    o.StaticFS,
		location:    o.Location,
		baseURL:     o.BaseURL,
		dummyHash:   o.DummyHash,
		inviteCode:  o.InviteCode,
		limiter:     newLimiter(),
		presence:    newHub(),
		stopping:    o.Stopping,
	}
	// scs's default writes a store error to the standard logger, unstructured,
	// and answers with a page of its own. serverError is the one place this app
	// logs and words a 500 (patterns/go-auth-sessions.md).
	a.sessions.ErrorFunc = a.serverError
	funcs := template.FuncMap{
		"version": func() string { return o.Version },
	}
	pages, err := fs.Glob(o.TemplatesFS, "*.html")
	if err != nil {
		return nil, fmt.Errorf("listing templates: %w", err)
	}
	for _, page := range pages {
		if page == "layout.html" {
			continue // the shell, parsed into every page below
		}
		ts, err := template.New(page).Funcs(funcs).ParseFS(o.TemplatesFS, "layout.html", page)
		if err != nil {
			return nil, fmt.Errorf("parsing template %s: %w", page, err)
		}
		a.templates[page] = ts
	}
	if len(a.templates) == 0 {
		return nil, fmt.Errorf("no page templates found: is the embedded tree right?")
	}
	return a, nil
}

// base is the part of every page's data that the layout reads.
type base struct {
	User  domain.User
	Flash string
	Nav   string // which bottom-nav entry is current: rooms, room, or profile
}

// newBase reads the signed-in user and takes the flash message off the session
// — a flash is shown once, so reading it removes it.
//
// Not for a fragment: a fragment has no layout to show the flash in, so one
// that popped it — a poll landing between a redirect and the page it leads to —
// would spend the message where nobody sees it. It is left for the page
// (patterns/go-forms-validation.md, Flash messages).
func (a *App) newBase(r *http.Request, nav string) base {
	b := base{User: userFrom(r.Context()), Nav: nav}
	if !isFragment(r) {
		b.Flash = a.sessions.PopString(r.Context(), "flash")
	}
	return b
}

// flash leaves a message for the page the browser lands on next. It is how a
// plain form reports what happened: the answer to its POST is a redirect, and a
// redirect carries no words of its own.
func (a *App) flash(r *http.Request, message string) {
	a.sessions.Put(r.Context(), "flash", message)
}

// render writes page as a full document, or only its named block when the
// request came from an htmx interaction. block == "" always renders the full
// page.
func (a *App) render(w http.ResponseWriter, r *http.Request, status int, page, block string, data any) {
	ts, ok := a.templates[page]
	if !ok {
		// A handler named a page that does not exist. Saying so beats the nil
		// map entry's panic, which arrives as a bare 500 with no clue in it.
		a.serverError(w, r, fmt.Errorf("no template parsed for page %q", page))
		return
	}
	name := "layout.html" // full page: the layout shell that invokes the page's "main"
	if block != "" && isFragment(r) {
		name = block
	}
	var buf bytes.Buffer
	if err := ts.ExecuteTemplate(&buf, name, data); err != nil {
		a.serverError(w, r, err)
		return
	}
	// Set here, or a fragment rooted at <li> is sniffed as text/plain.
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Add("Vary", "HX-Request-Type")
	w.WriteHeader(status)
	buf.WriteTo(w)
}

// isFragment reports whether the request wants a fragment. htmx says partial
// when it will swap the answer into an element, and full when the answer
// replaces the body: a boosted link or form. A plain browser says neither.
func isFragment(r *http.Request) bool {
	return r.Header.Get("HX-Request-Type") == "partial"
}

// redirect sends the browser to a page after an action. A fragment request
// cannot follow a 303 — fetch would follow it invisibly and swap a whole page
// into the fragment's target — so it gets the header htmx navigates on.
func (a *App) redirect(w http.ResponseWriter, r *http.Request, to string) {
	if isFragment(r) {
		w.Header().Add("Vary", "HX-Request-Type") // this response bypasses render
		w.Header().Set("HX-Redirect", to)
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, to, http.StatusSeeOther)
}

// serverError logs the cause at error level and answers with the 500 page,
// which says nothing about it (patterns/go-errors-logging.md rule 4).
//
// It makes no exception for a request whose client has gone. Answered with
// nothing, such a request is logged as a 200, and an outage that makes
// clients give up reads as traffic.
func (a *App) serverError(w http.ResponseWriter, r *http.Request, err error) {
	a.logger.Error("server error", "method", r.Method, "path", r.URL.Path, "err", err)
	a.write500(w)
}

// errorPage is the 500 page's data. Its base is the zero value, never
// newBase(r): this page is also written where no session was loaded — a panic
// caught outside the session middleware, a session store that failed — and
// scs panics when asked for a flash there. A zero base is a page with nobody
// signed in and nothing to say but the apology, which is all a 500 may say
// (patterns/go-forms-validation.md, Flash messages rule 2).
type errorPage struct {
	base
}

// write500 writes the 500 page. It reads nothing from the request, and if the
// page itself cannot be rendered it falls back to one line of text rather than
// to serverError, which would call it again.
func (a *App) write500(w http.ResponseWriter) {
	var buf bytes.Buffer
	ts, ok := a.templates["error.html"]
	if !ok || ts.ExecuteTemplate(&buf, "layout.html", errorPage{}) != nil {
		http.Error(w, "Sorry, something went wrong.", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusInternalServerError)
	buf.WriteTo(w)
}

func (a *App) clientError(w http.ResponseWriter, r *http.Request, status int) {
	a.logger.Debug("client error", "method", r.Method, "path", r.URL.Path, "status", status)
	http.Error(w, http.StatusText(status), status)
}
