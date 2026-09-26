package agent

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

// handleLoopAssessment processes the loop assessment result after an LLM round.
func (a *MainAgent) handleLoopAssessment(evt Event) {
	payload, ok := evt.Payload.(*LoopAssessment)
	if !ok || payload == nil {
		log.Warnf("handleLoopAssessment: invalid payload type payload_type=%v", fmt.Sprintf("%T", evt.Payload))
		return
	}
	if !a.loopState.Enabled {
		return
	}
	a.loopState.LastAssessmentMessage = payload.Message
	a.emitLoopStateChanged()
	switch payload.Action {
	case LoopAssessmentActionContinue:
		a.loopState.State = LoopStateExecuting
		a.emitLoopStateChanged()
		if a.shouldEmitLoopContinuationForAssessment(payload) {
			// A terminal stop has no tool result to attach the note to, so it is
			// persisted as a loop notice; the history already carries it and it
			// must not also ride the next request as an overlay.
			a.emitLoopContinuationNote(a.buildLoopContinuationNote(payload))
		}
		a.emitActivity("main", ActivityExecuting, "loop")
		a.handleContinueFromContext()
	case LoopAssessmentActionCompleted:
		a.loopState.State = LoopStateCompleted
		a.emitLoopStateChanged()
		a.emitToTUI(InfoEvent{Message: strings.TrimSpace(payload.Message)})
		a.loopState.disable()
		a.emitLoopStateChanged()
		a.setIdleAndDrainPending()
	case LoopAssessmentActionBlocked:
		a.loopState.State = LoopStateBlocked
		a.emitLoopStateChanged()
		a.emitToTUI(InfoEvent{Message: strings.TrimSpace(payload.Message)})
		a.loopState.disable()
		a.emitLoopStateChanged()
		a.setIdleAndDrainPending()
	case LoopAssessmentActionBudgetExhausted:
		a.loopState.State = LoopStateBudgetExhausted
		a.emitLoopStateChanged()
		a.emitToTUI(InfoEvent{Message: strings.TrimSpace(payload.Message)})
		a.loopState.disable()
		a.emitLoopStateChanged()
		a.setIdleAndDrainPending()
	default:
		a.loopState.State = LoopStateIdle
		a.emitLoopStateChanged()
		a.loopState.disable()
		a.emitLoopStateChanged()
		a.setIdleAndDrainPending()
	}
}

func (a *MainAgent) shouldEmitLoopContinuationForAssessment(assessment *LoopAssessment) bool {
	if assessment == nil {
		return false
	}
	if !a.loopState.DeferContinuationPromptUntilDone {
		return true
	}
	stopReason := strings.ToLower(strings.TrimSpace(assessment.TriggerStopReason))
	if stopReason == "done" {
		a.loopState.DeferContinuationPromptUntilDone = false
		a.emitLoopStateChanged()
		return true
	}
	return false
}

// loopKeepsMainBusy returns true if the loop controller is in an active state
// that should keep the main agent's "busy" status.
func (a *MainAgent) loopKeepsMainBusy() bool {
	if !a.loopState.Enabled {
		return false
	}
	switch a.loopState.State {
	case LoopStateExecuting, LoopStateAssessing:
		return true
	default:
		return false
	}
}

// hasOpenTodos returns true if any TODO items have pending or in_progress status.
func (a *MainAgent) hasOpenTodos() bool {
	a.todoMu.RLock()
	defer a.todoMu.RUnlock()
	for _, todo := range a.todoItems {
		switch strings.TrimSpace(todo.Status) {
		case "pending", "in_progress":
			return true
		}
	}
	return false
}

func (a *MainAgent) hasActiveSubAgents() bool {
	for _, sub := range a.subs.snapshotSubAgents() {
		if sub == nil {
			continue
		}
		switch sub.State() {
		case SubAgentStateCompleted, SubAgentStateFailed, SubAgentStateCancelled, SubAgentStateIdle:
			continue
		}
		return true
	}
	return false
}

func (a *MainAgent) hasActiveSubAgentWork() bool {
	a.subs.mu.RLock()
	defer a.subs.mu.RUnlock()
	for _, sub := range a.subs.subAgents {
		if sub != nil && (sub.State() == SubAgentStateRunning || sub.hasPendingUserInput()) {
			return true
		}
	}
	return false
}

