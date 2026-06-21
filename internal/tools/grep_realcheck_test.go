package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestGrepRealTempDirFastPath is a smoke test against the real system temp
// directory. It writes a probe file there, then greps the temp dir with an
// exact include for the probe filename. The fast path should read the file
// directly instead of walking the (potentially huge) temp dir, so the call must
// return well under the deadline rather than hanging like the pre-fix behavior.
func TestGrepRealTempDirFastPath(t *testing.T) {
	tmp := os.TempDir()
	target := filepath.Join(tmp, "chord-fastpath-probe.html")
	if err := os.WriteFile(target, []byte("probe-needle-xyz\n"), 0o644); err != nil {
		t.Skipf("cannot write probe file: %v", err)
	}
	defer os.Remove(target)

	raw, _ := json.Marshal(map[string]any{
		"pattern":  "probe-needle-xyz",
		"paths":    []string{tmp},
		"includes": []string{"chord-fastpath-probe.html"},
	})
	deadline := 5 * time.Second
	done := make(chan struct{})
	var (
		out string
		err error
	)
	go func() {
		out, err = GrepTool{}.Execute(context.Background(), raw)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(deadline):
		t.Fatalf("grep on real temp dir did not return within %v", deadline)
	}
	if err != nil {
		t.Fatalf("expected fast-path success, got err: %v", err)
	}
	if !strings.Contains(out, "probe-needle-xyz") {
		t.Fatalf("expected match, got:\n%s", out)
	}
}
