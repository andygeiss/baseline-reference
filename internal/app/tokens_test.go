package app

import (
	"bytes"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/andygeiss/baseline-reference/v5/internal/domain"
)

// findSecret returns the first machine-token secret in body, or "".
func findSecret(body string) string {
	_, rest, ok := strings.Cut(body, "gochat_")
	if !ok {
		return ""
	}
	n := strings.IndexFunc(rest, func(r rune) bool {
		return !('a' <= r && r <= 'z' || 'A' <= r && r <= 'Z' || '0' <= r && r <= '9' || r == '_' || r == '-')
	})
	if n < 0 {
		n = len(rest)
	}
	if n == 0 {
		return ""
	}
	return "gochat_" + rest[:n]
}

// makeToken creates a machine token and returns the secret, which the answer
// to that request shows, and nothing after it.
func (ta *testApp) makeToken(t *testing.T, label, password string) string {
	t.Helper()
	res, body := ta.do(t, http.MethodPost, "/profile/tokens",
		url.Values{"label": {label}, "password": {password}}, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("creating token %s: status %d, want 200", label, res.StatusCode)
	}
	secret := findSecret(body)
	if secret == "" {
		t.Fatalf("the answer to creating a token showed no secret:\n%s", body)
	}
	return secret
}

// TestTheSecretIsInTheAnswerThatMadeItAndNowhereAfter: creating a token shows
// its secret once, in the answer to that request. A redirect would have to
// carry it to the next page in the session, and the session is a row in the
// database: the plaintext of a credential, stored until somebody loads that
// page (patterns/go-auth-sessions.md, Machine tokens rule 3).
func TestTheSecretIsInTheAnswerThatMadeItAndNowhereAfter(t *testing.T) {
	t.Parallel()

	// The secret a page shows, where it shows it: inside the secret box.
	shown := func(body string) string {
		_, box, ok := strings.Cut(body, `<div class="secret">`)
		if !ok {
			return ""
		}
		return findSecret(box)
	}

	t.Run("a plain form gets the whole page back, with the secret on it", func(t *testing.T) {
		t.Parallel()
		ta := newTestApp(t)
		ta.signUp(t, "Ada", "correct-horse")
		ta.do(t, http.MethodGet, "/rooms", nil, nil) // spends the welcome flash

		res, body := ta.do(t, http.MethodPost, "/profile/tokens",
			url.Values{"label": {"laptop"}, "password": {"correct-horse"}}, nil)

		if res.StatusCode != http.StatusOK || !strings.Contains(body, "<!doctype html>") {
			t.Fatalf("status %d, and a whole page: %v; want 200 and the page", res.StatusCode, strings.Contains(body, "<!doctype html>"))
		}
		secret := shown(body)
		if secret == "" {
			t.Fatalf("the answer shows no secret:\n%s", body)
		}
		if !strings.Contains(body, `<th scope="row">laptop</th>`) {
			t.Error("the answer does not list the new token")
		}
		// The page is behind the sign-in, so the browser never stores it: Back
		// would otherwise show the secret again with no request made.
		if cc := res.Header.Get("Cache-Control"); cc != "no-store" {
			t.Errorf("Cache-Control = %q, want no-store on the answer that carries the secret", cc)
		}

		// The session's row in the store does not hold it: that row is where
		// a redirect would have had to leave it.
		row, found, err := ta.sessions.Store.Find(sessionToken(t, ta))
		if err != nil || !found {
			t.Fatalf("finding the session's row: found %v, %v", found, err)
		}
		if bytes.Contains(row, []byte(secret)) {
			t.Error("the session's row in the store holds the secret")
		}
		// So nothing carried it anywhere: the very next page has no secret,
		// and no flash about one either.
		_, next := ta.do(t, http.MethodGet, "/profile", nil, nil)
		if strings.Contains(next, secret) || shown(next) != "" {
			t.Error("the page after the answer shows the secret: something stored it")
		}
		if strings.Contains(next, `class="flash"`) {
			t.Errorf("a flash followed an answer that was no redirect:\n%s", next)
		}
		if !strings.Contains(next, `<th scope="row">laptop</th>`) {
			t.Error("the token is not listed by its label")
		}
	})

	t.Run("htmx gets the tokens region back, with the secret in it", func(t *testing.T) {
		t.Parallel()
		ta := newTestApp(t)
		ta.signUp(t, "Ada", "correct-horse")

		res, body := ta.do(t, http.MethodPost, "/profile/tokens",
			url.Values{"label": {"laptop"}, "password": {"correct-horse"}}, htmx())

		if res.StatusCode != http.StatusOK {
			t.Fatalf("status %d, want 200", res.StatusCode)
		}
		if strings.Contains(body, "<html") || !strings.HasPrefix(strings.TrimSpace(body), `<div id="token-region"`) {
			t.Fatalf("the answer is not the region the form swaps:\n%s", body)
		}
		if res.Header.Get("HX-Redirect") != "" || res.Header.Get("Location") != "" {
			t.Error("the answer sends the browser on, to a page that could only show the secret from storage")
		}
		if shown(body) == "" {
			t.Errorf("the region shows no secret:\n%s", body)
		}
		if !strings.Contains(body, `<th scope="row">laptop</th>`) {
			t.Error("the region does not list the new token")
		}
		// The form comes back empty, ready for the next one — and above all
		// without the password.
		if strings.Contains(body, "correct-horse") {
			t.Error("the answer carries the password that was typed")
		}
		if cc := res.Header.Get("Cache-Control"); cc != "no-store" {
			t.Errorf("Cache-Control = %q, want no-store", cc)
		}
		if _, next := ta.do(t, http.MethodGet, "/profile", nil, nil); shown(next) != "" {
			t.Error("the page after the answer shows the secret: something stored it")
		}
	})

	// The form is what makes the 200 safe to send: with hx-post it is a
	// fragment form, never a boosted one, and a boosted POST answered 200
	// pushes the POST's address into the history.
	t.Run("the form is a fragment form, and plain underneath", func(t *testing.T) {
		t.Parallel()
		ta := newTestApp(t)
		ta.signUp(t, "Ada", "correct-horse")

		_, page := ta.do(t, http.MethodGet, "/profile", nil, nil)

		_, form, ok := strings.Cut(page, `<form class="card-form" method="post" action="/profile/tokens"`)
		if !ok {
			t.Fatalf("the profile page has no plain token form:\n%s", page)
		}
		form, _, _ = strings.Cut(form, ">")
		for _, want := range []string{`hx-post="/profile/tokens"`, `hx-target="#token-region"`, `hx-swap="outerHTML"`} {
			if !strings.Contains(form, want) {
				t.Errorf("the token form lacks %s: %s", want, form)
			}
		}
		if !strings.Contains(page, `<div id="token-region"`) {
			t.Error("the page has no region for the form to swap")
		}
		if shown(page) != "" {
			t.Error("a page nobody made a token on shows a secret")
		}
	})
}

