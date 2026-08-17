package app

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"

	"github.com/andygeiss/baseline-reference/v3/internal/domain"
)

// multipartMemory is how much of an upload net/http keeps in memory before it
// spills the rest into os.TempDir().
//
// It is set to the whole cap on purpose, so nothing ever spills: this app's
// files end up in a BLOB, and a temp file that only exists for the length of
// one handler is a thing to get wrong for no gain. net/http removes those files
// when the handler returns, which is also why nothing may hold a multipart
// reader past the request.
const multipartMemory = domain.MaxAttachmentBytes

// parseUpload reads a body that may carry a file.
//
// ParseForm is not enough here. On a multipart request it leaves PostForm
// non-nil and empty, and every PostFormValue after it then answers "" — the
// field is there on the wire and gone in Go, with no error anywhere.
func (a *App) parseUpload(w http.ResponseWriter, r *http.Request) bool {
	err := r.ParseMultipartForm(multipartMemory)
	if errors.Is(err, http.ErrNotMultipart) {
		return a.parseForm(w, r) // a plain form post: no file to read
	}
	if err != nil {
		status := http.StatusBadRequest // malformed body — not a validation failure
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			status = http.StatusRequestEntityTooLarge // the route's own cap, in middleware.go
		}
		a.clientError(w, r, status)
		return false
	}
	return true
}

// attachmentFrom reads the uploaded file, if there is one. It returns nil for
// both attachment and bytes when the sender attached nothing, and a domain
// error when they attached something this app will not store.
//
// Two things the browser sent are ignored on purpose. The filename never
// becomes a path: the stored identity is a fresh random id, and the name is
// kept only for the download header. The declared Content-Type is never read
// at all: it describes a file somebody picked on their own machine, so it is
// user input, and the bytes are what decide.
func (a *App) attachmentFrom(r *http.Request, uploaderID string) (*domain.Attachment, []byte, error) {
	f, fh, err := r.FormFile("file")
	// Two ways to have sent no file, and both are ordinary. ErrMissingFile is a
	// multipart form with the field left empty; ErrNotMultipart is a plain
	// urlencoded post, which is what the JSON-free fallback and every test that
	// only types a message send.
	if errors.Is(err, http.ErrMissingFile) || errors.Is(err, http.ErrNotMultipart) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, fmt.Errorf("reading the uploaded file: %w", err)
	}
	defer f.Close()

	// The whole file, in one read, capped one byte past the limit so that going
	// over is a rule the domain refuses rather than a silent truncation.
	//
	// Reading it all is also why there is no seek back to the start here: the
	// usual trap is sniffing the first 512 bytes off a stream and then storing
	// the rest without rewinding, which loses exactly those bytes from every
	// file bigger than the sniff.
	bs, err := io.ReadAll(io.LimitReader(f, domain.MaxAttachmentBytes+1))
	if err != nil {
		return nil, nil, fmt.Errorf("reading the uploaded file: %w", err)
	}

	// DetectContentType looks at the first 512 bytes, always answers, and falls
	// back to application/octet-stream. Its answer is what gets stored and what
	// the download sends back.
	kind := http.DetectContentType(bs)
	att, err := domain.NewAttachment(rand.Text(), uploaderID, fh.Filename, kind, int64(len(bs)))
	if err != nil {
		return nil, nil, err
	}
	return att, bs, nil
}

// attachmentProblem words a refused upload for whoever sent it.
func attachmentProblem(err error) string {
	switch {
	case errors.Is(err, domain.ErrAttachmentBig):
		return fmt.Sprintf("Attach a file under %d MB.", domain.MaxAttachmentBytes>>20)
	case errors.Is(err, domain.ErrAttachmentEmpty):
		return "That file is empty."
	default:
		return "Attach a picture, a PDF, or a text file."
	}
}

// handleFileShow serves one attachment.
//
// Every download comes through here rather than through a file server. A file
// server names no reader, and it types a file by its extension — which is the
// sender's text, not a fact about the bytes.
func (a *App) handleFileShow(w http.ResponseWriter, r *http.Request) {
	// Resolving the room first keeps the URL honest: a file is reached through
	// the room it was posted in, so a wrong slug is a 404 before anything reads
	// the file table.
	if _, ok := a.room(w, r); !ok {
		return
	}
	file, content, err := a.attachments.Open(r.Context(), r.PathValue("id"))
	if errors.Is(err, domain.ErrNotFound) {
		a.clientError(w, r, http.StatusNotFound)
		return
	}
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	defer content.Close()

	// The type sniffed when it was uploaded, never one guessed from the name.
	// secureHeaders already sends X-Content-Type-Options: nosniff on every
	// response, and that is what makes this header binding rather than a hint.
	w.Header().Set("Content-Type", file.Kind)
	if !file.Inline() {
		// Anything this app does not render in a page of its own is a download,
		// so a browser that would have displayed it saves it instead.
		//
		// FormatMediaType quotes the name and RFC 2231-encodes a non-ASCII one.
		// Writing it raw would break the first download of a file called
		// report "final".pdf, and would be header injection for a worse name.
		w.Header().Set("Content-Disposition",
			mime.FormatMediaType("attachment", map[string]string{"filename": file.Name}))
	}
	w.Header().Set("Cache-Control", "private, max-age=300")
	// The name is empty because Content-Type is already set: ServeContent only
	// consults the name when it is not, and an extension must never overrule
	// the type the bytes were sniffed as.
	http.ServeContent(w, r, "", file.CreatedAt, content)
}

// handleFileDelete removes an attachment the signed-in reader uploaded.
//
// Somebody else's file answers exactly like one that was never there: the same
// redirect and the same words. Anything that distinguished them would turn the
// id space into a directory to walk (patterns/go-authorization.md rule 4).
func (a *App) handleFileDelete(w http.ResponseWriter, r *http.Request) {
	room, ok := a.room(w, r)
	if !ok {
		return
	}
	err := a.attachments.Delete(r.Context(), userFrom(r.Context()).ID, r.PathValue("id"))
	if err != nil && !errors.Is(err, domain.ErrNotFound) {
		a.serverError(w, r, err)
		return
	}
	a.flash(r, "That file is gone.")
	a.redirect(w, r, "/rooms/"+room.Slug)
}
