package chatapi_test

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/andygeiss/baseline-reference/v6/internal/chatapi"
	"github.com/andygeiss/baseline-reference/v6/internal/domain"
)

// serverSaying stands in for a Go Chat server, answering every request the same
// way. httptest, never the real API: the adapter under test is the translation
// from a status into an error, and that has to be checked one status at a time.
func serverSaying(t *testing.T, status int, body string) *chatapi.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return chatapi.New(srv.URL, "gochat_token")
}

// TestStatusTranslation is the adapter's whole job on the failure side: turn
// somebody else's HTTP into errors this program already knows how to branch on.
//
// What the error carries of the answer is its status code and nothing the
// server wrote (patterns/go-http-client.md, statusError). A body is where an API
// quotes the request back — an address, a name, a query — and an error ends up
// on stderr or in a log, where no account deletion reaches it
// (patterns/go-errors-logging.md, Never log).
func TestStatusTranslation(t *testing.T) {
	t.Parallel()

	const quoted = `{"error":"no room general for ada@example.com with token gochat_s3cr3t"}`
	tests := []struct {
		name   string
		status int
		want   error // nil: nobody's domain error
	}{
		{"unauthorized", http.StatusUnauthorized, domain.ErrUnauthorized},
		{"no such room", http.StatusNotFound, domain.ErrNotFound},
		{"refused input", http.StatusUnprocessableEntity, domain.ErrRejected},
		{"a status this client has no word for", http.StatusTeapot, nil},
		{"the server failing", http.StatusInternalServerError, nil},
		{"the server busy", http.StatusTooManyRequests, nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			client := serverSaying(t, tt.status, quoted)

			_, _, err := client.Messages(t.Context(), "general", 42)

			if err == nil {
				t.Fatalf("a %d was treated as success", tt.status)
			}
			if tt.want != nil && !errors.Is(err, tt.want) {
				t.Errorf("err = %v, want %v", err, tt.want)
			}
			if want := fmt.Sprintf("status %d", tt.status); !strings.Contains(err.Error(), want) {
				t.Errorf("err = %v, want it to carry %q", err, want)
			}
			for _, leak := range []string{"ada@example.com", "gochat_s3cr3t", "no room", "since=42", "/api/rooms", "general"} {
				if strings.Contains(err.Error(), leak) {
					t.Errorf("the error carries %q, which the answer or the request held: %v", leak, err)
				}
			}
		})
	}
}

// notHTTP is a peer that answers every connection with a line that is not
// HTTP, and returns its address. net/http's error for it quotes what the peer
// wrote.
func notHTTP(t *testing.T, line string) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return // the listener closed: the test is over
			}
			conn.Read(make([]byte, 1024)) // the request, so the answer is to something
			conn.Write([]byte(line + "\r\n\r\n"))
			conn.Close()
		}
	}()
	return ln.Addr().String()
}

// TestATransportErrorKeepsTheTypeAndNoText: Do returns a *url.Error. Its text
// is the whole URL, query string included, and the error under it can quote
// what the peer sent. The adapter keeps that error's type and none of its
// text, beside the method and the host this client was pointed at
// (patterns/go-http-client.md, Retries).
func TestATransportErrorKeepsTheTypeAndNoText(t *testing.T) {
	t.Parallel()
	// A port nothing listens on: opened, then closed again.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	refused := ln.Addr().String()
	ln.Close()

	calls := map[string]func(*chatapi.Client) error{
		"a read":  func(c *chatapi.Client) error { _, _, err := c.Messages(t.Context(), "general", 42); return err },
		"a write": func(c *chatapi.Client) error { _, err := c.Post(t.Context(), "general", "hello ada"); return err },
	}
	peers := map[string]struct {
		addr string
		want string // the type net/http's error has under its *url.Error
	}{
		"nothing listens there":             {refused, "(*net.OpError)"},
		"the peer's status line is not one": {notHTTP(t, "HTTP/1.1 ada@example.com said-hello"), "no usable answer ("},
		"the peer does not speak HTTP":      {notHTTP(t, "said-hello ada@example.com"), "no usable answer ("},
	}
	for peer, p := range peers {
		for name, call := range calls {
			t.Run(peer+", "+name, func(t *testing.T) {
				t.Parallel()
				err := call(chatapi.New("http://"+p.addr, "gochat_s3cr3t"))
				if err == nil {
					t.Fatal("a call that got no answer was treated as success")
				}
				if !strings.Contains(err.Error(), "no usable answer (") || !strings.Contains(err.Error(), p.want) {
					t.Errorf("err = %v, want it to name the type of what went wrong, %s", err, p.want)
				}
				if !strings.Contains(err.Error(), p.addr) {
					t.Errorf("err = %v, want it to name the host %s", err, p.addr)
				}
				for _, leak := range []string{
					"since=42", "/api/rooms", "general", "http://", "gochat_s3cr3t", "hello ada", // the request
					"connection refused", "dial tcp", // net's text
					"ada@example.com", "said-hello", "malformed", // the peer's bytes, and net/http's words around them
				} {
					if strings.Contains(err.Error(), leak) {
						t.Errorf("the error carries %q: %v", leak, err)
					}
				}
			})
		}
	}
}

