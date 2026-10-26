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
	a.processRuleIntentWithResult(toolName, intent, ownerRole)
}

func (a *MainAgent) processRuleIntentWithResult(toolName string, intent *ConfirmRuleIntent, ownerRole string) []ConfirmRuleIntentResult {
	if intent == nil {
		return nil
	}
	results := make([]ConfirmRuleIntentResult, 0, len(intent.Rules))
	for _, rule := range ruleIntentPermissions(toolName, intent) {
		err := a.addPermissionRule(rule, permission.RuleScope(intent.Scope), ownerRole)
		result := ConfirmRuleIntentResult{Tool: rule.Permission, Pattern: rule.Pattern}
		if err != nil {
			result.Error = err.Error()
		}
		results = append(results, result)
	}
	return results
}

// ruleIntentPermissions resolves the rules an intent installs. A rule without
// a tool inherits the confirmed tool; a rule that names one installs for that
// tool, which is how an apply_patch confirmation can relax the delete rule
// that blocked a moved source path.
func ruleIntentPermissions(toolName string, intent *ConfirmRuleIntent) []permission.Rule {
	if intent == nil {
		return nil
	}
	fallback := strings.TrimSpace(toolName)
	rules := make([]permission.Rule, 0, len(intent.Rules))
	for _, want := range intent.Rules {
		pattern := strings.TrimSpace(want.Pattern)
		if pattern == "" {
			continue
		}
		tool := strings.TrimSpace(want.Tool)
		if tool == "" {
			tool = fallback
		}
		if tool == "" {
			continue
		}
		rules = append(rules, permission.Rule{Permission: tool, Pattern: pattern, Action: permission.ActionAllow})
	}
	return rules
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
	projectPath, userGlobalPath := agentPermissionRulePaths(a.contentRoot, ownerRole)

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
	return &cfg
}

// syncSubAgentOverlay propagates overlay changes to all SubAgents while
// preserving each SubAgent's own agent-definition permission config.
func (a *MainAgent) syncSubAgentOverlay() {
	if a == nil || a.overlay == nil {
		return
	}
	type runtimeRulesetUpdate struct {
		instanceID string
		runtime    subAgentRuntime
		ruleset    permission.Ruleset
	}
	a.subs.mu.RLock()
	updates := make([]runtimeRulesetUpdate, 0, a.subs.liveCountLocked())
	for _, rt := range a.subs.snapshotRuntimesLocked() {
		switch runtime := rt.(type) {
		case *SubAgent:
			if runtime == nil {
				continue
			}
			updates = append(updates, runtimeRulesetUpdate{
				instanceID: runtime.instanceID,
				runtime:    runtime,
				ruleset:    a.buildSubAgentRuleset(a.agentConfigs[runtime.agentDefName]),
			})
		case *externalWorker:
			if runtime == nil {
				continue
			}
			def := runtime.agentDef
			if current := a.agentConfigs[runtime.agentDefName]; current != nil {
				def = current
			}
			updates = append(updates, runtimeRulesetUpdate{
				instanceID: runtime.instanceID,
				runtime:    runtime,
				ruleset:    a.buildSubAgentRuleset(def),
			})
		}
	}
	a.subs.mu.RUnlock()

	a.subs.mu.Lock()
	defer a.subs.mu.Unlock()
	for _, update := range updates {
		if current := a.subs.runtimeLocked(update.instanceID); current == update.runtime {
			switch runtime := current.(type) {
			case *SubAgent:
				runtime.setRuleset(update.ruleset)
			case *externalWorker:
				runtime.setRuleset(update.ruleset)
			}
		}
	}
}
