package app

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

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
	// Required: without this the message is lost, so nothing below it runs.
	if err := a.messages.Add(r.Context(), msg); err != nil {
		a.serverError(w, r, err)
		return
	}

	// Enhancement, and it runs only once the message is safe: the worst case is
	// a message that posts without an answer, which is a room carrying on
	// rather than a person losing what they typed. Ordered the other way, a
	// model outage would take the chat down with it
	// (patterns/go-errors-logging.md).
	a.assistantReply(r, room, msg)

	if !isHTMX(r) {
		// The anchor sits under the last message, so the browser lands on it.
		// That is the one bit of scrolling this stack gets for free — moving
		// the viewport otherwise needs JavaScript.
		http.Redirect(w, r, "/rooms/"+room.Slug+"#latest", http.StatusSeeOther)
		return
	}
	a.renderChat(w, r, http.StatusOK, room, messageForm{})
}

const (
	// assistantBudget bounds one whole reply: read the room, ask the model,
	// store the answer. It sits below the server's WriteTimeout, so a wedged
	// model ends the assistant's work rather than the reader's connection, and
	// at or below every outbound client timeout, so the budget is what gives up
	// (patterns/go-http-server.md, the timeout ladder).
	assistantBudget = 10 * time.Second

	// assistantContext is how much of the room the model is given. Enough to
	// follow the thread, bounded so a long-running room does not grow the bill
	// without limit.
	assistantContext = 20
)

// assistantReply answers a mention, or gives up quietly.
//
// Every failure here logs at Warn and returns: this step improves the result
// and cannot replace it, so the caller has already stored what the person
// typed. Warn rather than Error is the level's definition — degraded but
// self-healing — and keeping Error for the required steps is what stops the
// level meaning nothing.
//
// And if it never succeeds? The message is stored and can be answered by
// mentioning the assistant again. An enhancement whose failure lost something
// permanently would have been a required step wearing the wrong label.
func (a *App) assistantReply(r *http.Request, room domain.Room, msg *domain.Message) {
	if a.assistant == nil || !domain.MentionsAssistant(msg.Body) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), assistantBudget)
	defer cancel()

	history, err := a.messages.Recent(ctx, room.ID, assistantContext)
	if err != nil {
		a.logger.Warn("assistant: reading the room", "room", room.Slug, "err", err)
		return
	}

	said, err := a.assistant.Reply(ctx, history)
	switch {
	case err == nil:
	case errors.Is(err, domain.ErrRefused):
		// A refusal is an answer, not a fault: the room hears something instead
		// of watching a mention vanish.
		said = "I can't help with that one."
	default:
		a.logger.Warn("assistant: no reply", "room", room.Slug, "err", err)
		return
	}

	reply, err := domain.NewMessage(room.ID, domain.AssistantID, said)
	if err != nil {
		a.logger.Warn("assistant: unusable reply", "room", room.Slug, "err", err)
		return
	}
	// WithoutCancel because the answer is already paid for: losing it to a
	// budget that expired between the reply arriving and this write would waste
	// the call and leave the mention unanswered anyway.
	if err := a.messages.Add(context.WithoutCancel(ctx), reply); err != nil {
		a.logger.Warn("assistant: storing the reply", "room", room.Slug, "err", err)
	}
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
