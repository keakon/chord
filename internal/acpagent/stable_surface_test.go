package acpagent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The adapter may only touch ACP's stable surface. The unstable methods and
// capability fields can be reshaped or dropped without notice, and the SDK
// exports both from the same namespaces, so the compiler cannot tell them apart:
// this source check is what keeps the boundary deliberate.
func TestAdapterAvoidsUnstableACPSurface(t *testing.T) {
	needle := "Un" + "stable" // split so this file does not match itself
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	// Every file that speaks to the ACP SDK: this package, the mux frontend,
	// the session child entrypoint, and the stdout guard.
	patterns := []string{
		filepath.Join(dir, "*.go"),
		filepath.Join(dir, "..", "..", "internal", "acpmux", "*.go"),
		filepath.Join(dir, "..", "..", "cmd", "chord", "acp.go"),
		filepath.Join(dir, "..", "..", "cmd", "chord", "acp_child.go"),
		filepath.Join(dir, "..", "..", "cmd", "chord", "stdout_redirect.go"),
	}
	var files []string
	for _, pattern := range patterns {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			t.Fatalf("glob %s: %v", pattern, err)
		}
		if len(matches) == 0 {
			t.Fatalf("no source files match %s", pattern)
		}
		files = append(files, matches...)
	}
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		source, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("read %s: %v", file, err)
		}
		if strings.Contains(string(source), needle) {
			t.Errorf("%s references the unstable ACP surface", file)
		}
	}
}
