package app

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/andygeiss/baseline-reference/v3/internal/domain"
)

// seedUser puts somebody straight into the fake store. Registering over HTTP
// would work too and would spend the rate limiter these tests are also using.
func seedUser(ta *testApp, id, name, email string) {
	ta.users.byID[id] = domain.User{ID: id, Name: name, Email: email, PasswordHash: "x"}
}

// tokenFromMail pulls the plaintext token out of the one message that was
// queued. It exists in exactly one place, which is the message — the store kept
// only its SHA-256.
func tokenFromMail(t *testing.T, ta *testApp) string {
	t.Helper()
	mails := ta.resets.mails()
	if len(mails) != 1 {
		t.Fatalf("queued %d messages, want 1", len(mails))
	}
	_, after, ok := strings.Cut(mails[0].Text, "?t=")
	if !ok {
		t.Fatalf("no link in the message:\n%s", mails[0].Text)
	}
	return strings.TrimSpace(strings.SplitN(after, "\n", 2)[0])
}

// TestResetAnswersTheSameWhoeverAsks is the no-enumeration test. Three requests
// that the server knows three different things about, and one answer.
func TestResetAnswersTheSameWhoeverAsks(t *testing.T) {
	t.Parallel()
	ta := newTestApp(t)
	seedUser(ta, "ada", "Ada", "ada@example.com")
	seedUser(ta, "bob", "Bob", "") // real account, no address: unrecoverable

	ask := func(t *testing.T, name string) (int, string, string) {
		t.Helper()
		res, _ := ta.do(t, http.MethodPost, "/reset", url.Values{"name": {name}}, nil)
		// The words land on the page after the redirect, so that page is part
		// of the answer being compared.
		_, page := ta.do(t, http.MethodGet, "/login", nil, nil)
		return res.StatusCode, res.Header.Get("Location"), page
	}

	wantStatus, wantTo, wantPage := ask(t, "Ada")
	if wantStatus != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", wantStatus)
	}
	if !strings.Contains(wantPage, "a link is on its way") {
		t.Fatalf("the page after the redirect does not carry the answer:\n%s", wantPage)
	}

	for _, name := range []string{"Bob", "Nobody At All"} {
		status, to, page := ask(t, name)
		if status != wantStatus || to != wantTo {
			t.Errorf("%s: %d %q, want %d %q", name, status, to, wantStatus, wantTo)
		}
		if page != wantPage {
			t.Errorf("%s: the page differs from the one a real, recoverable account gets", name)
		}
	}

	// Exactly one message: for the account that has an address, and for neither
	// of the others.
	if mails := ta.resets.mails(); len(mails) != 1 || mails[0].To != "ada@example.com" {
		t.Errorf("queued %v, want one message to ada@example.com", mails)
	}
}

// TestResetLinkIgnoresTheHostHeader is the regression test for the tier-1 rule.
// Host is whatever the client sent; a link built from it mails a working token
// to whoever asked.
func TestResetLinkIgnoresTheHostHeader(t *testing.T) {
	t.Parallel()
	ta := newTestApp(t)
	seedUser(ta, "ada", "Ada", "ada@example.com")

	form := url.Values{"name": {"Ada"}}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost,
		ta.server.URL+"/reset", strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatalf("building the request: %v", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Host = "evil.example" // the header, not the address dialled

	res, err := ta.client.Do(req)
	if err != nil {
		t.Fatalf("POST /reset: %v", err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", res.StatusCode)
	}

	mails := ta.resets.mails()
	if len(mails) != 1 {
		t.Fatalf("queued %d messages, want 1", len(mails))
	}
	if strings.Contains(mails[0].Text, "evil.example") {
		t.Errorf("the link was built from the Host header:\n%s", mails[0].Text)
	}
	if !strings.Contains(mails[0].Text, "https://chat.example.com/reset/confirm?t=") {
		t.Errorf("the link was not built from the configured base URL:\n%s", mails[0].Text)
	}
}

func TestResetLinkWorksOnceAndEndsOtherSessions(t *testing.T) {
	t.Parallel()
	ta := newTestApp(t)

	// Ada is signed in on one machine, and she sets an address there.
	ta.signUp(t, "Ada", "correct horse battery")
	if res, _ := ta.do(t, http.MethodPost, "/profile/email",
		url.Values{"email": {"ada@example.com"}}, nil); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("saving the address: status = %d, want 303", res.StatusCode)
	}
	firstMachine := ta.client.Jar

	// On another machine, signed out, she asks for a link and uses it.
	ta.client.Jar = emptyJar(t)
	if res, _ := ta.do(t, http.MethodPost, "/reset", url.Values{"name": {"Ada"}}, nil); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("asking for a link: status = %d, want 303", res.StatusCode)
	}
	token := tokenFromMail(t, ta)

	res, _ := ta.do(t, http.MethodPost, "/reset/confirm", url.Values{
		"token": {token}, "password": {"a whole new password"},
	}, nil)
	if res.StatusCode != http.StatusSeeOther || res.Header.Get("Location") != "/rooms" {
		t.Fatalf("using the link: %d %q, want 303 to /rooms", res.StatusCode, res.Header.Get("Location"))
	}
	if page, _ := ta.do(t, http.MethodGet, "/rooms", nil, nil); page.StatusCode != http.StatusOK {
		t.Fatalf("after the reset: status = %d, want to be signed in", page.StatusCode)
	}

	t.Run("the link is spent", func(t *testing.T) {
		again, body := ta.do(t, http.MethodPost, "/reset/confirm", url.Values{
			"token": {token}, "password": {"another new password"},
		}, nil)
		if again.StatusCode != http.StatusUnprocessableEntity {
			t.Fatalf("second use: status = %d, want 422", again.StatusCode)
		}
		if !strings.Contains(body, "expired or has already been used") {
			t.Errorf("the page does not say why:\n%s", body)
		}
	})

	t.Run("the session on the other machine is over", func(t *testing.T) {
		// The point of resetting a password you think somebody else knows.
		ta.client.Jar = firstMachine
		res, _ := ta.do(t, http.MethodGet, "/rooms", nil, nil)
		if res.StatusCode != http.StatusSeeOther || res.Header.Get("Location") != "/login" {
			t.Errorf("the old session answered %d %q, want 303 to /login",
				res.StatusCode, res.Header.Get("Location"))
		}
	})
}

