package tui

import (
	"path/filepath"
	"strings"

	"github.com/keakon/chord/internal/permission"
)

func resolveRuleScopePath(scope permission.RuleScope, projectRoot, homeDir, role string) string {
	role = strings.TrimSpace(role)
	if role == "" {
		return ""
	}
	switch scope {
	case permission.ScopeProject:
		projectRoot = strings.TrimSpace(projectRoot)
		if projectRoot == "" {
			return ""
		}
		return filepath.Join(projectRoot, ".chord", "permissions", role+".yaml")
	case permission.ScopeUserGlobal:
		homeDir = strings.TrimSpace(homeDir)
		if homeDir == "" {
			return ""
		}
		return filepath.Join(homeDir, ".chord", "permissions", role+".yaml")
	default:
		return ""
	}
}
