package main

import (
	"bytes"
	"errors"
	"flag"
	"io"
	"log/slog"
	"strings"
	"testing"
)

// TestParseConfig_Precedence is the one most likely to regress silently: a
// wrong answer here still starts a working server, just not the one asked for.
func TestParseConfig_Precedence(t *testing.T) {
	t.Setenv("PORT", "9000")    // t.Setenv restores it, and forbids t.Parallel
	t.Setenv("HOST", "0.0.0.0") // overridden by the flag below

	got, err := parseConfig([]string{"-host", "10.0.0.1"}, io.Discard)
	if err != nil {
		t.Fatalf("parseConfig: %v", err)
	}
	if got.Host != "10.0.0.1" {
		t.Errorf("Host = %q, want 10.0.0.1 (the flag beats HOST)", got.Host)
	}
	if got.Port != "9000" {
		t.Errorf("Port = %q, want 9000 (from the environment)", got.Port)
	}
	if got.DBPath != "app.db" {
		t.Errorf("DBPath = %q, want the built-in default app.db", got.DBPath)
	}
}

// TestParseConfig_EmptyEnvironment pins the rule that the binary is runnable
// with nothing configured at all.
func TestParseConfig_EmptyEnvironment(t *testing.T) {
	for _, key := range []string{"HOST", "PORT", "OPS_PORT", "DATABASE_URL", "LOG_LEVEL", "ENV"} {
		t.Setenv(key, "")
	}

	got, err := parseConfig(nil, io.Discard)
	if err != nil {
		t.Fatalf("parseConfig: %v", err)
	}
	if got.Host != "127.0.0.1" || got.Port != "8080" || got.Env != "dev" || got.LogLevel != slog.LevelInfo {
		t.Errorf("defaults = %+v", got)
	}
}

func TestParseConfig_Invalid(t *testing.T) {
	tests := []struct {
		name, env, value string
		args             []string
		want             string
	}{
		{name: "port not a number", args: []string{"-port", "http"}, want: "want a number"},
		{name: "port out of range", args: []string{"-port", "99999"}, want: "want a number"},
		{name: "ops-port out of range", args: []string{"-ops-port", "70000"}, want: "ops-port"},
		{name: "log level", args: []string{"-log-level", "chatty"}, want: "want debug, info, warn, or error"},
		{name: "env", env: "ENV", value: "staging", want: "want dev or prod"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.env != "" {
				t.Setenv(tt.env, tt.value)
			}
			_, err := parseConfig(tt.args, io.Discard)
			if err == nil {
				t.Fatal("parseConfig succeeded, want an error")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error = %q, want it to mention %q", err, tt.want)
			}
		})
	}
}

// TestParseConfig_UsageErrorsAreNotPrintedTwice: a bad flag is reported by the
// FlagSet, so parseConfig returns the errUsage sentinel rather than the flag
// package's error — main prints the default branch, and would double up.
func TestParseConfig_UsageErrorsAreNotPrintedTwice(t *testing.T) {
	var stderr bytes.Buffer

	_, err := parseConfig([]string{"-nope"}, &stderr)
	if !errors.Is(err, errUsage) {
		t.Fatalf("err = %v, want errUsage", err)
	}
	if !strings.Contains(stderr.String(), "-nope") {
		t.Error("the FlagSet did not report the bad flag")
	}

	stderr.Reset()
	if _, err := parseConfig([]string{"-h"}, &stderr); !errors.Is(err, flag.ErrHelp) {
		t.Errorf("-h: err = %v, want flag.ErrHelp", err)
	}
	// ENV is not a flag, so only the custom Usage can document it.
	if !strings.Contains(stderr.String(), "ENV") {
		t.Error("-h does not document the environment-only variables")
	}
}

// TestConfig_LogValue proves the allowlist is wired: slog renders the group,
// not the struct's fields by reflection.
func TestConfig_LogValue(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	logger.Info("started", "config", Config{Host: "127.0.0.1", Port: "8080", Env: "dev"})

	if !strings.Contains(buf.String(), "config.host=127.0.0.1") {
		t.Errorf("log = %q, want a config group", buf.String())
	}
}
