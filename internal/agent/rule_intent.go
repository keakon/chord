package agent

import (
	"fmt"
	"log/slog"
	"strings"

	"github.com/keakon/chord/internal/permission"
)

// processRuleIntent handles adding a permission rule when the user
// confirms and also selects to add a rule from the picker.
func (a *MainAgent) processRuleIntent(toolName string, intent *ConfirmRuleIntent) {
	if intent == nil {
		return
	}
	if a.overlay == nil {
		a.initOverlay()
		if a.overlay == nil {
			return
		}
	}
	intent.Pattern = strings.TrimSpace(intent.Pattern)
	if intent.Pattern == "" {
		return
	}
	if strings.EqualFold(toolName, "Delete") {
		return // never persist always-allow for Delete
	}

	// Get the role name for tracking
	a.stateMu.RLock()
	roleName := ""
	if a.activeConfig != nil {
		roleName = strings.TrimSpace(a.activeConfig.Name)
	}
	a.stateMu.RUnlock()
	if roleName == "" {
		roleName = "builder"
	}

	rule := permission.Rule{
		Permission: toolName,
		Pattern:    intent.Pattern,
		Action:     permission.ActionAllow,
	}
	scope := permission.RuleScope(intent.Scope)

	var err error
	switch scope {
	case permission.ScopeSession:
		a.overlay.AddSessionRule(roleName, rule)
	case permission.ScopeProject:
		err = a.overlay.AddProjectRule(roleName, rule)
	case permission.ScopeUserGlobal:
		err = a.overlay.AddUserGlobalRule(roleName, rule)
	default:
		err = fmt.Errorf("unknown rule scope %d", intent.Scope)
	}
	if err != nil {
		slog.Warn("failed to add permission overlay rule",
			"tool", toolName,
			"pattern", intent.Pattern,
			"scope", scope.String(),
			"err", err,
		)
		if a.outputCh != nil {
			a.emitToTUI(ToastEvent{
				Message: fmt.Sprintf("Failed to add rule: %v", err),
				Level:   "error",
			})
		}
		return
	}

	// Update the merged ruleset
	a.stateMu.Lock()
	a.ruleset = a.overlay.MergedRuleset()
	a.stateMu.Unlock()

	// For SubAgents, sync their overlay too
	a.syncSubAgentOverlay()

	scopeText := scope.String()
	switch scope {
	case permission.ScopeProject:
		if p := strings.TrimSpace(a.overlay.ProjectPath()); p != "" {
			scopeText = "project — " + p
		}
	case permission.ScopeUserGlobal:
		if p := strings.TrimSpace(a.overlay.UserGlobalPath()); p != "" {
			scopeText = "user-global — " + p
		}
	}
	if a.outputCh != nil {
		a.emitToTUI(ToastEvent{
			Message: fmt.Sprintf("Rule added: %s %q · %s — /rules to undo", toolName, intent.Pattern, scopeText),
			Level:   "info",
		})
	}
}

// syncSubAgentOverlay propagates the overlay changes to all SubAgents.
func (a *MainAgent) syncSubAgentOverlay() {
	if a.overlay == nil {
		return
	}
	merged := a.overlay.MergedRuleset()
	a.mu.RLock()
	defer a.mu.RUnlock()
	for _, sub := range a.subAgents {
		sub.ruleset = merged
	}
}
