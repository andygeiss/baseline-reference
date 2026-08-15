package app

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestCreateRoom(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		roomName   string
		wantStatus int
		wantText   string
	}{
		{"a good name creates the room", "General Chat", http.StatusSeeOther, ""},
		{"an empty name is refused", "   ", http.StatusUnprocessableEntity, "Use 1 to 40 characters"},
		{"a name with no letters is refused", "!!!", http.StatusUnprocessableEntity, "at least one letter or digit"},
		{"an oversized name is refused", strings.Repeat("x", 41), http.StatusUnprocessableEntity, "Use 1 to 40 characters"},
		// /rooms/new is a route, so a room slugging to it could never be opened.
		{"a reserved name is refused", "New", http.StatusUnprocessableEntity, "reserved"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ta := newTestApp(t)
			ta.signUp(t, "Ada", "correct-horse")

			res, body := ta.do(t, http.MethodPost, "/rooms", url.Values{"name": {tt.roomName}}, nil)

			if res.StatusCode != tt.wantStatus {
				t.Errorf("status = %d, want %d", res.StatusCode, tt.wantStatus)
			}
			if tt.wantText != "" && !strings.Contains(body, tt.wantText) {
				t.Errorf("body does not explain the problem; want %q in:\n%s", tt.wantText, body)
			}
		})
	}
}

func TestCreateRoomRedirectsToIt(t *testing.T) {
	t.Parallel()
	ta := newTestApp(t)
	ta.signUp(t, "Ada", "correct-horse")

	res, _ := ta.do(t, http.MethodPost, "/rooms", url.Values{"name": {"General Chat"}}, nil)

	if got := res.Header.Get("Location"); got != "/rooms/general-chat" {
		t.Errorf("Location = %q, want /rooms/general-chat", got)
	}
}

func TestCreateRoomTakenSlug(t *testing.T) {
	t.Parallel()
	ta := newTestApp(t)
	ta.signUp(t, "Ada", "correct-horse")
	ta.makeRoom(t, "General Chat")

	// Different name, same address.
	res, body := ta.do(t, http.MethodPost, "/rooms", url.Values{"name": {"general chat"}}, nil)

	if res.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("status = %d, want 422", res.StatusCode)
	}
	if !strings.Contains(body, "already answers to that address") {
		t.Errorf("body does not say the address is taken:\n%s", body)
	}
}

// TestRoomListOffersTheDialogAndTheFallback covers both halves of the
// create-room control: the invoker button that opens the dialog with no script,
// and the page a browser without invoker commands can still reach.
func TestRoomListOffersTheDialogAndTheFallback(t *testing.T) {
	t.Parallel()
	ta := newTestApp(t)
	ta.signUp(t, "Ada", "correct-horse")

	_, body := ta.do(t, http.MethodGet, "/rooms", nil, nil)

	for _, want := range []string{
		`command="show-modal"`,
		`commandfor="new-room"`,
		`<dialog id="new-room"`,
		`href="/rooms/new"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the room list is missing %q:\n%s", want, body)
		}
	}

	res, _ := ta.do(t, http.MethodGet, "/rooms/new", nil, nil)
	if res.StatusCode != http.StatusOK {
		t.Errorf("the fallback page answered %d, want 200", res.StatusCode)
	}
}

func TestRoomListMarksTheCurrentSection(t *testing.T) {
	t.Parallel()
	ta := newTestApp(t)
	ta.signUp(t, "Ada", "correct-horse")

	_, rooms := ta.do(t, http.MethodGet, "/rooms", nil, nil)
	_, profile := ta.do(t, http.MethodGet, "/profile", nil, nil)

	// Color alone would not reach a screen reader; aria-current does.
	if !strings.Contains(rooms, `href="/rooms" aria-current="page"`) {
		t.Error("the rooms page does not mark Rooms as the current section")
	}
	if !strings.Contains(profile, `href="/profile" aria-current="page"`) {
		t.Error("the profile page does not mark You as the current section")
	}
}

// TestSignedOutPagesHaveNoBottomBar keeps the bar off the pages with nowhere
// to navigate.
func TestSignedOutPagesHaveNoBottomBar(t *testing.T) {
	t.Parallel()
	ta := newTestApp(t)

	_, body := ta.do(t, http.MethodGet, "/login", nil, nil)

	if strings.Contains(body, `aria-label="Sections"`) {
		t.Error("the login page carries the bottom bar")
	}
}