// Loop blocker categories. A `<blocked>category: reason</blocked>` marker names
// one of these, and the reason text is otherwise matched against them. The
// marker parser, the prompt, and the inference all read the same constants, so
// renaming a category cannot leave the inference naming a category the parser
// no longer accepts.
const (
	loopBlockerCredentialOrPermission = "credential_or_permission_missing"
	loopBlockerDependencyUnavailable  = "dependency_unavailable"
	loopBlockerRequiredInputMissing   = "required_input_missing"
	loopBlockerWorkspaceConflict      = "workspace_conflict"
	loopBlockerUserDecisionRequired   = "user_decision_required"
)

func inferLoopBlockerCategory(reason string) string {
	normalized := strings.ToLower(strings.TrimSpace(reason))
	switch {
	case normalized == "":
		return loopBlockerDependencyUnavailable
	case strings.Contains(normalized, "credential"),
		strings.Contains(normalized, "permission"),
		strings.Contains(normalized, "forbidden"),
		strings.Contains(normalized, "unauthorized"),
		strings.Contains(normalized, "token"),
		strings.Contains(normalized, "denied"):
		return loopBlockerCredentialOrPermission
	case strings.Contains(normalized, "input"),
		strings.Contains(normalized, "sample"),
		strings.Contains(normalized, "capture"),
		strings.Contains(normalized, "replay"),
		strings.Contains(normalized, "fixture"),
		strings.Contains(normalized, "missing data"):
		return loopBlockerRequiredInputMissing
	case strings.Contains(normalized, "conflict"),
		strings.Contains(normalized, "locked"),
		strings.Contains(normalized, "lock"),
		strings.Contains(normalized, "dirty worktree"),
		strings.Contains(normalized, "workspace"):
		return loopBlockerWorkspaceConflict
	case strings.Contains(normalized, "decision"),
		strings.Contains(normalized, "choose"),
		strings.Contains(normalized, "approval"),
		strings.Contains(normalized, "confirm"):
		return loopBlockerUserDecisionRequired
	default:
		return loopBlockerDependencyUnavailable
	}
}

// loopBlockerCategories are the categories a <blocked>category: reason</blocked>
// marker may name; anything else is inferred from the reason text.
var loopBlockerCategories = []string{
	loopBlockerCredentialOrPermission,
	loopBlockerDependencyUnavailable,
	loopBlockerRequiredInputMissing,
	loopBlockerWorkspaceConflict,
	loopBlockerUserDecisionRequired,
}

func parseLoopBlockedReason(raw string) (category, detail string) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return inferLoopBlockerCategory(""), ""
	}
	category = ""
	detail = raw
	if idx := strings.Index(raw, ":"); idx > 0 {
		maybeCategory := strings.TrimSpace(raw[:idx])
		if slices.Contains(loopBlockerCategories, maybeCategory) {
			category = maybeCategory
			detail = strings.TrimSpace(raw[idx+1:])
		}
	}
	if category == "" {
		category = inferLoopBlockerCategory(raw)
	}
	return category, detail
}

func formatLoopBlockedMessage(category, detail string) string {
	category = strings.TrimSpace(category)
	detail = strings.TrimSpace(detail)
	if detail == "" {
		if category == "" {
			return "Loop blocked."
		}
		return "Loop blocked (" + category + ")."
	}
	if category == "" {
		return "Loop blocked: " + detail
	}
	return "Loop blocked (" + category + "): " + detail
}

func (a *MainAgent) loopBlockedAssessment(rawReason string) *LoopAssessment {
	category, detail := parseLoopBlockedReason(rawReason)
	return &LoopAssessment{
		Action:  LoopAssessmentActionBlocked,
		Message: formatLoopBlockedMessage(category, detail),
	}
}

func (a *MainAgent) stopLoopAsBlocked(rawReason string) {
	if !a.loopState.Enabled {
		return
	}
	category, detail := parseLoopBlockedReason(rawReason)
	a.loopState.State = LoopStateBlocked
	a.emitLoopStateChanged()
	a.emitToTUI(InfoEvent{Message: formatLoopBlockedMessage(category, detail)})
	a.loopState.disable()
}

