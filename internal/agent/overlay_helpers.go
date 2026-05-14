package agent

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/permission"
	"github.com/keakon/chord/internal/tools"
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
		if shouldAutoAllowQuestionFollowUp(cfg, base) {
			base = append(append(permission.Ruleset(nil), base...), permission.Rule{
				Permission: tools.NameQuestion,
				Pattern:    "*",
				Action:     permission.ActionAllow,
			})
		}
	}

	projectPath, userGlobalPath := overlayPersistentRulePaths(projectRoot, roleName)
	a.overlay.SetActiveRole(roleName)
	a.overlay.SetBase(base)
	a.overlay.SetProjectPath(projectPath)
	a.overlay.SetUserGlobalPath(userGlobalPath)
	if err := a.overlay.LoadPersistentOverlays(); err != nil {
		log.Warnf("failed to load permission overlays role=%v err=%v", roleName, err)
	}

	a.stateMu.Lock()
	a.ruleset = a.overlay.MergedRuleset()
	a.stateMu.Unlock()
	a.syncSubAgentOverlay()
}

func shouldAutoAllowQuestionFollowUp(cfg *config.AgentConfig, base permission.Ruleset) bool {
	if cfg == nil || !cfg.QuestionFollowUpAtEnd {
		return false
	}
	for _, rule := range base {
		if rule.Permission != tools.NameQuestion {
			continue
		}
		if rule.Action == permission.ActionDeny {
			return false
		}
		return false
	}
	return true
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
	if err := a.overlay.RemoveAddedRule(index); err != nil {
		return err
	}
	a.stateMu.Lock()
	a.ruleset = a.overlay.MergedRuleset()
	a.stateMu.Unlock()
	a.syncSubAgentOverlay()
	return nil
}

func overlayPersistentRulePaths(projectRoot, roleName string) (projectPath, userGlobalPath string) {
	roleName = strings.TrimSpace(roleName)
	if roleName == "" {
		return "", ""
	}
	if projectRoot = strings.TrimSpace(projectRoot); projectRoot != "" {
		projectPath = filepath.Join(projectRoot, ".chord", "permissions", roleName+".yaml")
	}
	homeDir, err := os.UserHomeDir()
	if err == nil && strings.TrimSpace(homeDir) != "" {
		userGlobalPath = filepath.Join(homeDir, ".chord", "permissions", roleName+".yaml")
	}
	return projectPath, userGlobalPath
}
