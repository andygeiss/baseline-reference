package app

import (
	"crypto/rand"
	"errors"
	"fmt"
	"net/http"

	"github.com/andygeiss/baseline-reference/v3/internal/auth"
	"github.com/andygeiss/baseline-reference/v3/internal/domain"
)

type profilePage struct {
	base
	Tokens []domain.Token
	Form   tokenForm

	// NewSecret is the token just created. It is shown here once and never
	// again — the server kept only its hash.
	NewSecret string
}

func (a *App) handleProfile(w http.ResponseWriter, r *http.Request) {
	a.renderProfile(w, r, http.StatusOK, tokenForm{})
}

func (a *App) handleTokenCreate(w http.ResponseWriter, r *http.Request) {
	if !a.parseForm(w, r) {
		return
	}
	user := userFrom(r.Context())

	form := tokenForm{Label: domain.NormalizeLabel(r.PostFormValue("label"))}
	form.Check(domain.ValidateLabel(form.Label) == nil, "label",
		fmt.Sprintf("Give it a name of 1 to %d characters.", domain.MaxLabelLen))
	if !form.Valid() {
		a.renderProfile(w, r, http.StatusUnprocessableEntity, form)
		return
	}

	secret, hash, err := auth.NewToken()
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	// rand.Text: 128 unguessable bits, URL-safe, no encoding step.
	token := &domain.Token{ID: rand.Text(), UserID: user.ID, Hash: hash, Label: form.Label}
	if err := a.tokens.Add(r.Context(), token); err != nil {
		a.serverError(w, r, err)
		return
	}

	// The secret rides the session to the page after the redirect, and is
	// popped there. Answering the POST with it directly would show it once and
	// then mint a second token on every refresh; putting it here costs one
	// request's worth of storage in the same session row that could have
	// created the token anyway.
	a.sessions.Put(r.Context(), "newToken", secret)
	a.flash(r, "Token created. Copy it now — it is not shown again.")
	a.redirect(w, r, "/profile")
}

func (a *App) handleTokenDelete(w http.ResponseWriter, r *http.Request) {
	user := userFrom(r.Context())
	switch err := a.tokens.Delete(r.Context(), user.ID, r.PathValue("id")); {
	case err == nil:
		a.flash(r, "Token revoked.")
	case errors.Is(err, domain.ErrNotFound):
		// Somebody else's token, or one already revoked in another tab. Both
		// are a stale page rather than bad input, so the answer is the current
		// list and a word about it.
		a.flash(r, "That token is already gone.")
	default:
		a.serverError(w, r, err)
		return
	}
	a.redirect(w, r, "/profile")
}

func (a *App) renderProfile(w http.ResponseWriter, r *http.Request, status int, form tokenForm) {
	user := userFrom(r.Context())
	tokens, err := a.tokens.ByUser(r.Context(), user.ID)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	page := profilePage{
		base:      a.newBase(r, "profile"),
		Tokens:    tokens,
		Form:      form,
		NewSecret: a.sessions.PopString(r.Context(), "newToken"),
	}
	if status == http.StatusUnprocessableEntity {
		a.renderInvalid(w, r, "profile.html", page)
		return
	}
	a.render(w, r, status, "profile.html", "", page)
}
