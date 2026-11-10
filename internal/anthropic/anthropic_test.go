package anthropic

import (
	"context"
	"encoding/json/v2"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/andygeiss/baseline-reference/v5/internal/domain"
)

// serving points an adapter at a test server. No test in this package reaches
// the live API: it is slow, non-deterministic, and somebody's bill.
func serving(t *testing.T, h http.HandlerFunc) *Assistant {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	a := New("test-key")
	a.endpoint = srv.URL
	return a
}

func history() []domain.Message {
	return []domain.Message{
		{AuthorID: "u1", Author: "Ada", Body: "@assistant what is the answer?"},
	}
}

// TestRequestIsPinned is the half a fake cannot check and the half that
// silently rots. If a refactor drops the thinking setting, changes the effort
// level, or lets the token ceiling shrink below what thinking needs, nothing
// else in this repository notices — the app still compiles and the fake still
// answers. This test is what makes a model migration a green-or-red change
// rather than a hopeful one.
func TestRequestIsPinned(t *testing.T) {
	t.Parallel()

	var (
		got     request
		headers http.Header
	)
	a := serving(t, func(w http.ResponseWriter, r *http.Request) {
		headers = r.Header.Clone()
		if err := json.UnmarshalRead(r.Body, &got); err != nil {
			t.Errorf("decoding the request the adapter sent: %v", err)
		}
		writeText(w, "fine")
	})

	if _, err := a.Reply(t.Context(), history()); err != nil {
		t.Fatalf("Reply() = %v", err)
	}

	if got.Model != "claude-opus-5" {
		t.Errorf("model = %q, want claude-opus-5", got.Model)
	}
	// Adaptive, never disabled: thinking-off can push the reasoning into the
	// visible answer, which in a chat room is the product bug.
	if got.Thinking.Type != "adaptive" {
		t.Errorf("thinking.type = %q, want adaptive", got.Thinking.Type)
	}
	if got.OutputConfig.Effort != "low" {
		t.Errorf("output_config.effort = %q, want low", got.OutputConfig.Effort)
	}
	// The ceiling covers thinking plus the answer, so it is sized for both. A
	// ceiling sized for the two-sentence reply the prompt asks for would
	// truncate the reply the moment the model thinks.
	if got.MaxTokens < 2048 {
		t.Errorf("max_tokens = %d, too small to cover thinking plus the answer", got.MaxTokens)
	}
	if got.System != domain.SystemPrompt {
		t.Error("the system prompt is not the one in domain — an adapter with its own copy is one that drifts")
	}

	for header, want := range map[string]string{
		"X-Api-Key":         "test-key",
		"Anthropic-Version": apiVersion,
		"Content-Type":      "application/json",
	} {
		if got := headers.Get(header); got != want {
			t.Errorf("header %s = %q, want %q", header, got, want)
		}
	}

	// The port speaks domain.Message; the wire speaks roles. Ada's name rides
	// in the text, because three people talking would otherwise read as one.
	if len(got.Messages) != 1 || got.Messages[0].Role != "user" {
		t.Fatalf("messages = %+v, want one user turn", got.Messages)
	}
}

// TestResponseTranslation walks the statuses and stop reasons the vendor
// documents. It asserts on the translation — shape in, shape out, sentinel on
// refusal — and never on what a model said, which is not under test and cannot
// be.
func TestResponseTranslation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		handler http.HandlerFunc
		want    string
		wantErr error // nil means: any error, so long as it is not a sentinel
		wantOK  bool
	}{
		{
			name:    "text comes back as the answer",
			handler: func(w http.ResponseWriter, _ *http.Request) { writeText(w, "42.") },
			want:    "42.",
			wantOK:  true,
		},
		{
			name: "a refusal becomes the sentinel",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				// A declined request is a successful 200 whose content is empty
				// or partial. Reading the text first is the bug the ordering in
				// Reply prevents.
				writeJSON(w, http.StatusOK, `{"stop_reason":"refusal","content":[]}`)
			},
			wantErr: domain.ErrRefused,
		},
		{
			name: "a refusal wins even when there is partial text",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				writeJSON(w, http.StatusOK, `{"stop_reason":"refusal","content":[{"type":"text","text":"I was about to"}]}`)
			},
			wantErr: domain.ErrRefused,
		},
		{
			name: "200 with no text is an error, not an empty reply",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				writeJSON(w, http.StatusOK, `{"stop_reason":"end_turn","content":[]}`)
			},
		},
		{
			name: "a thinking block alone is not an answer",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				writeJSON(w, http.StatusOK, `{"stop_reason":"end_turn","content":[{"type":"thinking","text":""}]}`)
			},
		},
		{
			name: "an answer cut by the token ceiling is an error",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				writeJSON(w, http.StatusOK, `{"stop_reason":"max_tokens","content":[{"type":"text","text":"The answer is"}]}`)
			},
		},
		{
			name: "an answer cut by the context window is an error",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				writeJSON(w, http.StatusOK, `{"stop_reason":"model_context_window_exceeded","content":[{"type":"text","text":"The answer is"}]}`)
			},
		},
		{
			name: "503 fails without a domain sentinel",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusServiceUnavailable)
			},
		},
		{
			name: "401 fails without a domain sentinel",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusUnauthorized)
			},
		},
		{
			name: "a body that is not JSON fails",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				writeJSON(w, http.StatusOK, `not json`)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := serving(t, tt.handler).Reply(t.Context(), history())

			switch {
			case tt.wantOK:
				if err != nil {
					t.Fatalf("Reply() = %v, want the answer", err)
				}
				if got != tt.want {
					t.Errorf("Reply() = %q, want %q", got, tt.want)
				}
			case tt.wantErr != nil:
				if !errors.Is(err, tt.wantErr) {
					t.Errorf("Reply() = %v, want %v", err, tt.wantErr)
				}
			default:
				if err == nil {
					t.Fatal("Reply() = nil, want an error")
				}
				// Transient, so the caller retries or degrades. Returning the
				// refusal sentinel here would make the room say "I can't help
				// with that" about somebody else's outage.
				if errors.Is(err, domain.ErrRefused) {
					t.Errorf("Reply() = %v, which a caller will read as a refusal", err)
				}
			}
		})
	}
}

