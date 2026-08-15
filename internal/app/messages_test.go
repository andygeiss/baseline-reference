package app

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestRoomShow(t *testing.T) {
	t.Parallel()
	ta := newTestApp(t)
	ta.signUp(t, "Ada", "correct-horse")
	slug := ta.makeRoom(t, "General Chat")

	ta.do(t, http.MethodPost, "/rooms/"+slug+"/messages", url.Values{"body": {"hello"}}, nil)
	_, body := ta.do(t, http.MethodGet, "/rooms/"+slug, nil, nil)

	for _, want := range []string{
		"hello",       // the message
		`id="poll"`,   // the poller
		"?since=1",    // carrying the cursor
		`role="list"`, // the list keeps its semantics without markers
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the room page is missing %q:\n%s", want, body)
		}
	}
}

func TestRoomShowUnknownSlug(t *testing.T) {
	t.Parallel()
	ta := newTestApp(t)
	ta.signUp(t, "Ada", "correct-horse")

	res, _ := ta.do(t, http.MethodGet, "/rooms/nowhere", nil, nil)

	if res.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", res.StatusCode)
	}
}

func TestPostMessage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		body       string
		htmx       bool
		wantStatus int
	}{
		{"a plain form gets a redirect", "hello", false, http.StatusSeeOther},
		{"htmx gets the chat region", "hello", true, http.StatusOK},
		{"an empty message is refused", "   ", true, http.StatusUnprocessableEntity},
		{"an oversized message is refused", strings.Repeat("x", 2001), true, http.StatusUnprocessableEntity},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ta := newTestApp(t)
			ta.signUp(t, "Ada", "correct-horse")
			slug := ta.makeRoom(t, "General")

			var headers http.Header
			if tt.htmx {
				headers = htmx()
			}
			res, _ := ta.do(t, http.MethodPost, "/rooms/"+slug+"/messages",
				url.Values{"body": {tt.body}}, headers)

			if res.StatusCode != tt.wantStatus {
				t.Errorf("status = %d, want %d", res.StatusCode, tt.wantStatus)
			}
		})
	}
}

// TestPostMessageHTMXReturnsAFragment is the dual-mode rule: the same handler
// answers a whole page to a browser and a fragment to htmx.
func TestPostMessageHTMXReturnsAFragment(t *testing.T) {
	t.Parallel()
	ta := newTestApp(t)
	ta.signUp(t, "Ada", "correct-horse")
	slug := ta.makeRoom(t, "General")

	res, body := ta.do(t, http.MethodPost, "/rooms/"+slug+"/messages",
		url.Values{"body": {"hello"}}, htmx())

	if strings.Contains(body, "<html") {
		t.Error("htmx got a whole page, not a fragment")
	}
	if !strings.Contains(body, `id="chat"`) {
		t.Errorf("the fragment is not the chat region:\n%s", body)
	}
	if got := vary(res); !strings.Contains(got, "HX-Request") {
		t.Errorf("Vary = %q, want it to name HX-Request", got)
	}
}

// TestInvalidMessageSaysWhyAndKeepsTheTyping covers the 422 contract: the
// reason next to the field, tied to it for a screen reader, with the words kept.
func TestInvalidMessageSaysWhyAndKeepsTheTyping(t *testing.T) {
	t.Parallel()
	ta := newTestApp(t)
	ta.signUp(t, "Ada", "correct-horse")
	slug := ta.makeRoom(t, "General")

	tooLong := strings.Repeat("x", 2001)
	_, body := ta.do(t, http.MethodPost, "/rooms/"+slug+"/messages",
		url.Values{"body": {tooLong}}, htmx())

	for _, want := range []string{
		`aria-invalid="true"`,
		`aria-describedby="body-error"`,
		`id="body-error"`,
		"at most 2000 characters",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the 422 is missing %q", want)
		}
	}
	if !strings.Contains(body, tooLong) {
		t.Error("the typed message was thrown away, so it has to be retyped")
	}
}

// TestValidPageCarriesNoErrorState guards the other half: a field that is
// permanently marked invalid tells a screen reader nothing.
func TestValidPageCarriesNoErrorState(t *testing.T) {
	t.Parallel()
	ta := newTestApp(t)
	ta.signUp(t, "Ada", "correct-horse")
	slug := ta.makeRoom(t, "General")

	_, body := ta.do(t, http.MethodGet, "/rooms/"+slug, nil, nil)

	if strings.Contains(body, "aria-invalid") {
		t.Error("aria-invalid on a form with no error")
	}
}

