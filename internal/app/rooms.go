package app

import (
	"crypto/rand"
	"errors"
	"net/http"

	"github.com/andygeiss/baseline-reference/v3/internal/domain"
)

type roomsPage struct {
	base
	Rooms          []domain.Room
	MaxRoomNameLen int
}

// roomNewPage is the create-room form as a page of its own.
//
// It is the full-page fallback stack/html.md asks for beside a <dialog>: the
// invoker button on the room list opens the same form in the dialog, and a
// browser that does not know invoker commands still has this address. It is
// also where a rejected name lands — an open dialog is never a swap target
// (patterns/css-motion.md), so the answer to an invalid POST is this page with
// the dialog rendered open and the error inside it.
type roomNewPage struct {
	base
	Form           roomForm
	MaxRoomNameLen int
}

func (a *App) handleRoomList(w http.ResponseWriter, r *http.Request) {
	rooms, err := a.rooms.All(r.Context())
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	a.render(w, r, http.StatusOK, "rooms.html", "", roomsPage{
		base:           a.newBase(r, "rooms"),
		Rooms:          rooms,
		MaxRoomNameLen: domain.MaxRoomNameLen,
	})
}

func (a *App) handleRoomNew(w http.ResponseWriter, r *http.Request) {
	a.render(w, r, http.StatusOK, "room-new.html", "", a.newRoomView(r, roomForm{}))
}

func (a *App) handleRoomCreate(w http.ResponseWriter, r *http.Request) {
	if !a.parseForm(w, r) {
		return
	}

	form := roomForm{Name: domain.NormalizeRoomName(r.PostFormValue("name"))}
	if err := domain.ValidateRoomName(form.Name); err != nil {
		// The same wording as /api gives, from one place: two surfaces
		// explaining the same rule differently is two things to keep right.
		form.Check(false, "name", roomNameProblem(err))
	}
	if !form.Valid() {
		a.renderInvalid(w, r, "room-new.html", a.newRoomView(r, form))
		return
	}

	// rand.Text: 128 unguessable bits, URL-safe, no encoding step.
	room, err := domain.NewRoom(rand.Text(), form.Name)
	if err != nil {
		// Unreachable while the checks above state the same rules. It stays
		// because the domain, not this handler, decides what a room may be.
		a.serverError(w, r, err)
		return
	}

	switch err := a.rooms.Add(r.Context(), room); {
	case err == nil:
	case errors.Is(err, domain.ErrSlugTaken):
		form.Check(false, "name", "A room already answers to that address.")
		a.renderInvalid(w, r, "room-new.html", a.newRoomView(r, form))
		return
	default:
		a.serverError(w, r, err)
		return
	}

	a.flash(r, "Room created.")
	a.redirect(w, r, "/rooms/"+room.Slug)
}

// newRoomView builds the create-room page's data.
func (a *App) newRoomView(r *http.Request, form roomForm) roomNewPage {
	return roomNewPage{
		base:           a.newBase(r, "rooms"),
		Form:           form,
		MaxRoomNameLen: domain.MaxRoomNameLen,
	}
}
