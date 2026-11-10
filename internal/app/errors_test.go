package app

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/andygeiss/baseline-reference/v6/internal/ws"
)

// debugLog is a logger that keeps every level, so a test can tell an error
// line from a debug one.
func debugLog(buf *bytes.Buffer) *slog.Logger {
	return slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

// TestServerErrorMakesNoExceptionForAClientWhoLeft: a client that goes away
// cancels its request's context, and a store call under it then fails with
// that cancellation. serverError logs it at error level and answers 500 all
// the same (patterns/go-errors-logging.md rule 4). Excused — a debug line and
// nothing written — the request is a 200 in the request log, and so is every
// request of an outage slow enough that its clients stop waiting.
//
// So every case here ends the same way, whatever failed, whoever is still
// there, on a page and on the JSON surface.
func TestServerErrorMakesNoExceptionForAClientWhoLeft(t *testing.T) {
	t.Parallel()

	left := func(t *testing.T) context.Context {
		gone, cancel := context.WithCancel(t.Context())
		cancel()
		return gone
	}
	failures := map[string]error{
		// What a real store returns under a cancelled context.
		"the cancellation": context.Canceled,
		"a missing table":  errors.New("SQL logic error: no such table: tokens (1)"),
	}
	clients := map[string]func(*testing.T) context.Context{
		"the client left":           left,
		"the client is still there": func(t *testing.T) context.Context { return t.Context() },
	}
	surfaces := map[string]struct{ path, contentType string }{
		"a page":           {"/rooms", "text/html; charset=utf-8"},
		"the JSON surface": {"/api/me", "application/json; charset=utf-8"},
	}

	for failed, failure := range failures {
		for client, ctx := range clients {
			for surface, at := range surfaces {
				t.Run(failed+", "+client+", on "+surface, func(t *testing.T) {
					t.Parallel()
					var log bytes.Buffer
					ta := newTestApp(t, func(o *Options) { o.Logger = debugLog(&log) })
					ta.tokens.goDown(failure) // the lookup every bearer request makes

					r := httptest.NewRequestWithContext(ctx(t), http.MethodGet, at.path, nil)
					r.Header.Set("Authorization", "Bearer gochat_anything")
					rec := httptest.NewRecorder()
					ta.Routes().ServeHTTP(rec, r)

					if rec.Code != http.StatusInternalServerError {
						t.Errorf("status %d, want 500", rec.Code)
					}
					if got := rec.Header().Get("Content-Type"); got != at.contentType {
						t.Errorf("Content-Type = %q, want the 500 of this surface, %q", got, at.contentType)
					}
					if strings.Contains(rec.Body.String(), failure.Error()) {
						t.Errorf("the internal error text reached the answer:\n%s", rec.Body.String())
					}
					if !strings.Contains(log.String(), `level=ERROR msg="server error"`) ||
						!strings.Contains(log.String(), failure.Error()) {
						t.Errorf("the failure was not logged as an error, with its cause:\n%s", log.String())
					}
					// The request log is where an excused failure hid: a handler
					// that writes nothing is recorded as a 200.
					if !strings.Contains(log.String(), "msg=request method=GET path="+at.path+" status=500") {
						t.Errorf("the request log does not record a 500:\n%s", log.String())
					}
				})
			}
		}
	}
}

// TestThe500PageNeedsNoSession: the 500 page is also rendered where no session
// was loaded — on a session-store error, after a panic — and a page whose data
// popped the flash would panic there, inside the code that is answering the
// failure.
// The 500 page embeds a zero base (patterns/go-forms-validation.md, Flash
// messages rule 2).
func TestThe500PageNeedsNoSession(t *testing.T) {
	t.Parallel()
	isThe500Page := func(t *testing.T, status int, header http.Header, body string) {
		t.Helper()
		if status != http.StatusInternalServerError {
			t.Errorf("status %d, want 500", status)
		}
		if got := header.Get("Content-Type"); got != "text/html; charset=utf-8" {
			t.Errorf("Content-Type = %q, want a page", got)
		}
		for _, want := range []string{"<title>Something went wrong · Go Chat</title>", "Sorry, something went wrong."} {
			if !strings.Contains(body, want) {
				t.Errorf("the 500 page lacks %q:\n%s", want, body)
			}
		}
		if strings.Contains(body, "the session table is unreachable") || strings.Contains(body, "boom") {
			t.Errorf("the internal error text reached the browser:\n%s", body)
		}
	}

	t.Run("a session-store error", func(t *testing.T) {
		t.Parallel()
		ta := newTestApp(t, func(o *Options) { o.Sessions.Store = brokenStore{} })
		res, body := ta.do(t, http.MethodGet, "/login", nil, http.Header{"Cookie": {"session=anything"}})
		isThe500Page(t, res.StatusCode, res.Header, body)
	})

	t.Run("a panic, outside the session middleware", func(t *testing.T) {
		t.Parallel()
		var log bytes.Buffer
		ta := newTestApp(t, func(o *Options) { o.Logger = debugLog(&log) })
		h := ta.recoverPanic(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("boom") }))

		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/rooms", nil))

		isThe500Page(t, rec.Code, rec.Header(), rec.Body.String())
		if !strings.Contains(log.String(), "level=ERROR") || !strings.Contains(log.String(), "boom") {
			t.Errorf("the panic was not logged:\n%s", log.String())
		}
	})
}

