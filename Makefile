# Copied from the baseline (stack/makefile.md). Adjust per its rule 5; record
# any other deviation in the README.

# This module ships two binaries, so MAIN names the one `make run` starts and
# the build target takes them both (stack/makefile.md rule 5).
MAIN = ./cmd/server

.PHONY: check test run fmt build clean

# Default. Every gate CI runs, identically and in the same order
# (operations/ci.md). Green here means green CI — run before every push.
check:
	test -z "$$(gofmt -l .)" || (gofmt -l . && exit 1)
	go vet ./...
	go run honnef.co/go/tools/cmd/staticcheck@latest ./...
	go run golang.org/x/vuln/cmd/govulncheck@latest ./...
	go mod tidy -diff
	go test -race -shuffle=on ./...
	CGO_ENABLED=0 go build -trimpath ./...

# The inner loop.
test:
	go test -race -shuffle=on ./...

# Loads .env when it is there, so a local start is one command. Only run:
# check and test MUST NOT depend on a developer's machine (rule 6). One shell
# line, because each recipe line gets its own shell.
run:
	set -a; if [ -f .env ]; then . ./.env; fi; set +a; go run $(MAIN)

fmt:
	go run golang.org/x/tools/cmd/goimports@latest -w .

# Release-shaped local binaries in bin/ (go build creates the directory).
build:
	CGO_ENABLED=0 go build -trimpath -o bin/ ./cmd/...

clean:
	rm -rf bin/