func TestTokenSignsAProgramIn(t *testing.T) {
	t.Parallel()
	ta := newTestApp(t)
	ta.signUp(t, "Ada", "correct-horse")
	secret := ta.makeToken(t, "laptop", "correct-horse")
	slug := ta.makeRoom(t, "General")

	// A fresh client with no cookies: the token is the only credential.
	bearer := http.Header{"Authorization": {"Bearer " + secret}}
	ta.client.Jar = emptyJar(t)

	res, body := ta.do(t, http.MethodGet, "/rooms/"+slug, nil, bearer)

	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	if !strings.Contains(body, "General") {
		t.Errorf("the room did not render for a token holder:\n%s", body)
	}
}

func TestBadTokenIsRefusedWithoutARedirect(t *testing.T) {
	t.Parallel()
	ta := newTestApp(t)

	// A program gets an answer it can act on. A 303 to a login page would be
	// 200 of HTML it cannot read.
	res, _ := ta.do(t, http.MethodGet, "/rooms", nil,
		http.Header{"Authorization": {"Bearer gochat_nothing"}})

	if res.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", res.StatusCode)
	}
}

func TestTokenLabelIsChecked(t *testing.T) {
	t.Parallel()
	ta := newTestApp(t)
	ta.signUp(t, "Ada", "correct-horse")

	res, body := ta.do(t, http.MethodPost, "/profile/tokens", url.Values{"label": {"  "}}, nil)

	if res.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("status = %d, want 422", res.StatusCode)
	}
	if !strings.Contains(body, "Give it a name") {
		t.Errorf("body does not explain the problem:\n%s", body)
	}
}

func TestRevokingATokenStopsIt(t *testing.T) {
	t.Parallel()
	ta := newTestApp(t)
	ta.signUp(t, "Ada", "correct-horse")
	secret := ta.makeToken(t, "laptop", "correct-horse")

	tokens, err := ta.tokens.ByUser(t.Context(), ta.onlyUserID(t))
	if err != nil || len(tokens) != 1 {
		t.Fatalf("listing tokens: %v (%d tokens)", err, len(tokens))
	}
	res, _ := ta.do(t, http.MethodPost, "/profile/tokens/"+tokens[0].ID+"/delete", url.Values{}, nil)
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("revoking: status = %d, want 303", res.StatusCode)
	}

	// Revocation is a DELETE, so the credential stops working at once.
	ta.client.Jar = emptyJar(t)
	res, _ = ta.do(t, http.MethodGet, "/rooms", nil,
		http.Header{"Authorization": {"Bearer " + secret}})
	if res.StatusCode != http.StatusUnauthorized {
		t.Errorf("a revoked token answered %d, want 401", res.StatusCode)
	}
}

