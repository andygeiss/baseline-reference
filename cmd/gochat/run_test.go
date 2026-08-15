package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeServer answers the handful of calls the tool makes. It keeps real state,
// so a test can post a message and then read it back.
func fakeServer(t *testing.T) *httptest.Server {
	t.Helper()
	var posted []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/me":
			w.Write([]byte(`{"name":"Ada"}`))
		case r.URL.Path == "/api/rooms" && r.Method == http.MethodGet:
			w.Write([]byte(`{"rooms":[{"slug":"general","name":"General"},{"slug":"random","name":"Random"}]}`))
		case r.URL.Path == "/api/rooms/general/messages" && r.Method == http.MethodPost:
			var in struct {
				Body string `json:"body"`
			}
			json.NewDecoder(r.Body).Decode(&in)
			posted = append(posted, in.Body)
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(map[string]any{
				"message": map[string]any{
					"seq": len(posted), "author": "Ada", "body": in.Body,
					"created_at": "2026-08-15T10:00:00Z",
				},
				"since": len(posted),
			})
		case r.URL.Path == "/api/rooms/general/messages":
			w.Write([]byte(`{"messages":[{"seq":1,"author":"Ada","body":"hello","created_at":"2026-08-15T10:00:00Z"}],"since":1}`))
		case strings.HasPrefix(r.URL.Path, "/api/rooms/nowhere/"):
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"error":"No room answers to that name."}`))
		default:
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"error":"no such thing"}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// call runs the tool exactly as a shell would, and hands back both streams.
// run() takes its arguments and its writers, so no process is involved.
func call(t *testing.T, srv *httptest.Server, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	var out, errs bytes.Buffer
	full := append([]string{args[0], "-addr", srv.URL, "-token-file", tokenFile(t)}, args[1:]...)
	err = run(t.Context(), full, &out, &errs)
	return out.String(), errs.String(), err
}

// tokenFile writes a token where the tool reads one — a file, never the
// environment, because a variable is inherited by every child process.
func tokenFile(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte("gochat_token\n"), 0o600); err != nil {
		t.Fatalf("writing the token file: %v", err)
	}
	return path
}

func TestRunRooms(t *testing.T) {
	t.Parallel()
	srv := fakeServer(t)

	t.Run("plain output is one room per line", func(t *testing.T) {
		stdout, _, err := call(t, srv, "rooms")
		if err != nil {
			t.Fatalf("run: %v", err)
		}
		want := "general\tGeneral\nrandom\tRandom\n"
		if stdout != want {
			t.Errorf("stdout = %q, want %q", stdout, want)
		}
	})

	t.Run("-json parses back", func(t *testing.T) {
		stdout, _, err := call(t, srv, "rooms", "-json")
		if err != nil {
			t.Fatalf("run: %v", err)
		}
		// One object per line, so a reader can take it a line at a time.
		lines := strings.Split(strings.TrimSpace(stdout), "\n")
		if len(lines) != 2 {
			t.Fatalf("got %d lines, want 2:\n%s", len(lines), stdout)
		}
		for _, line := range lines {
			var room struct {
				Slug string `json:"slug"`
				Name string `json:"name"`
			}
			if err := json.Unmarshal([]byte(line), &room); err != nil {
				t.Fatalf("line %q does not parse: %v", line, err)
			}
			if room.Slug == "" || room.Name == "" {
				t.Errorf("line %q lost a field", line)
			}
		}
	})
}

func TestRunRead(t *testing.T) {
	t.Parallel()
	srv := fakeServer(t)

	stdout, stderr, err := call(t, srv, "read", "general")
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	if !strings.Contains(stdout, "Ada: hello") {
		t.Errorf("stdout = %q, want the message", stdout)
	}
	// The cursor is not data. It goes to stderr so it never lands in the middle
	// of what a pipe is reading.
	if strings.Contains(stdout, "next cursor") {
		t.Error("the cursor was written to stdout, where the data goes")
	}
	if !strings.Contains(stderr, "next cursor: 1") {
		t.Errorf("stderr = %q, want the next cursor", stderr)
	}
}

func TestRunPost(t *testing.T) {
	t.Parallel()
	srv := fakeServer(t)

	t.Run("plain output is the new cursor", func(t *testing.T) {
		stdout, _, err := call(t, srv, "post", "general", "hello there")
		if err != nil {
			t.Fatalf("run: %v", err)
		}
		if strings.TrimSpace(stdout) == "" {
			t.Error("post said nothing, so a script cannot carry on from it")
		}
	})

	t.Run("-json parses back", func(t *testing.T) {
		stdout, _, err := call(t, srv, "post", "-json", "general", "hello again")
		if err != nil {
			t.Fatalf("run: %v", err)
		}
		var msg struct {
			Seq  int64  `json:"seq"`
			Body string `json:"body"`
		}
		if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &msg); err != nil {
			t.Fatalf("stdout %q does not parse: %v", stdout, err)
		}
		if msg.Body != "hello again" || msg.Seq == 0 {
			t.Errorf("message = %+v, want the body back with a sequence", msg)
		}
	})
}

func TestRunWhoami(t *testing.T) {
	t.Parallel()
	srv := fakeServer(t)

	stdout, _, err := call(t, srv, "whoami")

	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if strings.TrimSpace(stdout) != "Ada" {
		t.Errorf("stdout = %q, want Ada", stdout)
	}
}

func TestRunUsageErrors(t *testing.T) {
	t.Parallel()
	srv := fakeServer(t)

	tests := []struct {
		name string
		args []string
	}{
		{"no command at all", []string{}},
		{"a command nobody has", []string{"dance"}},
		{"read without a room", []string{"read"}},
		{"read with two rooms", []string{"read", "general", "random"}},
		{"post without a room", []string{"post"}},
		{"a flag that does not exist", []string{"rooms", "-loud"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var out, errs bytes.Buffer
			args := tt.args
			if len(args) > 0 {
				args = append([]string{args[0], "-addr", srv.URL}, args[1:]...)
			}

			err := run(t.Context(), args, &out, &errs)

			// Exit 2 is the flag package's own convention for this, and scripts
			// branch on it.
			if !errors.Is(err, errUsage) {
				t.Errorf("err = %v, want errUsage", err)
			}
			// stdout is data. A usage complaint is not data.
			if out.Len() != 0 {
				t.Errorf("stdout = %q, want nothing", out.String())
			}
			if errs.Len() == 0 {
				t.Error("nothing on stderr to explain the problem")
			}
		})
	}
}

// TestRunHelpExitsZero keeps `gochat -h | less` from looking like a failure.
func TestRunHelpExitsZero(t *testing.T) {
	t.Parallel()

	for _, arg := range []string{"-h", "-help", "--help", "help"} {
		var out, errs bytes.Buffer
		if err := run(t.Context(), []string{arg}, &out, &errs); err != nil {
			t.Errorf("run(%s) = %v, want nil", arg, err)
		}
		if !strings.Contains(errs.String(), "Usage:") {
			t.Errorf("run(%s) printed no usage", arg)
		}
	}
}

// TestFlagsAfterTheRoomAreExplained covers the mistake this argument order
// invites. Go's flag parsing stops at the first plain argument, so a flag
// written after the room name silently becomes an extra argument.
func TestFlagsAfterTheRoomAreExplained(t *testing.T) {
	t.Parallel()
	srv := fakeServer(t)

	var out, errs bytes.Buffer
	err := run(t.Context(), []string{"read", "-addr", srv.URL, "general", "-json"}, &out, &errs)

	if !errors.Is(err, errUsage) {
		t.Fatalf("err = %v, want errUsage", err)
	}
	if !strings.Contains(errs.String(), "flags go before the room name") {
		t.Errorf("stderr does not explain the order:\n%s", errs.String())
	}
}

func TestRunVersion(t *testing.T) {
	t.Parallel()
	var out, errs bytes.Buffer

	if err := run(t.Context(), []string{"version"}, &out, &errs); err != nil {
		t.Fatalf("run: %v", err)
	}

	if strings.TrimSpace(out.String()) == "" {
		t.Error("version printed nothing")
	}
}

// TestServerFailuresBecomeAdvice checks the one thing a person needs when a
// token stops working: what to do about it.
func TestServerFailuresBecomeAdvice(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":"That token is not valid."}`))
	}))
	t.Cleanup(srv.Close)

	var out, errs bytes.Buffer
	err := run(t.Context(), []string{"rooms", "-addr", srv.URL, "-token-file", tokenFile(t)}, &out, &errs)

	if err == nil {
		t.Fatal("a 401 was treated as success")
	}
	// Exit 1, not 2: the command was well formed, the server refused it.
	if errors.Is(err, errUsage) {
		t.Error("a refused token was reported as a usage error")
	}
	if !strings.Contains(err.Error(), "token") {
		t.Errorf("err = %v, want it to mention the token", err)
	}
}

