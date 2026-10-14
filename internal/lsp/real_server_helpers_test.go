package lsp

import (
	"os/exec"
	"testing"
)

func requireRealGopls(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("real language-server round trip is disabled in short mode")
	}
	path, err := exec.LookPath("gopls")
	if err != nil {
		t.Skip("gopls not installed: skipping the real language-server round trip")
	}
	return path
}
