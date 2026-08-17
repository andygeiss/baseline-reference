package domain

import (
	"errors"
	"strings"
	"unicode/utf8"
)

var (
	// ErrBadHeader means a value bound for a mail header holds a line break.
	// A newline there lets whoever supplied the value write their own headers —
	// a Bcc, a second To — into a message this app signs with its own domain.
	ErrBadHeader = errors.New("header value has a line break")

	ErrEmailEmpty   = errors.New("email is empty")
	ErrEmailInvalid = errors.New("email does not look like an address")
	ErrEmailTooLong = errors.New("email is too long")
)

// MaxEmailLen is the longest address SMTP carries.
const MaxEmailLen = 254

// Mail is one message waiting to go out. Plain text only: an HTML body is a
// second thing to write, escape and test, and nothing this app sends needs one.
type Mail struct {
	To      string
	Subject string
	Text    string
}

// Validate reports why this message must not be sent, or nil when it may be.
//
// The header check is the load-bearing half and it runs on every header value,
// not only the ones that look like user input: To comes from a profile page and
// Subject is built with a room name in it often enough that "this one is ours"
// is a claim that goes stale.
func (m Mail) Validate() error {
	if m.To == "" {
		return ErrEmailEmpty
	}
	for _, v := range []string{m.To, m.Subject} {
		if strings.ContainsAny(v, "\r\n") {
			return ErrBadHeader
		}
	}
	return nil
}

// NormalizeEmail trims an address and folds it to lower case, which is how it
// is stored and compared.
func NormalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

// ValidateEmail reports why a normalized address cannot be stored, or nil when
// it can.
//
// The rule is deliberately thin: an address is valid when a mail server accepts
// it, and no pattern short of the whole of RFC 5322 predicts that. Anything
// stricter here refuses real addresses to catch typos that a delivery failure
// catches better.
func ValidateEmail(email string) error {
	at := strings.Index(email, "@")
	switch {
	case email == "":
		return ErrEmailEmpty
	case len(email) > MaxEmailLen:
		return ErrEmailTooLong
	case at <= 0 || at == len(email)-1:
		return ErrEmailInvalid
	case strings.ContainsAny(email, "\r\n \t"), !utf8.ValidString(email):
		return ErrEmailInvalid
	}
	return nil
}
