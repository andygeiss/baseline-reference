package main

import (
	"cmp"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	// tzdata puts the time zone database inside the binary. Without it
	// time.LoadLocation reads /usr/share/zoneinfo, which a minimal container
	// image does not have — so a zone name loads on the developer's machine and
	// fails at boot on the server. About 450 KB, paid once
	// (patterns/time-and-dates.md).
	_ "time/tzdata"
)

// Config is every knob this binary has. After parseConfig returns, nothing
// else reads os.Getenv — the struct is the whole contract.
type Config struct {
	Host     string
	Port     string // string: net.JoinHostPort takes one
	OpsPort  string
	DBPath   string
	LogLevel slog.Level
	Env      string // dev | prod — picks text vs JSON log output

	// InviteCode gates registration. It is a secret, so it arrives as a file
	// and never as a flag or an environment variable, and LogValue below leaves
	// it out. Empty means the deployment passed none and anybody may register.
	InviteCode string

	// Assistant picks which adapter answers a mention: "echo" needs nothing and
	// is the default, "anthropic" needs a key. The two settings below are one
	// setting in two halves, so they are validated as a pair.
	Assistant    string
	AnthropicKey string

	// BaseURL is where this app answers from. It is the only thing a link in an
	// outgoing email is built from — never the request's Host header, which is
	// whatever the client sent (patterns/go-email.md).
	BaseURL *url.URL

	// Location is the one zone every rendered time is shown in, with its
	// abbreviation beside it. There is no per-reader zone: without JavaScript
	// the server cannot read one from the browser, and guessing is worse than
	// saying which zone the clock is (patterns/time-and-dates.md).
	Location *time.Location

	// Mailer picks which adapter sends: "log" needs nothing and is the default,
	// "smtp" needs a relay. These are one setting in several halves, so they
	// are validated together at the end.
	Mailer       string
	SMTPAddr     string
	SMTPFrom     string
	SMTPUser     string
	SMTPPassword string
}

// errUsage means the message was already printed where the problem was found,
// so main only has to pick the exit code.
var errUsage = errors.New("usage error")

