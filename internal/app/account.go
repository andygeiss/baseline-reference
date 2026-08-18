package app

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/andygeiss/baseline-reference/v3/internal/auth"
	"github.com/andygeiss/baseline-reference/v3/internal/domain"
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
func (a *App) handleAccountDelete(w http.ResponseWriter, r *http.Request) {
	if !a.parseForm(w, r) {
		return
	}
	user := userFrom(r.Context())

	ok, err := auth.VerifyPassword(user.PasswordHash, r.PostFormValue("password"))
	if err != nil {
		a.serverError(w, r, fmt.Errorf("verifying the password of %q: %w", user.Name, err))
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

	// ErrNotFound means the account went while this form was open. The reader
	// asked for it gone and it is gone, so this is the same answer as success.
	if err := a.users.Delete(r.Context(), user.ID); err != nil && !errors.Is(err, domain.ErrNotFound) {
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
