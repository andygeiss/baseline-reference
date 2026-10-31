# Copied from the baseline (stack/makefile.md). Adjust per its rule 5; record
# any other deviation in the README.

# This module ships two binaries, so MAIN names the one `make run` starts and
# the build target takes them both (stack/makefile.md rule 5).
MAIN = ./cmd/server

# The room map's browser module (patterns/wasm-client.md). TINYGO_VERSION is
# the baseline's VERSIONS.md row, and wasm refuses any other TinyGo, so the
# .build file it records is true (stack/makefile.md rule 4).
TINYGO_VERSION = 0.44.0

# The flags the module is built with. wasm records them beside it, because the
# digest sees only the sources.
WASM_FLAGS = -no-debug -opt=z -panic=trap

# The module's size gate, in bytes: the measured build plus room for a change.
# check holds the committed module to it, and wasm the one it just built.
WASM_MAX_BYTES = 60000

# Every Go file of this module the js/wasm target compiles into the module, one
# a line, by import path: the digest's inputs. The test in module_test.go runs
# the same template, and the two have to stay the same one.
INPUTS = GOOS=js GOARCH=wasm go list -deps -f '{{if not .Standard}}{{range .GoFiles}}{{$$.ImportPath}}/{{.}}{{"\n"}}{{end}}{{end}}' ./cmd/roommap

# Every package of this module the module is built from, for check's js/wasm
# vet: the host vet never sees a js && wasm file.
BROWSER = GOOS=js GOARCH=wasm go list -deps -f '{{if not .Standard}}{{.ImportPath}}{{end}}' ./cmd/roommap

# Targets are alphabetical, so the default is named rather than first.
.DEFAULT_GOAL = check
.PHONY: build check ci clean fmt lan run test wasm

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
	GOOS=js GOARCH=wasm go vet $$($(BROWSER))
	s=$$(wc -c < web/static/roommap.wasm | tr -d ' '); test "$$s" -le $(WASM_MAX_BYTES) || (echo "roommap.wasm is $$s bytes, over WASM_MAX_BYTES ($(WASM_MAX_BYTES))" && exit 1)

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
# recipe before goimports could add it. go fix applies each fixer once and one
# rewrite exposes the next, so it repeats until its own diff is empty — bounded,
# so a fixer that cannot converge fails the recipe instead of hanging it.
# goimports again last: the import go fix adds to a single-import file is a
# second declaration, which gofmt keeps and only goimports merges.
fmt:
	go run golang.org/x/tools/cmd/goimports@latest -w .
	n=3; until go fix -diff ./... > /dev/null 2>&1; do [ $$n -gt 0 ] || exit 1; go fix ./... || exit 1; n=$$((n - 1)); done
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

# The browser module, rebuilt and committed: make ci needs no TinyGo, and the
# server embeds what is in the tree. wasm_exec.js is copied from the TinyGo
# that built the module, because it must match its compiler. Beside the module
# go the .build file (compiler and flags) and the .sum of its inputs — every Go
# file above, go.mod, and the .build — which module_test.go recomputes, so a
# source change cannot ship with an old module. The size check is last.
wasm:
	v=$$(tinygo version | awk '{print $$3}'); test "$$v" = "$(TINYGO_VERSION)" || (echo "tinygo is $$v, and VERSIONS.md pins $(TINYGO_VERSION): install that one" && exit 1)
	cp "$$(tinygo env TINYGOROOT)/targets/wasm_exec.js" web/static/js/wasm_exec.js
	tinygo build -target wasm $(WASM_FLAGS) -o web/static/roommap.wasm ./cmd/roommap
	printf 'tinygo-version %s\ntinygo-flags %s\n' '$(TINYGO_VERSION)' '$(WASM_FLAGS)' > web/static/roommap.wasm.build
	($(INPUTS) | sed 's|^github.com/andygeiss/baseline-reference/v5/||'; echo go.mod; echo web/static/roommap.wasm.build) | LC_ALL=C sort | xargs shasum -a 256 > web/static/roommap.wasm.sum
	s=$$(wc -c < web/static/roommap.wasm | tr -d ' '); test "$$s" -le $(WASM_MAX_BYTES) || (echo "roommap.wasm is $$s bytes, over WASM_MAX_BYTES ($(WASM_MAX_BYTES))" && exit 1)