func TestResetRefusesAPasswordThatIsTooShort(t *testing.T) {
	t.Parallel()
	ta := newTestApp(t)
	seedUser(ta, "ada", "Ada", "ada@example.com")
	if res, _ := ta.do(t, http.MethodPost, "/reset", url.Values{"name": {"Ada"}}, nil); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("asking for a link: status = %d", res.StatusCode)
	}
	token := tokenFromMail(t, ta)

	res, body := ta.do(t, http.MethodPost, "/reset/confirm", url.Values{
		"token": {token}, "password": {"short"},
	}, nil)
	if res.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", res.StatusCode)
	}
	if !strings.Contains(body, "at least 8 characters") {
		t.Errorf("the page does not say the rule:\n%s", body)
	}
	// The token survives the re-render, so a short password does not cost the
	// person their trip back to the mail.
	if !strings.Contains(body, token) {
		t.Error("the token was dropped from the re-rendered form")
	}
	// And it is still spendable, because a rejected password never reached the
	// store.
	if ok, _ := ta.do(t, http.MethodPost, "/reset/confirm", url.Values{
		"token": {token}, "password": {"a long enough password"},
	}, nil); ok.StatusCode != http.StatusSeeOther {
		t.Errorf("the link stopped working after a rejected password: %d", ok.StatusCode)
	}
}

func TestProfileStoresAnAddress(t *testing.T) {
	t.Parallel()
	ta := newTestApp(t)
	ta.signUp(t, "Ada", "correct horse battery")

	t.Run("a bad address is refused with the value kept", func(t *testing.T) {
		res, body := ta.do(t, http.MethodPost, "/profile/email",
			url.Values{"email": {"not-an-address"}}, nil)
		if res.StatusCode != http.StatusUnprocessableEntity {
			t.Fatalf("status = %d, want 422", res.StatusCode)
		}
		if !strings.Contains(body, "not-an-address") {
			t.Error("the typed address was thrown away")
		}
		if !strings.Contains(body, `aria-invalid="true"`) {
			t.Error("the field is not marked invalid for a screen reader")
		}
	})

	t.Run("an empty address clears it", func(t *testing.T) {
		if res, _ := ta.do(t, http.MethodPost, "/profile/email",
			url.Values{"email": {"ada@example.com"}}, nil); res.StatusCode != http.StatusSeeOther {
			t.Fatalf("saving: status = %d", res.StatusCode)
		}
		if res, _ := ta.do(t, http.MethodPost, "/profile/email",
			url.Values{"email": {""}}, nil); res.StatusCode != http.StatusSeeOther {
			t.Fatalf("clearing: status = %d", res.StatusCode)
		}
		u, err := ta.users.ByName(t.Context(), "Ada")
		if err != nil {
			t.Fatalf("reading Ada: %v", err)
		}
		if u.Email != "" {
			t.Errorf("Email = %q, want it cleared", u.Email)
		}
	})
}
