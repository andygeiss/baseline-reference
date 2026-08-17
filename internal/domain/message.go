package domain

import (
	"errors"
	"strings"
	"time"
	"unicode/utf8"
)

var (
	ErrBodyEmpty   = errors.New("message is empty")
	ErrBodyTooLong = errors.New("message is too long")
)

// MaxBodyLen caps a message at 2000 runes. Past that it is a document, and
// somebody should send a link.
const MaxBodyLen = 2000

// MaxPageLen caps how many messages one read returns. A reader who left the tab
// open over lunch comes back to one bounded response, not the whole room.
const MaxPageLen = 200

// Message is one thing somebody said.
//
// Seq is the message's identity and the cursor a reader polls with. It comes
// from an AUTOINCREMENT column, so it only ever grows: a timestamp would put
// two messages sent in the same second in random order, and a poll would then
// repeat one or skip one.
type Message struct {
	Seq       int64
	RoomID    string
	AuthorID  string
	Author    string // the author's display name, joined on read
	Body      string
	CreatedAt time.Time

	// Attachment is the file hanging on this message, or nil. It is read in the
	// same query: one file per message, so the join that reads a room stays a
	// join and a page of messages is still one round trip.
	Attachment *Attachment
}

// NormalizeBody trims the whitespace around a message and drops the trailing
// spaces on every line.
//
// Unlike a name, the inside is left alone: line breaks are how people write
// lists and paste code, so collapsing them would rewrite what somebody said.
func NormalizeBody(body string) string {
	lines := strings.Split(body, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimRight(line, " \t\r")
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

// ValidateBody reports why a normalized message cannot be posted, or nil when
// it can. Normalize first: this function reads "   " as a message of three
// spaces.
func ValidateBody(body string) error {
	switch {
	case body == "":
		return ErrBodyEmpty
	// Runes, not bytes: len("ü") is 2, so counting bytes would cut off people
	// who write with accents before the advertised limit.
	case utf8.RuneCountInString(body) > MaxBodyLen:
		return ErrBodyTooLong
	}
	return nil
}

// NewMessage returns a message with a normalized body, or an error when the
// body breaks a rule. The HTTP edge checks the same rules first, so it can name
// the field and word the message; this is where they are true whoever calls.
//
// att may be nil. When it is not, the body may be empty: a picture on its own
// is a message, and asking for a caption before accepting one would be a rule
// invented by the validator rather than by the chat.
func NewMessage(roomID, authorID, body string, att *Attachment) (*Message, error) {
	body = NormalizeBody(body)
	if err := ValidateBody(body); err != nil && !(att != nil && errors.Is(err, ErrBodyEmpty)) {
		return nil, err
	}
	return &Message{RoomID: roomID, AuthorID: authorID, Body: body, Attachment: att}, nil
}

// LastSeq returns the cursor a reader polls with after seeing msgs: the
// sequence number of the newest one, or since when there were none.
func LastSeq(msgs []Message, since int64) int64 {
	for _, m := range msgs {
		if m.Seq > since {
			since = m.Seq
		}
	}
	return since
}
