package agent

import (
	"fmt"
	"strings"
)

func (a *MainAgent) primaryAgentCoordinationPromptBlock() string {
	blocks := make([]string, 0, 3)
	// bugTriagePromptBlock is delivered as a per-turn overlay, not part of the
	// stable system prompt. See docs/architecture/prompt-and-context-engineering.md §5.
	if block := strings.TrimSpace(a.todoWorkflowPromptBlock()); block != "" {
		blocks = append(blocks, block)
	}
	if block := strings.TrimSpace(a.loopWorkflowPromptBlock()); block != "" {
		blocks = append(blocks, block)
	}
	if block := a.subAgentWorkflowPromptBlock(); block != "" {
		blocks = append(blocks, block)
	}
	if len(blocks) == 0 {
		return ""
	}
	return strings.Join(blocks, "\n\n")
}

func (a *MainAgent) loopWorkflowPromptBlock() string {
	if !a.loopState.Enabled {
		return ""
	}
	completionClause := "- A task is complete only when all requested work is finished"
	syncClause := ""
	if a.hasActiveSubAgents() {
		completionClause += ", no active subagents remain"
		var sb strings.Builder
		sb.WriteString("- Active subagents:\n")
		for _, line := range a.activeSubAgentContinuationLines() {
			sb.WriteString("  " + line + "\n")
		}
		sb.WriteString("- No active subagents must remain before completion.\n")
		syncClause = sb.String()
	}
	if a.hasOpenTodos() {
		completionClause += ", no open TODO items remain"
		var sb strings.Builder
		sb.WriteString("- Open TODO items:\n")
		for _, line := range a.openTodoContinuationLines() {
			sb.WriteString("  " + line + "\n")
		}
		if a.hasTodoWriteAccess() {
			sb.WriteString("- Mark every remaining open TODO item completed or cancelled with TodoWrite before finishing.\n")
		} else {
			sb.WriteString("- TodoWrite is not available in this role; finish the remaining work described in the above TODO items.\n")
		}
		syncClause += sb.String()
	}
	completionClause += ", and required verification is done or explicitly reported as not run.\n"
	return "## Loop Mode\n" +
		"- Loop mode is active. Keep driving toward the current loop target until it is completed, blocked, max iterations are reached, or it is explicitly disabled.\n" +
		completionClause +
		"- When completion requirements are satisfied, explicitly mark completion in your final response: clearly state the task is complete, summarize completed work, report verification status, and list any remaining limitations or unverified areas.\n" +
		"- If verification is not run, include <verify-not-run>reason</verify-not-run> in the terminal response.\n" +
		"- If the task is genuinely blocked, stop with <blocked>category: reason</blocked> using category in {credential_or_permission_missing, dependency_unavailable, required_input_missing, workspace_conflict, user_decision_required}.\n" +
		"- Do not mark completion merely because you produced a summary or reached a natural stopping point in reasoning.\n" +
		"- Default to making ordinary engineering decisions yourself. Do not stop to ask the user to choose between normal implementation options.\n" +
		a.loopUserConfirmationInstructionLine() + "\n" +
		"- A regular assistant response is not the end of the task. If work remains and no true blocker exists, continue.\n" +
		"- Only stop for missing external information, missing credentials or permissions, or genuinely high-risk irreversible actions.\n" +
		syncClause
}

