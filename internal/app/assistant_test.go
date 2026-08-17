package app

import (
	"errors"
	"html"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/andygeiss/baseline-reference/v3/internal/domain"
)

// withAssistant hands the app a scripted assistant instead of the harness
// default, so a test can decide what the model does.
func withAssistant(a Assistant) func(*Options) {
	return func(o *Options) { o.Assistant = a }
}

func TestAssistantAnswersAMention(t *testing.T) {
	t.Parallel()
	bot := newFakeAssistant()
	bot.answer("Ada, the answer is 42.", nil)

	ta := newTestApp(t, withAssistant(bot))
	ta.signUp(t, "Ada", "correct-horse")
	slug := ta.makeRoom(t, "General Chat")

	ta.do(t, http.MethodPost, "/rooms/"+slug+"/messages", url.Values{"body": {"@assistant what is the answer?"}}, nil)
	_, body := ta.do(t, http.MethodGet, "/rooms/"+slug, nil, nil)

	if !strings.Contains(body, "Ada, the answer is 42.") {
		t.Errorf("the room is missing the assistant's reply:\n%s", body)
	}
	if !strings.Contains(body, "Assistant") {
		t.Errorf("the reply is not attributed to the assistant:\n%s", body)
	}
}

func TestAssistantStaysQuietWithoutAMention(t *testing.T) {
	t.Parallel()
	bot := newFakeAssistant()
	bot.answer("I was not asked.", nil)

	ta := newTestApp(t, withAssistant(bot))
	ta.signUp(t, "Ada", "correct-horse")
	slug := ta.makeRoom(t, "General Chat")

	ta.do(t, http.MethodPost, "/rooms/"+slug+"/messages", url.Values{"body": {"morning all"}}, nil)
	_, body := ta.do(t, http.MethodGet, "/rooms/"+slug, nil, nil)

	if strings.Contains(body, "I was not asked.") {
		t.Errorf("the assistant answered a message that did not mention it:\n%s", body)
	}
}

// TestAssistantDegrades is the test the enhancement rule asks for: one per
// enhancement failure, asserting the operation still succeeds when that
// dependency is down. The assertion is on the response and the room, never on a
// log line (patterns/go-errors-logging.md).
func TestAssistantDegrades(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		said    string
		err     error
		wantSay string // "" means: the room gets no assistant message at all
	}{
		{
			name: "the model is unreachable",
			err:  errors.New("dial tcp: connection refused"),
		},
		{
			name: "the model answers with nothing",
			err:  errors.New("the model answered with no text"),
		},
		{
			name:    "the model declines",
			err:     domain.ErrRefused,
			wantSay: "I can't help with that one.",
		},
		{
			name:    "the model answers",
			said:    "Sure — 42.",
			wantSay: "Sure — 42.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			bot := newFakeAssistant()
			bot.answer(tt.said, tt.err)

			ta := newTestApp(t, withAssistant(bot))
			ta.signUp(t, "Ada", "correct-horse")
			slug := ta.makeRoom(t, "General Chat")

			res, _ := ta.do(t, http.MethodPost, "/rooms/"+slug+"/messages",
				url.Values{"body": {"@assistant what is the answer?"}},
				http.Header{"HX-Request": {"true"}})

			// The required step stands whatever the model did: the person's
			// message is posted and they get their page back, not a 500.
			if res.StatusCode != http.StatusOK {
				t.Errorf("status = %d, want 200 — an enhancement failure took the post down with it", res.StatusCode)
			}
			_, body := ta.do(t, http.MethodGet, "/rooms/"+slug, nil, nil)
			if !strings.Contains(body, "what is the answer?") {
				t.Errorf("the person's own message is missing from the room:\n%s", body)
			}

			switch {
			case tt.wantSay != "":
				// Escaped, because what the assistant said reaches the page
				// through html/template like any other message: the apostrophe
				// in "can't" arrives as &#39;.
				if !strings.Contains(body, html.EscapeString(tt.wantSay)) {
					t.Errorf("the room is missing %q:\n%s", tt.wantSay, body)
				}
			default:
				// Nothing was stored, so the mention can simply be made again.
				if strings.Contains(body, "Assistant") {
					t.Errorf("a failed reply still put an assistant message in the room:\n%s", body)
				}
			}
		})
	}
}
