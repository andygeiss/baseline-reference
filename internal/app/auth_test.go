package app

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"golang.org/x/crypto/argon2"

	"github.com/andygeiss/baseline-reference/v5/internal/auth"
	"github.com/andygeiss/baseline-reference/v5/internal/domain"
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
	// Made outside the bubble (newTestAppWith says why), and with a context
	// that never cancels: a Done channel first touched inside a bubble belongs
	// to it, and t.Context()'s is cancelled from outside.
	o := newTestOptions(t, withStopping(context.Background()))

	// The bucket refills on the clock, so the clock is synctest's: inside the
	// bubble it stands still while argon2 runs, however slow -race makes it,
	// and moves only when the test sleeps.
	synctest.Test(t, func(t *testing.T) {
		ta := newTestAppWith(t, o)
		attempt := func() *http.Response {
			res, _ := ta.do(t, http.MethodPost, "/login", url.Values{
				"name": {"Ada"}, "password": {"wrong-horse"},
			}, nil)
			return res
		}

		// The bucket holds five...
		for i := range 5 {
			if res := attempt(); res.StatusCode == http.StatusTooManyRequests {
				t.Fatalf("attempt %d = 429, want it let through", i+1)
			}
		}
		// ...and refuses the sixth, saying when to come back.
		res := attempt()
		if res.StatusCode != http.StatusTooManyRequests {
			t.Fatalf("sixth attempt = %d, want 429", res.StatusCode)
		}
		if got := res.Header.Get("Retry-After"); got != "3" {
			t.Errorf("Retry-After = %q, want 3", got)
		}

		// Then it refills one every three seconds: one more, and no more.
		synctest.Sleep(3 * time.Second)
		if res := attempt(); res.StatusCode == http.StatusTooManyRequests {
			t.Errorf("attempt after three seconds = 429, want it let through")
		}
		if res := attempt(); res.StatusCode != http.StatusTooManyRequests {
			t.Errorf("second attempt after three seconds = %d, want 429", res.StatusCode)
		}
	})
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

// brokenStore is a session store whose database has gone away.
type brokenStore struct{}

func (brokenStore) Find(string) ([]byte, bool, error) {
	return nil, false, errors.New("the session table is unreachable")
}
func (brokenStore) Commit(string, []byte, time.Time) error { return errors.New("unreachable") }
func (brokenStore) Delete(string) error                    { return errors.New("unreachable") }

// TestASessionStoreErrorIsTheAppsOwn500: scs's default ErrorFunc writes a store
// error to the standard logger, unstructured, and answers with its own page.
// The app's serverError is the one place a 500 is logged and worded
// (patterns/go-auth-sessions.md).
func TestASessionStoreErrorIsTheAppsOwn500(t *testing.T) {
	t.Parallel()
	var log bytes.Buffer
	ta := newTestApp(t, func(o *Options) {
		o.Logger = slog.New(slog.NewTextHandler(&log, nil))
		o.Sessions.Store = brokenStore{}
	})

	res, body := ta.do(t, http.MethodGet, "/login", nil, http.Header{"Cookie": {"session=anything"}})
	if res.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status %d, want 500", res.StatusCode)
	}
	if !strings.Contains(body, "Sorry, something went wrong.") {
		t.Errorf("the 500 is not the app's own: %q", body)
	}
	if !strings.Contains(log.String(), "the session table is unreachable") {
		t.Errorf("the store's error never reached the app's logger:\n%s", log.String())
	}
}

// TestTheLimiterKeysIPv6OnItsSlash64: one subscriber holds a whole /64 and can
// take a fresh address out of it for every guess. Keyed on the full address
// the limiter would never see that client twice.
func TestTheLimiterKeysIPv6OnItsSlash64(t *testing.T) {
	t.Parallel()
	keys := map[string]string{
		"192.0.2.10":            "192.0.2.10",
		"::ffff:192.0.2.10":     "192.0.2.10",   // an IPv4 client, however it is written: Unmap first
		"::ffff:c000:20a":       "192.0.2.10",   // the same client, spelled in hex
		"::ffff:198.51.100.7":   "198.51.100.7", // and never one bucket for every mapped address
		"2001:db8:1:2::1":       "2001:db8:1:2::/64",
		"2001:db8:1:2:a:b:c:d":  "2001:db8:1:2::/64",
		"2001:db8:1:3::1":       "2001:db8:1:3::/64", // the next subscriber
		"fe80::1%en0":           "fe80::/64",
		"not an address at all": "not an address at all",
		"":                      "",
	}
	for ip, want := range keys {
		if got := limiterKey(ip); got != want {
			t.Errorf("limiterKey(%q) = %q, want %q", ip, got, want)
		}
	}

	l := newLimiter()
	limited := 0
	for i := range 50 {
		if !l.allow(limiterKey(fmt.Sprintf("2001:db8:1:2::%x", i+1))) {
			limited++
		}
	}
	if limited != 45 {
		t.Errorf("50 guesses from one /64: %d limited, want 45 (the bucket holds five)", limited)
	}
}

