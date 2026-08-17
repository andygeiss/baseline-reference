package domain

import (
	"errors"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

var (
	ErrAttachmentEmpty = errors.New("file is empty")
	ErrAttachmentType  = errors.New("file type is not allowed")
	ErrAttachmentBig   = errors.New("file is too big")
)

// MaxAttachmentBytes caps one attachment at 2 MiB. Big enough for a screenshot
// or a log, small enough that the bytes sit in the database beside the message
// without turning the daily snapshot into a chore.
const MaxAttachmentBytes = 2 << 20

// MaxAttachmentNameLen caps the name kept for the download header. The name is
// the sender's text, so it is bounded like any other text they send.
const MaxAttachmentNameLen = 100

// AllowedAttachment is the exact set of types this app stores, keyed by what
// http.DetectContentType answers. An exact set, never a prefix test on
// "image/": a prefix admits every type a future sniffer learns, and it admits
// image/svg+xml — which is a document that can carry script, not a picture.
var AllowedAttachment = map[string]bool{
	"image/png":                 true,
	"image/jpeg":                true,
	"image/gif":                 true,
	"image/webp":                true,
	"application/pdf":           true,
	"text/plain; charset=utf-8": true,
}

// InlineAttachment is the shorter list: the types this app renders inside its
// own pages, in an <img>. Everything else is sent as an attachment, so a
// browser that would have displayed it downloads it instead.
var InlineAttachment = map[string]bool{
	"image/png":  true,
	"image/jpeg": true,
	"image/gif":  true,
	"image/webp": true,
}

// Attachment is one file hanging on one message.
//
// ID identifies the bytes. Name is what the browser called the file, kept for
// the download header and never used as a path — see CleanFileName.
type Attachment struct {
	ID         string
	MessageSeq int64
	UploaderID string
	Name       string
	Kind       string
	Size       int64
	CreatedAt  time.Time
}

// Inline reports whether the app renders this attachment in the page itself.
func (a Attachment) Inline() bool { return InlineAttachment[a.Kind] }

// CleanFileName turns whatever the browser sent into a name safe to put in a
// header and show on a page.
//
// filepath.Base is not enough on its own: it knows the separator this process
// runs on, and a browser on Windows sends the other one. Both are cut here, and
// so is every control character — a newline in this string would be a second
// header in the download response.
func CleanFileName(name string) string {
	if i := strings.LastIndexAny(name, `/\`); i >= 0 {
		name = name[i+1:]
	}
	name = strings.Map(func(r rune) rune {
		if r == utf8.RuneError || unicode.IsControl(r) || r == '"' {
			return -1
		}
		return r
	}, name)
	name = strings.Trim(name, " .")
	if n := []rune(name); len(n) > MaxAttachmentNameLen {
		name = string(n[:MaxAttachmentNameLen])
	}
	if name == "" {
		return "file" // "" is a name no download header can carry
	}
	return name
}

// NewAttachment returns an attachment for bytes that have already been sniffed,
// or an error saying why they cannot be stored.
//
// The caller passes the sniffed type, not the browser's claim: deciding that is
// the reading of the bytes, and this function is the rule about the answer.
func NewAttachment(id, uploaderID, name, kind string, size int64) (*Attachment, error) {
	switch {
	case size == 0:
		return nil, ErrAttachmentEmpty
	case size > MaxAttachmentBytes:
		return nil, ErrAttachmentBig
	case !AllowedAttachment[kind]:
		return nil, ErrAttachmentType
	}
	return &Attachment{
		ID:         id,
		UploaderID: uploaderID,
		Name:       CleanFileName(name),
		Kind:       kind,
		Size:       size,
	}, nil
}