// TestNothingToReplyTo covers the empty history the domain can hand back when a
// room holds only assistant turns.
func TestNothingToReplyTo(t *testing.T) {
	t.Parallel()
	a := serving(t, func(_ http.ResponseWriter, _ *http.Request) {
		t.Error("the adapter called the model with nothing to reply to")
	})

	if _, err := a.Reply(t.Context(), nil); err == nil {
		t.Error("Reply() = nil, want an error")
	}
}

func writeText(w http.ResponseWriter, text string) {
	writeJSON(w, http.StatusOK, `{"stop_reason":"end_turn","content":[{"type":"text","text":"`+text+`"}]}`)
}

func writeJSON(w http.ResponseWriter, status int, body string) {
	w.Header().Set("content-type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body))
}

// TestAnErrorCarriesNothingTheAPIWrote: the reason phrase and the body are the
// other side's to write, and an API's error quotes the request back — here, the
// room's own conversation. The error names the status code and stops
// (patterns/go-http-client.md, statusError; patterns/go-errors-logging.md,
// Never log).
func TestAnErrorCarriesNothingTheAPIWrote(t *testing.T) {
	t.Parallel()
	a := serving(t, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusBadRequest,
			`{"type":"error","error":{"type":"invalid_request_error","message":"messages.0: Ada said: my address is ada@example.com"}}`)
	})

	_, err := a.Reply(t.Context(), history())

	if err == nil {
		t.Fatal("Reply() = nil, want an error")
	}
	if !strings.Contains(err.Error(), "status 400") {
		t.Errorf("err = %v, want it to name the status code", err)
	}
	for _, leak := range []string{"ada@example.com", "Ada said", "invalid_request_error", "Bad Request"} {
		if strings.Contains(err.Error(), leak) {
			t.Errorf("the error carries %q, which the API wrote: %v", leak, err)
		}
	}
}

// TestATransportErrorKeepsTheTypeAndNoText: Do returns a *url.Error, whose text
// is the whole URL, and the error under it can quote what the peer sent. The
// adapter keeps that error's type and none of its text
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

	// And a peer that answers every connection with a line that is no HTTP
	// status line: net/http's error for it quotes what the peer wrote.
	peer, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { peer.Close() })
	go func() {
		for {
			conn, err := peer.Accept()
			if err != nil {
				return // the listener closed: the test is over
			}
			conn.Read(make([]byte, 4096)) // the request, so the answer is to something
			conn.Write([]byte("HTTP/1.1 ada@example.com said-hello\r\n\r\n"))
			conn.Close()
		}
	}()

	for name, tt := range map[string]struct{ addr, want string }{
		"nothing listens there":             {refused, "asking the model: no usable answer (*net.OpError)"},
		"the peer's status line is not one": {peer.Addr().String(), "asking the model: no usable answer ("},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			a := New("sk-test-key")
			a.endpoint = "http://" + tt.addr + "/v1/messages?debug=ada@example.com"

			_, err := a.Reply(t.Context(), history())

			if err == nil {
				t.Fatal("Reply() = nil, want an error")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("err = %v, want it to name the type of what went wrong: %s", err, tt.want)
			}
			for _, leak := range []string{
				"http://", "/v1/messages", "ada@example.com", "sk-test-key", tt.addr, // the request
				"connection refused", "dial tcp", // net's text
				"said-hello", "malformed", // the peer's bytes, and net/http's words around them
			} {
				if strings.Contains(err.Error(), leak) {
					t.Errorf("the error carries %q: %v", leak, err)
				}
			}
			// Transient to the caller, like every other failure that is no refusal.
			if errors.Is(err, domain.ErrRefused) {
				t.Errorf("Reply() = %v, which a caller will read as a refusal", err)
			}
		})
	}
}

