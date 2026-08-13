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

	"github.com/andygeiss/baseline-reference/internal/domain"
)

// GameStore is what the app needs from persistence (consumer-defined). Update
// takes the change as a function so the store can load, apply, and save inside
// one transaction — the game rules stay in the domain either way.
type GameStore interface {
	Create(ctx context.Context, g *domain.Game) error
	Get(ctx context.Context, id string) (*domain.Game, error)
	Update(ctx context.Context, id string, change func(*domain.Game) error) (*domain.Game, error)
}

type App struct {
	logger    *slog.Logger
	games     GameStore
	templates map[string]*template.Template
	staticFS  fs.FS
	version   string
}

func New(logger *slog.Logger, games GameStore, templatesFS, staticFS fs.FS, version string) (*App, error) {
	a := &App{
		logger:    logger,
		games:     games,
		templates: make(map[string]*template.Template),
		staticFS:  staticFS,
		version:   version,
	}
	funcs := template.FuncMap{
		"version": func() string { return version },
		// Board indices are 0-based; the labels a player hears are not.
		"inc": func(i int) int { return i + 1 },
	}
	for _, page := range []string{"home.html", "game.html"} {
		ts, err := template.New(page).Funcs(funcs).ParseFS(templatesFS, "layout.html", page)
		if err != nil {
			return nil, fmt.Errorf("parsing template %s: %w", page, err)
		}
		a.templates[page] = ts
	}
	return a, nil
}

// render writes page as a full document, or only its named block when the request
// came from an htmx interaction. block == "" always renders the full page.
func (a *App) render(w http.ResponseWriter, r *http.Request, status int, page, block string, data any) {
	name := "layout.html" // full page: the layout shell that invokes the page's "main"
	if block != "" && isHTMX(r) {
		name = block
	}
	var buf bytes.Buffer
	if err := a.templates[page].ExecuteTemplate(&buf, name, data); err != nil {
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

func (a *App) serverError(w http.ResponseWriter, r *http.Request, err error) {
	a.logger.Error("server error", "method", r.Method, "path", r.URL.Path, "err", err)
	http.Error(w, "Sorry, something went wrong.", http.StatusInternalServerError)
}

func (a *App) clientError(w http.ResponseWriter, r *http.Request, status int) {
	a.logger.Debug("client error", "method", r.Method, "path", r.URL.Path, "status", status)
	http.Error(w, http.StatusText(status), status)
}
