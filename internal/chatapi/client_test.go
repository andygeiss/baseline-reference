package chatapi_test

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"

	"github.com/andygeiss/baseline-reference/v5/internal/chatapi"
	"github.com/andygeiss/baseline-reference/v5/internal/domain"
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

// TestATransportErrorCarriesNoURL: Do returns a *url.Error, and its text is the
// whole URL, query string included. The cause stays; the URL goes.
func TestATransportErrorCarriesNoURL(t *testing.T) {
	t.Parallel()
	// A port nothing listens on: opened, then closed again.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()

	for name, call := range map[string]func(*chatapi.Client) error{
		"a read":  func(c *chatapi.Client) error { _, _, err := c.Messages(t.Context(), "general", 42); return err },
		"a write": func(c *chatapi.Client) error { _, err := c.Post(t.Context(), "general", "hello ada"); return err },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			err := call(chatapi.New("http://"+addr, "gochat_s3cr3t"))
			if !errors.Is(err, syscall.ECONNREFUSED) {
				t.Fatalf("err = %v, want it to still be a refused connection", err)
			}
			for _, leak := range []string{"since=42", "/api/rooms", "general", "http://", "gochat_s3cr3t"} {
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

func TestAnswerThatIsNotJSONIsAnError(t *testing.T) {
	t.Parallel()
	client := serverSaying(t, http.StatusOK, `<html>a login page</html>`)

	_, err := client.Me(t.Context())

	// This is what a proxy in front of the server looks like when it is
	// misconfigured, and it must not read as an empty answer.
	if err == nil {
		t.Fatal("a page of HTML was accepted as an answer")
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