// TestRevokingSomebodyElsesTokenDoesNothing checks the user ID in the WHERE
// clause: guessing another person's token ID must revoke nothing.
func TestRevokingSomebodyElsesTokenDoesNothing(t *testing.T) {
	t.Parallel()
	ta := newTestApp(t)
	ta.signUp(t, "Ada", "correct-horse")
	secret := ta.makeToken(t, "ada's laptop", "correct-horse")
	tokens, err := ta.tokens.ByUser(t.Context(), ta.onlyUserID(t))
	if err != nil || len(tokens) != 1 {
		t.Fatalf("listing tokens: %v (%d tokens)", err, len(tokens))
	}
	adasToken := tokens[0].ID

	ta.do(t, http.MethodPost, "/logout", url.Values{}, nil)
	ta.signUp(t, "Bob", "correct-horse")
	res, _ := ta.do(t, http.MethodPost, "/profile/tokens/"+adasToken+"/delete", url.Values{}, nil)
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", res.StatusCode)
	}

	// Ada's token still works, and Bob was told it was already gone.
	ta.client.Jar = emptyJar(t)
	res, _ = ta.do(t, http.MethodGet, "/rooms", nil,
		http.Header{"Authorization": {"Bearer " + secret}})
	if res.StatusCode != http.StatusOK {
		t.Errorf("Ada's token answered %d after Bob tried to revoke it, want 200", res.StatusCode)
	}
}

// onlyUserID returns the ID of the single registered user, for tests that need
// to reach past the HTTP surface. The assistant is seeded rather than
// registered and owns no tokens, so it is not a candidate.
func (ta *testApp) onlyUserID(t *testing.T) string {
	t.Helper()
	ta.users.mu.Lock()
	defer ta.users.mu.Unlock()
	for id := range ta.users.byID {
		if id == domain.AssistantID {
			continue
		}
		return id
	}
	t.Fatal("no user is registered")
	return ""
}

// TestCreatingATokenAsksForThePassword: a session alone must not mint a
// credential that outlives it. Whoever finds a browser unlocked would otherwise
// leave with a token no logout and no password reset ends
// (patterns/go-auth-sessions.md, Machine tokens rule 3).
func TestCreatingATokenAsksForThePassword(t *testing.T) {
	t.Parallel()
	ta := newTestApp(t)
	ta.signUp(t, "Ada", "correct-horse")

	for name, form := range map[string]url.Values{
		"no password":    {"label": {"laptop"}},
		"wrong password": {"label": {"laptop"}, "password": {"not-the-password"}},
	} {
		res, body := ta.do(t, http.MethodPost, "/profile/tokens", form, nil)
		if res.StatusCode != http.StatusUnprocessableEntity {
			t.Errorf("%s: status %d, want 422", name, res.StatusCode)
		}
		if !strings.Contains(body, "That is not your password.") {
			t.Errorf("%s: the page does not say why", name)
		}
		if findSecret(body) != "" {
			t.Errorf("%s: a secret was shown", name)
		}
	}
	if tokens, _ := ta.tokens.ByUser(t.Context(), ta.onlyUserID(t)); len(tokens) != 0 {
		t.Fatalf("%d token(s) were created without the password", len(tokens))
	}

	res, _ := ta.do(t, http.MethodPost, "/profile/tokens",
		url.Values{"label": {"laptop"}, "password": {"correct-horse"}}, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("with the password: status %d, want 200", res.StatusCode)
	}
	if tokens, _ := ta.tokens.ByUser(t.Context(), ta.onlyUserID(t)); len(tokens) != 1 {
		t.Errorf("%d token(s) after the one good request, want 1", len(tokens))
	}
}

