package domain_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/andygeiss/baseline-reference/v3/internal/domain"
)

func TestValidateName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want error
	}{
		{"a plain name", "Ada", nil},
		{"digits and joiners", "ada_lovelace-1", nil},
		{"a space inside", "Ada Lovelace", nil},
		{"accents are letters", "Zoë", nil},
		{"so is any other script", "李雷", nil},
		{"empty", "", domain.ErrNameEmpty},
		{"too short", "A", domain.ErrNameTooShort},
		{"too long", strings.Repeat("a", 25), domain.ErrNameTooLong},
		{"punctuation", "Ada!", domain.ErrNameCharacters},
		{"a slash could reach a URL", "ada/bob", domain.ErrNameCharacters},
		{"angle brackets", "<b>", domain.ErrNameCharacters},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := domain.ValidateName(tt.in); !errors.Is(got, tt.want) {
				t.Errorf("ValidateName(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

func TestNormalizeName(t *testing.T) {
	t.Parallel()

	tests := []struct{ in, want string }{
		{"  Ada  ", "Ada"},
		{"Ada\tLovelace", "Ada Lovelace"},
		{"Ada   Lovelace", "Ada Lovelace"},
		{"   ", ""},
	}

	for _, tt := range tests {
		if got := domain.NormalizeName(tt.in); got != tt.want {
			t.Errorf("NormalizeName(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestValidatePassword(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want error
	}{
		{"long enough", "correct-horse", nil},
		{"exactly the floor", "12345678", nil},
		{"one short", "1234567", domain.ErrPasswordTooShort},
		{"too long", strings.Repeat("a", 129), domain.ErrPasswordTooLong},
		// Spaces are part of the secret, so a password of spaces is a password.
		{"only spaces, but enough of them", "         ", nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := domain.ValidatePassword(tt.in); !errors.Is(got, tt.want) {
				t.Errorf("ValidatePassword(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

func TestSlug(t *testing.T) {
	t.Parallel()

	tests := []struct{ in, want string }{
		{"General", "general"},
		{"General Chat", "general-chat"},
		{"  General   Chat  ", "general-chat"},
		{"Go 1.26!", "go-1-26"},
		{"---", ""},
		{"", ""},
		// Only ASCII survives: keeping Unicode would mean percent encoding in
		// every link and a URL nobody can read aloud.
		{"Zoë", "zo"},
		{"李雷", ""},
		{"a---b", "a-b"},
		{"trailing-", "trailing"},
	}

	for _, tt := range tests {
		if got := domain.Slug(tt.in); got != tt.want {
			t.Errorf("Slug(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestValidateRoomName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want error
	}{
		{"a plain name", "General", nil},
		{"empty", "", domain.ErrRoomNameEmpty},
		{"too long", strings.Repeat("a", 41), domain.ErrRoomNameTooLong},
		{"nothing a URL can carry", "!!!", domain.ErrRoomNameUnslugable},
		{"a name only a route can have", "New", domain.ErrRoomNameReserved},
		{"and it is not about capitals", "new", domain.ErrRoomNameReserved},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := domain.ValidateRoomName(tt.in); !errors.Is(got, tt.want) {
				t.Errorf("ValidateRoomName(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

func TestNewRoomTakesItsAddressFromItsName(t *testing.T) {
	t.Parallel()

	room, err := domain.NewRoom("id-1", "  General   Chat  ")
	if err != nil {
		t.Fatalf("NewRoom: %v", err)
	}
	if room.Name != "General Chat" {
		t.Errorf("Name = %q, want %q", room.Name, "General Chat")
	}
	if room.Slug != "general-chat" {
		t.Errorf("Slug = %q, want %q", room.Slug, "general-chat")
	}
}

func TestNormalizeBody(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want string
	}{
		{"trims the outside", "  hello  ", "hello"},
		// Line breaks are how people write lists and paste code, so the inside
		// is left alone — unlike a name, which is collapsed to one line.
		{"keeps the line breaks", "one\ntwo", "one\ntwo"},
		{"keeps a blank line", "one\n\ntwo", "one\n\ntwo"},
		{"drops trailing spaces per line", "one   \ntwo\t", "one\ntwo"},
		{"drops a stray carriage return", "one\r\ntwo", "one\ntwo"},
		{"whitespace only is nothing", " \n\t ", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := domain.NormalizeBody(tt.in); got != tt.want {
				t.Errorf("NormalizeBody(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestValidateBody(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want error
	}{
		{"something", "hello", nil},
		{"exactly the cap", strings.Repeat("x", 2000), nil},
		{"empty", "", domain.ErrBodyEmpty},
		{"one over", strings.Repeat("x", 2001), domain.ErrBodyTooLong},
		// Runes, not bytes: counting bytes would cut off people who write with
		// accents before the advertised limit.
		{"the cap counts characters", strings.Repeat("ü", 2000), nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := domain.ValidateBody(tt.in); !errors.Is(got, tt.want) {
				t.Errorf("ValidateBody(len %d) = %v, want %v", len(tt.in), got, tt.want)
			}
		})
	}
}

// TestLastSeq is the cursor rule: the newest message wins, and an empty answer
// leaves the reader where they were.
func TestLastSeq(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		msgs  []domain.Message
		since int64
		want  int64
	}{
		{"nothing new keeps the cursor", nil, 12, 12},
		{"one message moves it", []domain.Message{{Seq: 13}}, 12, 13},
		{"several move it to the newest", []domain.Message{{Seq: 13}, {Seq: 14}}, 12, 14},
		{"order does not matter", []domain.Message{{Seq: 14}, {Seq: 13}}, 12, 14},
		{"the cursor never goes backwards", []domain.Message{{Seq: 3}}, 12, 12},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := domain.LastSeq(tt.msgs, tt.since); got != tt.want {
				t.Errorf("LastSeq(%v, %d) = %d, want %d", tt.msgs, tt.since, got, tt.want)
			}
		})
	}
}

func TestValidateLabel(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want error
	}{
		{"a note to yourself", "laptop", nil},
		{"empty", "", domain.ErrLabelEmpty},
		{"too long", strings.Repeat("a", 41), domain.ErrLabelTooLong},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := domain.ValidateLabel(tt.in); !errors.Is(got, tt.want) {
				t.Errorf("ValidateLabel(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}
