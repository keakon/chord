package tui

import (
	"path/filepath"
	"strings"

	"github.com/keakon/chord/internal/agent"
)

// lspWorkspaceLabel includes enough parents to distinguish equally named
// packages in separate checkouts, without printing every absolute path.
func lspWorkspaceLabel(row agent.LSPServerDisplay, rows []agent.LSPServerDisplay) string {
	if row.Root == "" {
		return row.Name
	}
	parts := strings.Split(filepath.Clean(row.Root), string(filepath.Separator))
	for depth := 1; depth <= len(parts); depth++ {
		suffix := strings.Join(parts[len(parts)-depth:], string(filepath.Separator))
		unique := true
		for _, other := range rows {
			if other.Name != row.Name || other.Root == row.Root || other.Root == "" {
				continue
			}
			clean := filepath.Clean(other.Root)
			if clean == suffix || strings.HasSuffix(clean, string(filepath.Separator)+suffix) {
				unique = false
				break
			}
		}
		if unique {
			return row.Name + " · " + suffix
		}
	}
	return row.Name + " · " + row.Root
}