// TestARefusedTokenFormComesBackAsWhatWasAsked: the token form is a fragment
// form, so its 422 is the region for htmx — a whole page there would land
// inside the region — and the page for a plain form
// (patterns/htmx-server-rendering.md).
func TestARefusedTokenFormComesBackAsWhatWasAsked(t *testing.T) {
	t.Parallel()
	ta := newTestApp(t)
	ta.signUp(t, "Ada", "correct-horse")
	form := url.Values{"label": {"laptop"}, "password": {"not-the-password"}}

	res, fragment := ta.do(t, http.MethodPost, "/profile/tokens", form, htmx())
	if res.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("htmx: status %d, want 422", res.StatusCode)
	}
	if strings.Contains(fragment, "<html") || !strings.HasPrefix(strings.TrimSpace(fragment), `<div id="token-region"`) {
		t.Errorf("htmx: the 422 is not the region the form swaps:\n%s", fragment)
	}
	for _, want := range []string{"That is not your password.", `value="laptop"`, `aria-describedby="token-password-error"`} {
		if !strings.Contains(fragment, want) {
			t.Errorf("htmx: the 422 lacks %s", want)
		}
	}

	res, page := ta.do(t, http.MethodPost, "/profile/tokens", form, nil)
	if res.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("plain: status %d, want 422", res.StatusCode)
	}
	if !strings.Contains(page, "<!doctype html>") || !strings.Contains(page, "That is not your password.") {
		t.Errorf("plain: the 422 is not the page with its error:\n%s", page)
	}
	if tokens, _ := ta.tokens.ByUser(t.Context(), ta.onlyUserID(t)); len(tokens) != 0 {
		t.Errorf("%d token(s) were created by refused forms", len(tokens))
	}
}

// TestATokenCannotMintATokenByItself: /profile takes either credential, so the
// password is also what stops one leaked token from making itself a spare.
func TestATokenCannotMintATokenByItself(t *testing.T) {
	t.Parallel()
	ta := newTestApp(t)
	ta.signUp(t, "Ada", "correct-horse")
	secret := ta.makeToken(t, "laptop", "correct-horse")
	ta.client.Jar = emptyJar(t)

	res, _ := ta.do(t, http.MethodPost, "/profile/tokens", url.Values{"label": {"spare"}},
		http.Header{"Authorization": {"Bearer " + secret}})
	if res.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("status %d, want 422", res.StatusCode)
	}
	if tokens, _ := ta.tokens.ByUser(t.Context(), ta.onlyUserID(t)); len(tokens) != 1 {
		t.Errorf("%d token(s), want the one made with the password", len(tokens))
	}
}

// TestATokenCreationHeldOpenAcrossAResetCreatesNothing: the password is checked
// against the row the request loaded when its headers arrived, and a body held
// open keeps that row for as long as the server will read. A reset that lands
// in between is the owner locking that password out; the insert names the
// counter the request loaded, so it changes no row and the answer is a refusal
// (patterns/go-auth-sessions.md rule 1). Inserted by user id alone, the held
// request left with a token no reset ends.
func TestATokenCreationHeldOpenAcrossAResetCreatesNothing(t *testing.T) {
	t.Parallel()
	ta := newTestApp(t)
	ta.signUp(t, "Ada", "correct-horse")
	// The reset lands once the credential middleware has read Ada's row, and
	// before the handler writes: the gap a held-open body keeps open.
	var once sync.Once
	ta.users.afterByID = func(u domain.User) {
		once.Do(func() { ta.users.reset(u.ID, "the-resets-hash") })
	}

	res, body := ta.do(t, http.MethodPost, "/profile/tokens",
		url.Values{"label": {"laptop"}, "password": {"correct-horse"}}, nil)

	if res.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("status %d, want 422", res.StatusCode)
	}
	if !strings.Contains(body, "That is not your password.") {
		t.Error("the refusal is not worded like any other wrong password")
	}
	if findSecret(body) != "" {
		t.Error("a secret was shown")
	}
	if tokens, _ := ta.tokens.ByUser(t.Context(), ta.onlyUserID(t)); len(tokens) != 0 {
		t.Errorf("%d token(s) were created under a password that had been reset", len(tokens))
	}
}

// TestTheTokenPasswordCheckIsRateLimited: a route that checks a password is a
// route to guess one at, so it sits behind the login's limiter.
func TestTheTokenPasswordCheckIsRateLimited(t *testing.T) {
	t.Parallel()
	ta := newTestApp(t)
	ta.signUp(t, "Ada", "correct-horse")

	limited := 0
	for range 8 {
		res, _ := ta.do(t, http.MethodPost, "/profile/tokens",
			url.Values{"label": {"laptop"}, "password": {"a-guess"}}, nil)
		if res.StatusCode == http.StatusTooManyRequests {
			limited++
			if res.Header.Get("Retry-After") == "" {
				t.Error("a 429 with no Retry-After")
			}
		}
	}
	if limited == 0 {
		t.Error("eight wrong passwords in a row and none was limited")
	}
}
