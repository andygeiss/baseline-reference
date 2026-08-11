# Copied from the baseline (stack/makefile.md). Adjust per its rule 5; record
# any other deviation in the README.

# The main package: ./cmd/server for a web application, . for a single-binary
# CLI.
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

run:
	go run $(MAIN)

fmt:
	go run golang.org/x/tools/cmd/goimports@latest -w .

# Release-shaped local binary in bin/ (go build creates the directory).
build:
	CGO_ENABLED=0 go build -trimpath -o bin/ $(MAIN)

clean:
	rm -rf bin/
