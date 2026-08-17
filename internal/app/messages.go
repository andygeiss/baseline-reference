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

// firstPage is how many messages a room shows at once — the first paint, and
// every press of "Show older" after it. Enough to pick up the thread, few
// enough to arrive fast on a phone.
const firstPage = 50

// messageView is one message as a template sees it.
//
// The template never gets the domain.Message itself, and the timestamp is the
// reason: a time.Time in a template invites .Format, which formats in whatever
// zone the process is in (patterns/time-and-dates.md). Everything a template
// needs is already a string by the time it arrives here.
type messageView struct {
	domain.Message
	Stamp    Stamp
	RoomSlug string
	// Mine says whether the reader may delete this message's attachment. The
	// template decides what to show; the route decides what to allow, and
	// AttachmentStore.Delete carries the same test as a predicate in its SQL.
	Mine bool
}

type roomPage struct {
	base
	Room     domain.Room
	Messages []messageView

	// The two cursors, and they are two on purpose. Since walks forward at the
	// arrival end, and the poller carries it. Older walks backward at the far
	// end, and the "Show older" control carries it. Sharing a name is how a
	// paged list and a polled one start answering each other's questions
	// (patterns/htmx-lists.md).
	Since    int64
	Older    int64
	HasOlder bool

	Form       messageForm
	MaxBodyLen int
	MaxFileMB  int
}

func (a *App) handleRoomShow(w http.ResponseWriter, r *http.Request) {
	room, ok := a.room(w, r)
	if !ok {
		return
	}
	msgs, hasOlder, err := a.page(r.Context(), room.ID, 0)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	a.render(w, r, http.StatusOK, "room.html", "",
		a.roomView(r, room, msgs, hasOlder, messageForm{}))
}

// handleMessageOlder answers one press of "Show older": the page of messages
// before the cursor, and a fresh control carrying the cursor before those.
//
// The control is an ordinary link as well as an htmx trigger, so this route
// answers a plain click with a whole page. That is what makes the cursor's
// place in the query string load-bearing rather than decorative: one URL serves
// both readers, and it can be bookmarked and sent to somebody.
func (a *App) handleMessageOlder(w http.ResponseWriter, r *http.Request) {
	room, ok := a.room(w, r)
	if !ok {
		return
	}
	before, err := strconv.ParseInt(r.URL.Query().Get("before"), 10, 64)
	if err != nil || before <= 0 {
		a.clientError(w, r, http.StatusBadRequest)
		return
	}
	msgs, hasOlder, err := a.page(r.Context(), room.ID, before)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	view := a.roomView(r, room, msgs, hasOlder, messageForm{})
	if !isHTMX(r) {
		a.render(w, r, http.StatusOK, "room.html", "", view)
		return
	}
	a.render(w, r, http.StatusOK, "room.html", "older-update", view)
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
	// A poll adds rows at the arrival end and touches nothing else. It must not
	// re-render the list: that would throw away the reader's scroll position
	// every three seconds, and on a room they have paged back through it would
	// throw away the pages too (patterns/htmx-live-updates.md rule 3).
	a.render(w, r, http.StatusOK, "room.html", "poll-update",
		a.roomView(r, room, msgs, false, messageForm{}))
}

