package app

import (
	"crypto/rand"
	"errors"
	"net/http"
	"strconv"

	"github.com/andygeiss/baseline-reference/internal/domain"
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
	// rand.Text: 128 unguessable bits, URL-safe, no encoding step.
	g := domain.NewGame(rand.Text())
	if err := a.games.Create(r.Context(), g); err != nil {
		a.serverError(w, r, err)
		return
	}
	http.Redirect(w, r, "/games/"+g.ID, http.StatusSeeOther)
}

func (a *App) handleGameShow(w http.ResponseWriter, r *http.Request) {
	g, err := a.games.Get(r.Context(), r.PathValue("id"))
	if errors.Is(err, domain.ErrNotFound) {
		a.clientError(w, r, http.StatusNotFound)
		return
	}
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	a.render(w, r, http.StatusOK, "game.html", "", gameView{Game: g})
}

func (a *App) handleMoveCreate(w http.ResponseWriter, r *http.Request) {
	cell, err := strconv.Atoi(r.PostFormValue("cell"))
	if err != nil {
		a.clientError(w, r, http.StatusBadRequest)
		return
	}

	// The move happens inside the store's transaction; a rule violation comes
	// back with the current game, which the response renders as the answer.
	g, err := a.games.Update(r.Context(), r.PathValue("id"),
		func(g *domain.Game) error { return g.Move(cell) })
	var message string
	switch {
	case errors.Is(err, domain.ErrNotFound):
		a.clientError(w, r, http.StatusNotFound)
		return
	case errors.Is(err, domain.ErrInvalidCell):
		a.clientError(w, r, http.StatusBadRequest)
		return
	case errors.Is(err, domain.ErrCellTaken):
		message = "That cell is already taken."
	case errors.Is(err, domain.ErrGameOver):
		message = "The game is over — start a new one."
	case err != nil:
		a.serverError(w, r, err)
		return
	}

	// htmx gets the board fragment; plain form posts follow POST→redirect→GET.
	if isHTMX(r) {
		a.render(w, r, http.StatusOK, "game.html", "board-swap", gameView{Game: g, Message: message})
		return
	}
	http.Redirect(w, r, "/games/"+g.ID, http.StatusSeeOther)
}
