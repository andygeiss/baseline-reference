package app

import (
	"net/http"
	"strings"
	"testing"
	"time"

	// The test loads a named zone, so it embeds the database for the same
	// reason the binary does: a machine without /usr/share/zoneinfo would
	// otherwise fail here and nowhere else.
	_ "time/tzdata"
)

func TestNewStamp(t *testing.T) {
	t.Parallel()
	berlin, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Fatalf("loading Europe/Berlin: %v", err)
	}
	at := func(s string) time.Time {
		t.Helper()
		moment, err := time.Parse(time.RFC3339, s)
		if err != nil {
			t.Fatalf("parsing %q: %v", s, err)
		}
		return moment
	}

	tests := []struct {
		name     string
		when     string
		now      string
		absolute string
		relative string
	}{
		{
			"an ordinary summer afternoon is two hours ahead of UTC",
			"2026-08-17T09:14:02Z", "2026-08-17T09:14:30Z",
			"17 Aug 2026, 11:14 CEST", "just now",
		},
		{
			// The hour that does not exist: 02:00 CET becomes 03:00 CEST, so
			// there is no 02:30 in Berlin on this date at all.
			"the hour the clocks skip",
			"2026-03-29T01:30:00Z", "2026-03-29T01:35:00Z",
			"29 Mar 2026, 03:30 CEST", "5 minutes ago",
		},
		{
			// The hour that happens twice. Same wall clock, two instants, and
			// the abbreviation is the only thing that tells them apart — which
			// is why it is never dropped.
			"the repeated hour, first time round",
			"2026-10-25T00:30:00Z", "2026-10-25T00:35:00Z",
			"25 Oct 2026, 02:30 CEST", "5 minutes ago",
		},
		{
			"the repeated hour, second time round",
			"2026-10-25T01:30:00Z", "2026-10-25T01:35:00Z",
			"25 Oct 2026, 02:30 CET", "5 minutes ago",
		},
		{
			// Late in the UTC day is already tomorrow in Berlin. A reader shown
			// the UTC date here would be told the wrong day.
			"an instant that is already tomorrow in this zone",
			"2026-08-17T22:30:00Z", "2026-08-17T22:31:00Z",
			"18 Aug 2026, 00:30 CEST", "1 minute ago",
		},
		{
			"an hour ago",
			"2026-08-17T09:00:00Z", "2026-08-17T10:30:00Z",
			"17 Aug 2026, 11:00 CEST", "1 hour ago",
		},
		{
			// Past a day the distance stops helping, so the date is printed.
			"last week",
			"2026-08-10T09:00:00Z", "2026-08-17T09:00:00Z",
			"10 Aug 2026, 11:00 CEST", "10 Aug",
		},
		{
			// Clocks disagree, so a moment can arrive from the future. Saying
			// "just now" is closer than a negative count.
			"a moment from the future",
			"2026-08-17T09:00:10Z", "2026-08-17T09:00:00Z",
			"17 Aug 2026, 11:00 CEST", "just now",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := newStamp(at(tt.when), berlin, at(tt.now))
			if got.Absolute != tt.absolute {
				t.Errorf("Absolute = %q, want %q", got.Absolute, tt.absolute)
			}
			if got.Relative != tt.relative {
				t.Errorf("Relative = %q, want %q", got.Relative, tt.relative)
			}
			// The machine-readable half is always UTC, whatever the reader sees.
			if got.ISO != tt.when {
				t.Errorf("ISO = %q, want %q", got.ISO, tt.when)
			}
		})
	}
}

// TestPagesRenderTimesTheOneWay walks the rule from the other end: no template
// in this app is handed a time.Time, so no page can carry Go's default
// rendering of one, and every moment on a page is machine-readable.
func TestPagesRenderTimesTheOneWay(t *testing.T) {
	t.Parallel()
	ta := newTestApp(t)
	slug := seedRoom(t, ta, 1)

	for _, path := range []string{"/rooms/" + slug, "/profile"} {
		_, body := ta.do(t, http.MethodGet, path, nil, nil)
		// "2026-08-17 09:14:02.123 +0000 UTC" is what {{.CreatedAt}} prints.
		// Finding it means a time.Time reached a template.
		if strings.Contains(body, "+0000 UTC") {
			t.Errorf("%s renders a raw time.Time", path)
		}
	}

	_, room := ta.do(t, http.MethodGet, "/rooms/"+slug, nil, nil)
	if !strings.Contains(room, `<time datetime="`) {
		t.Error("the room does not carry a machine-readable time")
	}
	// The app's configured zone, not the machine's: testZone is neither UTC nor
	// anywhere the test runner is likely to be.
	if !strings.Contains(room, "TST") {
		t.Error("the rendered time does not name the app's zone")
	}
}

// TestStampNamesItsZone is the rule in one line: every absolute time carries
// the abbreviation, because "11:14" is a different moment for every reader.
func TestStampNamesItsZone(t *testing.T) {
	t.Parallel()
	moment := time.Date(2026, 8, 17, 9, 14, 0, 0, time.UTC)
	for _, zone := range []*time.Location{time.UTC, testZone} {
		got := newStamp(moment, zone, moment)
		if len(got.Absolute) < 4 || got.Absolute[len(got.Absolute)-4] != ' ' {
			t.Errorf("Absolute = %q, want it to end in a zone abbreviation", got.Absolute)
		}
	}
}
