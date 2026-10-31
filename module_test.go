package reference

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// The room map's module is committed, because make ci needs no TinyGo, and
// embedded and served as it is (patterns/wasm-client.md rule 2). make wasm
// writes beside it the digest of its inputs — every Go file the js/wasm target
// compiles into it, go.mod, and the .build file naming compiler and flags — and
// this test recomputes it, so a source change without a rebuild goes red here
// rather than shipping an old module.

const module = "github.com/andygeiss/baseline-reference/v5"

func TestTheCommittedModuleIsBuiltFromTheTree(t *testing.T) {
	t.Parallel()
	if _, err := os.Stat("web/static/roommap.wasm"); err != nil {
		t.Fatalf("%v — run make wasm and commit the module", err)
	}
	want := sums(t, "web/static/roommap.wasm.sum")
	got := inputs(t)
	for file, hash := range got {
		switch {
		case want[file] == "":
			t.Errorf("%s is an input of roommap.wasm and not in its digest — run make wasm and commit the module", file)
		case want[file] != hash:
			t.Errorf("%s changed since roommap.wasm was built — run make wasm and commit the module", file)
		}
	}
	for file := range want {
		if got[file] == "" {
			t.Errorf("%s is in roommap.wasm's digest and no longer an input — run make wasm and commit the module", file)
		}
	}
}

// The digest sees only the sources, so the .build file records the TinyGo and
// the flags, and is hashed in with them. This holds its lines to the Makefile,
// so a flag edited there without a rebuild goes red like a source edit.
func TestTheRecordedBuildMatchesTheMakefile(t *testing.T) {
	t.Parallel()
	vars := makeVars(t)
	got := map[string]string{}
	b, err := os.ReadFile("web/static/roommap.wasm.build")
	if err != nil {
		t.Fatalf("%v — run make wasm and commit the module", err)
	}
	for line := range strings.Lines(string(b)) {
		if key, value, ok := strings.Cut(strings.TrimSpace(line), " "); ok {
			got[key] = value
		}
	}
	want := map[string]string{"tinygo-version": vars["TINYGO_VERSION"], "tinygo-flags": vars["WASM_FLAGS"]}
	for key, w := range want {
		if w == "" || got[key] != w {
			t.Errorf("roommap.wasm.build records %s %q, and the Makefile says %q — run make wasm and commit the module", key, got[key], w)
		}
	}
	if len(got) != len(want) {
		t.Errorf("roommap.wasm.build records %d settings, want %d", len(got), len(want))
	}
}

// makeVars reads the Makefile's plain NAME = value lines.
func makeVars(t *testing.T) map[string]string {
	t.Helper()
	b, err := os.ReadFile("Makefile")
	if err != nil {
		t.Fatal(err)
	}
	vars := map[string]string{}
	for line := range strings.Lines(string(b)) {
		if name, value, ok := strings.Cut(line, " = "); ok && !strings.ContainsAny(name, " \t#") {
			vars[name] = strings.TrimSpace(value)
		}
	}
	return vars
}

// sums reads a digest file: one "hash  path" line a file, as shasum writes them.
func sums(t *testing.T, path string) map[string]string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v — run make wasm and commit the digest", err)
	}
	want := map[string]string{}
	for line := range strings.Lines(string(b)) {
		if f := strings.Fields(line); len(f) == 2 {
			want[f[1]] = f[0]
		}
	}
	return want
}

// inputs is every file make wasm hashes, each with its digest. The go list
// template is the Makefile's INPUTS.
func inputs(t *testing.T) map[string]string {
	t.Helper()
	cmd := exec.Command("go", "list", "-deps", "-f",
		`{{if not .Standard}}{{range .GoFiles}}{{$.ImportPath}}/{{.}}{{"\n"}}{{end}}{{end}}`, "./cmd/roommap")
	cmd.Env = append(cmd.Environ(), "GOOS=js", "GOARCH=wasm")
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := errors.AsType[*exec.ExitError](err); ok {
			t.Fatalf("go list: %v\n%s", err, ee.Stderr)
		}
		t.Fatalf("go list: %v", err)
	}
	files := []string{"go.mod", "web/static/roommap.wasm.build"}
	for line := range strings.Lines(string(out)) {
		if line = strings.TrimSpace(line); line != "" {
			files = append(files, strings.TrimPrefix(line, module+"/"))
		}
	}
	got := map[string]string{}
	for _, file := range files {
		b, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(b)
		got[file] = hex.EncodeToString(sum[:])
	}
	return got
}
