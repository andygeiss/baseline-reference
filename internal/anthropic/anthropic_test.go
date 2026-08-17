package anthropic

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/andygeiss/baseline-reference/v3/internal/domain"
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
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
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
