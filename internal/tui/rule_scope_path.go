package tui

import (
	"path/filepath"
	"strings"

	"github.com/keakon/chord/internal/config"
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
		return filepath.Join(projectRoot, ".chord", "agents", role+".yaml")
	case permission.ScopeUserGlobal:
		configHome, err := config.ConfigHomeDir()
		if err != nil || strings.TrimSpace(configHome) == "" {
			return ""
		}
		return filepath.Join(configHome, "agents", role+".yaml")
	default:
		return ""
	}
}
