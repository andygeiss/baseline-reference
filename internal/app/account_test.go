package app

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
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