func (a *App) handleMessagePost(w http.ResponseWriter, r *http.Request) {
	// parseUpload, not parseForm: this form may be multipart, and ParseForm
	// leaves a multipart body unread while making every later PostFormValue
	// answer "".
	if !a.parseUpload(w, r) {
		return
	}
	room, ok := a.room(w, r)
	if !ok {
		return
	}

	form := messageForm{Body: domain.NormalizeBody(r.PostFormValue("body"))}
	att, blob, err := a.attachmentFrom(r, userFrom(r.Context()).ID)
	switch {
	case err == nil:
	case refusedAttachment(err):
		form.Check(false, "file", attachmentProblem(err))
	default:
		a.serverError(w, r, err)
		return
	}

	switch err := domain.ValidateBody(form.Body); {
	case err == nil:
	case errors.Is(err, domain.ErrBodyEmpty):
		// A picture on its own is a message. The check only bites when there is
		// nothing at all to post.
		form.Check(att != nil, "body", "Write something, or attach a file.")
	default:
		form.Check(false, "body", fmt.Sprintf("Use at most %d characters.", domain.MaxBodyLen))
	}
	if !form.Valid() {
		a.renderChat(w, r, http.StatusUnprocessableEntity, room, form)
		return
	}

	msg, err := domain.NewMessage(room.ID, userFrom(r.Context()).ID, form.Body, att)
	if err != nil {
		// Unreachable while the checks above state the same rules. It stays
		// because the domain, not this handler, decides what a message may be.
		a.serverError(w, r, err)
		return
	}
	// Required: without this the message is lost, so nothing below it runs. The
	// message and its file are one transaction inside the store.
	if err := a.messages.Add(r.Context(), msg, blob); err != nil {
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

// refusedAttachment reports whether an upload broke a rule, rather than the
// request or the disk breaking.
func refusedAttachment(err error) bool {
	return errors.Is(err, domain.ErrAttachmentBig) ||
		errors.Is(err, domain.ErrAttachmentEmpty) ||
		errors.Is(err, domain.ErrAttachmentType)
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

	history, err := a.messages.Page(ctx, room.ID, 0, assistantContext)
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

	reply, err := domain.NewMessage(room.ID, domain.AssistantID, said, nil)
	if err != nil {
		a.logger.Warn("assistant: unusable reply", "room", room.Slug, "err", err)
		return
	}
	// WithoutCancel because the answer is already paid for: losing it to a
	// budget that expired between the reply arriving and this write would waste
	// the call and leave the mention unanswered anyway.
	if err := a.messages.Add(context.WithoutCancel(ctx), reply, nil); err != nil {
		a.logger.Warn("assistant: storing the reply", "room", room.Slug, "err", err)
	}
}

// renderChat answers with the whole chat region: the newest page of messages,
// the poller carrying a fresh cursor, and the form.
//
// Re-rendering the list is right here and wrong for a poll. The reader pressed
// Send, so they are looking at the bottom of the list and expect it to change;
// a poll that did this would move the ground under somebody who is reading. It
// also means the sender's cursor and their form come from one read, so neither
// can go stale against the other.
//
// It does cost the sender the older pages they had loaded: this rebuilds the
// region from the newest page down. For a chat that is the behaviour people
// expect from pressing Send — the room snaps to the bottom — and it is the
// trade patterns/htmx-lists.md says to make on purpose and write down. A list
// somebody works through rather than talks in would append instead.
func (a *App) renderChat(w http.ResponseWriter, r *http.Request, status int, room domain.Room, form messageForm) {
	msgs, hasOlder, err := a.page(r.Context(), room.ID, 0)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	view := a.roomView(r, room, msgs, hasOlder, form)
	if status == http.StatusUnprocessableEntity && !isHTMX(r) {
		a.renderInvalid(w, r, "room.html", view) // a whole page, with HX-Push-Url handled
		return
	}
	a.render(w, r, status, "room.html", "chat", view)
}

// page reads one screen of a room and says whether there is another behind it.
//
// It asks for one row more than it shows. That row's existence is the whole
// answer to "is there an older page", and it costs nothing next to a COUNT(*)
// over a table that only grows. The result is oldest-first, so the extra row is
// the one at the front.
func (a *App) page(ctx context.Context, roomID string, before int64) ([]domain.Message, bool, error) {
	msgs, err := a.messages.Page(ctx, roomID, before, firstPage+1)
	if err != nil {
		return nil, false, err
	}
	if len(msgs) > firstPage {
		return msgs[1:], true, nil
	}
	return msgs, false, nil
}

// roomView builds the room's template data.
//
// Both cursors come from the messages in hand, so the poller, the "Show older"
// control and the form are rendered from one read and cannot drift apart.
func (a *App) roomView(r *http.Request, room domain.Room, msgs []domain.Message, hasOlder bool, form messageForm) roomPage {
	now := time.Now()
	reader := userFrom(r.Context())
	views := make([]messageView, len(msgs))
	for i, m := range msgs {
		views[i] = messageView{
			Message:  m,
			Stamp:    newStamp(m.CreatedAt, a.location, now),
			RoomSlug: room.Slug,
			Mine:     m.Attachment != nil && m.Attachment.UploaderID == reader.ID,
		}
	}
	page := roomPage{
		base:       a.newBase(r, "room"),
		Room:       room,
		Messages:   views,
		Since:      domain.LastSeq(msgs, 0),
		HasOlder:   hasOlder,
		Form:       form,
		MaxBodyLen: domain.MaxBodyLen,
		MaxFileMB:  domain.MaxAttachmentBytes >> 20,
	}
	if len(msgs) > 0 {
		page.Older = msgs[0].Seq
	}
	return page
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