func (a *MainAgent) terminalLoopAssessment(msg message.Message, suspectedStall bool) *LoopAssessment {
	if blockedReason := extractLoopBlockedReason(msg.Content); blockedReason != "" {
		return a.loopBlockedAssessment(blockedReason)
	}
	addSuspected := func(reasons []string) []string {
		if !suspectedStall {
			return reasons
		}
		return append([]string{"suspected_stall"}, reasons...)
	}
	if a.hasOpenTodos() {
		reasons := addSuspected(a.currentLoopContinuationReasonsForContent(msg.Content, "open_todos", "terminal_reply"))
		return &LoopAssessment{Action: LoopAssessmentActionContinue, Message: "Loop continuing: sync unfinished todos before finishing.", Reasons: reasons, TriggerStopReason: strings.TrimSpace(msg.StopReason)}
	}
	if a.hasActiveSubAgents() {
		reasons := addSuspected(a.currentLoopContinuationReasonsForContent(msg.Content, "subagents_active", "terminal_reply"))
		return &LoopAssessment{Action: LoopAssessmentActionContinue, Message: "Loop continuing: active subagents must finish before completion.", Reasons: reasons, TriggerStopReason: strings.TrimSpace(msg.StopReason)}
	}
	if len(msg.ToolCalls) > 0 {
		doneCount := 0
		mixedWithOtherTools := false
		for _, tc := range msg.ToolCalls {
			if tc.Name == tools.NameDone {
				doneCount++
				continue
			}
			mixedWithOtherTools = true
		}
		if doneCount > 0 && mixedWithOtherTools {
			reasons := addSuspected(a.currentLoopContinuationReasonsForContent(msg.Content, "done_mixed_with_other_tools", "terminal_reply"))
			return &LoopAssessment{Action: LoopAssessmentActionContinue, Message: "Loop continuing: " + toolPromptName(tools.NameDone) + " must be the only tool call in the final batch that requests loop exit.", Reasons: reasons, TriggerStopReason: strings.TrimSpace(msg.StopReason)}
		}
	}
	reasons := addSuspected(a.currentLoopContinuationReasonsForContent(msg.Content, "missing_done_tool", "terminal_reply"))
	if !a.doneToolPermitted() {
		// Reached only inside a loop, where entering mounted done; a denial
		// here means a rule changed mid-loop. With no reachable exit signal
		// the continuation must state the remaining work instead of demanding
		// a tool call the model cannot make.
		return &LoopAssessment{Action: LoopAssessmentActionContinue, Message: "Loop continuing: the " + toolPromptName(tools.NameDone) + " completion tool is not available in this role; continue the remaining in-scope work.", Reasons: reasons, TriggerStopReason: strings.TrimSpace(msg.StopReason)}
	}
	return &LoopAssessment{Action: LoopAssessmentActionContinue, Message: "Loop continuing: end this round with a " + toolPromptName(tools.NameDone) + " tool call to request loop exit.", Reasons: reasons, TriggerStopReason: strings.TrimSpace(msg.StopReason)}
}

