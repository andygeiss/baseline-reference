package app

import (
	"net/http"
	"strings"
	"testing"

	"github.com/andygeiss/baseline-reference/v3/internal/domain"
)

// Bytes that are what they claim to be, and bytes that are not.
var (
	pngBytes  = []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR\x00\x00\x00\x01")
	textBytes = []byte("goroutine 1 [running]:\nmain.main()\n")
	// An SVG is a document that can carry script. Nothing in Go's sniffing
	// table matches it as an image, which is the point of the test below.
	svgBytes = []byte(`<svg xmlns="http://www.w3.org/2000/svg" onload="alert(1)"></svg>`)
)

// oneFile signs somebody in, makes a room, and attaches one file to a message.
func oneFile(t *testing.T, ta *testApp, who, body string, file *upload) (slug string, res *http.Response) {
	t.Helper()
	ta.signUp(t, who, "correct horse battery")
	slug = ta.makeRoom(t, "General")
	res, _ = ta.postMessage(t, "/rooms/"+slug+"/messages", body, file, htmx())
	return slug, res
}

func TestUploadDecidesTheTypeFromTheBytes(t *testing.T) {
	t.Parallel()

	t.Run("a picture is stored under the type its bytes say", func(t *testing.T) {
		t.Parallel()
		ta := newTestApp(t)
		_, res := oneFile(t, ta, "Ada", "look at this", &upload{
			name: "shot.png", claims: "image/png", content: pngBytes,
		})
		if res.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200", res.StatusCode)
		}
		files := ta.attachments.all()
		if len(files) != 1 {
			t.Fatalf("stored %d files, want 1", len(files))
		}
		if files[0].Kind != "image/png" {
			t.Errorf("Kind = %q, want image/png", files[0].Kind)
		}
		if files[0].Size != int64(len(pngBytes)) {
			t.Errorf("Size = %d, want %d", files[0].Size, len(pngBytes))
		}
	})

	// The one that matters. The name says picture, the browser's declared type
	// says picture, and the bytes are a document that can carry script. Neither
	// of the first two is consulted, so what is stored is what the bytes are —
	// and text/plain is not something this app will ever put in an <img> or
	// hand back as a document.
	t.Run("an upload that lies about itself is stored as what it is", func(t *testing.T) {
		t.Parallel()
		ta := newTestApp(t)
		slug, res := oneFile(t, ta, "Ada", "totally a picture", &upload{
			name: "avatar.png", claims: "image/png", content: svgBytes,
		})
		if res.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200", res.StatusCode)
		}
		files := ta.attachments.all()
		if len(files) != 1 {
			t.Fatalf("stored %d files, want 1", len(files))
		}
		if got := files[0].Kind; got == "image/png" || strings.Contains(got, "svg") || strings.Contains(got, "html") {
			t.Fatalf("Kind = %q — the browser's claim or the extension won", got)
		}
		if files[0].Inline() {
			t.Error("the app would render this in a page, which is the whole hole")
		}

		// And the download proves it: the type the bytes were sniffed as, plus
		// the header that makes a browser save the file instead of opening it.
		down, _ := ta.do(t, http.MethodGet, "/rooms/"+slug+"/files/"+files[0].ID, nil, nil)
		if ct := down.Header.Get("Content-Type"); ct == "image/png" || strings.Contains(ct, "svg") {
			t.Errorf("Content-Type = %q, want the sniffed type", ct)
		}
		if cd := down.Header.Get("Content-Disposition"); !strings.HasPrefix(cd, "attachment") {
			t.Errorf("Content-Disposition = %q, want an attachment", cd)
		}
	})

	t.Run("bytes of no recognised type are refused", func(t *testing.T) {
		t.Parallel()
		ta := newTestApp(t)
		_, res := oneFile(t, ta, "Ada", "here", &upload{
			name: "report.pdf", claims: "application/pdf", content: []byte{0x00, 0x01, 0x02, 0x03, 0xff},
		})
		if res.StatusCode != http.StatusUnprocessableEntity {
			t.Fatalf("status = %d, want 422", res.StatusCode)
		}
		if files := ta.attachments.all(); len(files) != 0 {
			t.Errorf("stored %d files, want none", len(files))
		}
	})

	t.Run("a file too big for the cap is refused", func(t *testing.T) {
		t.Parallel()
		ta := newTestApp(t)
		big := make([]byte, domain.MaxAttachmentBytes+1)
		copy(big, pngBytes)
		_, res := oneFile(t, ta, "Ada", "big one", &upload{
			name: "huge.png", claims: "image/png", content: big,
		})
		if res.StatusCode != http.StatusUnprocessableEntity {
			t.Fatalf("status = %d, want 422", res.StatusCode)
		}
		if files := ta.attachments.all(); len(files) != 0 {
			t.Errorf("stored %d files, want none", len(files))
		}
	})
}

