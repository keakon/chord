package agent

import (
	"fmt"
	"log/slog"
	"sort"
	"strings"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/message"
)

// SwitchRole switches the MainAgent to the named role and emits RoleChangedEvent.
// Goroutine-safe (posts to eventCh).
func (a *MainAgent) SwitchRole(role string) {
	if err := a.switchRole(role, false); err != nil {
		slog.Warn("SwitchRole failed", "role", role, "error", err)
		return
	}
	a.emitToTUI(RoleChangedEvent{Role: role})
}

// AvailableRoles returns the ordered list of roles the user can cycle through.
// Only primary-mode agents (mode: "primary") are included; subagent-only configs
// are excluded. builder is always first; planner second (if present); custom
// roles after that are sorted alphabetically for deterministic Tab cycling.
func (a *MainAgent) AvailableRoles() []string {
	if a.agentConfigs == nil {
		return []string{"builder"}
	}
	names := make([]string, 0, len(a.agentConfigs))
	for _, name := range []string{"builder", "planner"} {
		if cfg, ok := a.agentConfigs[name]; ok && cfg != nil && !cfg.IsSubAgent() {
			names = append(names, name)
		}
	}
	custom := make([]string, 0, len(a.agentConfigs))
	for name, cfg := range a.agentConfigs {
		if name == "builder" || name == "planner" || cfg == nil || cfg.IsSubAgent() {
			continue
		}
		custom = append(custom, name)
	}
	sort.Strings(custom)
	names = append(names, custom...)
	return names
}

// AvailableAgents returns the names of agent roles available for Handoff selection.
// Only primary-mode agents (mode != "subagent") are eligible, and the current
// active role is excluded so planner cannot hand off to itself. builder remains
// the default when available so the selector always offers an execution agent.
// Non-builder roles are sorted alphabetically for deterministic selection order.
func (a *MainAgent) AvailableAgents() []string {
	if a.agentConfigs == nil {
		return []string{"builder"}
	}

	activeName := ""
	if cfg := a.currentActiveConfig(); cfg != nil {
		activeName = cfg.Name
	}

	names := make([]string, 0, len(a.agentConfigs))
	if cfg, ok := a.agentConfigs["builder"]; ok && cfg != nil && !cfg.IsSubAgent() && cfg.Name != activeName {
		names = append(names, "builder")
	}
	others := make([]string, 0, len(a.agentConfigs))
	for name, cfg := range a.agentConfigs {
		if name == "builder" || cfg == nil {
			continue
		}
		if cfg.IsSubAgent() || name == activeName {
			continue
		}
		others = append(others, name)
	}
	sort.Strings(others)
	names = append(names, others...)

	if len(names) == 0 {
		names = append(names, "builder")
	}
	return names
}

// CurrentRole returns the active role name. Goroutine-safe.
func (a *MainAgent) CurrentRole() string {
	if cfg := a.currentActiveConfig(); cfg != nil {
		return cfg.Name
	}
	return "builder"
}

func (a *MainAgent) ProjectRoot() string {
	return strings.TrimSpace(a.projectRoot)
}

func (a *MainAgent) LoopKeepsMainBusy() bool {
	return a.loopKeepsMainBusy()
}

func (a *MainAgent) CurrentLoopState() LoopState {
	if !a.loopState.Enabled {
		return ""
	}
	return a.loopState.State
}

func (a *MainAgent) CurrentLoopTarget() string {
	if !a.loopState.Enabled {
		return ""
	}
	return a.loopState.Target
}

func (a *MainAgent) CurrentLoopIteration() int {
	if !a.loopState.Enabled {
		return 0
	}
	return a.loopState.Iteration
}

func (a *MainAgent) CurrentLoopMaxIterations() int {
	if !a.loopState.Enabled {
		return 0
	}
	return a.loopState.MaxIterations
}

func (a *MainAgent) emitLoopStateChanged() {
	a.emitToTUI(LoopStateChangedEvent{})
}

func (a *MainAgent) appendLoopNoticeMessage(title, text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	msg := message.Message{Role: "user", Content: title + "\n\n" + text, Kind: "loop_notice"}
	a.ctxMgr.Append(msg)
	a.persistAsync("main", msg)
}

func (a *MainAgent) loopCompletionRequirementLines() []string {
	lines := []string{
		"- All requested work is finished",
		"- Required verification is completed, or explicitly reported as not run",
		"- If verification is not run, include <verify-not-run>single-line reason</verify-not-run> in the terminal response",
		"- The final assistant message should include a <done>reason</done> tag when the task is complete",
		"- If the task is blocked, use <blocked>category: reason</blocked> instead of <done>...</done>",
		a.loopCompletionDecisionRequirementLine(),
	}
	if a.hasActiveSubAgents() {
		lines = append(lines, "- Active subagents must finish before completion:")
		for _, line := range a.activeSubAgentContinuationLines() {
			lines = append(lines, "  "+line)
		}
		lines = append(lines, "- No active subagents remain after all subagent work is done")
	}
	if a.hasOpenTodos() {
		if a.hasTodoWriteAccess() {
			lines = append(lines, "- Mark every remaining open TODO item completed or cancelled with TodoWrite before finishing:")
			for _, line := range a.openTodoContinuationLines() {
				lines = append(lines, "  "+line)
			}
			lines = append(lines, "- No open TODO items remain after that final TodoWrite sync")
		} else {
			lines = append(lines, "- The following open TODO items exist but TodoWrite is not available in this role; finish the remaining work they describe:")
			for _, line := range a.openTodoContinuationLines() {
				lines = append(lines, "  "+line)
			}
			lines = append(lines, "- All work described in the above TODO items is done")
		}
	}
	return lines
}

