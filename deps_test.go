package reference

import (
	"errors"
	"os/exec"
	"strings"
	"testing"
)

// forbidden is what no package of the browser module may reach
// (patterns/wasm-client.md rule 4). Each is bytes in the module, and none of
// them means anything in a browser: it brings its own HTTP and WebSocket, has
// no signals, and the module runs on one thread.
var forbidden = []string{"net/http", "os/signal", "golang.org/x/sync"}

// The browser packages are not a list kept here: they are whatever the module
// is built from, under the target it builds for, so a package the module
// starts importing is held to the rule the moment it does.
func TestTheBrowserPackagesStaySmall(t *testing.T) {
	t.Parallel()
	cmd := exec.Command("go", "list", "-deps", "-f", "{{.ImportPath}}", "./cmd/roommap")
	cmd.Env = append(cmd.Environ(), "GOOS=js", "GOARCH=wasm")
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := errors.AsType[*exec.ExitError](err); ok {
			t.Fatalf("go list: %v\n%s", err, ee.Stderr)
		}
		t.Fatalf("go list: %v", err)
	}
	reached := map[string]bool{}
	ours := 0
	for line := range strings.Lines(string(out)) {
		path := strings.TrimSpace(line)
		reached[path] = true
		if strings.HasPrefix(path, module) {
			ours++
		}
	}
	for _, f := range forbidden {
		if reached[f] {
			t.Errorf("the browser module reaches %s — move the code that needs it to the server side", f)
		}
	}
	// The module and its wire format. A third package of ours is a decision,
	// and this is where it gets made.
	if ours != 2 {
		t.Errorf("the browser module is built from %d packages of this module, want 2 (cmd/roommap, internal/presence)", ours)
	}
}
