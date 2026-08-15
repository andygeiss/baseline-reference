package app

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestRegister(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		form       url.Values
		wantStatus int
		wantText   string
	}{
		{
			name:       "a good registration signs the person in",
			form:       url.Values{"name": {"Ada"}, "password": {"correct-horse"}},
			wantStatus: http.StatusSeeOther,
		},
		{
			name:       "a short name is refused with the reason",
			form:       url.Values{"name": {"A"}, "password": {"correct-horse"}},
			wantStatus: http.StatusUnprocessableEntity,
			wantText:   "letters, digits, spaces",
		},
		{
			name:       "a short password is refused with the reason",
			form:       url.Values{"name": {"Ada"}, "password": {"short"}},
			wantStatus: http.StatusUnprocessableEntity,
			wantText:   "at least 8 characters",
		},
		{
			name:       "a name of only punctuation is refused",
			form:       url.Values{"name": {"!!!!"}, "password": {"correct-horse"}},
			wantStatus: http.StatusUnprocessableEntity,
			wantText:   "letters, digits, spaces",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ta := newTestApp(t)

			res, body := ta.do(t, http.MethodPost, "/register", tt.form, nil)

			if res.StatusCode != tt.wantStatus {
				t.Errorf("status = %d, want %d", res.StatusCode, tt.wantStatus)
			}
			if tt.wantText != "" && !strings.Contains(body, tt.wantText) {
				t.Errorf("body does not explain the problem; want %q in:\n%s", tt.wantText, body)
			}
		})
	}
}

func TestRegisterKeepsTheNameAndNeverThePassword(t *testing.T) {
	t.Parallel()
	ta := newTestApp(t)

	_, body := ta.do(t, http.MethodPost, "/register", url.Values{
		"name": {"Ada"}, "password": {"short"},
	}, nil)

	if !strings.Contains(body, `value="Ada"`) {
		t.Error("the name was not kept, so it has to be retyped")
	}
	// A re-rendered password is a password in the HTML, in the browser cache,
	// and in any proxy that sees the page.
	if strings.Contains(body, "short") {
		t.Error("the submitted password came back in the response")
	}
}

func TestRegisterTakenName(t *testing.T) {
	t.Parallel()
	ta := newTestApp(t)
	ta.signUp(t, "Ada", "correct-horse")

	// Different capitals, same name: the store folds them together.
	res, body := ta.do(t, http.MethodPost, "/register", url.Values{
		"name": {"ada"}, "password": {"another-password"},
	}, nil)

	if res.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("status = %d, want 422", res.StatusCode)
	}
	if !strings.Contains(body, "already goes by that name") {
		t.Errorf("body does not say the name is taken:\n%s", body)
	}
}

func TestRegisterInviteCode(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		invite     string
		wantStatus int
	}{
		{"the right code gets in", "open-sesame", http.StatusSeeOther},
		{"a wrong code does not", "guess", http.StatusUnprocessableEntity},
		{"no code does not", "", http.StatusUnprocessableEntity},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ta := newTestApp(t, withInviteCode("open-sesame"))

			res, _ := ta.do(t, http.MethodPost, "/register", url.Values{
				"name": {"Ada"}, "password": {"correct-horse"}, "invite": {tt.invite},
			}, nil)

			if res.StatusCode != tt.wantStatus {
				t.Errorf("status = %d, want %d", res.StatusCode, tt.wantStatus)
			}
		})
	}
}

func TestRegisterFormShowsTheInviteFieldOnlyWhenItIsNeeded(t *testing.T) {
	t.Parallel()

	t.Run("gated", func(t *testing.T) {
		t.Parallel()
		ta := newTestApp(t, withInviteCode("open-sesame"))
		_, body := ta.do(t, http.MethodGet, "/register", nil, nil)
		if !strings.Contains(body, `id="invite"`) {
			t.Error("registration is gated but the form asks for no code")
		}
	})

	t.Run("open", func(t *testing.T) {
		t.Parallel()
		ta := newTestApp(t)
		_, body := ta.do(t, http.MethodGet, "/register", nil, nil)
		if strings.Contains(body, `id="invite"`) {
			t.Error("registration is open but the form asks for a code")
		}
	})
}

func TestLogin(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		form       url.Values
		wantStatus int
	}{
		{"the right password gets in", url.Values{"name": {"Ada"}, "password": {"correct-horse"}}, http.StatusSeeOther},
		{"a wrong password does not", url.Values{"name": {"Ada"}, "password": {"wrong-horse"}}, http.StatusUnprocessableEntity},
		{"an unknown name does not", url.Values{"name": {"Nobody"}, "password": {"correct-horse"}}, http.StatusUnprocessableEntity},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ta := newTestApp(t)
			ta.signUp(t, "Ada", "correct-horse")
			ta.do(t, http.MethodPost, "/logout", url.Values{}, nil)

			res, _ := ta.do(t, http.MethodPost, "/login", tt.form, nil)

			if res.StatusCode != tt.wantStatus {
				t.Errorf("status = %d, want %d", res.StatusCode, tt.wantStatus)
			}
		})
	}
}