func (a *MainAgent) subAgentWorkflowPromptBlock() string {
	if !a.hasDelegateWorkflowAccess() {
		return ""
	}
	agents := a.availableSubAgentsForPrompt()
	var sb strings.Builder
	sb.WriteString("## Available Agent Types (for Delegate tool)\n")
	for _, ac := range agents {
		desc := ac.Description
		if desc == "" {
			desc = "(no description)"
		}
		meta := make([]string, 0, 4)
		if len(ac.Capabilities) > 0 {
			meta = append(meta, "capabilities="+strings.Join(ac.Capabilities, ","))
		}
		if len(ac.PreferredTasks) > 0 {
			meta = append(meta, "preferred="+strings.Join(ac.PreferredTasks, ","))
		}
		if strings.TrimSpace(ac.WriteMode) != "" {
			meta = append(meta, "write_mode="+strings.TrimSpace(ac.WriteMode))
		}
		if strings.TrimSpace(ac.DelegationPolicy) != "" {
			meta = append(meta, "delegation_policy="+strings.TrimSpace(ac.DelegationPolicy))
		}
		if len(meta) > 0 {
			desc += " [" + strings.Join(meta, "; ") + "]"
		}
		fmt.Fprintf(&sb, "- **%s**: %s\n", ac.Name, desc)
	}
	sb.WriteString("\n## SubAgent Workflow\n")
	sb.WriteString("- The Delegate tool call returns immediately with a SubAgent instance ID.\n")
	sb.WriteString("- MainAgent receives SubAgent progress and completion updates automatically through the runtime coordination flow.\n")
	sb.WriteString("- Do NOT poll or retrieve SubAgent results with Spawn/SpawnStop — they are delivered asynchronously.\n")
	sb.WriteString("- For the same deliverable's follow-up, clarification, rework, added tests, added verification, or acceptance work, prefer Notify on the existing task instead of creating a new delegate.\n")
	sb.WriteString("- For a genuinely new objective with low overlap and a separately trackable result, prefer a new Delegate instead of overloading an existing worker.\n")
	sb.WriteString("- If continuity is stronger than independence, continue the existing task; if independence is stronger than continuity, create a new delegate.\n")
	sb.WriteString("- For implementation tasks, first dispatch all currently independent tasks whose write scopes are clearly disjoint.\n")
	sb.WriteString("- Dispatch tasks in parallel only when their write scopes are clearly independent; do not run parallel SubAgents that may edit the same file or tightly coupled targets.\n")
	sb.WriteString("- After dispatching the current independent implementation tasks, if there is no new independent task to send, stop doing implementation work in MainAgent and wait for runtime coordination to deliver the next decision point.\n")
	sb.WriteString("- Until you receive Escalate, Complete, or a clear error/blocked signal, do not take over implementation just because a SubAgent is briefly quiet, has not written files yet, or has not produced immediate visible output.\n")
	sb.WriteString("- You may dispatch multiple SubAgents in parallel or continue working on other non-implementation tasks while they run.\n")
	return sb.String()
}

func (a *MainAgent) hasTodoWriteAccess() bool {
	if a.tools == nil {
		return false
	}
	if _, ok := a.tools.Get("TodoWrite"); !ok {
		return false
	}
	ruleset := a.effectiveRuleset()
	if len(ruleset) == 0 {
		return true
	}
	return !ruleset.IsDisabled("TodoWrite")
}

func (a *MainAgent) hasDelegateAccess() bool {
	if a.tools == nil {
		return false
	}
	if _, ok := a.tools.Get("Delegate"); !ok {
		return false
	}
	ruleset := a.effectiveRuleset()
	if len(ruleset) == 0 {
		return true
	}
	return !ruleset.IsDisabled("Delegate")
}

func (a *MainAgent) hasDelegateWorkflowAccess() bool {
	if !a.hasDelegateAccess() {
		return false
	}
	visible := a.mainLLMVisibleToolNames()
	if len(visible) == 0 {
		return false
	}
	if _, ok := visible["Delegate"]; !ok {
		return false
	}
	return len(a.availableSubAgentsForPrompt()) > 0
}

func (a *MainAgent) todoWorkflowPromptBlock() string {
	if !a.hasTodoWriteAccess() {
		return ""
	}
	return "## Todo workflow\n" +
		"- For multi-step investigations or bug triage, TodoWrite may be used as a checklist when the task benefits from explicit step tracking.\n" +
		"- If you use TodoWrite, keep it aligned with real progress; before your final response, sync it (all completed or cancelled).\n" +
		"- Keep at most one todo item in_progress at a time.\n" +
		"- Do not finish with pending/in_progress items unless you say what is left and why.\n" +
		"- Before the final message: verify the outcome; if you used TodoWrite, update it first.\n"
}

func (a *MainAgent) executionStartInstruction() string {
	if a.hasTodoWriteAccess() {
		return "then execute the plan using the visible tools and coordination mechanisms available in this role. Initialise todos with TodoWrite, begin with tasks that have no unmet dependencies, and keep the todo list aligned with real progress."
	}
	return "then execute the plan using the visible tools and coordination mechanisms available in this role, beginning with tasks that have no unmet dependencies."
}

func (a *MainAgent) executionPacingInstruction() string {
	return "For independent tasks, use a pragmatic execution order. If this role exposes safe coordination or parallelism mechanisms, you may use them, but do not assume hidden workers or unavailable capabilities."
}
