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
// ownerRole is the agent role the rule belongs to: the MainAgent's active
// role when the MainAgent triggered the confirm, or the SubAgent's own agent
// definition name when a SubAgent did.
func (a *MainAgent) processRuleIntent(toolName string, intent *ConfirmRuleIntent, ownerRole string) {
	if intent == nil {
		return
	}
	for _, pattern := range intent.Patterns {
		pattern = strings.TrimSpace(pattern)
		if pattern == "" {
			continue
		}
		a.addPermissionRule(permission.Rule{Permission: toolName, Pattern: pattern, Action: permission.ActionAllow}, permission.RuleScope(intent.Scope), ownerRole)
	}
}

// AddOverlayRule adds a permission rule from the /rules UI and refreshes runtime state.
// Rules added here belong to the MainAgent's currently active role.
func (a *MainAgent) AddOverlayRule(rule permission.Rule, scope permission.RuleScope) error {
	return a.addPermissionRule(rule, scope, a.currentAgentName())
}

func (a *MainAgent) addPermissionRule(rule permission.Rule, scope permission.RuleScope, ownerRole string) error {
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

	// The rule is archived under (and session-bucketed for) the role that
	// triggered it: the MainAgent's active role for main-agent confirmations,
	// or the SubAgent's own agent definition for sub-agent confirmations.
	ownerRole = strings.TrimSpace(ownerRole)
	if ownerRole == "" {
		ownerRole = "builder"
	}
	projectPath, userGlobalPath := agentPermissionRulePaths(a.projectRoot, ownerRole)

	var err error
	scopePath := ""
	switch scope {
	case permission.ScopeSession:
		a.overlay.AddSessionRule(ownerRole, rule)
	case permission.ScopeProject:
		scopePath = projectPath
	case permission.ScopeUserGlobal:
		scopePath = userGlobalPath
	default:
		err = fmt.Errorf("unknown rule scope %d", scope)
	}
	if err == nil && (scope == permission.ScopeProject || scope == permission.ScopeUserGlobal) {
		if scopePath == "" {
			err = fmt.Errorf("%s agent config path is empty", scope.String())
		} else if _, err = config.UpsertAgentPermissionRuleForAgent(scopePath, a.snapshotAgentConfigByName(ownerRole), rule); err == nil {
			err = a.overlay.AddPersistentRule(ownerRole, rule, scope, scopePath)
		}
	}
	if err != nil {
		log.Warnf("failed to add permission overlay rule tool=%v pattern=%v action=%v scope=%v role=%v err=%v", rule.Permission, rule.Pattern, rule.Action, scope.String(), ownerRole, err)
		if a.outputCh != nil {
			a.emitToTUI(ToastEvent{
				Message: fmt.Sprintf("Failed to add rule: %v", err),
				Level:   "error",
			})
		}
		return err
	}

	// Update the merged ruleset
	a.applyOverlayRulesetChange()

	scopeText := scope.String()
	if scopePath != "" {
		scopeText = scope.String() + " — " + scopePath
	}
	if a.outputCh != nil {
		a.emitToTUI(ToastEvent{
			Message: fmt.Sprintf("Rule added: %s %s %q · %s — /rules to undo", rule.Permission, rule.Action, rule.Pattern, scopeText),
			Level:   "info",
		})
	}
	return nil
}

// snapshotAgentConfigByName returns a defensive copy of the agent config for
// roleName: the active config when it matches, otherwise the entry from the
// agent config registry. A nil result means the role has no config document.
func (a *MainAgent) snapshotAgentConfigByName(roleName string) *config.AgentConfig {
	roleName = strings.TrimSpace(roleName)
	a.stateMu.RLock()
	defer a.stateMu.RUnlock()
	var src *config.AgentConfig
	if a.activeConfig != nil && strings.TrimSpace(a.activeConfig.Name) == roleName {
		src = a.activeConfig
	} else if roleName != "" {
		src = a.agentConfigs[roleName]
	}
	if src == nil {
		return nil
	}
	cfg := *src
	cfg.ModelPools = append([]string(nil), src.ModelPools...)
	cfg.Capabilities = append([]string(nil), src.Capabilities...)
	cfg.PreferredTasks = append([]string(nil), src.PreferredTasks...)
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
			current.setRuleset(update.ruleset)
		}
	}
}
