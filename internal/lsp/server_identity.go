package lsp

import (
	"path/filepath"
	"slices"
	"strings"

	"github.com/keakon/chord/internal/config"
)

func matchesServerIdentity(name, command string, identities ...string) bool {
	name = strings.ToLower(strings.TrimSpace(name))
	command = strings.ToLower(filepath.Base(command))
	for _, suffix := range []string{".exe", ".cmd", ".bat"} {
		command = strings.TrimSuffix(command, suffix)
	}
	return slices.Contains(identities, name) || slices.Contains(identities, command)
}

func isPyrightServer(name string, cfg config.LSPServerConfig) bool {
	return matchesServerIdentity(name, cfg.Command, "pyright", "pyright-langserver", "basedpyright", "basedpyright-langserver")
}

func effectiveRootMarkers(name string, cfg config.LSPServerConfig) []string {
	if len(cfg.RootMarkers) > 0 {
		return cfg.RootMarkers
	}
	if isPyrightServer(name, cfg) {
		return []string{"pyrightconfig.json", "pyproject.toml", "requirements.txt"}
	}
	if matchesServerIdentity(name, cfg.Command, "typescript", "typescript-language-server") {
		return []string{"tsconfig.json", "jsconfig.json", "package.json"}
	}
	return nil
}
