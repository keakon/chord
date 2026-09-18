package agent

import (
	"strings"

	"github.com/keakon/chord/internal/tools"
)

func (a *MainAgent) primaryAgentCoordinationPromptBlock() string {
	// bugTriagePromptBlock is delivered as a per-turn overlay, not part of the
	// stable system prompt. Todo usage rules live entirely in the TodoWrite
	// tool description, so no separate todo workflow block is rendered here.
	return a.subAgentWorkflowPromptBlock()
}

// delegationStrategyPromptLines is the delegation strategy shared by every
// agent that can delegate: the MainAgent renders it inside "## SubAgent
// Workflow" and a SubAgent with nested delegation inside "## Nested
// Delegation", so the Delegate tool description can defer to the surrounding
// prompt in both roles without naming a role-specific section.
func delegationStrategyPromptLines(visible map[string]struct{}) string {
	delegate := toolPromptName(tools.NameDelegate)
	var sb strings.Builder
	sb.WriteString("- Prefer direct calls to available tools when one or a few calls suffice; use ")
	sb.WriteString(delegate)
	sb.WriteString(" for substantial, independent sub-work that benefits from a dedicated agent.\n")
	if hasVisibleTool(visible, tools.NameNotify) {
		sb.WriteString("- For the same deliverable's follow-up, clarification, rework, added tests, added verification, or acceptance work, prefer ")
		sb.WriteString(toolPromptName(tools.NameNotify))
		sb.WriteString(" on the existing task instead of creating a new delegate.\n")
		sb.WriteString("- If continuity is stronger than independence, continue the existing task; if independence is stronger than continuity, create a new delegate.\n")
	} else {
		sb.WriteString("- This role cannot send follow-up messages to existing workers; do not create a duplicate delegate while the same deliverable is still active. Report any coordination limitation clearly.\n")
	}
	if hasVisibleTool(visible, tools.NameCancel) {
		sb.WriteString("- Use ")
		sb.WriteString(toolPromptName(tools.NameCancel))
		sb.WriteString(" with the existing task_id when a delegated task should stop.\n")
	}
	sb.WriteString("- For a genuinely new objective with low overlap and a separately trackable result, prefer a new ")
	sb.WriteString(delegate)
	sb.WriteString(" instead of overloading an existing worker.\n")
	sb.WriteString("- Dispatch tasks in parallel only when their write scopes are clearly independent; do not run parallel SubAgents that may edit the same file or tightly coupled targets.\n")
	return sb.String()
}

func (a *MainAgent) subAgentWorkflowPromptBlock() string {
	if !a.hasDelegateWorkflowAccess() {
		return ""
	}
	delegate := toolPromptName(tools.NameDelegate)
	var sb strings.Builder
	// The delegate-able role catalogue (name, description, empty-scope rule)
	// is rendered once by the Delegate tool's agent_type parameter
	// description, which ships with the tool schema on every request where
	// delegation is visible; it is not duplicated as a prompt list here.
	sb.WriteString("## SubAgent Workflow\n")
	sb.WriteString("- The ")
	sb.WriteString(delegate)
	sb.WriteString(" tool call returns immediately; MainAgent receives SubAgent progress and completion updates automatically through the runtime coordination flow (see the ")
	sb.WriteString(delegate)
	sb.WriteString(" tool description for its call semantics).\n")
	sb.WriteString(delegationStrategyPromptLines(a.mainLLMVisibleToolNames()))
	if a.compactContextVisible() {
		// The compact_context tool exists only when it is visible and
		// executable; without it this guidance has no referent (a denied or
		// invisible tool must never be pushed onto the model as an option).
		sb.WriteString("- For sub-tasks that can be described and executed independently with results the main thread can consume, prefer ")
		sb.WriteString(delegate)
		sb.WriteString(" (SubAgent): the SubAgent runs in a fresh window and only the final result reaches the main thread, so its intermediate tool output never pollutes the main context. Use ")
		sb.WriteString(toolPromptName(tools.NameCompactContext))
		sb.WriteString(" when the main thread itself must keep reasoning and carrying its history costs more than restoring externalized state; the current task need not be complete.\n")
	}
	sb.WriteString("- For implementation tasks, first dispatch all currently independent tasks whose write scopes are clearly disjoint.\n")
	sb.WriteString("- After dispatching the current independent implementation tasks, if there is no new independent task to send, stop doing implementation work in MainAgent and wait for runtime coordination to deliver the next decision point.\n")
	sb.WriteString("- Until you receive an escalation, a completion, or a clear error/blocked signal from a worker, do not take over implementation just because a SubAgent is briefly quiet, has not written files yet, or has not produced immediate visible output.\n")
	sb.WriteString("- You may dispatch multiple SubAgents in parallel or continue working on other non-implementation tasks while they run.\n")
	return sb.String()
}

func (a *MainAgent) hasTodoWriteAccess() bool {
	if a.tools == nil {
		return false
	}
	if _, ok := a.tools.Get(tools.NameTodoWrite); !ok {
		return false
	}
	ruleset := a.effectiveRuleset()
	if len(ruleset) == 0 {
		return true
	}
	return !ruleset.IsDisabled(tools.NameTodoWrite)
}

func (a *MainAgent) hasDelegateAccess() bool {
	if a.tools == nil {
		return false
	}
	if _, ok := a.tools.Get(tools.NameDelegate); !ok {
		return false
	}
	ruleset := a.effectiveRuleset()
	if len(ruleset) == 0 {
		return true
	}
	return !ruleset.IsDisabled(tools.NameDelegate)
}

func (a *MainAgent) hasDelegateWorkflowAccess() bool {
	if !a.hasDelegateAccess() {
		return false
	}
	visible := a.mainLLMVisibleToolNames()
	if len(visible) == 0 {
		return false
	}
	if _, ok := visible[tools.NameDelegate]; !ok {
		return false
	}
	return len(a.availableSubAgentsForPrompt()) > 0
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
