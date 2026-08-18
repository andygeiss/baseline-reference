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
	"time"

	"github.com/alexedwards/scs/v2"

	"github.com/andygeiss/baseline-reference/v3/internal/domain"
)

// The ports: what this app needs from persistence, in its own words. Each one
// is defined here, beside the feature that uses it, not beside the store that
// happens to satisfy it.
type (
	UserStore interface {
		Add(ctx context.Context, u *domain.User) error
		ByName(ctx context.Context, name string) (domain.User, error)
		ByID(ctx context.Context, id string) (domain.User, error)
		UpdatePasswordHash(ctx context.Context, id, hash string) error
		// SetPassword returns the moment it stamped, which is what a fresh
		// session records so older ones stop working.
		SetPassword(ctx context.Context, id, hash string) (string, error)
		SetEmail(ctx context.Context, id, email string) error
		Delete(ctx context.Context, id string) error
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
	ResetStore interface {
		Add(ctx context.Context, res *domain.Reset, mail domain.Mail) error
		Take(ctx context.Context, hash string, now time.Time) (domain.Reset, error)
	}

	TokenStore interface {
		Add(ctx context.Context, t *domain.Token) error
		UserByHash(ctx context.Context, hash string) (domain.User, error)
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
}

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
	}
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
func (a *App) newBase(r *http.Request, nav string) base {
	return base{
		User:  userFrom(r.Context()),
		Flash: a.sessions.PopString(r.Context(), "flash"),
		Nav:   nav,
	}
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
	if block != "" && isHTMX(r) {
		name = block
	}
	var buf bytes.Buffer
	if err := ts.ExecuteTemplate(&buf, name, data); err != nil {
		a.serverError(w, r, err)
		return
	}
	w.Header().Add("Vary", "HX-Request, HX-Boosted")
	w.WriteHeader(status)
	buf.WriteTo(w)
}

// isHTMX reports whether the request wants a fragment: an htmx request that is
// not a boosted navigation (boosted requests swap the whole body).
func isHTMX(r *http.Request) bool {
	return r.Header.Get("HX-Request") == "true" && r.Header.Get("HX-Boosted") != "true"
}

// redirect sends the browser to a page after an action. A fragment request
// cannot follow a 303 — the XHR would follow it invisibly and swap a whole page
// into the fragment's target — so it gets the header htmx navigates on.
func (a *App) redirect(w http.ResponseWriter, r *http.Request, to string) {
	if isHTMX(r) {
		w.Header().Add("Vary", "HX-Request, HX-Boosted") // this response bypasses render
		w.Header().Set("HX-Redirect", to)
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, to, http.StatusSeeOther)
}

func (a *App) serverError(w http.ResponseWriter, r *http.Request, err error) {
	a.logger.Error("server error", "method", r.Method, "path", r.URL.Path, "err", err)
	http.Error(w, "Sorry, something went wrong.", http.StatusInternalServerError)
}

func (a *App) clientError(w http.ResponseWriter, r *http.Request, status int) {
	a.logger.Debug("client error", "method", r.Method, "path", r.URL.Path, "status", status)
	http.Error(w, http.StatusText(status), status)
}
