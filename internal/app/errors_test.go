package app

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/andygeiss/baseline-reference/v5/internal/ws"
)

// debugLog is a logger that keeps every level, so a test can tell an error
// line from a debug one.
func debugLog(buf *bytes.Buffer) *slog.Logger {
	return slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

// TestServerErrorAndARequestWhoseClientLeft: a client that goes away cancels
// its request's context, and every store call under it then fails. That is no
// fault of the server's, and logged as one it is an error line for every tab
// somebody closed (patterns/go-errors-logging.md rule 4).
func TestServerErrorAndARequestWhoseClientLeft(t *testing.T) {
	t.Parallel()

	// The store fails the way a real one does under a cancelled context.
	newApp := func(t *testing.T, log *bytes.Buffer) *testApp {
		ta := newTestApp(t, func(o *Options) { o.Logger = debugLog(log) })
		ta.tokens.goDown(context.Canceled)
		return ta
	}
	request := func(ctx context.Context) *http.Request {
		r := httptest.NewRequestWithContext(ctx, http.MethodGet, "/rooms", nil)
		r.Header.Set("Authorization", "Bearer gochat_anything")
		return r
	}

	t.Run("the client left: a debug line, and nothing is written", func(t *testing.T) {
		t.Parallel()
		var log bytes.Buffer
		ta := newApp(t, &log)
		gone, cancel := context.WithCancel(t.Context())
		cancel()

		rec := httptest.NewRecorder()
		ta.Routes().ServeHTTP(rec, request(gone))

		if strings.Contains(log.String(), "level=ERROR") {
			t.Errorf("an abandoned request was logged as an error:\n%s", log.String())
		}
		if !strings.Contains(log.String(), `level=DEBUG msg="client left"`) {
			t.Errorf("no debug line says the client left:\n%s", log.String())
		}
		if rec.Body.Len() != 0 || rec.Header().Get("Content-Type") != "" {
			t.Errorf("something was written to a client that left: %q, type %q",
				rec.Body.String(), rec.Header().Get("Content-Type"))
		}
	})

	// The exception is the request's own context, not the error's kind: the
	// same error under a request that is still there is a fault.
	t.Run("the client is still there: an error line and the 500", func(t *testing.T) {
		t.Parallel()
		var log bytes.Buffer
		ta := newApp(t, &log)

		rec := httptest.NewRecorder()
		ta.Routes().ServeHTTP(rec, request(t.Context()))

		if rec.Code != http.StatusInternalServerError {
			t.Errorf("status %d, want 500", rec.Code)
		}
		if !strings.Contains(log.String(), `level=ERROR msg="server error"`) {
			t.Errorf("a store failure under a live request was not logged as an error:\n%s", log.String())
		}
	})

	t.Run("the JSON surface makes the same exception", func(t *testing.T) {
		t.Parallel()
		var log bytes.Buffer
		ta := newApp(t, &log)
		gone, cancel := context.WithCancel(t.Context())
		cancel()
		r := httptest.NewRequestWithContext(gone, http.MethodGet, "/api/me", nil)
		r.Header.Set("Authorization", "Bearer gochat_anything")

		rec := httptest.NewRecorder()
		ta.Routes().ServeHTTP(rec, r)

		if strings.Contains(log.String(), "level=ERROR") || rec.Body.Len() != 0 {
			t.Errorf("an abandoned API request was answered %q and logged:\n%s", rec.Body.String(), log.String())
		}
	})
}

// TestThe500PageNeedsNoSession: serverError also runs where no session was
// loaded — a session-store error, a panic — and a page whose data popped the
// flash would panic there, inside the handler that is answering the failure.
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
