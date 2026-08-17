package app

import (
	"crypto/rand"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/andygeiss/baseline-reference/v3/internal/auth"
	"github.com/andygeiss/baseline-reference/v3/internal/domain"
)

// tokenView is one token as the page sees it. LastUsed is a Stamp rather than
// the stored string: what the store keeps is UTC, and what a reader should see
// is that moment in this app's zone (patterns/time-and-dates.md).
type tokenView struct {
	domain.Token
	Used     bool
	LastUsed Stamp
}

type profilePage struct {
	base
	Tokens    []tokenView
	Form      tokenForm
	EmailForm emailForm

	// NewSecret is the token just created. It is shown here once and never
	// again — the server kept only its hash.
	NewSecret string
}

func (a *App) handleProfile(w http.ResponseWriter, r *http.Request) {
	a.renderProfile(w, r, http.StatusOK, tokenForm{}, emailForm{})
}

// handleEmailSet stores the address a password-reset link would go to, or
// clears it.
//
// It is the only thing this app does with an address. Nothing renders it to
// anybody else, nothing sends anything else to it, and leaving it empty is a
// supported answer — it means this account cannot be recovered.
func (a *App) handleEmailSet(w http.ResponseWriter, r *http.Request) {
	if !a.parseForm(w, r) {
		return
	}
	user := userFrom(r.Context())
	form := emailForm{Email: domain.NormalizeEmail(r.PostFormValue("email"))}

	if form.Email != "" {
		form.Check(domain.ValidateEmail(form.Email) == nil, "email",
			"Write an address like you@example.com.")
	}
	if !form.Valid() {
		a.renderProfile(w, r, http.StatusUnprocessableEntity, tokenForm{}, form)
		return
	}
	if err := a.users.SetEmail(r.Context(), user.ID, form.Email); err != nil {
		a.serverError(w, r, err)
		return
	}
	if form.Email == "" {
		a.flash(r, "Address removed. This account can no longer be recovered by email.")
	} else {
		a.flash(r, "Address saved.")
	}
	a.redirect(w, r, "/profile")
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
		a.renderProfile(w, r, http.StatusUnprocessableEntity, form, emailForm{})
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

func (a *App) renderProfile(w http.ResponseWriter, r *http.Request, status int, form tokenForm, email emailForm) {
	user := userFrom(r.Context())
	tokens, err := a.tokens.ByUser(r.Context(), user.ID)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	// The form shows what is stored unless the reader is looking at something
	// they just typed and got back.
	if email.Email == "" && email.Valid() {
		email.Email = user.Email
	}
	now := time.Now()
	views := make([]tokenView, len(tokens))
	for i, t := range tokens {
		views[i] = tokenView{Token: t}
		if t.LastUsedAt == "" {
			continue
		}
		used, err := time.Parse(time.RFC3339, t.LastUsedAt)
		if err != nil {
			a.serverError(w, r, fmt.Errorf("parsing the last use of token %s: %w", t.ID, err))
			return
		}
		// Absolute, not relative: this page does not refresh itself, so
		// "3 minutes ago" would still say that an hour later
		// (patterns/time-and-dates.md).
		views[i].Used, views[i].LastUsed = true, newStamp(used, a.location, now)
	}
	page := profilePage{
		base:      a.newBase(r, "profile"),
		Tokens:    views,
		Form:      form,
		EmailForm: email,
		NewSecret: a.sessions.PopString(r.Context(), "newToken"),
	}
	if status == http.StatusUnprocessableEntity {
		a.renderInvalid(w, r, "profile.html", page)
		return
	}
	a.render(w, r, status, "profile.html", "", page)
}
