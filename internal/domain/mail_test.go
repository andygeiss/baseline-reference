package domain_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/andygeiss/baseline-reference/v3/internal/domain"
)

func TestMailValidate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		mail domain.Mail
		want error
	}{
		{"an ordinary message", domain.Mail{To: "ada@example.com", Subject: "Reset"}, nil},
		{"no recipient", domain.Mail{Subject: "Reset"}, domain.ErrEmailEmpty},
		// The rule this type exists for. A newline in a header value lets
		// whoever supplied it write their own headers into the message.
		{
			"a newline in the recipient",
			domain.Mail{To: "ada@example.com\r\nBcc: attacker@example.com", Subject: "Reset"},
			domain.ErrBadHeader,
		},
		{
			"a newline in the subject",
			domain.Mail{To: "ada@example.com", Subject: "Reset\nX-Priority: 1"},
			domain.ErrBadHeader,
		},
		{
			// A bare carriage return counts too: some parsers treat it as a
			// line ending on its own.
			"a carriage return on its own",
			domain.Mail{To: "ada@example.com", Subject: "Reset\rX-Priority: 1"},
			domain.ErrBadHeader,
		},
		{
			// The body is not a header. It goes out through a dot-writer that
			// handles line endings, so newlines there are just text.
			"newlines in the body are fine",
			domain.Mail{To: "ada@example.com", Subject: "Reset", Text: "one\ntwo\n.\nthree"},
			nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if err := tt.mail.Validate(); !errors.Is(err, tt.want) {
				t.Errorf("Validate() = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestValidateEmail(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		email string
		want  error
	}{
		{"an ordinary address", "ada@example.com", nil},
		{"a plus tag", "ada+chat@example.com", nil},
		{"empty", "", domain.ErrEmailEmpty},
		{"no at sign", "ada.example.com", domain.ErrEmailInvalid},
		{"nothing before the at sign", "@example.com", domain.ErrEmailInvalid},
		{"nothing after the at sign", "ada@", domain.ErrEmailInvalid},
		{"a space", "ada @example.com", domain.ErrEmailInvalid},
		{"a newline", "ada@example.com\r\nBcc: x", domain.ErrEmailInvalid},
		{"too long", strings.Repeat("a", 250) + "@example.com", domain.ErrEmailTooLong},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if err := domain.ValidateEmail(tt.email); !errors.Is(err, tt.want) {
				t.Errorf("ValidateEmail(%q) = %v, want %v", tt.email, err, tt.want)
			}
		})
	}
}

func TestNormalizeEmail(t *testing.T) {
	t.Parallel()
	if got := domain.NormalizeEmail("  Ada@Example.COM "); got != "ada@example.com" {
		t.Errorf("NormalizeEmail = %q, want it trimmed and folded", got)
	}
}
