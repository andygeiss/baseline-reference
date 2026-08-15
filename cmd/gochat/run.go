package main

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"runtime/debug"
	"strings"
	"time"

	"github.com/andygeiss/baseline-reference/v3/internal/chatapi"
	"github.com/andygeiss/baseline-reference/v3/internal/domain"
)

// errUsage means the message was already printed where the problem was found,
// so main only has to pick the exit code.
var errUsage = errors.New("usage error")

const usage = `chat — talk to a Go Chat server.

Usage:
  chat rooms [-json]                     list the rooms
  chat read [-since N] [-json] <room>    print what was said, oldest first
  chat post [-json] <room> [message]     say something; "-" or no message reads stdin
  chat whoami                            print the name the token belongs to
  chat version                           print the version

Flags come before the room name: "chat read -json general", not
"chat read general -json". That is how Go's flag parsing works — the first
argument that is not a flag ends the flags.

Every command takes:
  -addr        server address (env CHAT_ADDR, default http://localhost:8080)
  -token-file  file holding the API token (env CHAT_TOKEN_FILE)
  -json        one JSON object per line, for other programs

The token comes from -token-file, or from $CHAT_TOKEN_FILE, or from $CHAT_TOKEN.
Prefer a file: an environment variable is inherited by every process this one
starts, and printed by anything that inspects it. Make a token on the server's
"You" page.

Exit codes: 0 fine, 1 something failed, 2 you asked for something impossible.
`

func run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return errUsage
	}
	switch args[0] {
	case "rooms":
		return runRooms(ctx, args[1:], stdout, stderr)
	case "read":
		return runRead(ctx, args[1:], stdout, stderr)
	case "post":
		return runPost(ctx, args[1:], stdout, stderr)
	case "whoami":
		return runWhoami(ctx, args[1:], stdout, stderr)
	case "version":
		fmt.Fprintln(stdout, version())
		return nil
	case "-h", "-help", "--help", "help":
		fmt.Fprint(stderr, usage)
		return nil
	default:
		fmt.Fprintf(stderr, "unknown command %q\n%s", args[0], usage)
		return errUsage
	}
}

// common holds the flags every subcommand takes, so the contract is the same
// wherever it is read.
type common struct {
	addr      string
	tokenFile string
	asJSON    bool
}

func (c *common) bind(fs *flag.FlagSet) {
	// cmp.Or takes the first non-zero value, so flags-over-env-over-default is
	// one stdlib call.
	fs.StringVar(&c.addr, "addr", cmp.Or(os.Getenv("CHAT_ADDR"), "http://localhost:8080"),
		"server address (env CHAT_ADDR)")
	fs.StringVar(&c.tokenFile, "token-file", os.Getenv("CHAT_TOKEN_FILE"),
		"file holding the API token (env CHAT_TOKEN_FILE; CHAT_TOKEN also works)")
	fs.BoolVar(&c.asJSON, "json", false, "emit one JSON object per line")
}

// client builds the API client, reading the token as late as possible so a
// command that does not need one still runs.
func (c *common) client() (*chatapi.Client, error) {
	token, err := readToken(c.tokenFile)
	if err != nil {
		return nil, err
	}
	return chatapi.New(c.addr, token), nil
}

// readToken prefers the file, because $CHAT_TOKEN leaks into every child
// process and into anything that dumps the environment. A flag is worse still —
// its value shows up in ps and in shell history — so there is no -token flag.
func readToken(path string) (string, error) {
	if path == "" {
		return os.Getenv("CHAT_TOKEN"), nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read token file: %w", err)
	}
	return strings.TrimSpace(string(b)), nil // the file usually ends in a newline
}

// parse binds the common flags, parses args, and turns the flag package's own
// exits into errors the caller returns.
func parse(name string, c *common, args []string, stderr io.Writer) (*flag.FlagSet, error) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	c.bind(fs)
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil, nil // -h: usage already printed, exit 0
		}
		return nil, errUsage // bad flag: fs already said what was wrong
	}
	return fs, nil
}

func runRooms(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	var c common
	fs, err := parse("rooms", &c, args, stderr)
	if fs == nil {
		return err
	}
	client, err := c.client()
	if err != nil {
		return err
	}

	rooms, err := client.Rooms(ctx)
	if err != nil {
		return advise(err)
	}
	for _, room := range rooms {
		if c.asJSON {
			if err := writeJSON(stdout, jsonRoom{Slug: room.Slug, Name: room.Name}); err != nil {
				return err
			}
			continue
		}
		// Tab-separated: one room per line, so cut and awk work on it.
		fmt.Fprintf(stdout, "%s\t%s\n", room.Slug, room.Name)
	}
	return nil
}

