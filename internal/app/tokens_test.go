package app

import (
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
)

var secretPattern = regexp.MustCompile(`gochat_[A-Za-z0-9_-]+`)

// makeToken creates a machine token and returns the secret, which the profile
// page shows exactly once.
func (ta *testApp) makeToken(t *testing.T, label string) string {
	t.Helper()
	res, _ := ta.do(t, http.MethodPost, "/profile/tokens", url.Values{"label": {label}}, nil)
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("creating token %s: status %d, want 303", label, res.StatusCode)
	}
	_, body := ta.do(t, http.MethodGet, "/profile", nil, nil)
	secret := secretPattern.FindString(body)
	if secret == "" {
		t.Fatalf("the profile page showed no secret after creating a token:\n%s", body)
	}
	return secret
}

func TestTokenIsShownOnceAndOnlyOnce(t *testing.T) {
	t.Parallel()
	ta := newTestApp(t)
	ta.signUp(t, "Ada", "correct-horse")

	secret := ta.makeToken(t, "laptop")

	// The server kept only the hash, so there is nothing left to show.
	_, again := ta.do(t, http.MethodGet, "/profile", nil, nil)
	if strings.Contains(again, secret) {
		t.Error("the secret came back on a second visit")
	}
	if !strings.Contains(again, "laptop") {
		t.Error("the token is not listed by its label")
	}
}

func TestTokenSignsAProgramIn(t *testing.T) {
	t.Parallel()
	ta := newTestApp(t)
	ta.signUp(t, "Ada", "correct-horse")
	secret := ta.makeToken(t, "laptop")
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
	secret := ta.makeToken(t, "laptop")

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
	secret := ta.makeToken(t, "ada's laptop")
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
// to reach past the HTTP surface.
func (ta *testApp) onlyUserID(t *testing.T) string {
	t.Helper()
	ta.users.mu.Lock()
	defer ta.users.mu.Unlock()
	for id := range ta.users.byID {
		return id
	}
	t.Fatal("no user is registered")
	return ""
}
