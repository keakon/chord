package lsp

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/keakon/x/powernap/pkg/lsp/protocol"

	"github.com/keakon/chord/internal/config"
)

// Exercise the actual edit-output path with diagnostics in unrelated projects.
func BenchmarkLSPWorkspaceOutput(b *testing.B) {
	for _, count := range []int{1, 8} {
		b.Run(fmt.Sprintf("Workspaces%d", count), func(b *testing.B) {
			root := normalizeWaiterPath(b.TempDir())
			m := NewManager(&config.Config{}, root, nil)
			primary := ""
			for i := range count {
				dir := filepath.Join(root, fmt.Sprintf("project%d", i))
				if err := os.MkdirAll(dir, 0700); err != nil {
					b.Fatal(err)
				}
				c := &Client{cwd: dir, name: "gopls", cfg: config.LSPServerConfig{FileTypes: []string{"go"}}, diagnostics: make(map[protocol.DocumentURI][]protocol.Diagnostic)}
				m.clients[clientKey{name: "gopls", root: dir}] = c
				for j := range 40 {
					path := filepath.Join(dir, fmt.Sprintf("file%d.go", j))
					if err := os.WriteFile(path, []byte("package sample\n"), 0600); err != nil {
						b.Fatal(err)
					}
					c.diagnostics[protocol.DocumentURI(c.pathToURI(path))] = []protocol.Diagnostic{{Severity: 1, Message: "sample diagnostic"}}
					if i == 0 && j == 0 {
						primary = path
					}
				}
			}
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				_ = m.AppendLSPDiagnosticsToToolOutputForPaths("written", []string{primary}, true, nil, nil, nil, root)
			}
		})
	}
}
