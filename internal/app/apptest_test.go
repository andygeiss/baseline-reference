package app

import (
	"bytes"
	"io"
	"log/slog"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/textproto"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

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
	server      *httptest.Server
	client      *http.Client
	users       *fakeUsers
	rooms       *fakeRooms
	messages    *fakeMessages
	attachments *fakeAttachments
	resets      *fakeResets
	tokens      *fakeTokens
}

// testZone is a zone that is not UTC and not the machine's, so a handler that
// formatted a time without naming one would render the wrong string here.
var testZone = time.FixedZone("TST", 5*60*60)

// newTestApp wires the real app over the fakes and the real templates. Parsing
// the templates from disk is deliberate: a broken template is a bug these tests
// should catch, not something to stub past.
func newTestApp(t *testing.T, options ...func(*Options)) *testApp {
	t.Helper()

	o := newTestOptions(t, options...)
	a, err := New(o)
	if err != nil {
		t.Fatalf("building the app: %v", err)
	}

	// Registered before server.Close, so it runs after it: the listener stops,
	// then the work it started is joined. Without this a detached reply could
	// still be writing to a fake while the test binary tears the test down,
	// which -race reports and a sleeping test hides.
	t.Cleanup(a.Wait)

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
		users:       o.Users.(*fakeUsers),
		rooms:       o.Rooms.(*fakeRooms),
		messages:    o.Messages.(*fakeMessages),
		attachments: o.Attachments.(*fakeAttachments),
		resets:      o.Resets.(*fakeResets),
		tokens:      o.Tokens.(*fakeTokens),
	}
}

// newTestOptions is newTestApp without the listener, so a test that has to run
// inside a testing/synctest bubble can build the app without putting a
// goroutine blocked on a socket in there — network I/O never counts as durably
// blocked, and one such goroutine stops synctest.Wait from ever returning.
func newTestOptions(t *testing.T, options ...func(*Options)) Options {
	t.Helper()

	users := newFakeUsers()
	// Migration 0002 seeds this row in production, so the fake starts with it
	// too: the assistant posts as a user like anybody else, and the join that
	// reads a room has to resolve its name.
	users.byID[domain.AssistantID] = domain.User{ID: domain.AssistantID, Name: "Assistant"}

	files := newFakeAttachments()
	o := Options{
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		Users:       users,
		Rooms:       newFakeRooms(),
		Messages:    newFakeMessages(users, files),
		Attachments: files,
		Resets:      newFakeResets(),
		Tokens:      newFakeTokens(users),
		Sessions:    scs.New(), // the default in-memory store is right for a test
		TemplatesFS: os.DirFS("../../web/templates"),
		StaticFS:    os.DirFS("../../web/static"),
		Version:     "test",
		Location:    testZone,
		BaseURL:     &url.URL{Scheme: "https", Host: "chat.example.com"},
		DummyHash:   dummyHash(),
		Assistant:   newFakeAssistant(),
		// t.Context() is cancelled just before the cleanups below run, so a
		// detached reply still in flight at the end of a test is cancelled the
		// way a shutdown cancels one, and the Wait cleanup then joins it.
		Stopping: t.Context(),
	}
	for _, option := range options {
		option(&o)
	}
	return o
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

// upload is one attached file as a browser would send it: the name it was
// picked under, the type the browser claims, and the bytes. The three are
// separate fields on purpose — the tests that matter set them to disagree.
type upload struct {
	name    string
	claims  string
	content []byte
}

// postMessage sends the message form as multipart, the way a browser does when
// something is attached. file nil sends no file part at all.
func (ta *testApp) postMessage(t *testing.T, path, body string, file *upload, headers http.Header) (*http.Response, string) {
	t.Helper()

	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	if err := w.WriteField("body", body); err != nil {
		t.Fatalf("writing the body field: %v", err)
	}
	if file != nil {
		h := make(textproto.MIMEHeader)
		h.Set("Content-Disposition",
			mime.FormatMediaType("form-data", map[string]string{"name": "file", "filename": file.name}))
		h.Set("Content-Type", file.claims)
		part, err := w.CreatePart(h)
		if err != nil {
			t.Fatalf("creating the file part: %v", err)
		}
		if _, err := part.Write(file.content); err != nil {
			t.Fatalf("writing the file part: %v", err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("closing the multipart writer: %v", err)
	}

	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, ta.server.URL+path, &buf)
	if err != nil {
		t.Fatalf("building the request: %v", err)
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	for name, values := range headers {
		for _, v := range values {
			req.Header.Add(name, v)
		}
	}
	res, err := ta.client.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	defer res.Body.Close()
	b, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("reading the body of POST %s: %v", path, err)
	}
	return res, string(b)
}

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
