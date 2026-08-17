package app

import (
	"crypto/rand"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/andygeiss/baseline-reference/v3/internal/auth"
	"github.com/andygeiss/baseline-reference/v3/internal/domain"
)

type resetPage struct {
	base
	Form resetForm
}

type resetConfirmPage struct {
	base
	Form resetConfirmForm
}

// resetSent is the one answer POST /reset ever gives.
//
// It does not depend on whether the account exists, whether it has an address,
// or whether the mail went out — and it must not. Anything that changed with
// any of those turns this form into a way to ask which accounts are real.
const resetSent = "If that account has an email address, a link is on its way. It works for one hour."

func (a *App) handleResetForm(w http.ResponseWriter, r *http.Request) {
	a.render(w, r, http.StatusOK, "reset.html", "", resetPage{base: a.newBase(r, "")})
}

// handleReset queues a reset link, and says the same thing whatever happened.
func (a *App) handleReset(w http.ResponseWriter, r *http.Request) {
	if !a.parseForm(w, r) || !a.allow(w, r) {
		return
	}
	form := resetForm{Name: domain.NormalizeName(r.PostFormValue("name"))}

	user, err := a.users.ByName(r.Context(), form.Name)
	switch {
	case err == nil:
		if err := a.queueReset(r, user); err != nil {
			// The reader is told nothing about this: a queue that failed and an
			// account with no address have to look the same from out there. The
			// log is where it is visible, and the person can ask again.
			a.logger.Error("queueing a password reset", "user", user.ID, "err", err)
		}
	case errors.Is(err, domain.ErrNotFound):
		// Nobody by that name. Same answer, same page, and no work skipped that
		// anybody could time.
	default:
		a.serverError(w, r, err)
		return
	}

	a.flash(r, resetSent)
	a.redirect(w, r, "/login")
}

// queueReset stores a one-hour link and the message carrying it.
//
// The token is shown once, in that message. Only its SHA-256 is stored, so a
// copy of this database redeems nothing (patterns/go-auth-sessions.md).
func (a *App) queueReset(r *http.Request, user domain.User) error {
	if user.Email == "" {
		return nil // recoverable accounts are the ones with an address
	}
	token := rand.Text() // 128 unguessable bits, URL-safe, no encoding step
	res := &domain.Reset{
		Hash:      auth.HashToken(token),
		UserID:    user.ID,
		ExpiresAt: time.Now().UTC().Add(domain.ResetLifetime),
	}
	return a.resets.Add(r.Context(), res, domain.Mail{
		To:      user.Email,
		Subject: "Reset your Go Chat password",
		Text: fmt.Sprintf(`Hello %s,

Somebody asked to reset the password on your Go Chat account. If that was you,
open this link within the hour:

%s

If it was not you, do nothing. The link expires on its own and your password
has not changed.
`, user.Name, a.resetLink(token)),
	})
}

// resetLink builds the address in the message.
//
// It comes from the configured base URL and from nothing in the request. r.Host
// is whatever the client put in the Host header, and a proxy in front does not
// fix that — X-Forwarded-Host is the same header one hop further away. Built
// from either, this link mails a working token to whoever asked for it.
func (a *App) resetLink(token string) string {
	u := a.baseURL.JoinPath("reset", "confirm") // JoinPath copies, so baseURL is untouched
	u.RawQuery = url.Values{"t": {token}}.Encode()
	return u.String()
}

// handleResetConfirmForm shows the new-password form for a link that was
// clicked. The token is not checked here: checking it would make this page
// answer differently for a good link and a dead one, which is the same
// enumeration the form above avoids. It is spent on the POST.
func (a *App) handleResetConfirmForm(w http.ResponseWriter, r *http.Request) {
	a.render(w, r, http.StatusOK, "reset-confirm.html", "", resetConfirmPage{
		base: a.newBase(r, ""),
		Form: resetConfirmForm{Token: r.URL.Query().Get("t")},
	})
}

// handleResetConfirm spends a link and sets the new password.
func (a *App) handleResetConfirm(w http.ResponseWriter, r *http.Request) {
	if !a.parseForm(w, r) || !a.allow(w, r) {
		return
	}
	form := resetConfirmForm{Token: r.PostFormValue("token")}
	password := r.PostFormValue("password")

	form.Check(domain.ValidatePassword(password) == nil, "password", fmt.Sprintf(
		"Use at least %d characters.", domain.MinPasswordLen))
	if !form.Valid() {
		a.renderInvalid(w, r, "reset-confirm.html", resetConfirmPage{base: a.newBase(r, ""), Form: form})
		return
	}

	// Take deletes the row in the same transaction that reads it, so a link
	// works exactly once even if two requests arrive together.
	res, err := a.resets.Take(r.Context(), auth.HashToken(form.Token), time.Now().UTC())
	switch {
	case err == nil:
	case errors.Is(err, domain.ErrNotFound):
		form.CheckForm(false, "That link has expired or has already been used. Ask for a new one.")
		a.renderInvalid(w, r, "reset-confirm.html", resetConfirmPage{base: a.newBase(r, ""), Form: form})
		return
	default:
		a.serverError(w, r, err)
		return
	}

	user, err := a.users.ByID(r.Context(), res.UserID)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	// The stamp this returns is what ends every session opened before now,
	// including whichever one made the reset necessary.
	changedAt, err := a.users.SetPassword(r.Context(), user.ID, hash)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	user.PasswordChangedAt = changedAt
	if err := a.signIn(r, user); err != nil {
		a.serverError(w, r, err)
		return
	}
	a.flash(r, "That is your new password. Anywhere else you were signed in has been signed out.")
	a.redirect(w, r, "/rooms")
}
