package app

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"
	"strconv"

	"github.com/andygeiss/tictactoe/internal/domain"
	"github.com/andygeiss/tictactoe/internal/store"
)

// gameView is the template data for game.html and its board fragment.
type gameView struct {
	Game    *domain.Game
	Message string
}

func (a *App) handleHome(w http.ResponseWriter, r *http.Request) {
	a.render(w, r, http.StatusOK, "home.html", "", nil)
}

func (a *App) handleGameCreate(w http.ResponseWriter, r *http.Request) {
	g := domain.NewGame(newID())
	if err := a.games.Create(r.Context(), g); err != nil {
		a.serverError(w, r, err)
		return
	}
	http.Redirect(w, r, "/games/"+g.ID, http.StatusSeeOther)
}

func (a *App) handleGameShow(w http.ResponseWriter, r *http.Request) {
	g, err := a.games.Get(r.Context(), r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		a.clientError(w, http.StatusNotFound)
		return
	}
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	a.render(w, r, http.StatusOK, "game.html", "", gameView{Game: g})
}

func (a *App) handleMoveCreate(w http.ResponseWriter, r *http.Request) {
	g, err := a.games.Get(r.Context(), r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		a.clientError(w, http.StatusNotFound)
		return
	}
	if err != nil {
		a.serverError(w, r, err)
		return
	}

	cell, err := strconv.Atoi(r.PostFormValue("cell"))
	if err != nil {
		a.clientError(w, http.StatusBadRequest)
		return
	}

	view := gameView{Game: g}
	switch err := g.Move(cell); {
	case errors.Is(err, domain.ErrInvalidCell):
		a.clientError(w, http.StatusBadRequest)
		return
	case errors.Is(err, domain.ErrCellTaken):
		view.Message = "That cell is already taken."
	case errors.Is(err, domain.ErrGameOver):
		view.Message = "The game is over — start a new one."
	case err != nil:
		a.serverError(w, r, err)
		return
	default:
		if err := a.games.Update(r.Context(), g); err != nil {
			a.serverError(w, r, err)
			return
		}
	}

	// htmx gets the board fragment; plain form posts follow POST→redirect→GET.
	if isHTMX(r) {
		a.render(w, r, http.StatusOK, "game.html", "board", view)
		return
	}
	http.Redirect(w, r, "/games/"+g.ID, http.StatusSeeOther)
}

func newID() string {
	b := make([]byte, 8)
	rand.Read(b) // crypto/rand.Read never fails (panics on broken entropy)
	return hex.EncodeToString(b)
}
