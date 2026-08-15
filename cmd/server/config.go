package main

import (
	"cmp"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
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
	fs.Usage = func() {
		fmt.Fprintf(stderr, "Usage of server:\n")
		fs.PrintDefaults()
		fmt.Fprintf(stderr, "\nRead from the environment only:\n  ENV\n\tdev|prod, picks text vs JSON logs (default dev)\n")
		fmt.Fprintf(stderr, "\nRead from $CREDENTIALS_DIRECTORY, one file per secret:\n  invite_code\n\tregistration is open when this file is absent\n")
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
	return c, nil
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
// InviteCode is missing from this list on purpose, and adding a second secret
// to the struct does not add it here either. An allowlist is what makes that
// true — a blocklist forgets the field somebody adds next year.
//
// Whether registration is gated is worth a line in the boot log, so the fact is
// logged without the code that enforces it.
func (c Config) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("host", c.Host),
		slog.String("port", c.Port),
		slog.String("ops_port", c.OpsPort),
		slog.String("database_url", c.DBPath),
		slog.String("env", c.Env),
		slog.String("log_level", c.LogLevel.String()),
		slog.Bool("registration_gated", c.InviteCode != ""),
	)
}