func TestUploadedNameNeverBecomesAPath(t *testing.T) {
	t.Parallel()
	ta := newTestApp(t)
	_, res := oneFile(t, ta, "Ada", "", &upload{
		name: `../../etc/passwd`, claims: "text/plain", content: textBytes,
	})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	files := ta.attachments.all()
	if len(files) != 1 {
		t.Fatalf("stored %d files, want 1", len(files))
	}
	if strings.ContainsAny(files[0].Name, `/\`) {
		t.Errorf("Name = %q, want no separators left in it", files[0].Name)
	}
	// The identity of the bytes is the generated id, and it is not the name.
	if files[0].ID == files[0].Name || files[0].ID == "" {
		t.Errorf("ID = %q, Name = %q — the id must be generated", files[0].ID, files[0].Name)
	}
}

func TestDownloadRoundTripsTheBytes(t *testing.T) {
	t.Parallel()
	ta := newTestApp(t)
	slug, res := oneFile(t, ta, "Ada", "", &upload{
		name: "shot.png", claims: "image/png", content: pngBytes,
	})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	id := ta.attachments.all()[0].ID

	down, body := ta.do(t, http.MethodGet, "/rooms/"+slug+"/files/"+id, nil, nil)
	if down.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", down.StatusCode)
	}
	// Byte-identical, both ends. The usual way to lose the first 512 bytes is
	// to sniff them off a stream and store what is left.
	if body != string(pngBytes) {
		t.Errorf("downloaded %d bytes, want the %d that went in", len(body), len(pngBytes))
	}
	if ct := down.Header.Get("Content-Type"); ct != "image/png" {
		t.Errorf("Content-Type = %q, want image/png", ct)
	}
	// A picture this app renders in its own pages is not forced to download.
	if cd := down.Header.Get("Content-Disposition"); cd != "" {
		t.Errorf("Content-Disposition = %q, want none for an inline image", cd)
	}
}

func TestAFileIsReachedOnlyThroughAHandler(t *testing.T) {
	t.Parallel()
	ta := newTestApp(t)
	slug, _ := oneFile(t, ta, "Ada", "", &upload{
		name: "shot.png", claims: "image/png", content: pngBytes,
	})
	id := ta.attachments.all()[0].ID

	t.Run("a signed-out reader gets the sign-in page, not the file", func(t *testing.T) {
		// Same server, a client with no cookies: the download route is behind
		// the same requireAuth as every other page, because a file server would
		// have named nobody at all.
		signedIn := ta.client.Jar
		ta.client.Jar = emptyJar(t)
		t.Cleanup(func() { ta.client.Jar = signedIn })
		res, body := ta.do(t, http.MethodGet, "/rooms/"+slug+"/files/"+id, nil, nil)
		if res.StatusCode != http.StatusSeeOther {
			t.Fatalf("status = %d, want 303 to the sign-in page", res.StatusCode)
		}
		if strings.Contains(body, string(pngBytes)) {
			t.Error("the bytes came back to a signed-out reader")
		}
	})

	t.Run("a file that is not there is a 404", func(t *testing.T) {
		res, _ := ta.do(t, http.MethodGet, "/rooms/"+slug+"/files/nobody", nil, nil)
		if res.StatusCode != http.StatusNotFound {
			t.Errorf("status = %d, want 404", res.StatusCode)
		}
	})
}

// TestOnlyTheUploaderRemovesAFile is the two-user test for attachments: a
// second signed-in reader asks to delete the first one's file and gets the same
// answer they would get for a file that never existed.
func TestOnlyTheUploaderRemovesAFile(t *testing.T) {
	t.Parallel()
	ta := newTestApp(t)
	slug, _ := oneFile(t, ta, "Ada", "mine", &upload{
		name: "shot.png", claims: "image/png", content: pngBytes,
	})
	id := ta.attachments.all()[0].ID

	// Bob signs in on the same server, in his own browser.
	ta.client.Jar = emptyJar(t)
	ta.signUp(t, "Bob", "correct horse battery")

	res, _ := ta.do(t, http.MethodPost, "/rooms/"+slug+"/files/"+id+"/delete", nil, nil)
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("Bob deleting Ada's file: status = %d, want the ordinary 303", res.StatusCode)
	}
	if len(ta.attachments.all()) != 1 {
		t.Fatal("Bob deleted Ada's file")
	}

	// The answer for a file that is not there at all is the same one, word for
	// word: nothing about the id space is observable.
	missing, _ := ta.do(t, http.MethodPost, "/rooms/"+slug+"/files/nobody/delete", nil, nil)
	if missing.StatusCode != res.StatusCode ||
		missing.Header.Get("Location") != res.Header.Get("Location") {
		t.Errorf("a missing file answers %d %q and somebody else's answers %d %q — they must match",
			missing.StatusCode, missing.Header.Get("Location"),
			res.StatusCode, res.Header.Get("Location"))
	}

	// And Ada can remove her own.
	ta.client.Jar = emptyJar(t)
	ta.do(t, http.MethodPost, "/login", map[string][]string{
		"name": {"Ada"}, "password": {"correct horse battery"},
	}, nil)
	if res, _ := ta.do(t, http.MethodPost, "/rooms/"+slug+"/files/"+id+"/delete", nil, nil); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("Ada deleting her own file: status = %d, want 303", res.StatusCode)
	}
	if len(ta.attachments.all()) != 0 {
		t.Error("Ada's own file survived her delete")
	}
}

func TestTheRoomLinksItsAttachments(t *testing.T) {
	t.Parallel()
	ta := newTestApp(t)
	slug, _ := oneFile(t, ta, "Ada", "the trace", &upload{
		name: "trace.log", claims: "text/plain", content: textBytes,
	})
	id := ta.attachments.all()[0].ID

	_, body := ta.do(t, http.MethodGet, "/rooms/"+slug, nil, nil)
	if !strings.Contains(body, "/rooms/"+slug+"/files/"+id) {
		t.Error("the room page does not link the attachment")
	}
	if !strings.Contains(body, "trace.log") {
		t.Error("the room page does not name the attachment")
	}
	// A text file is not something this app renders in a page, so it is a link
	// rather than an <img>.
	if strings.Contains(body, `<img src="/rooms/`+slug+`/files/`+id) {
		t.Error("a text file was rendered as an image")
	}
}
