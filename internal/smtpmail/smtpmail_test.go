package smtpmail

import (
	"errors"
	"strings"
	"testing"

	"github.com/andygeiss/baseline-reference/v3/internal/domain"
)

// The adapter is tested at its edges: what it puts on the wire, and what it
// refuses to put there. Nothing here talks to a relay — a test that needed one
// would be testing the relay.

func TestRender(t *testing.T) {
	t.Parallel()
	s := New("mail.example.com:587", "chat@example.com", "", "")

	got := string(s.render(domain.Mail{
		To:      "ada@example.com",
		Subject: "Reset your Go Chat password",
		Text:    "Open this link:\nhttps://chat.example.com/reset/confirm?t=abc\n",
	}))

	for _, want := range []string{
		"From: chat@example.com\r\n",
		"To: ada@example.com\r\n",
		"Subject: Reset your Go Chat password\r\n",
		"Content-Type: text/plain; charset=utf-8\r\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the message is missing %q:\n%s", want, got)
		}
	}
	// One blank line ends the headers. Without it the body is read as more of
	// them, and the whole message arrives empty.
	head, body, ok := strings.Cut(got, "\r\n\r\n")
	if !ok {
		t.Fatalf("no blank line between the headers and the body:\n%s", got)
	}
	if strings.Contains(head, "https://") {
		t.Error("the link ended up in the headers")
	}
	if !strings.Contains(body, "https://chat.example.com/reset/confirm?t=abc") {
		t.Errorf("the body is missing the link:\n%s", body)
	}
}

func TestRenderEncodesANonASCIISubject(t *testing.T) {
	t.Parallel()
	s := New("mail.example.com:587", "chat@example.com", "", "")
	got := string(s.render(domain.Mail{To: "ada@example.com", Subject: "Grüße aus Köln"}))

	// QEncoding leaves ASCII alone and encodes the rest, so the raw bytes never
	// reach a header field that is defined as ASCII.
	if strings.Contains(got, "Grüße") {
		t.Errorf("the subject went out unencoded:\n%s", got)
	}
	if !strings.Contains(got, "=?utf-8?q?") {
		t.Errorf("the subject was not encoded:\n%s", got)
	}
}

// TestSendRefusesAHeaderWithALineBreak is the tier-1 rule at the adapter's
// door: the check runs before anything is dialled, so a message like this never
// reaches a relay at all.
func TestSendRefusesAHeaderWithALineBreak(t *testing.T) {
	t.Parallel()
	// An address that would resolve to nothing, to prove the refusal happens
	// before the dial rather than after it.
	s := New("127.0.0.1:1", "chat@example.com", "", "")

	err := s.Send(t.Context(), domain.Mail{
		To:      "ada@example.com\r\nBcc: attacker@example.com",
		Subject: "Reset",
	})
	if !errors.Is(err, domain.ErrBadHeader) {
		t.Fatalf("Send = %v, want ErrBadHeader", err)
	}
}
