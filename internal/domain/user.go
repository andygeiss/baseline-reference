package domain

import (
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"
)

var (
	ErrNameEmpty      = errors.New("name is empty")
	ErrNameTooShort   = errors.New("name is too short")
	ErrNameTooLong    = errors.New("name is too long")
	ErrNameCharacters = errors.New("name has characters that are not allowed")

	ErrPasswordTooShort = errors.New("password is too short")
	ErrPasswordTooLong  = errors.New("password is too long")
)

const (
	// MinNameLen and MaxNameLen bound a display name: long enough to be a
	// person, short enough to fit beside a message on a phone.
	//
	// The floor is two, not three, because a full name is two characters in
	// plenty of scripts — 李雷 is a whole name. A limit picked from English
	// nicknames would refuse a large part of the world by accident.
	MinNameLen = 2
	MaxNameLen = 24

	// MinPasswordLen is the OWASP floor. Length beats character classes, so
	// there is no rule here about digits or symbols.
	MinPasswordLen = 8

	// MaxPasswordLen bounds the work one request can ask for. argon2id costs
	// the same for any length, but hashing is not the only thing that touches
	// the string, and an unbounded field is an invitation.
	MaxPasswordLen = 128
)

// User is somebody who can post. The hash never leaves the store layer as
// anything but this field, and nothing renders it.
type User struct {
	ID           string
	Name         string
	PasswordHash string
}

// NormalizeName trims a name and collapses inner whitespace runs to one space.
func NormalizeName(name string) string {
	return strings.Join(strings.Fields(name), " ")
}

// ValidateName reports why a normalized name cannot be used, or nil when it
// can. Normalize first: this function reads "   " as a name of three spaces.
func ValidateName(name string) error {
	n := utf8.RuneCountInString(name)
	switch {
	case name == "":
		return ErrNameEmpty
	case n < MinNameLen:
		return ErrNameTooShort
	case n > MaxNameLen:
		return ErrNameTooLong
	}
	// Letters, digits, and the two joiners people expect. Unicode letters are
	// in: a name is a person's, not an identifier, so "Zoë" and "李雷" belong.
	for _, r := range name {
		switch {
		case unicode.IsLetter(r), unicode.IsDigit(r), r == '-', r == '_', r == ' ':
		default:
			return ErrNameCharacters
		}
	}
	return nil
}

// ValidatePassword reports why a password cannot be used, or nil when it can.
// The password is never normalized: every byte the person typed is the secret,
// including the spaces at either end.
func ValidatePassword(password string) error {
	switch {
	case utf8.RuneCountInString(password) < MinPasswordLen:
		return ErrPasswordTooShort
	case len(password) > MaxPasswordLen:
		return ErrPasswordTooLong
	}
	return nil
}

// NewUser returns a user with a normalized name, or an error when the name
// breaks a rule. The HTTP edge checks the same rules first, so it can name the
// field and word the message; this is where they are true whoever calls.
func NewUser(id, name, passwordHash string) (*User, error) {
	name = NormalizeName(name)
	if err := ValidateName(name); err != nil {
		return nil, err
	}
	return &User{ID: id, Name: name, PasswordHash: passwordHash}, nil
}
