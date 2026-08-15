package app

import (
	"crypto/rand"
	"crypto/subtle"
	"errors"
	"fmt"
	"net/http"

	"github.com/andygeiss/baseline-reference/v3/internal/auth"
	"github.com/andygeiss/baseline-reference/v3/internal/domain"
)

type loginPage struct {
	base
	Form loginForm
}

type registerPage struct {
	base
	Form registerForm
	// InviteRequired shows the invite field. The deployment decides it, by
	// passing a credential file or not.
	InviteRequired bool
}

func (a *App) handleLoginForm(w http.ResponseWriter, r *http.Request) {
	a.render(w, r, http.StatusOK, "login.html", "", loginPage{base: a.newBase(r, "")})
}

func (a *App) handleRegisterForm(w http.ResponseWriter, r *http.Request) {
	a.render(w, r, http.StatusOK, "register.html", "", registerPage{
		base:           a.newBase(r, ""),
		InviteRequired: a.inviteCode != "",
	})
}

func (a *App) handleLogin(w http.ResponseWriter, r *http.Request) {
	if !a.parseForm(w, r) || !a.allow(w, r) {
		return
	}

	form := loginForm{Name: domain.NormalizeName(r.PostFormValue("name"))}
	password := r.PostFormValue("password")

	// The name is looked up, and the password is verified either way. When
	// nobody has that name the check runs against a hash of a password nobody
	// has, so a wrong name and a wrong password cost the same time. Answering
	// "no such user" quickly is how an attacker collects the list of real ones.
	user, err := a.users.ByName(r.Context(), form.Name)
	known := err == nil
	if err != nil && !errors.Is(err, domain.ErrNotFound) {
		a.serverError(w, r, err)
		return
	}
	hash := a.dummyHash
	if known {
		hash = user.PasswordHash
	}
	ok, err := auth.VerifyPassword(hash, password)
	if err != nil {
		a.serverError(w, r, fmt.Errorf("verifying the password of %q: %w", form.Name, err))
		return
	}

	if !known || !ok {
		form.CheckForm(false, "We do not know that name and password.")
		a.renderInvalid(w, r, "login.html", loginPage{base: a.newBase(r, ""), Form: form})
		return
	}

	a.rehashIfWeak(r, user, password)
	if err := a.signIn(r, user.ID); err != nil {
		a.serverError(w, r, err)
		return
	}
	a.flash(r, "Welcome back, "+user.Name+".")
	a.redirect(w, r, "/rooms")
}

func (a *App) handleRegister(w http.ResponseWriter, r *http.Request) {
	if !a.parseForm(w, r) || !a.allow(w, r) {
		return
	}

	form := registerForm{
		Name:   domain.NormalizeName(r.PostFormValue("name")),
		Invite: r.PostFormValue("invite"),
	}
	password := r.PostFormValue("password")

	form.Check(domain.ValidateName(form.Name) == nil, "name", fmt.Sprintf(
		"Use %d to %d letters, digits, spaces, hyphens, or underscores.",
		domain.MinNameLen, domain.MaxNameLen))
	form.Check(domain.ValidatePassword(password) == nil, "password", fmt.Sprintf(
		"Use at least %d characters.", domain.MinPasswordLen))
	if a.inviteCode != "" {
		// Constant time: comparing byte by byte would let somebody find the
		// code one character at a time, by watching how long the answer takes.
		form.Check(subtle.ConstantTimeCompare([]byte(form.Invite), []byte(a.inviteCode)) == 1,
			"invite", "That invite code is not right.")
	}
	if !form.Valid() {
		a.renderInvalidRegister(w, r, form)
		return
	}

	hash, err := auth.HashPassword(password)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	// rand.Text: 128 unguessable bits, URL-safe, no encoding step.
	user, err := domain.NewUser(rand.Text(), form.Name, hash)
	if err != nil {
		// Unreachable while the checks above state the same rules. It stays
		// because the domain, not this handler, decides what a user may be.
		a.serverError(w, r, err)
		return
	}

	switch err := a.users.Add(r.Context(), user); {
	case err == nil:
	case errors.Is(err, domain.ErrNameTaken):
		form.Check(false, "name", "Somebody already goes by that name.")
		a.renderInvalidRegister(w, r, form)
		return
	default:
		a.serverError(w, r, err)
		return
	}

	if err := a.signIn(r, user.ID); err != nil {
		a.serverError(w, r, err)
		return
	}
	a.flash(r, "Welcome, "+user.Name+".")
	a.redirect(w, r, "/rooms")
}

func (a *App) handleLogout(w http.ResponseWriter, r *http.Request) {
	// Destroy, not RenewToken: logging out removes the session rather than
	// giving it a new name.
	if err := a.sessions.Destroy(r.Context()); err != nil {
		a.serverError(w, r, err)
		return
	}
	a.redirect(w, r, "/login")
}

// signIn starts the session. RenewToken first, always: without it, a token an
// attacker planted in the browser before the login is still valid after it.
func (a *App) signIn(r *http.Request, userID string) error {
	if err := a.sessions.RenewToken(r.Context()); err != nil {
		return fmt.Errorf("renewing the session token: %w", err)
	}
	a.sessions.Put(r.Context(), "userID", userID)
	return nil
}

// rehashIfWeak upgrades a stored password hash that was written under weaker
// argon2id settings than today's. A failure here is logged and swallowed: the
// password was right, so the login proceeds either way.
func (a *App) rehashIfWeak(r *http.Request, user domain.User, password string) {
	weak, err := auth.NeedsRehash(user.PasswordHash)
	if err != nil || !weak {
		return
	}
	hash, err := auth.HashPassword(password)
	if err == nil {
		err = a.users.UpdatePasswordHash(r.Context(), user.ID, hash)
	}
	if err != nil {
		a.logger.Error("rehashing password", "user", user.ID, "err", err)
	}
}

// allow reports whether this client may make another attempt at signing in.
// Over the limit it answers 429 and says when to come back.
func (a *App) allow(w http.ResponseWriter, r *http.Request) bool {
	if a.limiter.allow(clientIP(r)) {
		return true
	}
	w.Header().Set("Retry-After", "3")
	a.clientError(w, r, http.StatusTooManyRequests)
	return false
}

// parseForm reads the body, answering the reader itself when it cannot.
func (a *App) parseForm(w http.ResponseWriter, r *http.Request) bool {
	if err := r.ParseForm(); err != nil {
		status := http.StatusBadRequest // malformed body — not a validation failure
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			status = http.StatusRequestEntityTooLarge // the 1 MiB cap
		}
		a.clientError(w, r, status)
		return false
	}
	return true
}

func (a *App) renderInvalidRegister(w http.ResponseWriter, r *http.Request, form registerForm) {
	a.renderInvalid(w, r, "register.html", registerPage{
		base:           a.newBase(r, ""),
		Form:           form,
		InviteRequired: a.inviteCode != "",
	})
}

// renderInvalid re-renders a whole page at 422 with the submitted values and
// the errors. These pages have no fragment worth swapping on their own, so the
// answer is the page either way.
func (a *App) renderInvalid(w http.ResponseWriter, r *http.Request, page string, data any) {
	if r.Header.Get("HX-Boosted") == "true" {
		// A boosted swap otherwise pushes the POST URL into history, and a
		// refresh then GETs a route that only answers POST.
		w.Header().Set("HX-Push-Url", "false")
	}
	a.render(w, r, http.StatusUnprocessableEntity, page, "", data)
}