// TestAFragmentLeavesTheFlashForThePage: a fragment has no layout to show a
// flash in. One that popped it anyway — a poll that lands between the redirect
// and the page — would spend the message where nobody sees it
// (patterns/go-forms-validation.md, Flash messages).
func TestAFragmentLeavesTheFlashForThePage(t *testing.T) {
	t.Parallel()
	ta := newTestApp(t)
	ta.signUp(t, "Ada", "correct-horse")
	if _, body := ta.do(t, http.MethodGet, "/rooms", nil, nil); !strings.Contains(body, "Welcome, Ada.") {
		t.Fatal("the welcome flash never showed") // and is spent now
	}
	slug := ta.makeRoom(t, "General") // puts "Room created." and redirects

	// Before the browser has fetched the page it was sent to, a fragment
	// request arrives on the same session.
	res, fragment := ta.do(t, http.MethodPost, "/rooms/"+slug+"/messages", url.Values{"body": {"hello"}}, htmx())
	if res.StatusCode != http.StatusOK || strings.Contains(fragment, "<html") {
		t.Fatalf("the htmx post answered %d and not a fragment", res.StatusCode)
	}
	if strings.Contains(fragment, "Room created.") {
		t.Error("the fragment rendered the flash")
	}

	_, page := ta.do(t, http.MethodGet, "/rooms/"+slug, nil, nil)
	if !strings.Contains(page, `<p class="flash" role="alert">Room created.</p>`) {
		t.Errorf("the page after the fragment has no flash: a fragment spent it")
	}
	if _, again := ta.do(t, http.MethodGet, "/rooms/"+slug, nil, nil); strings.Contains(again, "Room created.") {
		t.Error("the flash showed twice")
	}
}

// lockedBuffer is a log sink goroutines may write to while a test reads it.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// TestTheSocketsLogLinesNameNobody: a log is a copy no account deletion
// reaches, so it names people by id (patterns/go-errors-logging.md, Never log).
func TestTheSocketsLogLinesNameNobody(t *testing.T) {
	t.Parallel()
	var log lockedBuffer
	ta := newTestApp(t, func(o *Options) {
		o.Logger = slog.New(slog.NewTextHandler(&log, &slog.HandlerOptions{Level: slog.LevelDebug}))
	})
	ta.signUp(t, "Ada Lovelace", "correct-horse")
	slug := ta.makeRoom(t, "General")

	c, err := openSocket(t, ta, ta.client, slug, "")
	if err != nil {
		t.Fatalf("opening the socket: %v", err)
	}
	c.Close(ws.CloseNormal, "")
	ta.Wait() // the socket's goroutine logs its close before it ends

	got := log.String()
	for _, line := range []string{`msg="socket opened"`, `msg="socket closed"`} {
		if !strings.Contains(got, line) {
			t.Fatalf("no %s line in the log:\n%s", line, got)
		}
	}
	if strings.Contains(got, "Ada") || strings.Contains(got, "Lovelace") {
		t.Errorf("a log line names the person:\n%s", got)
	}
	if id := ta.onlyUserID(t); !strings.Contains(got, "user="+id) {
		t.Errorf("the socket's lines do not carry the user's id %s:\n%s", id, got)
	}
}

// TestASignedInPageIsNeverStored: a page behind the sign-in is not kept by the
// browser, or Back after signing out shows it again with no request made
// (patterns/go-performance.md, HTML responses). The sign-in page itself may be.
func TestASignedInPageIsNeverStored(t *testing.T) {
	t.Parallel()
	ta := newTestApp(t)
	ta.signUp(t, "Ada", "correct-horse")
	slug := ta.makeRoom(t, "General")
	ta.postMessage(t, "/rooms/"+slug+"/messages", "hello", nil, nil)

	for name, get := range map[string]struct {
		path    string
		headers http.Header
	}{
		"a page":             {"/rooms", nil},
		"a boosted page":     {"/rooms/" + slug, boosted()},
		"a fragment":         {"/rooms/" + slug + "/messages?since=0", htmx()},
		"the profile":        {"/profile", nil},
		"a page that is 404": {"/rooms/no-such-room", nil},
	} {
		res, _ := ta.do(t, http.MethodGet, get.path, nil, get.headers)
		if cc := res.Header.Get("Cache-Control"); cc != "no-store" {
			t.Errorf("%s: Cache-Control = %q, want no-store", name, cc)
		}
	}
}
