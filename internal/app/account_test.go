package app

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"testing/synctest"

	"github.com/andygeiss/baseline-reference/v6/internal/domain"
)

// The delete route names no account. It erases whoever is signed in, so the
// two-user test every owned row gets (patterns/go-authorization.md) has no id
// to pass — the check that matters is the one below: Alice leaving does not
// take Bob with her.

func TestDeletingAnAccountNeedsTheNameAndThePassword(t *testing.T) {
	t.Parallel()
	ta := newTestApp(t)
	ta.signUp(t, "alice", "correct horse battery")

	cases := map[string]url.Values{
		"wrong name":     {"name": {"alicia"}, "password": {"correct horse battery"}},
		"wrong password": {"name": {"alice"}, "password": {"hunter2"}},
		"nothing at all": {"name": {""}, "password": {""}},
	}
	for name, form := range cases {
		t.Run(name, func(t *testing.T) {
			res, body := ta.do(t, http.MethodPost, "/account/delete", form, nil)
			if res.StatusCode != http.StatusUnprocessableEntity {
				t.Errorf("status %d, want 422", res.StatusCode)
			}
			if !strings.Contains(body, "Delete your account") {
				t.Error("the confirmation page did not come back with the error")
			}
		})
	}

	// Still signed in, and still there.
	if res, _ := ta.do(t, http.MethodGet, "/profile", nil, nil); res.StatusCode != http.StatusOK {
		t.Errorf("profile after three refused deletes: status %d, want 200", res.StatusCode)
	}
}

func TestDeletingAnAccountSignsTheBrowserOut(t *testing.T) {
	t.Parallel()
	ta := newTestApp(t)
	ta.signUp(t, "alice", "correct horse battery")

	res, _ := ta.do(t, http.MethodPost, "/account/delete", url.Values{
		"name": {"alice"}, "password": {"correct horse battery"},
	}, nil)
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("deleting the account: status %d, want 303", res.StatusCode)
	}
	if got := res.Header.Get("Location"); got != "/login" {
		t.Errorf("Location = %q, want /login", got)
	}

	// The cookie the jar is still holding names an account that is gone. This
	// is the half no cascade reaches: the session row is untouched, and what
	// ends it is authenticate resolving the credential to a row.
	if res, _ := ta.do(t, http.MethodGet, "/profile", nil, nil); res.StatusCode != http.StatusSeeOther {
		t.Errorf("profile with a deleted account's cookie: status %d, want 303 to the sign-in page", res.StatusCode)
	}
}

// TestAnAccountDeleteHeldOpenAcrossAResetDeletesNothing: the delete is allowed
// by a password checked against the row the request loaded, and a reset that
// lands while its body is held open has made that check worthless — it is the
// owner locking out whoever knew the old password. The DELETE names the counter
// the request loaded, so it changes no row (patterns/go-data-deletion.md,
// Deleting is a write). By id alone, the old password still erased the account.
func TestAnAccountDeleteHeldOpenAcrossAResetDeletesNothing(t *testing.T) {
	t.Parallel()
	ta := newTestApp(t)
	ta.signUp(t, "alice", "correct horse battery")
	id := ta.onlyUserID(t)
	var once sync.Once
	ta.users.afterByID = func(u domain.User) {
		once.Do(func() { ta.users.reset(u.ID, "the-resets-hash") })
	}

	res, body := ta.do(t, http.MethodPost, "/account/delete", url.Values{
		"name": {"alice"}, "password": {"correct horse battery"},
	}, nil)

	if res.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("status %d, want 422", res.StatusCode)
	}
	if !strings.Contains(body, "That is not your password.") {
		t.Error("the refusal is not worded like any other wrong password")
	}
	got, err := ta.users.ByID(t.Context(), id)
	if err != nil {
		t.Fatalf("the account after the refused delete: %v", err)
	}
	if got.PasswordHash != "the-resets-hash" {
		t.Errorf("hash = %q, want the reset's", got.PasswordHash)
	}
}

func TestDeletingAnAccountLeavesEverybodyElseAlone(t *testing.T) {
	t.Parallel()
	ta := newTestApp(t)
	ta.signUp(t, "bob", "correct horse battery")
	bob := ta.client.Jar

	ta.client.Jar = emptyJar(t)
	ta.signUp(t, "alice", "correct horse battery")
	res, _ := ta.do(t, http.MethodPost, "/account/delete", url.Values{
		"name": {"alice"}, "password": {"correct horse battery"},
	}, nil)
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("deleting alice: status %d, want 303", res.StatusCode)
	}

	ta.client.Jar = bob
	res, body := ta.do(t, http.MethodGet, "/profile", nil, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("bob's profile after alice left: status %d, want 200", res.StatusCode)
	}
	if !strings.Contains(body, "bob") {
		t.Error("bob's own page no longer names him")
	}
}

// The confirmation is a page, not a dialog. hx-confirm is what the token revoke
// uses; it is drawn by htmx, so it is nothing with htmx switched off, and the
// server cannot check it (patterns/go-data-deletion.md).
func TestTheDeleteConfirmationIsServerSide(t *testing.T) {
	t.Parallel()
	ta := newTestApp(t)
	ta.signUp(t, "alice", "correct horse battery")

	res, body := ta.do(t, http.MethodGet, "/account/delete", nil, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("the confirmation page: status %d, want 200", res.StatusCode)
	}
	if strings.Contains(body, "hx-confirm") {
		t.Error("the confirmation page leans on hx-confirm, which htmx draws and the server cannot check")
	}
	for _, field := range []string{`name="name"`, `name="password"`} {
		if !strings.Contains(body, field) {
			t.Errorf("the confirmation form is missing %s", field)
		}
	}
}

// TestTheDeletePasswordCheckIsRateLimited is the other half of asking for the
// password again: a form that checks a password is a place to guess one, so it
// sits behind the login's limiter, or an unlocked session is also a way to
// find out the password at full speed (patterns/go-data-deletion.md).
func TestTheDeletePasswordCheckIsRateLimited(t *testing.T) {
	t.Parallel()
	// Made outside the bubble, with a context that never cancels, and on
	// synctest's clock so the bucket cannot refill while argon2 runs —
	// TestLoginRateLimit says why.
	o := newTestOptions(t, withStopping(context.Background()))

	synctest.Test(t, func(t *testing.T) {
		ta := newTestAppWith(t, o)
		ta.signUp(t, "alice", "correct horse battery")

		guess := url.Values{"name": {"alice"}, "password": {"hunter2"}}
		// The bucket holds five and signing up spent some of them, so six
		// guesses cannot all be answered.
		var last *http.Response
		for range 6 {
			last, _ = ta.do(t, http.MethodPost, "/account/delete", guess, nil)
		}
		if last.StatusCode != http.StatusTooManyRequests {
			t.Fatalf("sixth wrong password on the delete form = %d, want 429", last.StatusCode)
		}
		// Over the limit the right password is refused too, and the account
		// is still there: the limiter answers before anything is checked.
		right := url.Values{"name": {"alice"}, "password": {"correct horse battery"}}
		if res, _ := ta.do(t, http.MethodPost, "/account/delete", right, nil); res.StatusCode != http.StatusTooManyRequests {
			t.Errorf("delete while limited = %d, want 429", res.StatusCode)
		}
		if res, _ := ta.do(t, http.MethodGet, "/profile", nil, nil); res.StatusCode != http.StatusOK {
			t.Errorf("profile after the refused deletes: status %d, want 200", res.StatusCode)
		}
	})
}
