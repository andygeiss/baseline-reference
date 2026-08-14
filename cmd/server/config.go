package main

import (
	"cmp"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strconv"
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
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return Config{}, err // -h: usage printed, exit 0
		}
		return Config{}, errUsage // fs already printed the message and the usage
	}

	c.Env = cmp.Or(os.Getenv("ENV"), "dev") // not a flag: the unit file sets it

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
	return c, nil
}

// LogValue is what slog logs for a Config. This app holds no secrets, so the
// list is every field; the allowlist shape is the point — a secret added to
// Config does not reach the logs until somebody adds it here on purpose.
func (c Config) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("host", c.Host),
		slog.String("port", c.Port),
		slog.String("ops_port", c.OpsPort),
		slog.String("database_url", c.DBPath),
		slog.String("env", c.Env),
		slog.String("log_level", c.LogLevel.String()),
	)
}