// nextLoopAssessmentFromAssistant evaluates the loop state after an assistant message
// and returns the appropriate assessment action.
func (a *MainAgent) nextLoopAssessmentFromAssistant(msg message.Message) *LoopAssessment {
	if !a.loopState.Enabled {
		return nil
	}
	if a.persistenceDegraded() {
		// Tool dispatch is blocked while persistence is degraded; auto-advancing
		// the loop would only produce a repeated fail-and-report cycle.
		a.loopState.State = LoopStateAssessing
		a.emitLoopStateChanged()
		return a.loopBlockedAssessment("persistence degraded")
	}
	a.loopState.State = LoopStateAssessing
	a.emitLoopStateChanged()
	if a.loopState.ProgressVersion != a.loopState.LastAssessmentVersion {
		a.loopState.LastAssessmentVersion = a.loopState.ProgressVersion
		a.loopState.LastProgressSignature = normalizeLoopProgressSignature(msg.Content, msg.StopReason)
		a.loopState.ConsecutiveNoProgress = 0
		stopReason := strings.TrimSpace(msg.StopReason)
		if stopReason == "stop" || stopReason == "end_turn" || stopReason == "tool_calls" {
			return a.terminalLoopAssessment(msg, false)
		}
		return &LoopAssessment{
			Action:            LoopAssessmentActionContinue,
			Message:           "Loop continuing after observable progress.",
			Reasons:           a.currentLoopContinuationReasons("progress_continuation"),
			TriggerStopReason: stopReason,
		}
	}
	signature := normalizeLoopProgressSignature(msg.Content, msg.StopReason)
	if signature != "" && signature == a.loopState.LastProgressSignature {
		a.loopState.ConsecutiveNoProgress++
	} else if signature != "" {
		a.loopState.LastProgressSignature = signature
		// Different assistant text is NOT hard progress; counter keeps climbing.
		a.loopState.ConsecutiveNoProgress++
	} else {
		a.loopState.ConsecutiveNoProgress++
	}
	// Stall detector: two consecutive no-progress rounds mark suspected_stall;
	// three consecutive rounds exhaust the loop budget.
	if a.loopState.ConsecutiveNoProgress >= 3 {
		return &LoopAssessment{
			Action:  LoopAssessmentActionBudgetExhausted,
			Message: "Loop stopped: no observable progress for 3 consecutive rounds.",
		}
	}
	stopReason := strings.TrimSpace(msg.StopReason)
	if stopReason == "stop" || stopReason == "end_turn" {
		return a.terminalLoopAssessment(msg, a.loopState.ConsecutiveNoProgress >= 2)
	}
	reasons := a.currentLoopContinuationReasons("context_continue")
	if a.loopState.ConsecutiveNoProgress >= 2 {
		reasons = append([]string{"suspected_stall"}, reasons...)
	}
	return &LoopAssessment{
		Action:            LoopAssessmentActionContinue,
		Message:           "Loop continuing from existing context.",
		Reasons:           reasons,
		TriggerStopReason: stopReason,
	}
}

// currentLoopContinuationReasons builds a deduplicated list of reasons for loop continuation.
func (a *MainAgent) currentLoopContinuationReasons(extra ...string) []string {
	return a.currentLoopContinuationReasonsForContent("", extra...)
}

func (a *MainAgent) currentLoopContinuationReasonsForContent(_ string, extra ...string) []string {
	reasons := make([]string, 0, 6)
	seen := map[string]struct{}{}
	add := func(reason string) {
		reason = strings.TrimSpace(reason)
		if reason == "" {
			return
		}
		if _, ok := seen[reason]; ok {
			return
		}
		seen[reason] = struct{}{}
		reasons = append(reasons, reason)
	}
	for _, reason := range extra {
		add(reason)
	}
	if a.hasOpenTodos() {
		add("open_todos")
	}
	if a.hasActiveSubAgents() {
		add("subagents_active")
	}
	return reasons
}

// openTodoContinuationLines returns formatted lines for open TODO items.
func (a *MainAgent) openTodoContinuationLines() []string {
	a.todoMu.RLock()
	defer a.todoMu.RUnlock()
	lines := make([]string, 0, len(a.todoItems))
	for _, todo := range a.todoItems {
		status := strings.TrimSpace(todo.Status)
		if status != "pending" && status != "in_progress" {
			continue
		}
		content := strings.TrimSpace(todo.Content)
		if content == "" {
			continue
		}
		if status == "in_progress" {
			lines = append(lines, "- [in_progress] "+content)
		} else {
			lines = append(lines, "- [pending] "+content)
		}
	}
	return lines
}

// activeSubAgentContinuationLines returns formatted lines for active subagents.
func (a *MainAgent) activeSubAgentContinuationLines() []string {
	subs := a.subs.snapshotSubAgents()
	lines := make([]string, 0, len(subs))
	for _, sub := range subs {
		if sub == nil {
			continue
		}
		id := sub.instanceID
		state := sub.State()
		switch state {
		case SubAgentStateCompleted, SubAgentStateFailed, SubAgentStateCancelled, SubAgentStateIdle:
			continue
		}
		summary := strings.TrimSpace(sub.LastSummary())
		if summary != "" {
			lines = append(lines, fmt.Sprintf("- %s (%s): %s", id, state, summary))
		} else {
			lines = append(lines, fmt.Sprintf("- %s (%s)", id, state))
		}
	}
	sort.Strings(lines)
	return lines
}

func loopContinuationTitle(_ LoopAssessmentAction) string {
	return "LOOP CONTINUE"
}

