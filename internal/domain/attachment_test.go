package domain_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/andygeiss/baseline-reference/v3/internal/domain"
)

func TestCleanFileName(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"an ordinary name is left alone", "screenshot.png", "screenshot.png"},
		{"a unix path keeps only its last part", "/etc/passwd", "passwd"},
		{"a traversal keeps only its last part", "../../etc/passwd", "passwd"},
		// A browser on Windows sends the separator this process does not know
		// about, which is why filepath.Base is not the fix.
		{"a windows path keeps only its last part", `C:\Users\ada\notes.txt`, "notes.txt"},
		{"a newline is dropped", "notes\r\nBcc: x.txt", "notesBcc: x.txt"},
		{"a quote is dropped", `report "final".pdf`, "report final.pdf"},
		{"trailing dots and spaces go", "  archive.zip.  ", "archive.zip"},
		{"a name made only of separators becomes a name", "///", "file"},
		{"an empty name becomes a name", "", "file"},
		{"a very long name is cut", strings.Repeat("a", 300), strings.Repeat("a", domain.MaxAttachmentNameLen)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := domain.CleanFileName(tt.in); got != tt.want {
				t.Errorf("CleanFileName(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestNewAttachment(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		kind string
		size int64
		want error
	}{
		{"a png is fine", "image/png", 10, nil},
		{"a text file is fine", "text/plain; charset=utf-8", 10, nil},
		{"an empty file is not a file", "image/png", 0, domain.ErrAttachmentEmpty},
		{"past the cap is refused", "image/png", domain.MaxAttachmentBytes + 1, domain.ErrAttachmentBig},
		{"an unknown type is refused", "application/octet-stream", 10, domain.ErrAttachmentType},
		// The exact set is the rule. A prefix test on "image/" would let this
		// through, and an SVG is a document that can carry script.
		{"svg is refused however it arrives", "image/svg+xml", 10, domain.ErrAttachmentType},
		{"html is refused", "text/html; charset=utf-8", 10, domain.ErrAttachmentType},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := domain.NewAttachment("id", "ada", "f.bin", tt.kind, tt.size)
			if !errors.Is(err, tt.want) {
				t.Fatalf("NewAttachment(%q, %d) = %v, want %v", tt.kind, tt.size, err, tt.want)
			}
			if tt.want == nil && got.Kind != tt.kind {
				t.Errorf("Kind = %q, want the type it was given", got.Kind)
			}
		})
	}
}

// TestInlineIsShorterThanAllowed pins the two lists apart. Everything the app
// renders in a page it also accepts; the other way round is what would put a
// PDF or a log file into an <img>.
func TestInlineIsShorterThanAllowed(t *testing.T) {
	t.Parallel()
	if len(domain.InlineAttachment) >= len(domain.AllowedAttachment) {
		t.Errorf("%d inline types of %d allowed — inline must be the shorter list",
			len(domain.InlineAttachment), len(domain.AllowedAttachment))
	}
	for kind := range domain.InlineAttachment {
		if !domain.AllowedAttachment[kind] {
			t.Errorf("%q is rendered inline but is not accepted", kind)
		}
		if !strings.HasPrefix(kind, "image/") {
			t.Errorf("%q is rendered inline and is not an image", kind)
		}
	}
}
