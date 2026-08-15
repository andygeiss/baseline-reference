package app

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// apiCall makes a request to the JSON surface with a bearer token and no
// cookies, exactly as a program would, and decodes the answer.
func (ta *testApp) apiCall(t *testing.T, method, path, token, body string) (*http.Response, map[string]any) {
	t.Helper()

	var reader *strings.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	var req *http.Request
	var err error
	if reader != nil {
		req, err = http.NewRequestWithContext(t.Context(), method, ta.server.URL+path, reader)
	} else {
		req, err = http.NewRequestWithContext(t.Context(), method, ta.server.URL+path, nil)
	}
	if err != nil {
		t.Fatalf("building the request: %v", err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}

	// A client of its own: no cookie jar, so the token is the only credential.
	res, err := (&http.Client{}).Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer res.Body.Close()

	var out map[string]any
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatalf("%s %s answered something that is not JSON: %v", method, path, err)
	}
	return res, out
}

// signedUpWithToken registers somebody and returns a machine token for them.
func signedUpWithToken(t *testing.T) (*testApp, string) {
	t.Helper()
	ta := newTestApp(t)
	ta.signUp(t, "Ada", "correct-horse")
	return ta, ta.makeToken(t, "cli")
}

func TestAPIAnswersJSONNotHTML(t *testing.T) {
	t.Parallel()
	ta, token := signedUpWithToken(t)

	res, body := ta.apiCall(t, http.MethodGet, "/api/me", token, "")

	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	if got := res.Header.Get("Content-Type"); got != "application/json; charset=utf-8" {
		t.Errorf("Content-Type = %q, want JSON", got)
	}
	if body["name"] != "Ada" {
		t.Errorf("name = %v, want Ada", body["name"])
	}
}

// TestAPINeedsACredentialAndSaysSoInJSON is why /api is its own surface. The
// pages answer a signed-out reader with 303 to a sign-in page; a program
// following that would get 200 and a screenful of HTML, which reads as success.
func TestAPINeedsACredentialAndSaysSoInJSON(t *testing.T) {
	t.Parallel()
	ta := newTestApp(t)

	tests := []struct {
		name  string
		token string
	}{
		{"no token at all", ""},
		{"a token nobody issued", "gochat_nothing"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res, body := ta.apiCall(t, http.MethodGet, "/api/rooms", tt.token, "")

			if res.StatusCode != http.StatusUnauthorized {
				t.Errorf("status = %d, want 401", res.StatusCode)
			}
			if body["error"] == nil || body["error"] == "" {
				t.Errorf("a 401 with no error to read: %v", body)
			}
		})
	}
}

func TestAPIRooms(t *testing.T) {
	t.Parallel()
	ta, token := signedUpWithToken(t)

	t.Run("an empty list is a list, never null", func(t *testing.T) {
		// A null would make every caller check for it before ranging.
		_, body := ta.apiCall(t, http.MethodGet, "/api/rooms", token, "")
		rooms, ok := body["rooms"].([]any)
		if !ok {
			t.Fatalf("rooms = %v, want an array", body["rooms"])
		}
		if len(rooms) != 0 {
			t.Errorf("rooms = %v, want empty", rooms)
		}
	})

	t.Run("creating one answers 201 and its address", func(t *testing.T) {
		res, body := ta.apiCall(t, http.MethodPost, "/api/rooms", token, `{"name":"General Chat"}`)
		if res.StatusCode != http.StatusCreated {
			t.Fatalf("status = %d, want 201", res.StatusCode)
		}
		room, ok := body["room"].(map[string]any)
		if !ok || room["slug"] != "general-chat" {
			t.Errorf("room = %v, want slug general-chat", body["room"])
		}
	})

	t.Run("a bad name is 422 with the field named", func(t *testing.T) {
		res, body := ta.apiCall(t, http.MethodPost, "/api/rooms", token, `{"name":"!!!"}`)
		if res.StatusCode != http.StatusUnprocessableEntity {
			t.Fatalf("status = %d, want 422", res.StatusCode)
		}
		fields, ok := body["fields"].(map[string]any)
		if !ok || fields["name"] == nil {
			t.Errorf("fields = %v, want the name field named", body["fields"])
		}
	})
}

