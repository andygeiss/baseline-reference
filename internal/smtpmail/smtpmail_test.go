package smtpmail

import (
	"bufio"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/andygeiss/baseline-reference/v5/internal/domain"
)

// The adapter is tested at its edges: what it puts on the wire, and what it
// refuses to put there. Nothing here talks to a relay — a test that needed one
// would be testing the relay.

// sent is the instant every rendered message here carries, so its Date header
// is a string a test can compare.
var sent = time.Date(2026, time.November, 10, 9, 14, 2, 0, time.FixedZone("NZDT", 13*60*60))

func TestMessage(t *testing.T) {
	t.Parallel()
	s := New("mail.example.com:587", "chat@example.com", "", "", "chat.example.com")

	got := string(s.message(domain.Mail{
		To:      "ada@example.com",
		Subject: "Reset your Go Chat password",
		Text:    "Open this link:\nhttps://chat.example.com/reset/confirm?t=abc\n",
	}, sent))

	for _, want := range []string{
		"From: chat@example.com\r\n",
		"To: ada@example.com\r\n",
		"Date: Mon, 09 Nov 2026 20:14:02 +0000\r\n", // the instant, in UTC
		"Subject: Reset your Go Chat password\r\n",
		"Content-Type: text/plain; charset=utf-8\r\n",
		"Content-Transfer-Encoding: 8bit\r\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the message is missing %q:\n%s", want, got)
		}
	}
	// An id under the app's own host name, and a new one for every message.
	id := func(msg string) string {
		_, rest, _ := strings.Cut(msg, "Message-ID: <")
		id, _, _ := strings.Cut(rest, ">\r\n")
		return id
	}
	first := id(got)
	if local, host, ok := strings.Cut(first, "@"); !ok || local == "" || host != "chat.example.com" {
		t.Errorf("Message-ID = <%s>, want <something@chat.example.com>", first)
	}
	if again := id(string(s.message(domain.Mail{To: "ada@example.com"}, sent))); again == first {
		t.Errorf("two messages share the id <%s>", first)
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

// A bare CR is the one byte the dot-writer passes through, and a relay that
// reads it as a line end takes "\r.\r" for the end of the message
// (patterns/go-email.md). The body leaves without one.
func TestMessageLeavesNoBareCarriageReturn(t *testing.T) {
	t.Parallel()
	s := New("mail.example.com:587", "chat@example.com", "", "", "chat.example.com")
	got := string(s.message(domain.Mail{
		To:   "ada@example.com",
		Text: "one\rtwo\r\nthree\r.\rMAIL FROM:<attacker@example.com>\r",
	}, sent))

	_, body, _ := strings.Cut(got, "\r\n\r\n")
	if strings.Contains(body, "\r") {
		t.Errorf("the body still holds a carriage return: %q", body)
	}
	if want := "one\ntwo\nthree\n.\nMAIL FROM:<attacker@example.com>\n"; body != want {
		t.Errorf("body = %q, want %q", body, want)
	}
}

func TestMessageEncodesANonASCIISubject(t *testing.T) {
	t.Parallel()
	s := New("mail.example.com:587", "chat@example.com", "", "", "chat.example.com")
	got := string(s.message(domain.Mail{To: "ada@example.com", Subject: "Grüße aus Köln"}, sent))

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
	s := New("127.0.0.1:1", "chat@example.com", "", "", "chat.example.com")

	err := s.Send(t.Context(), domain.Mail{
		To:      "ada@example.com\r\nBcc: attacker@example.com",
		Subject: "Reset",
	})
	if !errors.Is(err, domain.ErrBadHeader) {
		t.Fatalf("Send = %v, want ErrBadHeader", err)
	}
}

// TestSendKeepsTheRelaysWordsOutOfTheError is why Send returns a reply's code
// and nothing else of it: a relay quotes the recipient back when it refuses
// one, and the error ends up in a log line. The relay here is a listener on
// this machine that plays one refusal — a script, not a relay.
func TestSendKeepsTheRelaysWordsOutOfTheError(t *testing.T) {
	t.Parallel()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		r := bufio.NewReader(conn)
		say := func(line string) { _, _ = conn.Write([]byte(line + "\r\n")) }
		say("220 relay.test ESMTP")
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			switch verb := strings.ToUpper(line); {
			case strings.HasPrefix(verb, "EHLO"):
				say("250 relay.test")
			case strings.HasPrefix(verb, "MAIL FROM"):
				say("250 2.1.0 ok")
			case strings.HasPrefix(verb, "RCPT TO"):
				say("550 5.1.1 <ada@example.com>: no such user here")
			default:
				say("221 2.0.0 bye")
				return
			}
		}
	}()

	s := New(ln.Addr().String(), "chat@example.com", "", "", "chat.example.com")
	err = s.Send(t.Context(), domain.Mail{To: "ada@example.com", Subject: "Reset", Text: "link"})
	if err == nil {
		t.Fatal("Send = nil, want the refusal")
	}
	if !strings.Contains(err.Error(), "550") {
		t.Errorf("the error lost the reply's code: %v", err)
	}
	for _, leak := range []string{"ada@example.com", "no such user"} {
		if strings.Contains(err.Error(), leak) {
			t.Errorf("the error carries %q: %v", leak, err)
		}
	}
}