func TestPoll(t *testing.T) {
	t.Parallel()
	ta := newTestApp(t)
	ta.signUp(t, "Ada", "correct-horse")
	slug := ta.makeRoom(t, "General")
	ta.do(t, http.MethodPost, "/rooms/"+slug+"/messages", url.Values{"body": {"first"}}, nil)

	t.Run("a stale cursor brings back what is new", func(t *testing.T) {
		res, body := ta.do(t, http.MethodGet, "/rooms/"+slug+"/messages?since=0", nil, htmx())

		if res.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200", res.StatusCode)
		}
		if !strings.Contains(body, "first") {
			t.Errorf("the new message is missing:\n%s", body)
		}
		// The answer ends with a new poller, which is what replaces the old one.
		if !strings.Contains(body, `id="poll"`) {
			t.Errorf("the answer carries no poller, so polling would stop:\n%s", body)
		}
		if !strings.Contains(body, "?since=1") {
			t.Errorf("the cursor did not move on:\n%s", body)
		}
	})

	t.Run("a current cursor answers 204", func(t *testing.T) {
		res, body := ta.do(t, http.MethodGet, "/rooms/"+slug+"/messages?since=1", nil, htmx())

		// htmx does not swap a 204, so the poller keeps the cursor it has.
		if res.StatusCode != http.StatusNoContent {
			t.Errorf("status = %d, want 204", res.StatusCode)
		}
		if body != "" {
			t.Errorf("a 204 carried a body: %q", body)
		}
	})

	t.Run("a broken cursor is a bad request", func(t *testing.T) {
		res, _ := ta.do(t, http.MethodGet, "/rooms/"+slug+"/messages?since=soon", nil, htmx())

		if res.StatusCode != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", res.StatusCode)
		}
	})

	t.Run("without htmx it redirects to the room", func(t *testing.T) {
		// The route is an optimization of "reload the page", not a feature, so
		// a plain reader is sent to the page itself rather than a fragment.
		res, _ := ta.do(t, http.MethodGet, "/rooms/"+slug+"/messages?since=0", nil, nil)

		if res.StatusCode != http.StatusSeeOther {
			t.Errorf("status = %d, want 303", res.StatusCode)
		}
		if got := res.Header.Get("Location"); got != "/rooms/"+slug {
			t.Errorf("Location = %q, want /rooms/%s", got, slug)
		}
	})
}

// TestPollNeverRepeatsOrSkips is the cursor's whole job. Two messages arrive
// between polls; the reader must see both, exactly once.
func TestPollNeverRepeatsOrSkips(t *testing.T) {
	t.Parallel()
	ta := newTestApp(t)
	ta.signUp(t, "Ada", "correct-horse")
	slug := ta.makeRoom(t, "General")

	ta.do(t, http.MethodPost, "/rooms/"+slug+"/messages", url.Values{"body": {"one"}}, nil)
	_, first := ta.do(t, http.MethodGet, "/rooms/"+slug+"/messages?since=0", nil, htmx())

	ta.do(t, http.MethodPost, "/rooms/"+slug+"/messages", url.Values{"body": {"two"}}, nil)
	ta.do(t, http.MethodPost, "/rooms/"+slug+"/messages", url.Values{"body": {"three"}}, nil)
	_, second := ta.do(t, http.MethodGet, "/rooms/"+slug+"/messages?since=1", nil, htmx())

	if !strings.Contains(first, "one") {
		t.Error("the first poll missed the first message")
	}
	if strings.Contains(second, ">one<") {
		t.Error("the second poll repeated a message the reader already had")
	}
	for _, want := range []string{"two", "three"} {
		if !strings.Contains(second, want) {
			t.Errorf("the second poll skipped %q", want)
		}
	}
}

// TestMessagesAreEscaped is the whole reason user text goes through
// html/template. Chat is nothing but user text.
func TestMessagesAreEscaped(t *testing.T) {
	t.Parallel()
	ta := newTestApp(t)
	ta.signUp(t, "Ada", "correct-horse")
	slug := ta.makeRoom(t, "General")

	const attack = `<script>alert("xss")</script>`
	ta.do(t, http.MethodPost, "/rooms/"+slug+"/messages", url.Values{"body": {attack}}, nil)
	_, body := ta.do(t, http.MethodGet, "/rooms/"+slug, nil, nil)

	if strings.Contains(body, attack) {
		t.Error("a message reached the page as live markup")
	}
	if !strings.Contains(body, "&lt;script&gt;") {
		t.Errorf("the message was not escaped into text:\n%s", body)
	}
}

func TestMessageRoutesNeedAUser(t *testing.T) {
	t.Parallel()
	ta := newTestApp(t)

	for _, path := range []string{"/rooms", "/rooms/general", "/rooms/general/messages", "/profile"} {
		res, _ := ta.do(t, http.MethodGet, path, nil, nil)
		if res.StatusCode != http.StatusSeeOther || res.Header.Get("Location") != "/login" {
			t.Errorf("GET %s: %d -> %q, want 303 -> /login",
				path, res.StatusCode, res.Header.Get("Location"))
		}
	}
}

// TestUnauthenticatedHTMXGetsARedirectHeader covers the htmx half of
// requireAuth: an XHR follows a 303 by itself and would swap the whole login
// page into whatever fragment the reader was looking at.
func TestUnauthenticatedHTMXGetsARedirectHeader(t *testing.T) {
	t.Parallel()
	ta := newTestApp(t)

	res, _ := ta.do(t, http.MethodGet, "/rooms/general/messages?since=0", nil, htmx())

	if res.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", res.StatusCode)
	}
	if got := res.Header.Get("HX-Redirect"); got != "/login" {
		t.Errorf("HX-Redirect = %q, want /login", got)
	}
	if got := vary(res); !strings.Contains(got, "HX-Request") {
		t.Errorf("Vary = %q, want it to name HX-Request", got)
	}
}
