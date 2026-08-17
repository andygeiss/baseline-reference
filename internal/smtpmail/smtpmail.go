// Package smtpmail sends mail through a relay. It is the adapter: the only
// package that knows this app's messages leave over SMTP.
package smtpmail

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"mime"
	"net"
	"net/smtp"

	"github.com/andygeiss/baseline-reference/v3/internal/domain"
)

// Sender talks to one relay. Constructing it connects to nothing: boot
// validates what is local and stops there, so a relay being down is a message
// that waits in the outbox rather than a process that will not start.
type Sender struct {
	addr string // host:port
	from string
	auth smtp.Auth
}

// New returns a sender for the relay at addr. A user of "" means the relay
// wants no credentials, which is the usual shape for a relay on localhost.
func New(addr, from, user, password string) *Sender {
	s := &Sender{addr: addr, from: from}
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
		return fmt.Errorf("sending to %q: %w", m.To, err)
	}
	body := s.render(m)

	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", s.addr)
	if err != nil {
		return fmt.Errorf("dialling relay %s: %w", s.addr, err)
	}
	defer conn.Close()
	if deadline, ok := ctx.Deadline(); ok {
		// The context stops mattering the moment net/smtp owns the socket, so
		// its deadline is copied onto the connection, which net/smtp honours.
		_ = conn.SetDeadline(deadline)
	}

	host, _, err := net.SplitHostPort(s.addr)
	if err != nil {
		return fmt.Errorf("relay address %q: %w", s.addr, err)
	}
	c, err := smtp.NewClient(conn, host)
	if err != nil {
		return fmt.Errorf("greeting relay %s: %w", s.addr, err)
	}
	defer c.Close()

	if ok, _ := c.Extension("STARTTLS"); ok {
		if err := c.StartTLS(&tls.Config{ServerName: host}); err != nil {
			return fmt.Errorf("starting TLS with relay %s: %w", s.addr, err)
		}
	}
	if s.auth != nil {
		// PlainAuth refuses to hand credentials to an unencrypted connection
		// unless the host is localhost. A relay that advertised no STARTTLS
		// fails here rather than leaking the password onto the wire.
		if err := c.Auth(s.auth); err != nil {
			return fmt.Errorf("authenticating to relay %s: %w", s.addr, err)
		}
	}
	if err := c.Mail(s.from); err != nil {
		return fmt.Errorf("sending MAIL FROM to %s: %w", s.addr, err)
	}
	if err := c.Rcpt(m.To); err != nil {
		return fmt.Errorf("sending RCPT TO %q: %w", m.To, err)
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("starting DATA with %s: %w", s.addr, err)
	}
	// The writer net/smtp hands back is a textproto dot-writer: it escapes a
	// line that is a single dot and fixes the line endings. Doing either by
	// hand here would do it twice.
	if _, err := w.Write(body); err != nil {
		return fmt.Errorf("writing the message to %s: %w", s.addr, err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("finishing the message to %s: %w", s.addr, err)
	}
	return c.Quit()
}

// render turns a message into RFC 5322 bytes.
//
// Nothing here escapes a header value, because nothing here may need to:
// Validate has already refused a carriage return or newline in To and Subject,
// and From is a configured value the boot check refused a line break in. That
// order is the rule — the check runs before anything writes a header, not
// after.
func (s *Sender) render(m domain.Mail) []byte {
	var b bytes.Buffer
	fmt.Fprintf(&b, "From: %s\r\n", s.from)
	fmt.Fprintf(&b, "To: %s\r\n", m.To)
	// QEncoding leaves plain ASCII alone and encodes anything else, so a room
	// name with an umlaut in the subject arrives as one.
	fmt.Fprintf(&b, "Subject: %s\r\n", mime.QEncoding.Encode("utf-8", m.Subject))
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/plain; charset=utf-8\r\n\r\n")
	b.WriteString(m.Text)
	return b.Bytes()
}