// parseConfig applies the baseline's precedence rule — flags over environment
// variables over built-in defaults — and validates before anything binds.
func parseConfig(args []string, stderr io.Writer) (Config, error) {
	fs := flag.NewFlagSet("server", flag.ContinueOnError)
	fs.SetOutput(stderr)

	var c Config
	fs.StringVar(&c.Host, "host", cmp.Or(os.Getenv("HOST"), "127.0.0.1"), "bind address, env HOST (the proxy is the public listener)")
	fs.StringVar(&c.Port, "port", cmp.Or(os.Getenv("PORT"), "8080"), "app listen port (env PORT)")
	fs.StringVar(&c.OpsPort, "ops-port", cmp.Or(os.Getenv("OPS_PORT"), "6060"), "ops listen port, localhost only (env OPS_PORT)")
	fs.StringVar(&c.DBPath, "database-url", cmp.Or(os.Getenv("DATABASE_URL"), "app.db"), "SQLite file path (env DATABASE_URL)")
	level := fs.String("log-level", cmp.Or(os.Getenv("LOG_LEVEL"), "info"), "debug|info|warn|error (env LOG_LEVEL)")
	fs.StringVar(&c.Assistant, "assistant", cmp.Or(os.Getenv("ASSISTANT"), "echo"), "echo|anthropic — who answers a mention (env ASSISTANT)")
	base := fs.String("base-url", os.Getenv("BASE_URL"), "public address, e.g. https://chat.example.com — emailed links are built from it (env BASE_URL, default http://<host>:<port>)")
	zone := fs.String("timezone", cmp.Or(os.Getenv("TIMEZONE"), "UTC"), "IANA zone every time is shown in, e.g. Europe/Berlin (env TIMEZONE)")
	fs.StringVar(&c.Mailer, "mailer", cmp.Or(os.Getenv("MAILER"), "log"), "log|smtp — how a password-reset link is delivered (env MAILER)")
	fs.StringVar(&c.SMTPAddr, "smtp-addr", os.Getenv("SMTP_ADDR"), "relay host:port, required by -mailer=smtp (env SMTP_ADDR)")
	fs.StringVar(&c.SMTPFrom, "smtp-from", os.Getenv("SMTP_FROM"), "address mail is sent from, required by -mailer=smtp (env SMTP_FROM)")
	fs.StringVar(&c.SMTPUser, "smtp-user", os.Getenv("SMTP_USER"), "relay username; leave empty for a relay that wants none (env SMTP_USER)")
	fs.Usage = func() {
		fmt.Fprintf(stderr, "Usage of server:\n")
		fs.PrintDefaults()
		fmt.Fprintf(stderr, "\nRead from the environment only:\n  ENV\n\tdev|prod, picks text vs JSON logs (default dev)\n")
		fmt.Fprintf(stderr, "\nRead from $CREDENTIALS_DIRECTORY, one file per secret:\n  invite_code\n\tregistration is open when this file is absent\n  anthropic_key\n\trequired by -assistant=anthropic, unused otherwise\n  smtp_password\n\trequired when -smtp-user is set, unused otherwise\n")
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return Config{}, err // -h: usage printed, exit 0
		}
		return Config{}, errUsage // fs already printed the message and the usage
	}

	c.Env = cmp.Or(os.Getenv("ENV"), "dev") // not a flag: the deployment sets it, never a command line

	if err := c.LogLevel.UnmarshalText([]byte(*level)); err != nil {
		return Config{}, fmt.Errorf("log-level %q: want debug, info, warn, or error", *level)
	}
	for _, p := range []struct{ name, value string }{{"port", c.Port}, {"ops-port", c.OpsPort}} {
		if _, err := strconv.ParseUint(p.value, 10, 16); err != nil {
			return Config{}, fmt.Errorf("%s %q: want a number from 0 to 65535", p.name, p.value)
		}
	}
	if c.Env != "dev" && c.Env != "prod" {
		return Config{}, fmt.Errorf("ENV %q: want dev or prod", c.Env)
	}

	inviteCode, err := readCredential("invite_code")
	if err != nil {
		return Config{}, err
	}
	c.InviteCode = inviteCode

	if c.Assistant != "echo" && c.Assistant != "anthropic" {
		return Config{}, fmt.Errorf("assistant %q: want echo or anthropic", c.Assistant)
	}
	anthropicKey, err := readCredential("anthropic_key")
	if err != nil {
		return Config{}, err
	}
	c.AnthropicKey = anthropicKey

	// The checks above see one field at a time and cannot see a pair. These two
	// are really one setting — which adapter, and the key it needs — so they
	// get their own check, and it says which half is missing and what to do
	// about it (patterns/go-config.md rule 7).
	//
	// Without it the half-configured pair starts fine and fails on the first
	// mention, which is the worst place to find out.
	switch {
	case c.Assistant == "anthropic" && c.AnthropicKey == "":
		return Config{}, errors.New(`assistant "anthropic": no key — write it to $CREDENTIALS_DIRECTORY/anthropic_key, or run with -assistant=echo`)
	case c.Assistant != "anthropic" && c.AnthropicKey != "":
		return Config{}, fmt.Errorf("assistant %q: an anthropic_key credential is present — set -assistant=anthropic to use it, or remove the file", c.Assistant)
	}

	// The zone is loaded here rather than where a page is rendered: a name
	// nobody has heard of is a boot error naming the fix, not a nil location
	// found three weeks later by the one reader who looked at a timestamp.
	c.Location, err = time.LoadLocation(*zone)
	if err != nil {
		return Config{}, fmt.Errorf("timezone %q: want an IANA name like UTC or Europe/Berlin", *zone)
	}

	// The default is only right in development, where the address the browser
	// uses and the address the app binds are the same. A deployment behind a
	// proxy passes the real one, and nothing at request time can work it out.
	c.BaseURL, err = parseBaseURL(cmp.Or(*base, "http://"+net.JoinHostPort(c.Host, c.Port)))
	if err != nil {
		return Config{}, err
	}

	smtpPassword, err := readCredential("smtp_password")
	if err != nil {
		return Config{}, err
	}
	c.SMTPPassword = smtpPassword
	if err := c.checkMailer(); err != nil {
		return Config{}, err
	}
	return c, nil
}

// parseBaseURL refuses anything a link cannot be built from.
func parseBaseURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	switch {
	case err != nil:
		return nil, fmt.Errorf("base-url %q: %v", raw, err)
	case u.Scheme != "http" && u.Scheme != "https":
		return nil, fmt.Errorf("base-url %q: want an http:// or https:// address", raw)
	case u.Host == "":
		return nil, fmt.Errorf("base-url %q: no host — write it as https://chat.example.com", raw)
	}
	u.Path = strings.TrimSuffix(u.Path, "/") // JoinPath would double the slash
	return u, nil
}

