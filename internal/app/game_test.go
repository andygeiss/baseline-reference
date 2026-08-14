package app

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	reference "github.com/andygeiss/baseline-reference"
	"github.com/andygeiss/baseline-reference/internal/domain"
	"github.com/andygeiss/baseline-reference/internal/store"
)

type testApp struct {
	*App
	games *store.Games
	srv   *httptest.Server
}

func newTestApp(t *testing.T) *testApp {
	t.Helper()
	db, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("opening test db: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	templatesFS, err := fs.Sub(reference.TemplatesFS, "web/templates")
	if err != nil {
		t.Fatalf("templates fs: %v", err)
	}
	staticFS, err := fs.Sub(reference.StaticFS, "web")
	if err != nil {
		t.Fatalf("static fs: %v", err)
	}
	games := store.NewGames(db)
	a, err := New(slog.New(slog.DiscardHandler), games, templatesFS, staticFS, "test")
	if err != nil {
		t.Fatalf("building app: %v", err)
	}

	srv := httptest.NewServer(a.Routes())
	t.Cleanup(srv.Close)
	srv.Client().CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse // assert on redirects instead of following them
	}
	return &testApp{App: a, games: games, srv: srv}
}

// do issues a request against the full middleware chain and returns the
// response with its body read.
func (ta *testApp) do(t *testing.T, method, path string, form url.Values, headers map[string]string) (*http.Response, string) {
	t.Helper()
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequest(method, ta.srv.URL+path, body)
	if err != nil {
		t.Fatalf("building request: %v", err)
	}
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	res, err := ta.srv.Client().Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer res.Body.Close()
	b, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("reading body: %v", err)
	}
	return res, string(b)
}

// createGame seeds a game directly through the store.
func (ta *testApp) createGame(t *testing.T) *domain.Game {
	t.Helper()
	g := domain.NewGame("testgame")
	if err := ta.games.Create(t.Context(), g); err != nil {
		t.Fatalf("seeding game: %v", err)
	}
	return g
}

// play makes moves through the store, the way the handler does.
func (ta *testApp) play(t *testing.T, id string, cells ...int) {
	t.Helper()
	for _, cell := range cells {
		if _, err := ta.games.Update(t.Context(), id,
			func(g *domain.Game) error { return g.Move(cell) }); err != nil {
			t.Fatalf("seeding move %d: %v", cell, err)
		}
	}
}

var htmxHeaders = map[string]string{"HX-Request": "true"}

func TestHome(t *testing.T) {
	t.Parallel()
	res, body := newTestApp(t).do(t, "GET", "/", nil, nil)
	if res.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", res.StatusCode)
	}
	if !strings.Contains(body, "<html") || !strings.Contains(body, "Start a new game") {
		t.Error("home page missing document or new-game form")
	}
	// The security headers are asserted once, in TestSecureHeaders — the same
	// single-owner rule the baseline applies to the policy itself.
}

func TestGameCreate_RedirectsToGame(t *testing.T) {
	t.Parallel()
	res, _ := newTestApp(t).do(t, "POST", "/games", url.Values{}, nil)
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", res.StatusCode)
	}
	if loc := res.Header.Get("Location"); !strings.HasPrefix(loc, "/games/") {
		t.Errorf("Location = %q, want /games/<id>", loc)
	}
}

