// Command chat talks to a Go Chat server from the command line.
//
// It starts, does one job, and exits. There is no prompt and no live tail: a
// tool that holds a conversation with a person is a web application, and this
// baseline has one of those. `watch -n3 chat read general` is a live tail, and
// it composes out of parts that already exist.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	err := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	switch {
	case err == nil:
	case errors.Is(err, errUsage):
		os.Exit(2) // the message was already printed where the problem was found
	default:
		fmt.Fprintf(os.Stderr, "chat: %v\n", err)
		os.Exit(1)
	}
}
