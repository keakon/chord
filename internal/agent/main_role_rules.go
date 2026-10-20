package agent

import (
	"fmt"
	"strings"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/permission"
)

func (a *MainAgent) currentActiveConfig() *config.AgentConfig {
	a.stateMu.RLock()
	defer a.stateMu.RUnlock()
	return a.activeConfig
}

// snapshotRuleset returns a defensive copy of the main agent's merged ruleset,
// without any YOLO-mode filtering. Use this when the caller needs the same
// rules the user configured, regardless of the temporary YOLO bypass.
func (a *MainAgent) snapshotRuleset() permission.Ruleset {
	a.stateMu.RLock()
	defer a.stateMu.RUnlock()
	if len(a.ruleset) == 0 {
		return nil
	}
	return append(permission.Ruleset(nil), a.ruleset...)
}

// effectiveRuleset returns the ruleset that should drive the main agent's own
// LLM-facing surface (system prompt, tool visibility) and rule evaluation.
// Under YOLO it returns the yoloRuleset view of the user's rules: unprotected
// rules drop out (those tools bypass), while delegate/handoff/cancel/done/
// compact_context rules survive together with the mechanism tools' mirrored
// defaults, so the visible surface matches what the execution gate still
// decides. SubAgents must use subAgentBaseRuleset instead.
func (a *MainAgent) effectiveRuleset() permission.Ruleset {
	ruleset := a.snapshotRuleset()
	if a.yoloEnabled.Load() {
		return yoloRuleset(ruleset)
	}
	return ruleset
}

// subAgentBaseRuleset returns the unfiltered ruleset SubAgents should inherit
// when they are created or refreshed. SubAgents evaluate the user's full rule
// set; the only YOLO effect they inherit is the ask→allow downgrade the
// SubAgent execution pipeline applies while the main agent's YOLO mode is on.
func (a *MainAgent) subAgentBaseRuleset() permission.Ruleset {
	return a.snapshotRuleset()
}

// buildSubAgentRuleset returns the ruleset a freshly created or restored
// SubAgent should evaluate tool permissions against: the unfiltered main-agent
// ruleset merged with the SubAgent's own session-rule bucket (so rules the
// SubAgent triggered persist across MainAgent role switches) and the
// SubAgent's own agent-definition permission config.
func (a *MainAgent) buildSubAgentRuleset(agentDef *config.AgentConfig) permission.Ruleset {
	ruleset := a.subAgentBaseRuleset()
	if agentDef != nil {
		if role := strings.TrimSpace(agentDef.Name); role != "" && a.overlay != nil {
			if session := a.overlay.SessionRulesForRole(role); len(session) > 0 {
				ruleset = permission.Merge(ruleset, session)
			}
		}
		if agentDef.Permission.Kind != 0 {
			ruleset = permission.Merge(ruleset, permission.ParsePermission(&agentDef.Permission))
		}
	}
	return ruleset
}

// switchRole changes the MainAgent's active role.
// If clearHistory is true, the conversation history is wiped (used for
// Handoff-triggered switches where the new role starts fresh).
// If clearHistory is false, history is preserved (used for user-initiated
// Tab cycling where the context should carry over).
func (a *MainAgent) switchRole(roleName string, clearHistory bool) error {
	cfg, ok := a.agentConfigs[roleName]
	if !ok {
		return fmt.Errorf("unknown role %q", roleName)
	}
	if cfg == nil || cfg.IsSubAgent() || cfg.IsExternalBackend() {
		return fmt.Errorf("role %q is not available", roleName)
	}

	// Resolve and validate the role's model before committing any role state,
	// so a model failure leaves the agent fully unchanged (role, history, and
	// model policy) instead of half-switched with an error. The prepared model
	// is installed only after the role state is committed below; installing
	// cannot fail, so the role and its model always change together.
	var prepared *preparedMainModel
	if nextRef := a.defaultRoleModelRef(cfg); nextRef != "" {
		var err error
		prepared, err = a.prepareMainModelForRole(nextRef, cfg)
		if err != nil {
			return fmt.Errorf("apply role %q model %q: %w", roleName, nextRef, err)
		}
	}

	a.stateMu.Lock()
	a.activeConfig = cfg
	a.stateMu.Unlock()
	a.clearSystemPromptOverride()

	// Rebuild permissions from active agent config.
	a.rebuildRuleset()

	// Prompt and tool visibility depend on the role's permissions. Rebuild them
	// together at the next request boundary so the model sees one coherent surface.
	a.markRuntimeSurfaceDirty()
	a.NotifyEnvStatusUpdated()

	if clearHistory {
		// Clear conversation history so the new role starts fresh.
		if r := a.directoryInstructions.Swap(nil); r != nil {
			r.cancel()
		}
		a.directoryPreparation = nil
		a.pendingNativeContextAppends = nil
		a.ctxMgr.RestoreMessages(nil)
		a.installContextNoticePresence(nil)
		a.clearEvidenceCandidates()
	}

	if prepared != nil {
		a.installPreparedMainModel(prepared)
	}

	// Keep a lazy rebuild fallback when the role has no explicit model list.
	a.mainModelPolicyDirty.Store(prepared == nil)

	log.Infof("switched MainAgent role role=%v clear_history=%v model_ref=%v", roleName, clearHistory, a.ProviderModelRef())
	// Persist the active role immediately so a later resume/startup restore does
	// not fall back to the default builder role.
	a.saveRecoverySnapshot()
	return nil
}

// rebuildRuleset reconstructs the permission ruleset from the active agent
// config. This is called whenever the active role changes.
func (a *MainAgent) rebuildRuleset() {
	a.initOverlay()
}
