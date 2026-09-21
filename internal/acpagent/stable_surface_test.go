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
	files, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if len(files) == 0 {
		t.Fatalf("no source files found under %s", dir)
	}
	for _, name := range []string{"acp.go", "stdout_redirect.go"} {
		files = append(files, filepath.Join(dir, "..", "..", "cmd", "chord", name))
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
