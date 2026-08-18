package app

import (
	"context"
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

// withStopping hands the app a context the test can cancel, standing in for the
// errgroup's context in main.
func withStopping(ctx context.Context) func(*Options) {
	return func(o *Options) { o.Stopping = ctx }
}

// blockingAssistant stays inside Reply until a test lets it out, and says on the
// way out why it stopped.
//
// That is what makes the two rules below testable without a clock: while Reply
// is blocked the reply is provably still in flight, so a POST that has already
// answered provably did not wait for it. And ctx.Err() tells the two ways it can
// end apart — Canceled means shutdown reached it, DeadlineExceeded means only
// the budget did.
type blockingAssistant struct {
	entered chan struct{} // Reply announces that it has started
	release chan struct{} // the test closes it to let Reply answer
	ended   chan error    // why Reply stopped: nil, or ctx.Err()
}

func newBlockingAssistant() *blockingAssistant {
	return &blockingAssistant{
		entered: make(chan struct{}, 1),
		release: make(chan struct{}),
		ended:   make(chan error, 1),
	}
}

func (b *blockingAssistant) Reply(ctx context.Context, _ []domain.Message) (string, error) {
	b.entered <- struct{}{}
	select {
	case <-b.release:
		b.ended <- nil
		return "Sure — 42.", nil
	case <-ctx.Done():
		b.ended <- ctx.Err()
		return "", ctx.Err()
	}
}

// TestAMentionDoesNotMakeTheSenderWait is the detached shape's whole point: the
// required step is the person's own message, and the answer is not on its path.
func TestAMentionDoesNotMakeTheSenderWait(t *testing.T) {
	t.Parallel()
	bot := newBlockingAssistant()

	ta := newTestApp(t, withAssistant(bot))
	ta.signUp(t, "Ada", "correct-horse")
	slug := ta.makeRoom(t, "General Chat")

	res, _ := ta.do(t, http.MethodPost, "/rooms/"+slug+"/messages",
		url.Values{"body": {"@assistant what is the answer?"}}, htmx())
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	// Reply has not returned and cannot until the line below, so the response
	// above provably did not wait for it. No timer says this; the channel does.
	<-bot.entered

	close(bot.release)
	ta.Wait()

	_, body := ta.do(t, http.MethodGet, "/rooms/"+slug, nil, nil)
	if !strings.Contains(body, "Sure — 42.") {
		t.Errorf("the room is missing the detached reply:\n%s", body)
	}
}

// TestShutdownEndsAReplyInFlight is the trap patterns/go-background-work.md
// exists to catch, from the other side: srv.Shutdown does not wait for this
// goroutine, so something has to cancel it and something has to join it.
//
// Both halves are asserted without a clock. context.Canceled rather than
// context.DeadlineExceeded is what says the cancel reached the reply rather than
// its own budget expiring; Wait returning is what says main can join it.
func TestShutdownEndsAReplyInFlight(t *testing.T) {
	t.Parallel()
	stopping, stop := context.WithCancel(t.Context())
	bot := newBlockingAssistant()

	ta := newTestApp(t, withAssistant(bot), withStopping(stopping))
	ta.signUp(t, "Ada", "correct-horse")
	slug := ta.makeRoom(t, "General Chat")

	ta.do(t, http.MethodPost, "/rooms/"+slug+"/messages",
		url.Values{"body": {"@assistant what is the answer?"}}, nil)
	<-bot.entered

	stop() // what errgroup does to its context as g.Wait returns
	if err := <-bot.ended; !errors.Is(err, context.Canceled) {
		t.Errorf("the reply ended with %v, want context.Canceled — shutdown never reached it", err)
	}
	ta.Wait() // main's second wait; it hangs for the whole budget without the line above

	_, body := ta.do(t, http.MethodGet, "/rooms/"+slug, nil, nil)
	if strings.Contains(body, "Sure — 42.") {
		t.Errorf("a reply cut short by shutdown was stored anyway:\n%s", body)
	}
}

func TestAssistantAnswersAMention(t *testing.T) {
	t.Parallel()
	bot := newFakeAssistant()
	bot.answer("Ada, the answer is 42.", nil)

	ta := newTestApp(t, withAssistant(bot))
	ta.signUp(t, "Ada", "correct-horse")
	slug := ta.makeRoom(t, "General Chat")

	ta.do(t, http.MethodPost, "/rooms/"+slug+"/messages", url.Values{"body": {"@assistant what is the answer?"}}, nil)
	// The reply is written outside the request now, so the POST returning says
	// nothing about it. Wait on the app's own counter — a sleep here is a test
	// that passes on this machine and fails on a loaded one
	// (patterns/go-background-work.md).
	ta.Wait()

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
	ta.Wait() // nothing to wait for, and that is what this asserts

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
			// message is posted and they get their page back, not a 500. It
			// also does not wait for the model — that is the point of the
			// detached shape, and it is why the assertions below come after a
			// Wait rather than straight after the POST.
			if res.StatusCode != http.StatusOK {
				t.Errorf("status = %d, want 200 — an enhancement failure took the post down with it", res.StatusCode)
			}
			ta.Wait()

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
