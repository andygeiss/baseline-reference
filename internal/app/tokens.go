package app

import (
	"crypto/rand"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/andygeiss/baseline-reference/v5/internal/auth"
	"github.com/andygeiss/baseline-reference/v5/internal/domain"
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

	// NewSecret is the token just created, on the answer to the request that
	// created it and on no other: the server kept only its hash.
	NewSecret string
}

func (a *App) handleProfile(w http.ResponseWriter, r *http.Request) {
	a.renderProfile(w, r, http.StatusOK, "", profilePage{})
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
		a.renderProfile(w, r, http.StatusUnprocessableEntity, "", profilePage{EmailForm: form})
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

// handleTokenCreate makes a machine token, once the password has been typed
// again, and shows its secret in the answer to that same request.
//
// A session alone must not be enough. A token outlives the session that made
// it — no logout ends it and no password reset does — so whoever found a
// browser unlocked, or holds one leaked token, would otherwise leave with a
// credential of their own. It is a route that checks a password, so it sits
// behind the login's limiter, in the login's bucket
// (patterns/go-auth-sessions.md, Machine tokens rule 3).
//
// Its 200 and its 422 are the tokens region for htmx and the whole page for a
// plain form: the form is a fragment form, because its good answer cannot be
// a redirect.
func (a *App) handleTokenCreate(w http.ResponseWriter, r *http.Request) {
	if !a.parseForm(w, r) || !a.allow(w, r) {
		return
	}
	user := userFrom(r.Context())

	ok, err := auth.VerifyPassword(user.PasswordHash, r.PostFormValue("password"))
	if err != nil {
		a.serverError(w, r, fmt.Errorf("verifying the password of user %s: %w", user.ID, err))
		return
	}

	form := tokenForm{Label: domain.NormalizeLabel(r.PostFormValue("label"))}
	form.Check(domain.ValidateLabel(form.Label) == nil, "label",
		fmt.Sprintf("Give it a name of 1 to %d characters.", domain.MaxLabelLen))
	form.Check(ok, "password", "That is not your password.")
	if !form.Valid() {
		a.renderProfile(w, r, http.StatusUnprocessableEntity, "tokens", profilePage{Form: form})
		return
	}

	secret, hash, err := auth.NewToken()
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	// rand.Text: 128 unguessable bits, URL-safe, no encoding step.
	token := &domain.Token{ID: rand.Text(), UserID: user.ID, Hash: hash, Label: form.Label}
	// The password was checked against the row this request loaded when its
	// headers arrived, and the body may have been held open since. The insert
	// names that row's password counter, so a reset that landed in between
	// leaves no token behind: what was typed is no longer the password
	// (patterns/go-auth-sessions.md rule 1).
	switch err := a.tokens.Add(r.Context(), token, user.PasswordVersion); {
	case err == nil:
	case errors.Is(err, domain.ErrStale):
		form.Check(false, "password", "That is not your password.")
		a.renderProfile(w, r, http.StatusUnprocessableEntity, "tokens", profilePage{Form: form})
		return
	default:
		a.serverError(w, r, err)
		return
	}

	// The secret is written into the answer to this request and nowhere else.
	// A redirect would have to carry it to the next page in the session, and
	// the session is a row in the database: the plaintext of a credential,
	// stored until somebody loads that page. No flash either — the region
	// htmx swaps, or the page a plain form gets back, is the feedback.
	//
	// What a 200 costs a browser without htmx: a reload of this answer asks
	// to send the form again, and sent again it makes a second token.
	a.renderProfile(w, r, http.StatusOK, "tokens", profilePage{NewSecret: secret})
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

// renderProfile answers with the profile page, or with block when the request
// is a fragment's. The caller sets what it has to show — a form that came
// back, the secret just made — and this fills in the rest.
func (a *App) renderProfile(w http.ResponseWriter, r *http.Request, status int, block string, page profilePage) {
	user := userFrom(r.Context())
	tokens, err := a.tokens.ByUser(r.Context(), user.ID)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	// The form shows what is stored unless the reader is looking at something
	// they just typed and got back.
	if page.EmailForm.Email == "" && page.EmailForm.Valid() {
		page.EmailForm.Email = user.Email
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
	page.base = a.newBase(r, "profile")
	page.Tokens = views
	// render writes the block only for a fragment request, so a 422 or the new
	// secret reaches a plain form as the whole page.
	a.render(w, r, status, "profile.html", block, page)
}
