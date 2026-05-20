package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/permission"
)

// initOverlay initializes the overlay and sets the base ruleset.
// Called whenever role/rules are rebuilt.
func (a *MainAgent) initOverlay() {
	if a.overlay == nil {
		a.overlay = permission.NewOverlay()
	}

	a.stateMu.RLock()
	cfg := a.activeConfig
	projectRoot := a.projectRoot
	a.stateMu.RUnlock()

	var base permission.Ruleset
	roleName := ""
	if cfg != nil {
		roleName = strings.TrimSpace(cfg.Name)
		if cfg.Permission.Kind != 0 {
			base = permission.ParsePermission(&cfg.Permission)
		}
	}

	projectPath, userGlobalPath := agentPermissionRulePaths(projectRoot, roleName)
	a.overlay.SetActiveRole(roleName)
	a.overlay.SetBase(base)
	a.overlay.SetProjectPath(projectPath)
	a.overlay.SetUserGlobalPath(userGlobalPath)
	warnLegacyPermissionOverlayOnce(projectRoot, roleName, projectPath, userGlobalPath)

	a.stateMu.Lock()
	a.ruleset = a.overlay.MergedRuleset()
	a.stateMu.Unlock()
	a.syncSubAgentOverlay()
}

// Overlay returns the overlay for external access (e.g., from TUI).
func (a *MainAgent) Overlay() *permission.Overlay {
	return a.overlay
}

// AddedOverlayRules returns rules added from the confirm rule picker in this session.
func (a *MainAgent) AddedOverlayRules() []permission.AddedRule {
	if a.overlay == nil {
		return nil
	}
	return a.overlay.AddedRules()
}

// RemoveOverlayAddedRule removes one picker-added rule and refreshes merged ruleset.
func (a *MainAgent) RemoveOverlayAddedRule(index int) error {
	if a.overlay == nil {
		return nil
	}
	added := a.overlay.AddedRules()
	if index < 0 || index >= len(added) {
		return a.overlay.RemoveAddedRule(index)
	}
	rule := added[index]
	if rule.Scope == permission.ScopeProject || rule.Scope == permission.ScopeUserGlobal {
		path := strings.TrimSpace(rule.Path)
		if path == "" {
			return fmt.Errorf("%s agent config path is empty", rule.Scope.String())
		}
		if _, err := config.RemoveAgentPermissionRule(path, rule.Rule); err != nil {
			return err
		}
	}
	if err := a.overlay.RemoveAddedRule(index); err != nil {
		return err
	}
	a.stateMu.Lock()
	a.ruleset = a.overlay.MergedRuleset()
	a.stateMu.Unlock()
	a.syncSubAgentOverlay()
	return nil
}

func agentPermissionRulePaths(projectRoot, roleName string) (projectPath, userGlobalPath string) {
	roleName = strings.TrimSpace(roleName)
	if roleName == "" {
		return "", ""
	}
	if projectRoot = strings.TrimSpace(projectRoot); projectRoot != "" {
		projectPath = filepath.Join(projectRoot, ".chord", "agents", roleName+".yaml")
	}
	if configHome, err := config.ConfigHomeDir(); err == nil && strings.TrimSpace(configHome) != "" {
		userGlobalPath = filepath.Join(configHome, "agents", roleName+".yaml")
	}
	return projectPath, userGlobalPath
}

// legacyPermissionOverlayWarned guards the one-shot warning emitted when stale
// per-role files under the removed permissions overlay directory are still
// present. The map is keyed by the legacy path so each location warns at most
// once per process.
var legacyPermissionOverlayWarned sync.Map

// warnLegacyPermissionOverlayOnce inspects the locations that previously held
// per-role overlay rules and emits a single warning per location so users
// upgrading from before the permissions/ → agents/ move discover stale files
// instead of being silently ignored.
func warnLegacyPermissionOverlayOnce(projectRoot, roleName, projectAgentPath, userGlobalAgentPath string) {
	roleName = strings.TrimSpace(roleName)
	if roleName == "" {
		return
	}
	if projectRoot = strings.TrimSpace(projectRoot); projectRoot != "" {
		legacy := filepath.Join(projectRoot, ".chord", "permissions", roleName+".yaml")
		warnLegacyPermissionPathIfPresent(legacy, projectAgentPath, "project")
	}
	if home, err := os.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" {
		legacy := filepath.Join(home, ".chord", "permissions", roleName+".yaml")
		warnLegacyPermissionPathIfPresent(legacy, userGlobalAgentPath, "user-global")
	}
}

func warnLegacyPermissionPathIfPresent(legacyPath, currentPath, scope string) {
	if strings.TrimSpace(legacyPath) == "" {
		return
	}
	if _, err := os.Stat(legacyPath); err != nil {
		return
	}
	if _, loaded := legacyPermissionOverlayWarned.LoadOrStore(legacyPath, struct{}{}); loaded {
		return
	}
	log.Warnf("ignoring legacy %s permission overlay file %q; rules now live in %q — merge any remembered rules manually", scope, legacyPath, currentPath)
}