// weakHash is password hashed the way an older release of this app would have:
// argon2id under settings below today's, which is what makes a sign-in re-hash.
func weakHash(password string) string {
	salt := []byte("sixteen byte salt")[:16]
	key := argon2.IDKey([]byte(password), salt, 1, 8*1024, 1, 32)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=1,p=1$%s$%s", argon2.Version, 8*1024,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key))
}

// TestARehashAtSignInSetsNoPassword: every statement that sets a new password
// moves the counter, and the re-hash a sign-in makes sets none — the password is
// the one it was (patterns/go-auth-sessions.md rule 1). Moved by a re-hash, the
// counter signed every other session of that user out at their first sign-in
// after the argon2id settings were raised.
func TestARehashAtSignInSetsNoPassword(t *testing.T) {
	t.Parallel()
	ta := newTestApp(t)
	const password = "correct horse battery"
	ta.users.byID["u1"] = domain.User{ID: "u1", Name: "Ada", PasswordHash: weakHash(password)}
	// A reset link Ada asked for and has not used. A sign-in is not what
	// spends it, with or without a re-hash.
	ta.resets.links["adas-link"] = domain.Reset{Hash: "adas-link", UserID: "u1", ExpiresAt: time.Now().Add(time.Hour)}
	signIn := func() {
		t.Helper()
		if res, _ := ta.do(t, http.MethodPost, "/login", url.Values{
			"name": {"Ada"}, "password": {password},
		}, nil); res.StatusCode != http.StatusSeeOther {
			t.Fatalf("signing in: status %d, want 303", res.StatusCode)
		}
	}

	signIn()
	first, err := ta.users.ByID(t.Context(), "u1")
	if err != nil {
		t.Fatal(err)
	}
	if weak, err := auth.NeedsRehash(first.PasswordHash); err != nil || weak {
		t.Fatalf("the hash was not rewritten at sign-in (weak %v, %v)", weak, err)
	}
	if first.PasswordVersion != 0 {
		t.Errorf("counter = %d after the re-hash, want it where it was, at 0", first.PasswordVersion)
	}
	if res, _ := ta.do(t, http.MethodGet, "/rooms", nil, nil); res.StatusCode != http.StatusOK {
		t.Fatalf("the session that re-hashed is signed out: status %d", res.StatusCode)
	}
	firstBrowser := ta.client.Jar

	// The settings are raised again, and Ada signs in on another machine.
	first.PasswordHash = weakHash(password)
	ta.users.mu.Lock()
	ta.users.byID["u1"] = first
	ta.users.mu.Unlock()
	ta.client.Jar = emptyJar(t)
	signIn()
	if res, _ := ta.do(t, http.MethodGet, "/rooms", nil, nil); res.StatusCode != http.StatusOK {
		t.Errorf("the second sign-in is signed out: status %d", res.StatusCode)
	}

	// The first machine is still signed in: nobody set a password.
	ta.client.Jar = firstBrowser
	if res, _ := ta.do(t, http.MethodGet, "/rooms", nil, nil); res.StatusCode != http.StatusOK {
		t.Errorf("the session opened before the second re-hash answered %d, want 200", res.StatusCode)
	}
	ta.resets.mu.Lock()
	_, kept := ta.resets.links["adas-link"]
	ta.resets.mu.Unlock()
	if !kept {
		t.Error("a sign-in that re-hashed deleted the reset link")
	}
}

// TestASignInDoesNotWriteOverAReset: the attack the counter in the WHERE closes.
// A sign-in verifies the password against the row it loaded, a reset lands, and
// the sign-in then re-hashes what it verified. Written by id, that puts the old
// password — the one the reset was made to lock out — back over the new one.
func TestASignInDoesNotWriteOverAReset(t *testing.T) {
	t.Parallel()
	ta := newTestApp(t)
	const password = "the password a thief knows"
	ta.users.byID["u1"] = domain.User{ID: "u1", Name: "Ada", PasswordHash: weakHash(password)}
	// The reset lands after the sign-in has read the row, and once.
	var once sync.Once
	ta.users.afterByName = func(u domain.User) {
		once.Do(func() {
			ta.users.reset(u.ID, "the-resets-hash")
		})
	}

	res, body := ta.do(t, http.MethodPost, "/login", url.Values{"name": {"Ada"}, "password": {password}}, nil)
	if res.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("the sign-in that lost the race answered %d, want 422", res.StatusCode)
	}
	if !strings.Contains(body, "We do not know that name and password.") {
		t.Error("the refusal is not worded like any other failed sign-in")
	}
	got, err := ta.users.ByID(t.Context(), "u1")
	if err != nil {
		t.Fatal(err)
	}
	if got.PasswordHash != "the-resets-hash" || got.PasswordVersion != 1 {
		t.Errorf("row = %q at %d, want the reset's hash at 1", got.PasswordHash, got.PasswordVersion)
	}
	if res, _ := ta.do(t, http.MethodGet, "/rooms", nil, nil); res.StatusCode != http.StatusSeeOther {
		t.Errorf("the refused sign-in is signed in: status %d", res.StatusCode)
	}
}