func runRead(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	var c common
	fs := flag.NewFlagSet("read", flag.ContinueOnError)
	fs.SetOutput(stderr)
	c.bind(fs)
	since := fs.Int64("since", 0, "only what was said after this cursor")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return errUsage
	}
	if fs.NArg() != 1 {
		fmt.Fprintf(stderr, "chat read needs exactly one room\n%s%s", flagOrderHint(fs.Args()), usage)
		return errUsage
	}
	client, err := c.client()
	if err != nil {
		return err
	}

	msgs, next, err := client.Messages(ctx, fs.Arg(0), *since)
	if err != nil {
		return advise(err)
	}
	for _, m := range msgs {
		if c.asJSON {
			if err := writeJSON(stdout, newJSONMessage(m)); err != nil {
				return err
			}
			continue
		}
		// A message may hold line breaks, which is exactly why -json exists:
		// this format is for a person to read, and only the JSON one survives
		// being cut into lines.
		fmt.Fprintf(stdout, "%s %s: %s\n", m.CreatedAt.Local().Format("15:04"), m.Author, m.Body)
	}
	// The cursor goes to stderr, so it never lands in the middle of the data.
	// -json readers take it from the last object's seq instead.
	fmt.Fprintf(stderr, "next cursor: %d\n", next)
	return nil
}

func runPost(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	var c common
	fs, err := parse("post", &c, args, stderr)
	if fs == nil {
		return err
	}
	if fs.NArg() < 1 || fs.NArg() > 2 {
		fmt.Fprintf(stderr, "chat post needs a room and a message\n%s%s", flagOrderHint(fs.Args()), usage)
		return errUsage
	}
	client, err := c.client()
	if err != nil {
		return err
	}

	body := ""
	if fs.NArg() == 2 {
		body = fs.Arg(1)
	}
	// No message, or the conventional "-": read it from stdin, so `git log |
	// chat post general -` works.
	if body == "" || body == "-" {
		b, err := io.ReadAll(io.LimitReader(os.Stdin, int64(domain.MaxBodyLen)*4+1))
		if err != nil {
			return fmt.Errorf("reading the message from stdin: %w", err)
		}
		body = string(b)
	}

	msg, err := client.Post(ctx, fs.Arg(0), body)
	if err != nil {
		return advise(err)
	}
	if c.asJSON {
		return writeJSON(stdout, newJSONMessage(msg))
	}
	// The sequence number is the cursor of what was just said, so a script can
	// carry on reading from exactly there.
	fmt.Fprintln(stdout, msg.Seq)
	return nil
}

func runWhoami(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	var c common
	fs, err := parse("whoami", &c, args, stderr)
	if fs == nil {
		return err
	}
	client, err := c.client()
	if err != nil {
		return err
	}

	name, err := client.Me(ctx)
	if err != nil {
		return advise(err)
	}
	if c.asJSON {
		return writeJSON(stdout, struct {
			Name string `json:"name"`
		}{Name: name})
	}
	fmt.Fprintln(stdout, name)
	return nil
}

// The machine-readable shapes. Every field name here is a contract: renaming
// one breaks whatever script reads it, so it happens in a major release.
type (
	jsonRoom struct {
		Slug string `json:"slug"`
		Name string `json:"name"`
	}
	jsonMessage struct {
		Seq       int64  `json:"seq"`
		Author    string `json:"author"`
		Body      string `json:"body"`
		CreatedAt string `json:"created_at"`
	}
)

func newJSONMessage(m domain.Message) jsonMessage {
	return jsonMessage{
		Seq:       m.Seq,
		Author:    m.Author,
		Body:      m.Body,
		CreatedAt: m.CreatedAt.UTC().Format(time.RFC3339),
	}
}

// writeJSON emits one object per line — ND-JSON, so a reader can take it a line
// at a time instead of holding the whole answer.
func writeJSON(w io.Writer, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("encoding the answer: %w", err)
	}
	_, err = fmt.Fprintf(w, "%s\n", b)
	return err
}

// flagOrderHint spots the mistake this argument order invites — a flag written
// after the room name, which the flag package hands back as a plain argument —
// and says so, rather than leaving the reader with "needs exactly one room"
// while looking at a command line that plainly has one.
func flagOrderHint(rest []string) string {
	for _, arg := range rest {
		if strings.HasPrefix(arg, "-") && arg != "-" {
			return fmt.Sprintf("%q is a flag, and flags go before the room name.\n", arg)
		}
	}
	return ""
}

// advise turns the conditions worth explaining into an error that says what to
// do about it. Everything else goes up as it came.
func advise(err error) error {
	switch {
	case errors.Is(err, domain.ErrUnauthorized):
		return fmt.Errorf("%w\nMake a token on the server's \"You\" page, then point -token-file at it", err)
	case errors.Is(err, domain.ErrNotFound):
		return fmt.Errorf("%w\nRun `chat rooms` to see what there is", err)
	default:
		return err
	}
}

// version returns the build version the toolchain embedded — the tag when HEAD
// sits on one, a pseudo-version otherwise, "+dirty" when the tree was modified.
func version() string {
	info, ok := debug.ReadBuildInfo() // nil outside module mode — reading it would panic
	if !ok {
		return "unknown"
	}
	if v := info.Main.Version; v != "" && v != "(devel)" {
		return v // go install @version, or VCS-derived since Go 1.24
	}
	return "unknown" // "(devel)" means no VCS metadata — vcs.* is absent too
}
