// Package app is the HTTP edge: routing, middleware, handlers, rendering.
package app

import (
	"bytes"
	"context"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"

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
	}

	RoomStore interface {
		Add(ctx context.Context, r *domain.Room) error
		All(ctx context.Context) ([]domain.Room, error)
		BySlug(ctx context.Context, slug string) (domain.Room, error)
	}

	MessageStore interface {
		Add(ctx context.Context, m *domain.Message) error
		Recent(ctx context.Context, roomID string, limit int) ([]domain.Message, error)
		Since(ctx context.Context, roomID string, since int64) ([]domain.Message, error)
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
	Tokens      TokenStore
	Sessions    *scs.SessionManager
	TemplatesFS fs.FS
	StaticFS    fs.FS
	Version     string

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
	logger     *slog.Logger
	users      UserStore
	rooms      RoomStore
	messages   MessageStore
	tokens     TokenStore
	sessions   *scs.SessionManager
	assistant  Assistant
	templates  map[string]*template.Template
	staticFS   fs.FS
	dummyHash  string
	inviteCode string
	limiter    *limiter
}

// New parses the page templates once, failing the boot on any error. Version is
// the asset cache-buster: it reaches the pages as a template function, so no
// handler has to carry it in its view data.
//
// The page list comes from the directory rather than a list in this file. A
// hand-kept list is one a new page gets left out of, and the symptom is a 500
// from one route while every test of every other route stays green.
func New(o Options) (*App, error) {
	a := &App{
		logger:     o.Logger,
		users:      o.Users,
		rooms:      o.Rooms,
		messages:   o.Messages,
		tokens:     o.Tokens,
		sessions:   o.Sessions,
		assistant:  o.Assistant,
		templates:  make(map[string]*template.Template),
		staticFS:   o.StaticFS,
		dummyHash:  o.DummyHash,
		inviteCode: o.InviteCode,
		limiter:    newLimiter(),
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