func TestTokenComesFromTheFileFirst(t *testing.T) {
	// No t.Parallel: t.Setenv below restores the variable, and forbids it.
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("Authorization")
		w.Write([]byte(`{"name":"Ada"}`))
	}))
	t.Cleanup(srv.Close)

	// Both are set. The file wins, because the variable is the leaky one.
	t.Setenv("GOCHAT_TOKEN", "gochat_from_env")
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte("gochat_from_file\n"), 0o600); err != nil {
		t.Fatalf("writing the token file: %v", err)
	}

	var out, errs bytes.Buffer
	if err := run(t.Context(), []string{"whoami", "-addr", srv.URL, "-token-file", path}, &out, &errs); err != nil {
		t.Fatalf("run: %v", err)
	}

	if got != "Bearer gochat_from_file" {
		t.Errorf("Authorization = %q, want the token from the file", got)
	}
}

func TestTokenFallsBackToTheEnvironment(t *testing.T) {
	// No t.Parallel, for the same reason as the test above.
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("Authorization")
		w.Write([]byte(`{"name":"Ada"}`))
	}))
	t.Cleanup(srv.Close)

	t.Setenv("GOCHAT_TOKEN", "gochat_from_env")
	t.Setenv("GOCHAT_TOKEN_FILE", "")

	var out, errs bytes.Buffer
	if err := run(t.Context(), []string{"whoami", "-addr", srv.URL}, &out, &errs); err != nil {
		t.Fatalf("run: %v", err)
	}

	if got != "Bearer gochat_from_env" {
		t.Errorf("Authorization = %q, want the token from the environment", got)
	}
}

func TestAMissingTokenFileIsReported(t *testing.T) {
	t.Parallel()
	srv := fakeServer(t)
	var out, errs bytes.Buffer

	err := run(t.Context(), []string{"rooms", "-addr", srv.URL, "-token-file", "/no/such/file"}, &out, &errs)

	// Silently falling back to no token would answer "not signed in", which
	// sends the reader looking in the wrong place.
	if err == nil {
		t.Fatal("a missing token file was ignored")
	}
	if !strings.Contains(err.Error(), "token file") {
		t.Errorf("err = %v, want it to name the token file", err)
	}
}
