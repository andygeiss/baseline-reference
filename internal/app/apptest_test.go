package app

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/alexedwards/scs/v2"

	"github.com/andygeiss/baseline-reference/v3/internal/auth"
	"github.com/andygeiss/baseline-reference/v3/internal/domain"
)

// dummyHash is built once for the whole package. Each one costs a real
// argon2id run at 19 MiB, and every test app needs one.
var dummyHash = sync.OnceValue(func() string {
	h, err := auth.DummyHash()
	if err != nil {
		panic(err) // a test binary that cannot hash cannot test anything
	}
	return h
})

type testApp struct {
	*App
	server   *httptest.Server
	client   *http.Client
	users    *fakeUsers
	rooms    *fakeRooms
	messages *fakeMessages
	tokens   *fakeTokens
}

// newTestApp wires the real app over the fakes and the real templates. Parsing
// the templates from disk is deliberate: a broken template is a bug these tests
// should catch, not something to stub past.
func newTestApp(t *testing.T, options ...func(*Options)) *testApp {
	t.Helper()

	users := newFakeUsers()
	// Migration 0002 seeds this row in production, so the fake starts with it
	// too: the assistant posts as a user like anybody else, and the join that
	// reads a room has to resolve its name.
	users.byID[domain.AssistantID] = domain.User{ID: domain.AssistantID, Name: "Assistant"}

	o := Options{
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		Users:       users,
		Rooms:       newFakeRooms(),
		Messages:    newFakeMessages(users),
		Tokens:      newFakeTokens(users),
		Sessions:    scs.New(), // the default in-memory store is right for a test
		TemplatesFS: os.DirFS("../../web/templates"),
		StaticFS:    os.DirFS("../../web/static"),
		Version:     "test",
		DummyHash:   dummyHash(),
		Assistant:   newFakeAssistant(),
	}
	for _, option := range options {
		option(&o)
	}

	a, err := New(o)
	if err != nil {
		t.Fatalf("building the app: %v", err)
	}

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("building the cookie jar: %v", err)
	}
	server := httptest.NewServer(a.Routes())
	t.Cleanup(server.Close)

	return &testApp{
		App:    a,
		server: server,
		client: &http.Client{
			Jar: jar,
			// Redirects are the thing under test in half of these cases, so
			// they are returned rather than followed.
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		users:    users,
		rooms:    o.Rooms.(*fakeRooms),
		messages: o.Messages.(*fakeMessages),
		tokens:   o.Tokens.(*fakeTokens),
	}
}

// withInviteCode gates registration on this app, the way a credential file does
// in production.
func withInviteCode(code string) func(*Options) {
	return func(o *Options) { o.InviteCode = code }
}

// do makes one request and reads the whole body. form nil means no body;
// anything non-nil is sent as a POST form.
func (ta *testApp) do(t *testing.T, method, path string, form url.Values, headers http.Header) (*http.Response, string) {
	t.Helper()

	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequestWithContext(t.Context(), method, ta.server.URL+path, body)
	if err != nil {
		t.Fatalf("building the request: %v", err)
	}
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	for name, values := range headers {
		for _, v := range values {
			req.Header.Add(name, v)
		}
	}

	res, err := ta.client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer res.Body.Close()
	b, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("reading the body of %s %s: %v", method, path, err)
	}
	return res, string(b)
}

// htmx is the header set an htmx fragment request carries.
func htmx() http.Header { return http.Header{"HX-Request": {"true"}} }

// signUp registers somebody and leaves them signed in, so a test that is about
// something else can start from there.
func (ta *testApp) signUp(t *testing.T, name, password string) {
	t.Helper()
	res, _ := ta.do(t, http.MethodPost, "/register", url.Values{
		"name": {name}, "password": {password},
	}, nil)
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("registering %s: status %d, want 303", name, res.StatusCode)
	}
}

// makeRoom creates a room and returns its slug.
func (ta *testApp) makeRoom(t *testing.T, name string) string {
	t.Helper()
	res, _ := ta.do(t, http.MethodPost, "/rooms", url.Values{"name": {name}}, nil)
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("creating room %s: status %d, want 303", name, res.StatusCode)
	}
	return strings.TrimPrefix(res.Header.Get("Location"), "/rooms/")
}

// emptyJar returns a cookie jar with nothing in it, so a test can drop the
// browser session and prove a request stands on another credential alone.
func emptyJar(t *testing.T) http.CookieJar {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("building the cookie jar: %v", err)
	}
	return jar
}

// vary joins every Vary line on a response. Header.Get would return only the
// first, and this app sends two: scs adds "Cookie", the render helper adds the
// htmx pair. Repeated field lines mean the same thing as one comma-joined list.
func vary(res *http.Response) string {
	return strings.Join(res.Header.Values("Vary"), ", ")
}