// TestAReplyTheAppGaveUpOnReturnsTheContextsError: a context that ends while
// the request is out — the budget, or shutdown — is this app's doing, not the
// API's. The error is still the context's, so errors.Is tells a reply that was
// cancelled from a model that is down (patterns/go-http-client.md, Retries).
func TestAReplyTheAppGaveUpOnReturnsTheContextsError(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	held := make(chan struct{})
	a := serving(t, func(http.ResponseWriter, *http.Request) {
		cancel() // the app gives up while the API holds the request
		<-held   // and no answer is ever written
	})
	t.Cleanup(func() { close(held) }) // before serving's Close, which waits for the handler

	_, err := a.Reply(ctx, history())

	if !errors.Is(err, context.Canceled) {
		t.Errorf("Reply() = %v, want context.Canceled", err)
	}
	if err != nil && strings.Contains(err.Error(), "no usable answer") {
		t.Errorf("Reply() = %v: the app's own cancel reads as the API failing", err)
	}
}

// roundTrip lets a test answer for the transport, where a test server cannot
// say when the client has started reading.
type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// cancelOnRead is a body whose first read is the moment the app gives up.
type cancelOnRead struct{ cancel context.CancelFunc }

func (b cancelOnRead) Read([]byte) (int, error) { b.cancel(); return 0, context.Canceled }
func (cancelOnRead) Close() error               { return nil }

// TestACancelWhileTheReplyIsReadReturnsTheContextsError: the answer's headers
// have arrived and the app gives up while the body is read. That is the app
// stopping, not a reply that does not decode, and shutdown tells the two apart
// by errors.Is (patterns/go-http-client.md, One request, end to end).
func TestACancelWhileTheReplyIsReadReturnsTheContextsError(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	a := New("test-key")
	a.http = &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: cancelOnRead{cancel}, Request: r}, nil
	})}

	_, err := a.Reply(ctx, history())

	if !errors.Is(err, context.Canceled) {
		t.Errorf("Reply() = %v, want context.Canceled", err)
	}
	if err != nil && strings.Contains(err.Error(), "decoding the reply") {
		t.Errorf("Reply() = %v: the app's own cancel reads as a reply that does not decode", err)
	}
}

// TestAReplyThatDoesNotDecodeStaysOutOfTheError: the decoder's text quotes the
// answer — the name of a member it met twice, here — and the answer is the
// model's words about the room. The error is a fixed sentence
// (patterns/go-http-client.md, One request, end to end).
func TestAReplyThatDoesNotDecodeStaysOutOfTheError(t *testing.T) {
	t.Parallel()
	a := serving(t, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, `{"stop_reason":"end_turn","Ada said ada@example.com":1,"Ada said ada@example.com":2}`)
	})

	_, err := a.Reply(t.Context(), history())

	if err == nil {
		t.Fatal("Reply() = nil, want an error")
	}
	if !strings.Contains(err.Error(), "decoding the reply") {
		t.Errorf("err = %v, want it to say what was being done", err)
	}
	for _, leak := range []string{"ada@example.com", "Ada said", "duplicate", "jsontext"} {
		if strings.Contains(err.Error(), leak) {
			t.Errorf("the error carries %q, which the answer held or the decoder wrote: %v", leak, err)
		}
	}
}

// TestAnEndpointThatDoesNotParseStaysOutOfTheError: NewRequestWithContext
// returns a *url.Error as Do does, and its text is the whole URL. The wrap
// around building the request carries no %w (patterns/go-http-client.md, One
// request, end to end).
func TestAnEndpointThatDoesNotParseStaysOutOfTheError(t *testing.T) {
	t.Parallel()
	a := New("sk-test-key")
	a.endpoint = "http://api.example.com /v1/messages?debug=ada@example.com"

	_, err := a.Reply(t.Context(), history())

	if err == nil {
		t.Fatal("an endpoint that does not parse was accepted")
	}
	if !strings.Contains(err.Error(), "the endpoint does not parse") {
		t.Errorf("err = %v, want it to say what is wrong", err)
	}
	for _, leak := range []string{"http://", "api.example.com", "/v1/messages", "ada@example.com", "sk-test-key"} {
		if strings.Contains(err.Error(), leak) {
			t.Errorf("the error carries %q: %v", leak, err)
		}
	}
}
