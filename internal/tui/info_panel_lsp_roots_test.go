package tui

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/tui/terminaltext"
)

func TestLSPRowsIdentifyIndependentWorkspaces(t *testing.T) {
	backend := newInfoPanelAgent()
	backend.lspRows = []agent.LSPServerDisplay{
		{Name: "gopls", Root: filepath.Join("projects", "frontend"), OK: true, Errors: 1},
		{Name: "gopls", Root: filepath.Join("projects", "backend"), Pending: true},
	}
	m := NewModel(backend)
	section := strings.Join(infoPanelSectionLines(infoPanelPlainLines(m.renderInfoPanel(60, 24)), "LSP"), "\n")
	if !strings.Contains(section, "gopls · frontend 1 E") || !strings.Contains(section, "gopls · backend") {
		t.Fatalf("workspace rows are indistinguishable: %s", section)
	}
}

func TestLSPRootChangeInvalidatesPanelCache(t *testing.T) {
	backend := newInfoPanelAgent()
	backend.lspRows = []agent.LSPServerDisplay{{Name: "gopls", Root: "frontend", OK: true}}
	m := NewModel(backend)
	first := terminaltext.StripANSI(m.renderInfoPanel(60, 24))
	backend.lspRows[0].Root = "backend"
	second := terminaltext.StripANSI(m.renderInfoPanel(60, 24))
	if !strings.Contains(first, "gopls · frontend") || !strings.Contains(second, "gopls · backend") || strings.Contains(second, "gopls · frontend") {
		t.Fatalf("root change did not refresh: %q -> %q", first, second)
	}
}

func TestLSPRowsDistinguishSameNamedPackages(t *testing.T) {
	rows := []agent.LSPServerDisplay{
		{Name: "sample", Root: filepath.Join("main", "frontend")},
		{Name: "sample", Root: filepath.Join("task", "frontend")},
	}
	for _, row := range rows {
		if got := lspWorkspaceLabel(row, rows); got != "sample · "+row.Root {
			t.Fatalf("label = %q", got)
		}
	}
}
