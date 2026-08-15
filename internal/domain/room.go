package domain

import (
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"
)

var (
	ErrRoomNameEmpty   = errors.New("room name is empty")
	ErrRoomNameTooLong = errors.New("room name is too long")

	// ErrRoomNameUnslugable means the name is made only of characters that
	// cannot appear in a URL, so there is no address left to give the room.
	ErrRoomNameUnslugable = errors.New("room name has no letters or digits")

	// ErrRoomNameReserved means the name slugs to an address that already
	// belongs to a route.
	ErrRoomNameReserved = errors.New("room name is reserved")
)

// MaxRoomNameLen caps a room name at 40 runes — a heading on a phone.
const MaxRoomNameLen = 40

// reservedSlugs are the addresses under /rooms/ that are not rooms. A room
// slugging to one of these would be shadowed by that route and unreachable
// forever, so it is refused at the door instead.
var reservedSlugs = map[string]bool{"new": true}

// Room is one conversation. Slug is what the URL carries; Name is what people
// read.
type Room struct {
	ID   string
	Slug string
	Name string
}

// NormalizeRoomName trims a room name and collapses inner whitespace runs to
// one space.
func NormalizeRoomName(name string) string {
	return strings.Join(strings.Fields(name), " ")
}

// ValidateRoomName reports why a normalized room name cannot be used, or nil
// when it can.
func ValidateRoomName(name string) error {
	switch {
	case name == "":
		return ErrRoomNameEmpty
	case utf8.RuneCountInString(name) > MaxRoomNameLen:
		return ErrRoomNameTooLong
	case Slug(name) == "":
		return ErrRoomNameUnslugable
	case reservedSlugs[Slug(name)]:
		return ErrRoomNameReserved
	}
	return nil
}

// Slug turns a room name into the part of the URL that names it: lowercase
// letters and digits, every other run replaced by one hyphen.
//
// Only ASCII letters and digits survive. Keeping Unicode would mean percent
// encoding in every link and a URL nobody can read aloud, and a room name is
// already free to say anything — the slug is only its address.
func Slug(name string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(name) {
		switch {
		case r <= unicode.MaxASCII && (unicode.IsLetter(r) || unicode.IsDigit(r)):
			b.WriteRune(r)
			dash = false
		case !dash && b.Len() > 0:
			b.WriteByte('-')
			dash = true
		}
	}
	return strings.TrimSuffix(b.String(), "-")
}

// NewRoom returns a room with a normalized name and its slug, or an error when
// the name breaks a rule.
func NewRoom(id, name string) (*Room, error) {
	name = NormalizeRoomName(name)
	if err := ValidateRoomName(name); err != nil {
		return nil, err
	}
	return &Room{ID: id, Slug: Slug(name), Name: name}, nil
}
