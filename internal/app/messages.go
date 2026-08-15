package app

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/andygeiss/baseline-reference/v3/internal/domain"
)

// firstPage is how many messages a room shows when it first paints. Enough to
// pick up the thread, few enough to arrive fast on a phone.
const firstPage = 50

type roomPage struct {
	base
	Room       domain.Room
	Messages   []domain.Message
	Since      int64 // the cursor the poller carries
	Form       messageForm
	MaxBodyLen int
}

func (a *App) handleRoomShow(w http.ResponseWriter, r *http.Request) {
	room, ok := a.room(w, r)
	if !ok {
		return
	}
	msgs, err := a.messages.Recent(r.Context(), room.ID, firstPage)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	a.render(w, r, http.StatusOK, "room.html", "", a.roomView(r, room, msgs, messageForm{}))
}

// handleMessagePoll answers one tick of the reader's poll: everything posted
// since their cursor, and a fresh poller carrying the new one.
func (a *App) handleMessagePoll(w http.ResponseWriter, r *http.Request) {
	// The route exists only for htmx. A reader without it is not missing a
	// feature — the room page already renders every message, and reloading it
	// is the plain version of polling.
	if !isHTMX(r) {
		http.Redirect(w, r, "/rooms/"+r.PathValue("slug"), http.StatusSeeOther)
		return
	}
	room, ok := a.room(w, r)
	if !ok {
		return
	}
	since, err := strconv.ParseInt(r.URL.Query().Get("since"), 10, 64)
	if err != nil || since < 0 {
		a.clientError(w, r, http.StatusBadRequest)
		return
	}

	msgs, err := a.messages.Since(r.Context(), room.ID, since)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	if len(msgs) == 0 {
		// htmx does not swap a 204, so the poller stays put with the cursor it
		// already has. The quiet case — which is most cases — costs one indexed
		// read and an empty response.
		w.Header().Add("Vary", "HX-Request, HX-Boosted") // this response bypasses render
		w.WriteHeader(http.StatusNoContent)
		return
	}
	a.render(w, r, http.StatusOK, "room.html", "poll-update", a.roomView(r, room, msgs, messageForm{}))
}

func (a *App) handleMessagePost(w http.ResponseWriter, r *http.Request) {
	if !a.parseForm(w, r) {
		return
	}
	room, ok := a.room(w, r)
	if !ok {
		return
	}

	form := messageForm{Body: domain.NormalizeBody(r.PostFormValue("body"))}
	switch err := domain.ValidateBody(form.Body); {
	case err == nil:
	case errors.Is(err, domain.ErrBodyEmpty):
		form.Check(false, "body", "Write something first.")
	default:
		form.Check(false, "body", fmt.Sprintf("Use at most %d characters.", domain.MaxBodyLen))
	}
	if !form.Valid() {
		a.renderChat(w, r, http.StatusUnprocessableEntity, room, form)
		return
	}

	msg, err := domain.NewMessage(room.ID, userFrom(r.Context()).ID, form.Body)
	if err != nil {
		// Unreachable while the checks above state the same rules. It stays
		// because the domain, not this handler, decides what a message may be.
		a.serverError(w, r, err)
		return
	}
	if err := a.messages.Add(r.Context(), msg); err != nil {
		a.serverError(w, r, err)
		return
	}

	if !isHTMX(r) {
		// The anchor sits under the last message, so the browser lands on it.
		// That is the one bit of scrolling this stack gets for free — moving
		// the viewport otherwise needs JavaScript.
		http.Redirect(w, r, "/rooms/"+room.Slug+"#latest", http.StatusSeeOther)
		return
	}
	a.renderChat(w, r, http.StatusOK, room, messageForm{})
}

// renderChat answers with the whole chat region: the messages, the poller
// carrying a fresh cursor, and the form.
//
// Re-rendering the list is right here and wrong for a poll. The reader pressed
// Send, so they are looking at the bottom of the list and expect it to change;
// a poll that did this would throw away their scroll position every few seconds
// (patterns/htmx-live-updates.md rule 3). It also means the sender's cursor and
// their form come from one read, so neither can go stale against the other.
func (a *App) renderChat(w http.ResponseWriter, r *http.Request, status int, room domain.Room, form messageForm) {
	msgs, err := a.messages.Recent(r.Context(), room.ID, firstPage)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	view := a.roomView(r, room, msgs, form)
	if status == http.StatusUnprocessableEntity && !isHTMX(r) {
		a.renderInvalid(w, r, "room.html", view) // a whole page, with HX-Push-Url handled
		return
	}
	a.render(w, r, status, "room.html", "chat", view)
}

// roomView builds the room's template data. Since always comes from the
// messages in hand, so the poller and the form are re-rendered from one number
// and cannot drift apart.
func (a *App) roomView(r *http.Request, room domain.Room, msgs []domain.Message, form messageForm) roomPage {
	return roomPage{
		base:       a.newBase(r, "room"),
		Room:       room,
		Messages:   msgs,
		Since:      domain.LastSeq(msgs, 0),
		Form:       form,
		MaxBodyLen: domain.MaxBodyLen,
	}
}

// room resolves the slug in the URL, answering 404 itself when no room does.
func (a *App) room(w http.ResponseWriter, r *http.Request) (domain.Room, bool) {
	room, err := a.rooms.BySlug(r.Context(), r.PathValue("slug"))
	switch {
	case err == nil:
		return room, true
	case errors.Is(err, domain.ErrNotFound):
		a.clientError(w, r, http.StatusNotFound)
	default:
		a.serverError(w, r, err)
	}
	return domain.Room{}, false
}
