package agent

import (
	"fmt"
	"strings"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/permission"
)

// processRuleIntent handles adding a permission rule when the user
// confirms and also selects to add a rule from the picker.
func (a *MainAgent) processRuleIntent(toolName string, intent *ConfirmRuleIntent) {
	if intent == nil {
		return
	}
	for _, pattern := range intent.Patterns {
		pattern = strings.TrimSpace(pattern)
		if pattern == "" {
			continue
		}
		a.addPermissionRule(permission.Rule{Permission: toolName, Pattern: pattern, Action: permission.ActionAllow}, permission.RuleScope(intent.Scope))
	}
}

// AddOverlayRule adds a permission rule from the /rules UI and refreshes runtime state.
func (a *MainAgent) AddOverlayRule(rule permission.Rule, scope permission.RuleScope) error {
	return a.addPermissionRule(rule, scope)
}

func (a *MainAgent) addPermissionRule(rule permission.Rule, scope permission.RuleScope) error {
	if a.overlay == nil {
		a.initOverlay()
		if a.overlay == nil {
			return fmt.Errorf("permission overlay unavailable")
		}
	}
	rule.Permission = strings.TrimSpace(rule.Permission)
	rule.Pattern = strings.TrimSpace(rule.Pattern)
	if rule.Permission == "" {
		return fmt.Errorf("permission tool is required")
	}
	if rule.Pattern == "" {
		return fmt.Errorf("permission pattern is required")
	}
	if rule.Action == "" {
		rule.Action = permission.ActionAllow
	}
	switch rule.Action {
	case permission.ActionAllow, permission.ActionAsk, permission.ActionDeny:
	default:
		return fmt.Errorf("unsupported permission action %q", rule.Action)
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

	var err error
	scopePath := ""
	switch scope {
	case permission.ScopeSession:
		a.overlay.AddSessionRule(roleName, rule)
	case permission.ScopeProject:
		scopePath = strings.TrimSpace(a.overlay.ProjectPath())
	case permission.ScopeUserGlobal:
		scopePath = strings.TrimSpace(a.overlay.UserGlobalPath())
	default:
		err = fmt.Errorf("unknown rule scope %d", scope)
	}
	if err == nil && (scope == permission.ScopeProject || scope == permission.ScopeUserGlobal) {
		if scopePath == "" {
			err = fmt.Errorf("%s agent config path is empty", scope.String())
		} else if _, err = config.UpsertAgentPermissionRuleForAgent(scopePath, a.activeConfigSnapshot(), rule); err == nil {
			err = a.overlay.AddPersistentRule(roleName, rule, scope, scopePath)
		}
	}
	if err != nil {
		log.Warnf("failed to add permission overlay rule tool=%v pattern=%v action=%v scope=%v err=%v", rule.Permission, rule.Pattern, rule.Action, scope.String(), err)
		if a.outputCh != nil {
			a.emitToTUI(ToastEvent{
				Message: fmt.Sprintf("Failed to add rule: %v", err),
				Level:   "error",
			})
		}
		return err
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
			Message: fmt.Sprintf("Rule added: %s %s %q · %s — /rules to undo", rule.Permission, rule.Action, rule.Pattern, scopeText),
			Level:   "info",
		})
	}
	return nil
}

func (a *MainAgent) activeConfigSnapshot() *config.AgentConfig {
	a.stateMu.RLock()
	defer a.stateMu.RUnlock()
	if a.activeConfig == nil {
		return nil
	}
	cfg := *a.activeConfig
	cfg.ModelPools = append([]string(nil), a.activeConfig.ModelPools...)
	cfg.Capabilities = append([]string(nil), a.activeConfig.Capabilities...)
	cfg.PreferredTasks = append([]string(nil), a.activeConfig.PreferredTasks...)
	return &cfg
}

// syncSubAgentOverlay propagates overlay changes to all SubAgents while
// preserving each SubAgent's own agent-definition permission config.
func (a *MainAgent) syncSubAgentOverlay() {
	if a == nil || a.overlay == nil {
		return
	}
	type subAgentRulesetUpdate struct {
		instanceID string
		sub        *SubAgent
		ruleset    permission.Ruleset
	}
	a.subs.mu.RLock()
	updates := make([]subAgentRulesetUpdate, 0, len(a.subs.subAgents))
	for _, sub := range a.subs.subAgents {
		if sub == nil {
			continue
		}
		updates = append(updates, subAgentRulesetUpdate{
			instanceID: sub.instanceID,
			sub:        sub,
			ruleset:    a.buildSubAgentRuleset(a.agentConfigs[sub.agentDefName]),
		})
	}
	a.subs.mu.RUnlock()

	a.subs.mu.Lock()
	defer a.subs.mu.Unlock()
	for _, update := range updates {
		if current := a.subs.subAgents[update.instanceID]; current == update.sub {
			current.ruleset = update.ruleset
		}
	}
}