// checkMailer is the pair check for how mail goes out. Every one of these is a
// half-configured setting that starts fine and fails at the one moment somebody
// needed it — the request for a reset link (patterns/go-config.md rule 7).
func (c Config) checkMailer() error {
	switch {
	case c.Mailer != "log" && c.Mailer != "smtp":
		return fmt.Errorf("mailer %q: want log or smtp", c.Mailer)

	// logmail writes the whole message, reset link included, to the log. That
	// is exactly what makes it useful in development and unacceptable anywhere
	// the log is not the developer's own screen.
	case c.Mailer == "log" && c.Env == "prod":
		return errors.New(`mailer "log" writes reset links to the log, which is not safe with ENV=prod — set -mailer=smtp`)

	case c.Mailer == "smtp" && c.SMTPAddr == "":
		return errors.New(`mailer "smtp": no relay — set -smtp-addr=host:port, or run with -mailer=log`)
	case c.Mailer == "smtp" && c.SMTPFrom == "":
		return errors.New(`mailer "smtp": no sender — set -smtp-from=chat@example.com, or run with -mailer=log`)
	case c.Mailer != "smtp" && (c.SMTPAddr != "" || c.SMTPFrom != "" || c.SMTPUser != ""):
		return fmt.Errorf("mailer %q: SMTP settings are present — set -mailer=smtp to use them, or remove them", c.Mailer)

	// A line break here would let the sender write extra headers into every
	// message this app sends. The check belongs at boot, because this value
	// reaches a header on a path with no request on it (patterns/go-email.md).
	case strings.ContainsAny(c.SMTPFrom, "\r\n"):
		return fmt.Errorf("smtp-from %q: no line breaks — that value goes into a mail header", c.SMTPFrom)

	case c.SMTPUser != "" && c.SMTPPassword == "":
		return errors.New(`smtp-user is set but no password — write it to $CREDENTIALS_DIRECTORY/smtp_password, or clear -smtp-user`)
	case c.SMTPUser == "" && c.SMTPPassword != "":
		return errors.New("an smtp_password credential is present but -smtp-user is not set — set it, or remove the file")
	}
	if c.Mailer == "smtp" {
		if _, _, err := net.SplitHostPort(c.SMTPAddr); err != nil {
			return fmt.Errorf("smtp-addr %q: want host:port, like mail.example.com:587", c.SMTPAddr)
		}
	}
	return nil
}

// readCredential returns the credential file of that name, or "" when the
// deployment passes none — which is the normal case in dev. The caller decides
// whether an empty secret is fatal; that depends on the feature, not on config.
//
// $CREDENTIALS_DIRECTORY is set by the deployment and points somewhere only the
// service user can read. It is the one environment variable read for a secret,
// and it holds a directory path rather than the secret itself: a flag value
// shows up in `ps` and in shell history, and an environment variable is
// inherited by every child process.
func readCredential(name string) (string, error) {
	dir := os.Getenv("CREDENTIALS_DIRECTORY")
	if dir == "" {
		return "", nil
	}
	b, err := os.ReadFile(filepath.Join(dir, name))
	if errors.Is(err, os.ErrNotExist) {
		return "", nil // the directory exists, this secret does not: the feature is off
	}
	if err != nil {
		return "", fmt.Errorf("credential %q: %w", name, err)
	}
	return strings.TrimSpace(string(b)), nil // the file usually ends in a newline
}

// LogValue is what slog logs for a Config: everything except the secrets.
// InviteCode and AnthropicKey are missing from this list on purpose, and adding
// a third secret to the struct does not add it here either. An allowlist is
// what makes that true — a blocklist forgets the field somebody adds next year.
//
// Whether registration is gated is worth a line in the boot log, so the fact is
// logged without the code that enforces it.
func (c Config) LogValue() slog.Value {
	// Both of these are set by the time anything logs a parsed Config. They are
	// still read through a nil check, because a LogValue that panics puts a
	// stack trace where the boot line should be — slog catches it and prints
	// that instead of the config.
	baseURL, timezone := "", ""
	if c.BaseURL != nil {
		baseURL = c.BaseURL.String()
	}
	if c.Location != nil {
		timezone = c.Location.String()
	}
	return slog.GroupValue(
		slog.String("host", c.Host),
		slog.String("port", c.Port),
		slog.String("ops_port", c.OpsPort),
		slog.String("database_url", c.DBPath),
		slog.String("assistant", c.Assistant),
		slog.String("base_url", baseURL),
		slog.String("timezone", timezone),
		slog.String("mailer", c.Mailer),
		slog.String("smtp_addr", c.SMTPAddr),
		slog.String("smtp_from", c.SMTPFrom),
		slog.String("env", c.Env),
		slog.String("log_level", c.LogLevel.String()),
		slog.Bool("registration_gated", c.InviteCode != ""),
	)
}
