package app

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/andygeiss/tictactoe"
	"github.com/andygeiss/tictactoe/internal/domain"
	"github.com/andygeiss/tictactoe/internal/store"
	"io/fs"
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

	templatesFS, err := fs.Sub(tictactoe.TemplatesFS, "web/templates")
	if err != nil {
		t.Fatalf("templates fs: %v", err)
	}
	games := store.NewGames(db)
	a, err := New(slog.New(slog.DiscardHandler), games, templatesFS, fstest.MapFS{}, "test")
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
	if csp := res.Header.Get("Content-Security-Policy"); csp != "default-src 'self'" {
		t.Errorf("CSP = %q", csp)
	}
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
	if vary := res.Header.Values("Vary"); !strings.Contains(strings.Join(vary, ","), "HX-Request") {
		t.Errorf("Vary = %v, want HX-Request", vary)
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
	if err := g.Move(4); err != nil {
		t.Fatal(err)
	}
	if err := ta.games.Update(t.Context(), g); err != nil {
		t.Fatal(err)
	}

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
