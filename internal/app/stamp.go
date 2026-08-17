package app

import (
	"fmt"
	"time"
)

// Stamp is how a moment reaches a template.
//
// No template in this app is handed a time.Time, and this type is the reason.
// A time.Time in a template invites {{.CreatedAt.Format "15:04"}}, which
// formats in whatever zone the value happens to carry — the container's UTC in
// production and the developer's zone on their laptop. The two disagree, the
// tests run on the laptop, and nothing goes red.
//
// Building a Stamp is the only place in this app that formats a time, so it is
// the only place that names a zone.
type Stamp struct {
	ISO      string // the UTC value, machine-readable, for <time datetime>
	Absolute string // what the reader sees when the exact moment matters
	Relative string // "3 minutes ago", where the page refreshes often enough
}

// newStamp renders one moment in loc, relative to now.
//
// now is a parameter rather than a call to time.Now: it is what makes
// "3 minutes ago" testable without waiting three minutes.
func newStamp(t time.Time, loc *time.Location, now time.Time) Stamp {
	local := t.In(loc)
	return Stamp{
		ISO: t.UTC().Format(time.RFC3339),
		// The zone abbreviation is not decoration. "11:14" is a different
		// moment for every reader; "11:14 CEST" is one moment for all of them.
		Absolute: local.Format("2 Jan 2006, 15:04 MST"),
		Relative: relative(t, now, local),
	}
}

// relative words a moment as a distance from now, and gives up at a day.
//
// Past that it prints the date instead: "4 months ago" is a date the reader has
// to work out, and this string is rendered on the server, so it is already
// stale by the time anybody reads it. A day is the point where a rounded
// distance stops being more useful than the answer.
func relative(t, now time.Time, local time.Time) string {
	d := now.Sub(t)
	switch {
	// A moment in the future means the clocks disagree, not that something has
	// not happened yet. Saying "just now" is closer than a negative count.
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return plural(int(d.Minutes()), "minute")
	case d < 24*time.Hour:
		return plural(int(d.Hours()), "hour")
	default:
		return local.Format("2 Jan")
	}
}

func plural(n int, unit string) string {
	if n == 1 {
		return fmt.Sprintf("1 %s ago", unit)
	}
	return fmt.Sprintf("%d %ss ago", n, unit)
}
