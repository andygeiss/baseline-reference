package app

import (
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/andygeiss/baseline-reference/v3/internal/domain"
)

// olderLink finds the cursor on the "Show older" control, or reports that there
// is no control on the page.
var olderLink = regexp.MustCompile(`/older\?before=(\d+)`)

// seedRoom signs Ada in, makes a room, and fills it with n numbered messages.
// The messages go straight into the store: this is a test about reading a long
// room, and sending fifty of them over HTTP would only exercise the poster.
func seedRoom(t *testing.T, ta *testApp, n int) string {
	t.Helper()
	ta.signUp(t, "Ada", "correct horse battery")
	slug := ta.makeRoom(t, "General")

	ada, err := ta.users.ByName(t.Context(), "Ada")
	if err != nil {
		t.Fatalf("reading Ada: %v", err)
	}
	roomID := ta.rooms.rooms[0].ID
	for i := 1; i <= n; i++ {
		m := &domain.Message{RoomID: roomID, AuthorID: ada.ID, Body: fmt.Sprintf("m%d", i)}
		if err := ta.messages.Add(t.Context(), m, nil); err != nil {
			t.Fatalf("seeding message %d: %v", i, err)
		}
	}
	return slug
}

// says reports which of the seeded messages a page holds. It matches whole
// bodies, so "m1" does not count as a sighting of "m10".
func says(body string, n int) map[string]bool {
	seen := make(map[string]bool)
	for i := 1; i <= n; i++ {
		if strings.Contains(body, fmt.Sprintf(">m%d<", i)) {
			seen[fmt.Sprintf("m%d", i)] = true
		}
	}
	return seen
}

func TestRoomPagesBackwards(t *testing.T) {
	t.Parallel()
	total := firstPage + 12
	ta := newTestApp(t)
	slug := seedRoom(t, ta, total)

	_, first := ta.do(t, http.MethodGet, "/rooms/"+slug, nil, nil)
	seen := says(first, total)
	if len(seen) != firstPage {
		t.Fatalf("the room's first paint shows %d messages, want %d", len(seen), firstPage)
	}
	if !seen[fmt.Sprintf("m%d", total)] {
		t.Error("the newest message is not on the first page")
	}
	if seen["m1"] {
		t.Error("the oldest message is on the first page, so nothing was paged")
	}

	cursor := olderLink.FindStringSubmatch(first)
	if cursor == nil {
		t.Fatal("no Show older control on a room with more messages than one page")
	}

	// One press. The answer replaces the control, so it carries the rest of the
	// room and no control of its own once there is nothing older.
	_, older := ta.do(t, http.MethodGet, "/rooms/"+slug+"/older?before="+cursor[1], nil, htmx())
	for body := range says(older, total) {
		if seen[body] {
			t.Errorf("%s is on both pages — the cursor repeated a row", body)
		}
		seen[body] = true
	}
	if len(seen) != total {
		t.Errorf("after paging back, %d of %d messages have been seen — the cursor skipped some",
			len(seen), total)
	}
	if olderLink.MatchString(older) {
		t.Error("the control came back on the last page, with nothing older to ask for")
	}
}

func TestARoomThatFitsHasNoControl(t *testing.T) {
	t.Parallel()
	ta := newTestApp(t)
	slug := seedRoom(t, ta, 3)

	_, body := ta.do(t, http.MethodGet, "/rooms/"+slug, nil, nil)
	if olderLink.MatchString(body) {
		t.Error("a room of three messages offers to show older ones")
	}
	if len(says(body, 3)) != 3 {
		t.Error("a room of three messages does not show all three")
	}
}

// TestOlderWorksWithoutHTMX is why the cursor rides in the query string: one
// URL serves the fragment and the whole page, and it can be sent to somebody.
func TestOlderWorksWithoutHTMX(t *testing.T) {
	t.Parallel()
	total := firstPage + 5
	ta := newTestApp(t)
	slug := seedRoom(t, ta, total)

	_, first := ta.do(t, http.MethodGet, "/rooms/"+slug, nil, nil)
	cursor := olderLink.FindStringSubmatch(first)
	if cursor == nil {
		t.Fatal("no Show older control to follow")
	}

	// No HX-Request header: a plain click, or somebody opening the link.
	res, body := ta.do(t, http.MethodGet, "/rooms/"+slug+"/older?before="+cursor[1], nil, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	if !strings.Contains(body, "<!doctype html>") {
		t.Error("a plain request got a fragment instead of a page")
	}
	if !says(body, total)["m1"] {
		t.Error("the page does not hold the older messages it was asked for")
	}
}

// TestTheTwoCursorsAreNotTheSame pins the composition. One walks forward at the
// arrival end and one walks backward at the far end; sharing a name is how they
// start answering each other's questions.
func TestTheTwoCursorsAreNotTheSame(t *testing.T) {
	t.Parallel()
	ta := newTestApp(t)
	slug := seedRoom(t, ta, firstPage+2)

	_, body := ta.do(t, http.MethodGet, "/rooms/"+slug, nil, nil)
	if !strings.Contains(body, "/messages?since=") {
		t.Error("the poller's cursor is not on the page")
	}
	if !strings.Contains(body, "/older?before=") {
		t.Error("the pager's cursor is not on the page")
	}
	if strings.Contains(body, "/older?since=") || strings.Contains(body, "/messages?before=") {
		t.Error("the two cursors have swapped names")
	}
}

func TestOlderRefusesACursorThatIsNotOne(t *testing.T) {
	t.Parallel()
	ta := newTestApp(t)
	slug := seedRoom(t, ta, 3)

	for _, before := range []string{"", "0", "-1", "banana"} {
		res, _ := ta.do(t, http.MethodGet, "/rooms/"+slug+"/older?before="+before, nil, htmx())
		if res.StatusCode != http.StatusBadRequest {
			t.Errorf("before=%q: status = %d, want 400", before, res.StatusCode)
		}
	}
}