func TestAPIMessages(t *testing.T) {
	t.Parallel()
	ta, token := signedUpWithToken(t)
	ta.apiCall(t, http.MethodPost, "/api/rooms", token, `{"name":"General"}`)

	t.Run("posting answers the message and the next cursor", func(t *testing.T) {
		res, body := ta.apiCall(t, http.MethodPost, "/api/rooms/general/messages", token, `{"body":"hello"}`)
		if res.StatusCode != http.StatusCreated {
			t.Fatalf("status = %d, want 201", res.StatusCode)
		}
		msg, ok := body["message"].(map[string]any)
		if !ok {
			t.Fatalf("message = %v, want an object", body["message"])
		}
		if msg["body"] != "hello" || msg["author"] != "Ada" {
			t.Errorf("message = %v, want Ada saying hello", msg)
		}
		if msg["created_at"] == nil || msg["seq"] == nil {
			t.Errorf("message = %v, want a seq and a time", msg)
		}
		if body["since"] != msg["seq"] {
			t.Errorf("since = %v, want the new message's seq %v", body["since"], msg["seq"])
		}
	})

	t.Run("reading without a cursor starts at the beginning", func(t *testing.T) {
		_, body := ta.apiCall(t, http.MethodGet, "/api/rooms/general/messages", token, "")
		msgs, ok := body["messages"].([]any)
		if !ok || len(msgs) != 1 {
			t.Fatalf("messages = %v, want one", body["messages"])
		}
	})

	t.Run("reading with the current cursor brings back nothing", func(t *testing.T) {
		// No 204 here: on this surface the answer is a document, and an empty
		// list plus the unchanged cursor is that document.
		res, body := ta.apiCall(t, http.MethodGet, "/api/rooms/general/messages?since=1", token, "")
		if res.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200", res.StatusCode)
		}
		msgs, _ := body["messages"].([]any)
		if len(msgs) != 0 {
			t.Errorf("messages = %v, want none", msgs)
		}
		if body["since"] != float64(1) {
			t.Errorf("since = %v, want the cursor to stand still at 1", body["since"])
		}
	})

	t.Run("an unknown room is 404", func(t *testing.T) {
		res, _ := ta.apiCall(t, http.MethodGet, "/api/rooms/nowhere/messages", token, "")
		if res.StatusCode != http.StatusNotFound {
			t.Errorf("status = %d, want 404", res.StatusCode)
		}
	})

	t.Run("a broken cursor is 400", func(t *testing.T) {
		res, _ := ta.apiCall(t, http.MethodGet, "/api/rooms/general/messages?since=soon", token, "")
		if res.StatusCode != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", res.StatusCode)
		}
	})

	t.Run("an empty message is 422", func(t *testing.T) {
		res, body := ta.apiCall(t, http.MethodPost, "/api/rooms/general/messages", token, `{"body":"   "}`)
		if res.StatusCode != http.StatusUnprocessableEntity {
			t.Errorf("status = %d, want 422", res.StatusCode)
		}
		fields, ok := body["fields"].(map[string]any)
		if !ok || fields["body"] == nil {
			t.Errorf("fields = %v, want the body field named", body["fields"])
		}
	})
}

func TestAPIRefusesABodyItDoesNotUnderstand(t *testing.T) {
	t.Parallel()
	ta, token := signedUpWithToken(t)
	ta.apiCall(t, http.MethodPost, "/api/rooms", token, `{"name":"General"}`)

	tests := []struct {
		name string
		body string
	}{
		{"not JSON", `hello`},
		{"an array where an object goes", `["hello"]`},
		// A field the server does not know is a typo or a version mismatch.
		// Dropping it quietly would post a message the caller thinks it sent.
		{"an unknown field", `{"bodyy":"hello"}`},
		{"two objects", `{"body":"one"}{"body":"two"}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res, body := ta.apiCall(t, http.MethodPost, "/api/rooms/general/messages", token, tt.body)
			if res.StatusCode != http.StatusBadRequest {
				t.Errorf("status = %d, want 400", res.StatusCode)
			}
			if body["error"] == nil {
				t.Errorf("a 400 with no error to read: %v", body)
			}
		})
	}
}

// TestAPIMessagesAreNotEscaped is the seam between the two surfaces. The pages
// run everything through html/template, and they must; JSON is not HTML, so the
// text comes back exactly as it was written and the caller decides what to do
// with it.
func TestAPIMessagesAreNotEscaped(t *testing.T) {
	t.Parallel()
	ta, token := signedUpWithToken(t)
	ta.apiCall(t, http.MethodPost, "/api/rooms", token, `{"name":"General"}`)

	const said = `a < b && c > d`
	ta.apiCall(t, http.MethodPost, "/api/rooms/general/messages", token,
		`{"body":`+mustJSON(t, said)+`}`)
	_, body := ta.apiCall(t, http.MethodGet, "/api/rooms/general/messages", token, "")

	msgs, _ := body["messages"].([]any)
	if len(msgs) != 1 {
		t.Fatalf("messages = %v, want one", body["messages"])
	}
	got := msgs[0].(map[string]any)["body"]
	if got != said {
		t.Errorf("body = %q, want it back exactly as written: %q", got, said)
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("encoding %v: %v", v, err)
	}
	return string(b)
}
