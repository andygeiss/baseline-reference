package reference

import (
	"strings"
	"testing"
)

// forbidden is what no package of the browser module may reach
// (patterns/wasm-client.md rule 4). Each is bytes in the module, and none of
// them means anything in a browser: it brings its own HTTP and WebSocket, has
// no signals, and the module runs on one thread.
var forbidden = []string{"net/http", "os/signal", "golang.org/x/sync"}

// The browser packages are not a list kept here: they are whatever the module
// is built from, under the target and the build tags TinyGo builds it with, so
// a package the module starts importing is held to the rule the moment it does.
func TestTheBrowserPackagesStaySmall(t *testing.T) {
	t.Parallel()
	out := goListWasm(t, "-deps", "-f", "{{.ImportPath}}", "./cmd/roommap")
	reached := map[string]bool{}
	ours := 0
	for line := range strings.Lines(out) {
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