// buildLoopContinuationNote constructs the continuation notice injected into the next LLM request.
func (a *MainAgent) buildLoopContinuationNote(assessment *LoopAssessment) *LoopContinuationNote {
	if assessment == nil || assessment.Action != LoopAssessmentActionContinue {
		return nil
	}
	reasons := assessment.Reasons
	if len(reasons) == 0 {
		reasons = a.currentLoopContinuationReasons()
	}
	sections := make([]string, 0, 10)
	sections = append(sections, "<loop-continuation>", "Continue required.")

	// Automatic Done interception budget. The counter already counts the
	// rejection that produced this notice: autoRejectLoopExitAndContinue
	// records the intercept before building it, so the number shown is what
	// the model has actually spent, not what is left before the next one.
	maxIter := a.loopState.MaxIterations
	iter := a.loopState.Iteration
	sections = append(sections, a.loopBudgetLines(iter)...)
	sections = append(sections, a.loopStateListSections()...)
	if reason, ok := extractDoneRejectedReason(assessment.Message); ok {
		sections = append(sections, "", "Latest Done rejection reason:", reason)
	}

	// Concrete reasons for continuation — no vague fallback.
	gapLines := make([]string, 0, len(reasons))
	seenGap := map[string]struct{}{}
	addGap := func(line string) {
		line = strings.TrimSpace(line)
		if line == "" {
			return
		}
		if _, ok := seenGap[line]; ok {
			return
		}
		seenGap[line] = struct{}{}
		gapLines = append(gapLines, "- "+line)
	}
	for _, reason := range reasons {
		switch reason {
		case "terminal_reply":
			addGap("latest assistant reply stopped before loop completion criteria were met")
		case "missing_done_tool":
			addGap("submit the full completion report through a final " + toolPromptName(tools.NameDone) + " tool call")
		case "done_mixed_with_other_tools":
			addGap(toolPromptName(tools.NameDone) + " must be the only tool call in the final exit-request batch")
		case "progress_continuation":
			addGap("the task made progress and should continue toward completion")
		case "context_continue":
			addGap("continue from the existing context to complete the current goal")
		case "open_todos":
			addGap("open TODO items remain")
		case "subagents_active":
			addGap("active subagents are still running")
		case "done_rejected":
			addGap("the previous Done request was rejected; address the rejection reason before trying Done again")
		case "suspected_stall":
			addGap("no hard progress detected for the last two rounds — take concrete action instead of summarizing")
		case "repeated_tool_call":
			addGap("the same tool call was repeated three times with identical arguments — do not repeat it unchanged again")
		default:
			addGap(reason)
		}
	}
	// Only add a generic line if there are truly no concrete reasons.
	if len(gapLines) == 0 {
		addGap("loop is not yet complete")
	}
	if len(gapLines) > 0 {
		sections = append(sections, "", "Why this loop continues:", strings.Join(gapLines, "\n"))
	}

	sections = append(sections, "", "Completion requirements:", strings.Join(a.loopCompletionRequirementLines(), "\n"))

	// Dynamic instruction lines. When to ask the user is owned by the system
	// prompt's Guidelines; only the loop-specific budget rule is stated here.
	instructionLines := []string{
		"- Continue toward the current objective, led by the latest user request or Done rejection; work on the unresolved items above only while they still serve it, and do not let earlier goals or stale TODOs override newer user instructions",
		a.loopContinuationDecisionInstructionLine(),
	}
	if maxIter > 0 && maxIter-iter <= 2 {
		instructionLines = append(instructionLines, "- You are near the automatic Done interception limit: avoid marginal work before the next `done`, and prepare for a user decision if it is rejected again")
	}
	if a.hasActiveSubAgents() {
		instructionLines = append(instructionLines, "- If a subagent appears stuck or blocked, escalate or cancel it rather than waiting indefinitely")
	}
	if slices.Contains(reasons, "suspected_stall") {
		instructionLines = append(instructionLines, "- No new progress was detected. Take the next concrete action within the requested scope: gather distinguishing evidence for analysis, implement or verify for coding, or report a real blocker. Do not repeat the same summary.")
	}
	instructionLines = append(instructionLines, "</loop-continuation>")
	sections = append(sections, "", "Instruction:")
	sections = append(sections, instructionLines...)

	return &LoopContinuationNote{
		Title:    loopContinuationTitle(assessment.Action),
		Text:     strings.Join(sections, "\n"),
		DedupKey: string(assessment.Action) + ":" + strings.Join(reasons, "|"),
	}
}