func TestGameShow(t *testing.T) {
	t.Parallel()
	ta := newTestApp(t)
	g := ta.createGame(t)

	res, body := ta.do(t, "GET", "/games/"+g.ID, nil, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	if !strings.Contains(body, `id="board"`) || !strings.Contains(body, "X's turn") {
		t.Error("game page missing board or turn status")
	}
	if !strings.Contains(body, `aria-label="Cell 1"`) || strings.Contains(body, `aria-label="Cell 0"`) {
		t.Error("cells are labelled 1–9 for the player, not 0–8")
	}

	res, _ = ta.do(t, "GET", "/games/missing", nil, nil)
	if res.StatusCode != http.StatusNotFound {
		t.Errorf("unknown game: status = %d, want 404", res.StatusCode)
	}
}

func TestMoveCreate_PlainFormRedirects(t *testing.T) {
	t.Parallel()
	ta := newTestApp(t)
	g := ta.createGame(t)

	res, _ := ta.do(t, "POST", "/games/"+g.ID+"/moves", url.Values{"cell": {"4"}}, nil)
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303 (POST-redirect-GET without htmx)", res.StatusCode)
	}
	got, err := ta.games.Get(t.Context(), g.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Board[4] != domain.PlayerX {
		t.Error("move was not persisted")
	}
}

func TestMoveCreate_HTMXReturnsFragment(t *testing.T) {
	t.Parallel()
	ta := newTestApp(t)
	g := ta.createGame(t)

	res, body := ta.do(t, "POST", "/games/"+g.ID+"/moves", url.Values{"cell": {"4"}}, htmxHeaders)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	if strings.Contains(body, "<html") {
		t.Error("htmx request got a full page, want fragment only")
	}
	if !strings.Contains(body, `id="board"`) || !strings.Contains(body, "O's turn") {
		t.Error("fragment missing swapped board or updated turn")
	}
	if vary := strings.Join(res.Header.Values("Vary"), ","); !strings.Contains(vary, "HX-Request") ||
		!strings.Contains(vary, "HX-Boosted") {
		t.Errorf("Vary = %q, want HX-Request and HX-Boosted", vary)
	}
}

func TestMoveCreate_BoostedGetsRedirect(t *testing.T) {
	t.Parallel()
	ta := newTestApp(t)
	g := ta.createGame(t)

	res, _ := ta.do(t, "POST", "/games/"+g.ID+"/moves", url.Values{"cell": {"0"}},
		map[string]string{"HX-Request": "true", "HX-Boosted": "true"})
	if res.StatusCode != http.StatusSeeOther {
		t.Errorf("boosted: status = %d, want 303 (needs full page, not fragment)", res.StatusCode)
	}
}

func TestMoveCreate_RuleViolations(t *testing.T) {
	t.Parallel()
	ta := newTestApp(t)
	g := ta.createGame(t)
	ta.play(t, g.ID, 4)

	res, body := ta.do(t, "POST", "/games/"+g.ID+"/moves", url.Values{"cell": {"4"}}, htmxHeaders)
	if res.StatusCode != http.StatusOK || !strings.Contains(body, "already taken") {
		t.Errorf("taken cell: status = %d, want 200 with message", res.StatusCode)
	}

	res, _ = ta.do(t, "POST", "/games/"+g.ID+"/moves", url.Values{"cell": {"nope"}}, htmxHeaders)
	if res.StatusCode != http.StatusBadRequest {
		t.Errorf("bad cell value: status = %d, want 400", res.StatusCode)
	}

	res, _ = ta.do(t, "POST", "/games/"+g.ID+"/moves", url.Values{"cell": {"11"}}, htmxHeaders)
	if res.StatusCode != http.StatusBadRequest {
		t.Errorf("out-of-range cell: status = %d, want 400", res.StatusCode)
	}
}

func TestMoveCreate_FinishedGameRejectsMoves(t *testing.T) {
	t.Parallel()
	ta := newTestApp(t)
	g := ta.createGame(t)
	ta.play(t, g.ID, 0, 3, 1, 4, 2) // X takes the top row

	res, body := ta.do(t, "POST", "/games/"+g.ID+"/moves", url.Values{"cell": {"8"}}, htmxHeaders)
	if res.StatusCode != http.StatusOK || !strings.Contains(body, "game is over") {
		t.Errorf("move after the win: status = %d, want 200 with message", res.StatusCode)
	}
	got, err := ta.games.Get(t.Context(), g.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Board[8] != domain.PlayerNone {
		t.Error("move after the win was persisted")
	}
}

// The status line lives outside the swapped board, in a live region the swap
// leaves in place — so the fragment updates it out of band.
func TestMoveCreate_FragmentUpdatesStatusOutOfBand(t *testing.T) {
	t.Parallel()
	ta := newTestApp(t)
	g := ta.createGame(t)

	_, body := ta.do(t, "POST", "/games/"+g.ID+"/moves", url.Values{"cell": {"4"}}, htmxHeaders)
	if !strings.Contains(body, `hx-swap-oob="innerHTML:#status"`) {
		t.Error("fragment does not update the status region out of band")
	}

	_, page := ta.do(t, "GET", "/games/"+g.ID, nil, nil)
	if !strings.Contains(page, `id="status"`) || !strings.Contains(page, `aria-live="polite"`) {
		t.Error("page is missing the live status region the fragment targets")
	}
	if strings.Contains(page, "hx-swap-oob") {
		t.Error("full page renders an out-of-band status: it would show twice")
	}
}

// failingStore fails every call, so the handlers' unexpected-error path runs.
// The message is a stand-in for the kind of internal detail a real driver error
// carries — it must never reach the browser.
type failingStore struct{}

const storeFailure = "table games is locked"

func (failingStore) Create(context.Context, *domain.Game) error { return errors.New(storeFailure) }

func (failingStore) Get(context.Context, string) (*domain.Game, error) {
	return nil, errors.New(storeFailure)
}

func (failingStore) Update(context.Context, string, func(*domain.Game) error) (*domain.Game, error) {
	return nil, errors.New(storeFailure)
}

func TestHandlers_StoreFailureIs500WithoutTheDetail(t *testing.T) {
	t.Parallel()
	ta := newTestApp(t)
	ta.App.games = failingStore{} // before the first request: no concurrent use

	tests := []struct {
		name, method, path string
		form               url.Values
	}{
		{"create", "POST", "/games", url.Values{}},
		{"show", "GET", "/games/testgame", nil},
		{"move", "POST", "/games/testgame/moves", url.Values{"cell": {"0"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res, body := ta.do(t, tt.method, tt.path, tt.form, nil)
			if res.StatusCode != http.StatusInternalServerError {
				t.Errorf("status = %d, want 500", res.StatusCode)
			}
			if strings.Contains(body, storeFailure) {
				t.Errorf("response leaks the internal error: %q", body)
			}
		})
	}
}

func TestStaticAssets(t *testing.T) {
	t.Parallel()
	ta := newTestApp(t)

	res, body := ta.do(t, "GET", "/static/css/app.css", nil, nil)
	if res.StatusCode != http.StatusOK || !strings.Contains(body, "@layer") {
		t.Fatalf("app.css: status = %d", res.StatusCode)
	}
	if cc := res.Header.Get("Cache-Control"); cc != "public, max-age=31536000, immutable" {
		t.Errorf("Cache-Control = %q", cc)
	}

	res, _ = ta.do(t, "GET", "/static/css/", nil, nil)
	if res.StatusCode != http.StatusNotFound {
		t.Errorf("directory listing: status = %d, want 404", res.StatusCode)
	}
}

func TestCSRF_CrossOriginPostRejected(t *testing.T) {
	t.Parallel()
	ta := newTestApp(t)
	g := ta.createGame(t)

	res, _ := ta.do(t, "POST", "/games/"+g.ID+"/moves", url.Values{"cell": {"4"}},
		map[string]string{"Sec-Fetch-Site": "cross-site"})
	if res.StatusCode != http.StatusForbidden {
		t.Errorf("cross-site POST: status = %d, want 403", res.StatusCode)
	}
}
