// Package smtpmail sends mail through a relay. It is the adapter: the only
// package that knows this app's messages leave over SMTP.
package smtpmail

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"errors"
	"fmt"
	"mime"
	"net"
	"net/smtp"
	"net/textproto"
	"strings"
	"time"

	"github.com/andygeiss/baseline-reference/v5/internal/domain"
)

// Sender talks to one relay. Constructing it connects to nothing: boot
// validates what is local and stops there, so a relay being down is a message
// that waits in the outbox rather than a process that will not start.
type Sender struct {
	addr string // host:port
	from string
	host string // the base URL's host name: the right half of every Message-ID
	auth smtp.Auth
}

// New returns a sender for the relay at addr. A user of "" means the relay
// wants no credentials, which is the usual shape for a relay on localhost.
// host is the base URL's Hostname(): a domain this app owns, with no port.
func New(addr, from, user, password, host string) *Sender {
	s := &Sender{addr: addr, from: from, host: host}
	if user != "" {
		host, _, err := net.SplitHostPort(addr)
		if err != nil {
			host = addr // parseConfig already refused an addr without a port
		}
		s.auth = smtp.PlainAuth("", user, password, host)
	}
	return s
}

// Send delivers one message, or returns why it could not.
//
// net/smtp is frozen and its SendMail takes no context, so the connection is
// dialled here instead: a relay that accepts the TCP connection and then says
// nothing would otherwise hold this goroutine open past shutdown.
func (s *Sender) Send(ctx context.Context, m domain.Mail) error {
	if err := m.Validate(); err != nil {
		return fmt.Errorf("refusing a message: %w", err)
	}
	body := s.message(m, time.Now())

	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", s.addr)
	if err != nil {
		return fmt.Errorf("dialling the relay: %w", err)
	}
	defer conn.Close()
	if deadline, ok := ctx.Deadline(); ok {
		// The context stops mattering the moment net/smtp owns the socket, so
		// its deadline is copied onto the connection, which net/smtp honours.
		_ = conn.SetDeadline(deadline)
	}

	host, _, err := net.SplitHostPort(s.addr)
	if err != nil {
		return fmt.Errorf("reading the relay address: %w", err)
	}
	c, err := smtp.NewClient(conn, host)
	if err != nil {
		return said("greeting the relay", err)
	}
	defer c.Close()

	if ok, _ := c.Extension("STARTTLS"); ok {
		if err := c.StartTLS(&tls.Config{ServerName: host}); err != nil {
			return said("starting TLS with the relay", err)
		}
	}
	if s.auth != nil {
		// PlainAuth refuses to hand credentials to an unencrypted connection
		// unless the host is localhost. A relay that advertised no STARTTLS
		// fails here rather than leaking the password onto the wire.
		if err := c.Auth(s.auth); err != nil {
			return said("authenticating to the relay", err)
		}
	}
	if err := c.Mail(s.from); err != nil {
		return said("sending MAIL FROM", err)
	}
	if err := c.Rcpt(m.To); err != nil {
		return said("sending RCPT TO", err)
	}
	w, err := c.Data()
	if err != nil {
		return said("starting DATA", err)
	}
	// The writer net/smtp hands back is a textproto dot-writer: it doubles a
	// leading dot and turns a bare LF into CRLF. Doing either by hand here
	// would do it twice.
	if _, err := w.Write(body); err != nil {
		return said("writing the message", err)
	}
	if err := w.Close(); err != nil {
		return said("finishing the message", err)
	}
	if err := c.Quit(); err != nil {
		return said("ending the session", err)
	}
	return nil
}

// said wraps what went wrong at one step, and keeps a relay's own words out of
// it. A refusal can quote the recipient back — "550 5.1.1 <ada@example.com>:
// no such user" — and whatever this returns ends up in a log line, where no
// account deletion reaches it. So of a reply only the code is kept
// (patterns/go-email.md). Nothing else here names the recipient either.
func said(doing string, err error) error {
	if reply, ok := errors.AsType[*textproto.Error](err); ok {
		return fmt.Errorf("%s: the relay answered %d", doing, reply.Code)
	}
	// A reply with no code net/smtp can parse comes back quoted whole.
	if _, ok := errors.AsType[textproto.ProtocolError](err); ok {
		return fmt.Errorf("%s: the relay's reply could not be read", doing)
	}
	return fmt.Errorf("%s: %w", doing, err)
}

// message turns a mail into RFC 5322 bytes.
//
// Nothing here escapes a header value, because nothing here may need to:
// Validate has already refused a carriage return or newline in To and Subject,
// and From and the base URL are configured values whose boot checks refused a
// line break. That order is the rule — the checks run before anything writes a
// header, not after.
func (s *Sender) message(m domain.Mail, sent time.Time) []byte {
	var b bytes.Buffer
	fmt.Fprintf(&b, "From: %s\r\n", s.from)
	fmt.Fprintf(&b, "To: %s\r\n", m.To)
	// RFC 5322 requires the date, and receivers mark down a message with no id.
	// s.host has no port: an id wants a domain the app owns.
	fmt.Fprintf(&b, "Date: %s\r\n", sent.UTC().Format(time.RFC1123Z))
	fmt.Fprintf(&b, "Message-ID: <%s@%s>\r\n", rand.Text(), s.host)
	// QEncoding leaves plain ASCII alone and encodes anything else, so a room
	// name with an umlaut in the subject arrives as one.
	fmt.Fprintf(&b, "Subject: %s\r\n", mime.QEncoding.Encode("utf-8", m.Subject))
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/plain; charset=utf-8\r\n")
	b.WriteString("Content-Transfer-Encoding: 8bit\r\n\r\n")
	// A bare CR is the one thing the dot-writer passes through, and a relay that
	// reads it as a line end takes "\r.\r" for the end of the message.
	b.WriteString(strings.NewReplacer("\r\n", "\n", "\r", "\n").Replace(m.Text))
	return b.Bytes()
}
