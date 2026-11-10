package app

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/andygeiss/baseline-reference/v6/internal/auth"
	"github.com/andygeiss/baseline-reference/v6/internal/domain"
)

type deleteAccountPage struct {
	base
	Form deleteAccountForm
}

// deleteAccountForm carries no values back. The name is retyped on purpose and
// the password is a password, so a re-render starts both boxes empty.
type deleteAccountForm struct {
	Validator
}

func (a *App) handleAccountDeleteForm(w http.ResponseWriter, r *http.Request) {
	a.render(w, r, http.StatusOK, "account-delete.html", "",
		deleteAccountPage{base: a.newBase(r, "")})
}

// handleAccountDelete erases the signed-in account.
//
// Two things have to be right first, and both are checked here rather than in
// the browser. The password, because a session somebody found unlocked would
// otherwise be enough to erase its owner. And the account name, retyped,
// because an irreversible action should cost a sentence.
//
// hx-confirm is what the token revoke uses, and it is not enough for this one:
// it is a dialog htmx draws, so it is nothing at all with htmx switched off,
// and a guard the server cannot check is not a guard
// (patterns/go-data-deletion.md).
//
// The password is checked behind the login's limiter, in the login's bucket:
// without it an unlocked session is also a way to guess the password at full
// speed (patterns/go-data-deletion.md).
func (a *App) handleAccountDelete(w http.ResponseWriter, r *http.Request) {
	if !a.parseForm(w, r) || !a.allow(w, r) {
		return
	}
	user := userFrom(r.Context())

	ok, err := auth.VerifyPassword(user.PasswordHash, r.PostFormValue("password"))
	if err != nil {
		a.serverError(w, r, fmt.Errorf("verifying the password of user %s: %w", user.ID, err))
		return
	}

	var form deleteAccountForm
	form.Check(domain.NormalizeName(r.PostFormValue("name")) == user.Name,
		"name", "That is not the name on this account.")
	form.Check(ok, "password", "That is not your password.")
	if !form.Valid() {
		a.renderInvalid(w, r, "account-delete.html",
			deleteAccountPage{base: a.newBase(r, ""), Form: form})
		return
	}

	// The DELETE names the password counter of the row this request loaded,
	// the row the password was just checked against. ErrStale means a reset
	// landed while this form was open: what was typed is no longer the
	// password, and nothing is deleted (patterns/go-data-deletion.md,
	// Deleting is a write). ErrNotFound means the account went while this form
	// was open. The reader asked for it gone and it is gone, so that is the
	// same answer as success.
	switch err := a.users.Delete(r.Context(), user.ID, user.PasswordVersion); {
	case err == nil, errors.Is(err, domain.ErrNotFound):
	case errors.Is(err, domain.ErrStale):
		form.Check(false, "password", "That is not your password.")
		a.renderInvalid(w, r, "account-delete.html",
			deleteAccountPage{base: a.newBase(r, ""), Form: form})
		return
	default:
		a.serverError(w, r, err)
		return
	}

	// The row is gone, so authenticate would drop this session on the next
	// request anyway — that is the rule that makes a delete reach a browser no
	// cascade can see. Destroying it here is what stops the redirect bouncing
	// through a page the reader is no longer entitled to.
	if err := a.sessions.Destroy(r.Context()); err != nil {
		a.serverError(w, r, err)
		return
	}
	a.redirect(w, r, "/login")
}