func (a *MainAgent) loopFinalCompletionResponseLines() []string {
	return []string{
		"- Clearly state that the requested task is complete",
		"- Include a <done>reason</done> line in the final response",
		"- Summarize the completed work",
		"- Report verification status explicitly",
		"- If verification was not run, include <verify-not-run>reason</verify-not-run>",
		"- List any remaining limitations or unverified areas",
	}
}

func (a *MainAgent) sendLoopAnchorFromCommand(target string) {
	target = strings.TrimSpace(target)
	if target == "" {
		return
	}
	sections := []string{
		"Target:",
		"- " + target,
	}
	// Iteration budget.
	maxIter := a.loopState.MaxIterations
	if maxIter > 0 {
		sections = append(sections, fmt.Sprintf("Iteration budget: %d", maxIter))
	} else {
		sections = append(sections, "Iteration budget: unlimited")
	}
	if todoLines := a.openTodoContinuationLines(); len(todoLines) > 0 {
		sections = append(sections, "", "Open TODO items:", strings.Join(todoLines, "\n"))
	}
	if subLines := a.activeSubAgentContinuationLines(); len(subLines) > 0 {
		sections = append(sections, "", "Active subagents:", strings.Join(subLines, "\n"))
	}
	sections = append(sections,
		"",
		"Completion requirements:",
		strings.Join(a.loopCompletionRequirementLines(), "\n"),
		"",
		"Final completion response requirements:",
		strings.Join(a.loopFinalCompletionResponseLines(), "\n"),
	)
	noticeText := strings.Join(sections, "\n")
	a.appendLoopNoticeMessage("LOOP", noticeText)
	a.emitToTUI(LoopNoticeEvent{
		Title:    "LOOP",
		Text:     noticeText,
		DedupKey: "loop-start:" + target,
	})
	if a.turn != nil {
		a.pendingUserMessages = enqueuePendingUserMessage(a.pendingUserMessages, pendingUserMessage{Content: target, FromUser: true})
		return
	}
	a.newTurn()
	turnID := a.turn.ID
	turnCtx := a.turn.Ctx
	userMsg := message.Message{Role: "user", Content: target}
	a.recordCommittedUserMessage(userMsg)
	a.syncBugTriagePromptFromSnapshot()
	a.beginMainLLMAfterPreparation(turnCtx, turnID, "")
}

func (a *MainAgent) EnableLoopMode(target string) {
	a.loopState.enableWithTarget(target)
	if !a.loopState.MaxIterationsSet && a.loopState.MaxIterations == 0 {
		a.loopState.MaxIterations = 10
	}
	if a.loopState.State == "" || a.loopState.State == LoopStateIdle || a.loopState.State == LoopStateCompleted || a.loopState.State == LoopStateBlocked || a.loopState.State == LoopStateBudgetExhausted {
		a.loopState.State = LoopStateExecuting
	}
	a.refreshSystemPrompt()
	a.emitLoopStateChanged()
	msg := fmt.Sprintf("Loop enabled. Max iterations: %d.", a.loopState.MaxIterations)
	if a.loopState.MaxIterationsSet && a.loopState.MaxIterations == 0 {
		msg = "Loop enabled. Max iterations: unlimited."
	}
	a.emitToTUI(InfoEvent{Message: msg})
}

func (a *MainAgent) DisableLoopMode() {
	a.loopState.disable()
	a.refreshSystemPrompt()
	a.emitLoopStateChanged()
	a.emitToTUI(InfoEvent{Message: "Loop disabled."})
}

// CurrentRoleConfig returns the active role configuration. The returned
// config must be treated as read-only by callers.
func (a *MainAgent) CurrentRoleConfig() *config.AgentConfig {
	return a.currentActiveConfig()
}

// CurrentRoleModelRefs returns the configured model chain for the active role.
// Entries preserve the original AgentConfig.Models strings, including any
// inline @variant suffixes.
// A nil/empty slice means "use global default model only".
func (a *MainAgent) CurrentRoleModelRefs() []string {
	cfg := a.currentActiveConfig()
	if cfg == nil || len(cfg.Models) == 0 {
		return nil
	}
	out := make([]string, len(cfg.Models))
	copy(out, cfg.Models)
	return out
}