// TestACallTheCallerGaveUpOnReturnsTheContextsError: a context that ends while
// the request is out — Ctrl-C, a shutdown — is the caller's doing, not the
// server's. The call returns the context's own error, so errors.Is still tells
// the two apart, and a read is not tried again
// (patterns/go-http-client.md, Retries).
func TestACallTheCallerGaveUpOnReturnsTheContextsError(t *testing.T) {
	t.Parallel()
	for name, call := range map[string]func(context.Context, *chatapi.Client) error{
		"a read":  func(ctx context.Context, c *chatapi.Client) error { _, err := c.Rooms(ctx); return err },
		"a write": func(ctx context.Context, c *chatapi.Client) error { _, err := c.Post(ctx, "general", "hi"); return err },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var calls atomic.Int32
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			held := make(chan struct{})
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				cancel() // the caller gives up while the server holds the request
				<-held   // and no answer is ever written
			}))
			t.Cleanup(srv.Close)
			t.Cleanup(func() { close(held) }) // first: Close waits for the handler

			err := call(ctx, chatapi.New(srv.URL, "gochat_token"))

			if !errors.Is(err, context.Canceled) {
				t.Errorf("err = %v, want context.Canceled", err)
			}
			if strings.Contains(err.Error(), "no usable answer") {
				t.Errorf("err = %v: the caller's own cancel reads as the server failing", err)
			}
			if got := calls.Load(); got != 1 {
				t.Errorf("the server was called %d times, want 1: a cancelled call is not retried", got)
			}
		})
	}
}

// TestABaseURLThatDoesNotParseStaysOutOfTheError: NewRequestWithContext
// returns a *url.Error too, and its text quotes the whole URL — the address
// somebody configured, a password in it if they put one there, and the path
// and query this client built from its arguments. The wrap around building
// the request carries no %w (patterns/go-http-client.md, One request, end to
// end).
func TestABaseURLThatDoesNotParseStaysOutOfTheError(t *testing.T) {
	t.Parallel()
	// A trailing space, as a pasted address has: the host no longer parses.
	client := chatapi.New("http://ada:hunter2@chat.example.com ", "gochat_secret")
	calls := map[string]func() error{
		"a read": func() error {
			_, _, err := client.Messages(t.Context(), "private-room", 12)
			return err
		},
		"a write": func() error {
			_, err := client.Post(t.Context(), "private-room", "hello")
			return err
		},
	}
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			err := call()
			if err == nil {
				t.Fatal("a base URL that does not parse was accepted")
			}
			if !strings.Contains(err.Error(), "the base URL does not parse") {
				t.Errorf("err = %v, want it to name the fix", err)
			}
			for _, leak := range []string{"http://", "chat.example.com", "hunter2", "/api/", "private-room", "since=12", "gochat_secret"} {
				if strings.Contains(err.Error(), leak) {
					t.Errorf("the error carries %q: %v", leak, err)
				}
			}
		})
	}
}

func TestUnknownStatusIsStillAnError(t *testing.T) {
	t.Parallel()
	client := serverSaying(t, http.StatusTeapot, `{"error":"no coffee"}`)

	_, err := client.Me(t.Context())

	if err == nil {
		t.Fatal("a 418 was treated as success")
	}
	// It is nobody's domain error, so it must not be mistaken for one.
	for _, sentinel := range []error{domain.ErrNotFound, domain.ErrUnauthorized, domain.ErrRejected} {
		if errors.Is(err, sentinel) {
			t.Errorf("a 418 came back as %v", sentinel)
		}
	}
}

