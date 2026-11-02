package lsp

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/keakon/chord/internal/config"
)

// publishPerVersionProbe measures repeated publishes for representative input.
// A single-publish sample alone does not establish a settle-skip guarantee.
type publishPerVersionProbe struct {
	name     string
	command  string
	args     []string
	fileName string
	content  string
}

// TestRealServerPublishesPerVersion measures, for each probed server, how many
// publishDiagnostics notifications arrive for the written document version in
// the 15s after a write. Run it with the servers installed:
//
//	make test-fast PKGS='./internal/lsp -run TestRealServerPublishesPerVersion -count=1 -v'
//
// Counts land in the test log. Errors and larger workspaces must also be
// checked before changing the wait policy.
func TestRealServerPublishesPerVersion(t *testing.T) {
	t.Parallel()
	probes := []publishPerVersionProbe{
		{
			name:     "pyright",
			command:  "pyright-langserver",
			args:     []string{"--stdio"},
			fileName: "main.py",
			content:  "import os\n\n\ndef main() -> None:\n    print(os)\n",
		},
		{
			name:     "typescript-language-server",
			command:  "typescript-language-server",
			args:     []string{"--stdio"},
			fileName: "main.ts",
			content:  "const value: number = 1;\nconsole.log(value);\n",
		},
	}
	for _, probe := range probes {
		t.Run(probe.name, func(t *testing.T) {
			t.Parallel()
			if testing.Short() {
				t.Skip("real language-server probe is disabled in short mode")
			}
			bin := strings.Fields(probe.command)[0]
			if _, err := exec.LookPath(bin); err != nil {
				t.Skipf("%s not installed: skipping the publish-count probe", bin)
			}
			root := resolvedTempDir(t)
			path := filepath.Join(root, probe.fileName)
			if err := os.WriteFile(path, []byte(probe.content), 0o644); err != nil {
				t.Fatal(err)
			}
			// tsserver only analyzes a workspace it can configure; without a
			// tsconfig it stays silent.
			if probe.name == "typescript-language-server" {
				if err := os.WriteFile(filepath.Join(root, "tsconfig.json"), []byte("{\"compilerOptions\":{\"strict\":true}}"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			mgr := NewManager(&config.Config{LSP: config.LSPConfig{
				probe.name: {Command: probe.command, Args: probe.args, FileTypes: []string{filepath.Ext(probe.fileName)}},
			}}, root, nil)
			defer stopManager(mgr)
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()

			waiter := mgr.PrepareWaiter(path)
			mgr.AfterFileWriteToolResult(ctx, path, probe.content, "wrote", false, WatchedFileChanged, "")

			const probeWindow = 15 * time.Second
			deadline := time.After(probeWindow)
			type publish struct {
				version int32
				at      time.Duration
				count   int
			}
			start := time.Now()
			var publishes []publish
			for {
				select {
				case ev := <-waiter:
					// A publish without a document version still counts: the
					// settle decision is about repeated passes for the same
					// content, and servers like typescript-language-server do
					// not echo versions at all.
					found := false
					for i := range publishes {
						if publishes[i].version == ev.version {
							publishes[i].count++
							found = true
							break
						}
					}
					if !found {
						publishes = append(publishes, publish{version: ev.version, at: time.Since(start), count: 1})
					}
				case <-deadline:
					for _, p := range publishes {
						t.Logf("version %d: %d publishes, first at %v", p.version, p.count, p.at)
					}
					if len(publishes) == 0 {
						t.Logf("no publish within %v", probeWindow)
					}
					return
				}
			}
		})
	}
}
