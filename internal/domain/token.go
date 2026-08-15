package domain

import (
	"errors"
	"strings"
	"unicode/utf8"
)

var (
	ErrLabelEmpty   = errors.New("label is empty")
	ErrLabelTooLong = errors.New("label is too long")
)

// MaxLabelLen caps a token label at 40 runes. It is a note to yourself —
// "laptop", "build server" — not a description.
const MaxLabelLen = 40

// Token is a credential a program signs in with. The secret is shown once, at
// creation, and never stored: Hash is its SHA-256, which is all the server
// needs to recognize it again.
type Token struct {
	ID         string
	UserID     string
	Hash       string
	Label      string
	CreatedAt  string
	LastUsedAt string // empty until the token is first used
}

// NormalizeLabel trims a label and collapses inner whitespace runs to one space.
func NormalizeLabel(label string) string {
	return strings.Join(strings.Fields(label), " ")
}

// ValidateLabel reports why a normalized label cannot be used, or nil when it
// can.
func ValidateLabel(label string) error {
	switch {
	case label == "":
		return ErrLabelEmpty
	case utf8.RuneCountInString(label) > MaxLabelLen:
		return ErrLabelTooLong
	}
	return nil
}
