package main

import (
	"runtime/debug"
	"testing"
)

// TestResolveVersion covers all three cases, which is only possible because
// resolveVersion takes its build info as a parameter: the test binary's own
// build is one case, and the other two are reachable only as table rows.
func TestResolveVersion(t *testing.T) {
	t.Parallel()

	settings := func(revision, modified string) []debug.BuildSetting {
		return []debug.BuildSetting{
			{Key: "vcs.revision", Value: revision},
			{Key: "vcs.modified", Value: modified},
		}
	}

	// want == "" means: any name, so long as it is not the version string the
	// toolchain reported. Those cases produce a boot id, which changes between
	// runs, so there is no fixed string to assert against.
	tests := []struct {
		name string
		info *debug.BuildInfo
		want string
	}{
		{
			name: "a released version is used as it is",
			info: &debug.BuildInfo{Main: debug.Module{Version: "v1.4.0"}},
			want: "v1.4.0",
		},
		{
			name: "a clean checkout is named by its commit",
			info: &debug.BuildInfo{
				Main:     debug.Module{Version: "(devel)"},
				Settings: settings("abcdef0123456789abcdef", "false"),
			},
			want: "abcdef012345",
		},
		{
			name: "a short revision is not truncated past its length",
			info: &debug.BuildInfo{
				Main:     debug.Module{Version: "(devel)"},
				Settings: settings("abcdef", "false"),
			},
			want: "abcdef",
		},
		{
			name: "a tag built from an edited tree does not pass through",
			info: &debug.BuildInfo{
				Main:     debug.Module{Version: "v0.3.0+dirty"},
				Settings: settings("abcdef0123456789abcdef", "true"),
			},
		},
		{
			name: "an edited checkout is named per boot, not by its commit",
			info: &debug.BuildInfo{
				Main:     debug.Module{Version: "(devel)"},
				Settings: settings("abcdef0123456789abcdef", "true"),
			},
		},
		{
			name: "no VCS metadata at all is named per boot",
			info: &debug.BuildInfo{Main: debug.Module{Version: "(devel)"}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := resolveVersion(tt.info)
			switch {
			case tt.want != "":
				if got != tt.want {
					t.Errorf("resolveVersion() = %q, want %q", got, tt.want)
				}
			case got == "":
				t.Error("resolveVersion() = \"\", want a boot id")
			case got == tt.info.Main.Version:
				// The regression this whole function exists to prevent: a
				// string that repeats itself across edits reaching the asset
				// cache-buster.
				t.Errorf("resolveVersion() = %q, which repeats after every edit", got)
			}
		})
	}
}