// TestLoginTellsNobodyWhichNamesExist is the no-enumeration rule. A wrong
// password and an unknown name must be indistinguishable, or an attacker
// collects the list of real accounts one guess at a time.
//
// The two bodies are not compared whole: each echoes back the name that was
// typed, which is the attacker's own input and tells them nothing. What must
// match is the message.
func TestLoginTellsNobodyWhichNamesExist(t *testing.T) {
	t.Parallel()
	ta := newTestApp(t)
	ta.signUp(t, "Ada", "correct-horse")
	ta.do(t, http.MethodPost, "/logout", url.Values{}, nil)

	const message = "We do not know that name and password."

	_, wrongPassword := ta.do(t, http.MethodPost, "/login", url.Values{
		"name": {"Ada"}, "password": {"wrong-horse"},
	}, nil)
	_, unknownName := ta.do(t, http.MethodPost, "/login", url.Values{
		"name": {"Nobody"}, "password": {"correct-horse"},
	}, nil)

	for what, body := range map[string]string{
		"a wrong password": wrongPassword,
		"an unknown name":  unknownName,
	} {
		if !strings.Contains(body, message) {
			t.Errorf("%s does not answer with the shared message:\n%s", what, body)
		}
	}
	// The pair also has to differ by nothing else: strip each name back out and
	// what is left must be identical.
	if strings.ReplaceAll(wrongPassword, "Ada", "") != strings.ReplaceAll(unknownName, "Nobody", "") {
		t.Error("the two failures differ by more than the name that was typed")
	}
}

// TestLoginSpendsTheSameTimeOnAnUnknownName guards the other half of the
// no-enumeration rule. An unknown name must still cost a real argon2id run
// against the dummy hash — skipping it answers in a fraction of the time, and
// that gap is as good as a "no such user" message.
func TestLoginSpendsTheSameTimeOnAnUnknownName(t *testing.T) {
	t.Parallel()
	ta := newTestApp(t)
	ta.signUp(t, "Ada", "correct-horse")
	ta.do(t, http.MethodPost, "/logout", url.Values{}, nil)

	timeOf := func(name string) time.Duration {
		start := time.Now()
		ta.do(t, http.MethodPost, "/login", url.Values{
			"name": {name}, "password": {"wrong-horse"},
		}, nil)
		return time.Since(start)
	}
	known := timeOf("Ada")
	unknown := timeOf("Nobody")

	// A generous bound on purpose: hashing costs tens of milliseconds and
	// skipping it costs microseconds, so the failure this catches is a factor
	// of a hundred, not of two. Anything tighter would flake on a busy machine.
	if unknown < known/4 {
		t.Errorf("an unknown name answered in %v against %v for a known one — "+
			"the dummy hash is not being verified", unknown, known)
	}
}

func TestLoginRenewsTheSessionToken(t *testing.T) {
	t.Parallel()
	ta := newTestApp(t)
	ta.signUp(t, "Ada", "correct-horse")

	before := ta.sessionCookie(t)
	ta.do(t, http.MethodPost, "/logout", url.Values{}, nil)
	ta.do(t, http.MethodPost, "/login", url.Values{
		"name": {"Ada"}, "password": {"correct-horse"},
	}, nil)
	after := ta.sessionCookie(t)

	// Without RenewToken, a token planted in the browser before the login is
	// still valid after it.
	if before == "" || after == "" {
		t.Fatalf("no session cookie: before=%q after=%q", before, after)
	}
	if before == after {
		t.Error("the session token survived the login, so a fixed token still works")
	}
}

func TestLogoutEndsTheSession(t *testing.T) {
	t.Parallel()
	ta := newTestApp(t)
	ta.signUp(t, "Ada", "correct-horse")

	ta.do(t, http.MethodPost, "/logout", url.Values{}, nil)
	res, _ := ta.do(t, http.MethodGet, "/rooms", nil, nil)

	if res.StatusCode != http.StatusSeeOther || res.Header.Get("Location") != "/login" {
		t.Errorf("after logout: %d -> %q, want 303 -> /login",
			res.StatusCode, res.Header.Get("Location"))
	}
}

func TestLoginRateLimit(t *testing.T) {
	t.Parallel()
	ta := newTestApp(t)

	// The bucket holds five, then refills one every three seconds.
	var last int
	for range 7 {
		res, _ := ta.do(t, http.MethodPost, "/login", url.Values{
			"name": {"Ada"}, "password": {"wrong-horse"},
		}, nil)
		last = res.StatusCode
	}

	if last != http.StatusTooManyRequests {
		t.Errorf("status after seven attempts = %d, want 429", last)
	}
}

// sessionCookie returns the current session token, or "" when there is none.
func (ta *testApp) sessionCookie(t *testing.T) string {
	t.Helper()
	u, err := url.Parse(ta.server.URL)
	if err != nil {
		t.Fatalf("parsing the server URL: %v", err)
	}
	for _, c := range ta.client.Jar.Cookies(u) {
		if c.Name == "session" {
			return c.Value
		}
	}
	return ""
}