// TestAnswerThatIsNotJSONIsAnError: a 200 this client cannot decode is an
// error, and a fixed sentence. The decoder's own text quotes the answer — a
// member's name here — so it is not wrapped
// (patterns/go-http-client.md, One request, end to end).
// TestACancelWhileTheAnswerIsReadReturnsTheContextsError: the headers have
// arrived and the caller gives up while the body is read. That is the caller
// stopping, not an answer that is not JSON
// (patterns/go-http-client.md, One request, end to end).
//
// The server waits before it cancels so that the client is reading the body by
// then. Were the cancel to land earlier, the call would fail in transport and
// this test would pass without testing the decode: it can go quiet, never red.
func TestACancelWhileTheAnswerIsReadReturnsTheContextsError(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	held := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"name":`) // the headers and the start of an answer
		w.(http.Flusher).Flush()
		select {
		case <-time.After(100 * time.Millisecond):
		case <-held:
		}
		cancel()
		<-held // and the rest never comes
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(held) }) // first: Close waits for the handler

	_, err := chatapi.New(srv.URL, "gochat_token").Me(ctx)

	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	if err != nil && strings.Contains(err.Error(), "not the JSON") {
		t.Errorf("err = %v: the caller's own cancel reads as a bad answer", err)
	}
}

func TestAnswerThatIsNotJSONIsAnError(t *testing.T) {
	t.Parallel()
	for name, body := range map[string]string{
		// What a proxy in front of the server looks like when it is
		// misconfigured. It must not read as an empty answer.
		"a page of HTML": `<html>a login page for ada@example.com</html>`,
		// The decoder names the member it met twice.
		"a member the decoder quotes": `{"name":"Ada","ada@example.com":1,"ada@example.com":2}`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, err := serverSaying(t, http.StatusOK, body).Me(t.Context())

			if err == nil {
				t.Fatal("an answer that is not the JSON this client expects was accepted")
			}
			if !strings.Contains(err.Error(), "not the JSON this client expects") {
				t.Errorf("err = %v, want it to say what is wrong", err)
			}
			for _, leak := range []string{"ada@example.com", "login page", "duplicate", "jsontext"} {
				if strings.Contains(err.Error(), leak) {
					t.Errorf("the error carries %q, which the answer held or the decoder wrote: %v", leak, err)
				}
			}
		})
	}
}

func TestTheTokenIsSent(t *testing.T) {
	t.Parallel()
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("Authorization")
		w.Write([]byte(`{"name":"Ada"}`))
	}))
	t.Cleanup(srv.Close)

	name, err := chatapi.New(srv.URL, "gochat_secret").Me(t.Context())

	if err != nil {
		t.Fatalf("Me: %v", err)
	}
	if name != "Ada" {
		t.Errorf("name = %q, want Ada", name)
	}
	if got != "Bearer gochat_secret" {
		t.Errorf("Authorization = %q, want the bearer token", got)
	}
}

// TestReadsAreRetriedAndWritesAreNot is the one rule that keeps a flaky network
// from saying the same thing twice in a room.
func TestReadsAreRetriedAndWritesAreNot(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		call     func(*chatapi.Client) error
		attempts int32
	}{
		{
			name:     "a read is retried",
			call:     func(c *chatapi.Client) error { _, err := c.Rooms(t.Context()); return err },
			attempts: 3,
		},
		{
			// The first attempt may have reached the server and only its answer
			// got lost. Trying again would post the message twice.
			name:     "a write is not",
			call:     func(c *chatapi.Client) error { _, err := c.Post(t.Context(), "general", "hi"); return err },
			attempts: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var calls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.WriteHeader(http.StatusServiceUnavailable)
				w.Write([]byte(`{"error":"busy"}`))
			}))
			t.Cleanup(srv.Close)

			err := tt.call(chatapi.New(srv.URL, "gochat_token"))

			if err == nil {
				t.Fatal("a 503 was treated as success")
			}
			if got := calls.Load(); got != tt.attempts {
				t.Errorf("the server was called %d times, want %d", got, tt.attempts)
			}
		})
	}
}

func TestMessagesComeBackAsDomainValues(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("since"); got != "12" {
			t.Errorf("since = %q, want 12", got)
		}
		w.Write([]byte(`{"messages":[
			{"seq":13,"author":"Ada","body":"hello","created_at":"2026-08-15T10:00:00Z"}
		],"since":13}`))
	}))
	t.Cleanup(srv.Close)

	msgs, next, err := chatapi.New(srv.URL, "t").Messages(t.Context(), "general", 12)

	if err != nil {
		t.Fatalf("Messages: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("msgs = %v, want one", msgs)
	}
	// The rest of the program deals in domain.Message, and never learns that
	// any of this arrived over HTTP.
	if msgs[0].Seq != 13 || msgs[0].Author != "Ada" || msgs[0].Body != "hello" {
		t.Errorf("msgs[0] = %+v, want Ada saying hello at 13", msgs[0])
	}
	if msgs[0].CreatedAt.IsZero() {
		t.Error("the time did not survive the translation")
	}
	if next != 13 {
		t.Errorf("next = %d, want 13", next)
	}
}

func TestAnUnreadableTimeIsAnError(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"messages":[{"seq":1,"author":"Ada","body":"hi","created_at":"yesterday"}],"since":1}`))
	}))
	t.Cleanup(srv.Close)

	_, _, err := chatapi.New(srv.URL, "t").Messages(t.Context(), "general", 0)

	// Better to fail loudly than to hand back a zero time that prints as 1970.
	if err == nil {
		t.Fatal("an unreadable time was accepted")
	}
	// And without quoting it: what the answer held stays out of the error.
	if strings.Contains(err.Error(), "yesterday") {
		t.Errorf("the error quotes the answer: %v", err)
	}
}

func TestBaseURLTrailingSlashIsHarmless(t *testing.T) {
	t.Parallel()
	var path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		w.Write([]byte(`{"name":"Ada"}`))
	}))
	t.Cleanup(srv.Close)

	// A person setting CHAT_ADDR will sometimes end it with a slash.
	if _, err := chatapi.New(srv.URL+"/", "t").Me(t.Context()); err != nil {
		t.Fatalf("Me: %v", err)
	}
	if path != "/api/me" {
		t.Errorf("path = %q, want /api/me", path)
	}
}
