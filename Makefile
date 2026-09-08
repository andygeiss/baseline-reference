# Copied from the baseline (stack/makefile.md). Adjust per its rule 5; record
# any other deviation in the README.

# This module ships two binaries, so MAIN names the one `make run` starts and
# the build target takes them both (stack/makefile.md rule 5).
MAIN = ./cmd/server

# Targets are alphabetical, so the default is named rather than first.
.DEFAULT_GOAL = check
.PHONY: build check ci clean fmt lan run test

# Release-shaped local binaries in bin/ (go build creates the directory).
build:
	CGO_ENABLED=0 go build -trimpath -o bin/ ./cmd/...

# Default. Every gate, in this order (operations/ci.md), against the working
# tree. Run before every commit.
check:
	test -z "$$(gofmt -l .)" || (gofmt -l . && exit 1)
	go vet ./...
	go fix -diff ./...
	go run honnef.co/go/tools/cmd/staticcheck@latest ./...
	go run golang.org/x/vuln/cmd/govulncheck@latest ./...
	go mod tidy -diff
	go test -race -shuffle=on ./...
	CGO_ENABLED=0 go build -trimpath ./...

# The same gates against the commit: a file never added, or a .env, cannot
# make it green. Run before every push. go version runs first, inside the
# copy, so the run records which toolchain ran. The archive goes through a
# file so git's exit status stops the run; one shell line so the trap cleans
# up however check ends.
ci:
	t=$$(mktemp); d=$$(mktemp -d); trap 'rm -rf "$$t" "$$d"' EXIT; git archive -o "$$t" HEAD && tar -xf "$$t" -C "$$d" && go -C "$$d" version && $(MAKE) -C "$$d" check

clean:
	rm -rf bin/

# goimports first: go fix type-checks, so a missing import would stop the
# recipe before goimports could add it. go fix manages the imports its own
# rewrites need.
fmt:
	go run golang.org/x/tools/cmd/goimports@latest -w .
	n=3; until go fix -diff ./... > /dev/null 2>&1 || [ $$n -eq 0 ]; do go fix ./... || exit 1; n=$$((n - 1)); done
	go run golang.org/x/tools/cmd/goimports@latest -w .

# Reaches this app from a phone over HTTPS, which install needs
# (patterns/local-https.md). A real recurring command, so rule 3 allows it.
# Runs beside `make run`, in a second shell — Caddy answers 502 until the app
# is up. scutil is macOS; on Linux the same name comes from Avahi.
lan:
	LAN_HOST="$$(scutil --get LocalHostName).local" caddy run --config Caddyfile.lan

# Loads .env when it is there, so a local start is one command. Only run:
# check and test MUST NOT depend on a developer's machine (rule 6). One shell
# line, because each recipe line gets its own shell.
run:
	set -a; if [ -f .env ]; then . ./.env; fi; set +a; go run $(MAIN)

# The inner loop.
test:
	go test -race -shuffle=on ./...
