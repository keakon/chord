package agent

import (
	"fmt"
	"sort"
	"strings"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/message"
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
		if a.loopState.advanceIteration() {
			a.loopState.State = LoopStateBudgetExhausted
			a.emitLoopStateChanged()
			a.emitToTUI(InfoEvent{Message: fmt.Sprintf("Loop stopped: max iterations reached (%d).", a.loopState.MaxIterations)})
			a.loopState.disable()
			a.emitLoopStateChanged()
			a.setIdleAndDrainPending()
			return
		}
		a.loopState.State = LoopStateExecuting
		a.emitLoopStateChanged()
		a.pendingLoopContinuation = a.buildLoopContinuationNote(payload)
		if a.pendingLoopContinuation != nil {
			a.appendLoopNoticeMessage(a.pendingLoopContinuation.Title, a.pendingLoopContinuation.Text)
			a.emitToTUI(LoopNoticeEvent{Title: a.pendingLoopContinuation.Title, Text: a.pendingLoopContinuation.Text, DedupKey: a.pendingLoopContinuation.DedupKey})
		}
		a.emitActivity("main", ActivityExecuting, "loop")
		a.handleContinueFromContext(Event{Type: EventContinue})
	case LoopAssessmentActionCompleted:
		a.loopState.State = LoopStateCompleted
		a.emitLoopStateChanged()
		a.emitToTUI(InfoEvent{Message: strings.TrimSpace(payload.Message)})
		a.loopState.disable()
		a.emitLoopStateChanged()
		a.setIdleAndDrainPending()
	case LoopAssessmentActionVerify:
		a.loopState.State = LoopStateVerifying
		a.emitLoopStateChanged()
		a.emitActivity("main", ActivityVerifying, "loop")
		a.pendingLoopContinuation = a.buildLoopContinuationNote(payload)
		if a.pendingLoopContinuation != nil {
			a.appendLoopNoticeMessage(a.pendingLoopContinuation.Title, a.pendingLoopContinuation.Text)
			a.emitToTUI(LoopNoticeEvent{Title: a.pendingLoopContinuation.Title, Text: a.pendingLoopContinuation.Text, DedupKey: a.pendingLoopContinuation.DedupKey})
		}
		a.handleContinueFromContext(Event{Type: EventContinue})
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

// loopKeepsMainBusy returns true if the loop controller is in an active state
// that should keep the main agent's "busy" status.
func (a *MainAgent) loopKeepsMainBusy() bool {
	if !a.loopState.Enabled {
		return false
	}
	switch a.loopState.State {
	case LoopStateExecuting, LoopStateVerifying, LoopStateAssessing:
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
	for _, sub := range a.subAgents {
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

func (a *MainAgent) loopVerificationSatisfied(content string) bool {
	if a.loopState.VerificationVersion > 0 {
		return true
	}
	return extractLoopVerifyNotRunReason(content) != ""
}

func inferLoopBlockerCategory(reason string) string {
	normalized := strings.ToLower(strings.TrimSpace(reason))
	switch {
	case normalized == "":
		return "dependency_unavailable"
	case strings.Contains(normalized, "credential"),
		strings.Contains(normalized, "permission"),
		strings.Contains(normalized, "forbidden"),
		strings.Contains(normalized, "unauthorized"),
		strings.Contains(normalized, "token"),
		strings.Contains(normalized, "denied"):
		return "credential_or_permission_missing"
	case strings.Contains(normalized, "input"),
		strings.Contains(normalized, "sample"),
		strings.Contains(normalized, "capture"),
		strings.Contains(normalized, "replay"),
		strings.Contains(normalized, "fixture"),
		strings.Contains(normalized, "missing data"):
		return "required_input_missing"
	case strings.Contains(normalized, "conflict"),
		strings.Contains(normalized, "locked"),
		strings.Contains(normalized, "lock"),
		strings.Contains(normalized, "dirty worktree"),
		strings.Contains(normalized, "workspace"):
		return "workspace_conflict"
	case strings.Contains(normalized, "decision"),
		strings.Contains(normalized, "choose"),
		strings.Contains(normalized, "approval"),
		strings.Contains(normalized, "confirm"):
		return "user_decision_required"
	default:
		return "dependency_unavailable"
	}
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
		switch maybeCategory {
		case "credential_or_permission_missing", "dependency_unavailable", "required_input_missing", "workspace_conflict", "user_decision_required":
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
		reasons := addSuspected(a.currentLoopContinuationReasons("open_todos", "terminal_reply"))
		return &LoopAssessment{Action: LoopAssessmentActionContinue, Message: "Loop continuing: sync unfinished todos before finishing.", Reasons: reasons}
	}
	if a.hasActiveSubAgents() {
		reasons := addSuspected(a.currentLoopContinuationReasons("subagents_active", "terminal_reply"))
		return &LoopAssessment{Action: LoopAssessmentActionContinue, Message: "Loop continuing: active subagents must finish before completion.", Reasons: reasons}
	}
	if !a.loopVerificationSatisfied(msg.Content) {
		reasons := addSuspected(a.currentLoopContinuationReasons("missing_verification_status", "terminal_reply"))
		return &LoopAssessment{
			Action:  LoopAssessmentActionContinue,
			Message: "Loop continuing: report verification status or include <verify-not-run>reason</verify-not-run> before finishing.",
			Reasons: reasons,
		}
	}
	doneReason := extractLoopDoneReason(msg.Content)
	if doneReason == "" {
		reasons := addSuspected(a.currentLoopContinuationReasons("missing_done_tag", "terminal_reply"))
		return &LoopAssessment{Action: LoopAssessmentActionContinue, Message: "Loop continuing: terminal assistant reply must include a <done>single-line reason</done> tag.", Reasons: reasons}
	}
	return &LoopAssessment{Action: LoopAssessmentActionCompleted, Message: "Loop completed: " + doneReason}
}

// nextLoopAssessmentFromAssistant evaluates the loop state after an assistant message
// and returns the appropriate assessment action.
func (a *MainAgent) nextLoopAssessmentFromAssistant(msg message.Message) *LoopAssessment {
	if !a.loopState.Enabled {
		return nil
	}
	a.loopState.State = LoopStateAssessing
	a.emitLoopStateChanged()
	if a.loopState.ProgressVersion != a.loopState.LastAssessmentVersion {
		a.loopState.LastAssessmentVersion = a.loopState.ProgressVersion
		a.loopState.LastProgressSignature = normalizeLoopProgressSignature(msg.Content, msg.StopReason)
		a.loopState.ConsecutiveNoProgress = 0
		stopReason := strings.TrimSpace(msg.StopReason)
		if stopReason == "stop" || stopReason == "end_turn" {
			return a.terminalLoopAssessment(msg, false)
		}
		return &LoopAssessment{Action: LoopAssessmentActionContinue, Message: "Loop continuing after observable progress.", Reasons: a.currentLoopContinuationReasons("progress_continuation")}
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
	return &LoopAssessment{Action: LoopAssessmentActionContinue, Message: "Loop continuing from existing context.", Reasons: reasons}
}

// currentLoopContinuationReasons builds a deduplicated list of reasons for loop continuation.
func (a *MainAgent) currentLoopContinuationReasons(extra ...string) []string {
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
	lines := make([]string, 0, len(a.subAgents))
	for id, sub := range a.subAgents {
		if sub == nil {
			continue
		}
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

func loopContinuationTitle(action LoopAssessmentAction) string {
	if action == LoopAssessmentActionVerify {
		return "LOOP VERIFY"
	}
	return "LOOP CONTINUE"
}

// buildLoopContinuationNote constructs the continuation notice injected into the next LLM request.
func (a *MainAgent) buildLoopContinuationNote(assessment *LoopAssessment) *LoopContinuationNote {
	if assessment == nil || (assessment.Action != LoopAssessmentActionContinue && assessment.Action != LoopAssessmentActionVerify) {
		return nil
	}
	reasons := assessment.Reasons
	if len(reasons) == 0 {
		reasons = a.currentLoopContinuationReasons()
	}
	sections := make([]string, 0, 10)
	if assessment.Action == LoopAssessmentActionVerify {
		sections = append(sections, "<loop-continuation>", "Verification required.")
	} else {
		sections = append(sections, "<loop-continuation>", "Continue required.")
	}

	// Iteration budget.
	maxIter := a.loopState.MaxIterations
	iter := a.loopState.Iteration
	if maxIter > 0 {
		remaining := maxIter - iter
		if remaining < 0 {
			remaining = 0
		}
		sections = append(sections, fmt.Sprintf("Iteration %d of %d (%d remaining).", iter, maxIter, remaining))
		if remaining <= 2 {
			sections = append(sections, "Budget is nearly exhausted — prioritize closing out remaining work over starting new subtasks.")
		}
	} else {
		sections = append(sections, fmt.Sprintf("Iteration %d (unlimited).", iter))
	}

	if todoLines := a.openTodoContinuationLines(); len(todoLines) > 0 {
		sections = append(sections, "", "Open TODO items:", strings.Join(todoLines, "\n"))
	}
	if subLines := a.activeSubAgentContinuationLines(); len(subLines) > 0 {
		sections = append(sections, "", "Active subagents:", strings.Join(subLines, "\n"))
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
		case "missing_done_tag":
			addGap("the final assistant reply must include <done>single-line reason</done>")
		case "missing_verification_status":
			addGap("verification status is missing; run verification or include <verify-not-run>reason</verify-not-run>")
		case "progress_continuation":
			addGap("the task made progress and should continue toward completion")
		case "context_continue":
			addGap("continue from the existing context to complete the current goal")
		case "verification_required":
			addGap("verification is required before completion")
		case "open_todos":
			addGap("open TODO items remain")
		case "subagents_active":
			addGap("active subagents are still running")
		case "suspected_stall":
			addGap("no hard progress detected for the last two rounds — take concrete action instead of summarizing")
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
	finalLines := append(a.loopFinalCompletionResponseLines(), "- Do not mark completion merely because you produced a summary or reached a natural stopping point")
	sections = append(sections, "", "Final completion response requirements:", strings.Join(finalLines, "\n"))

	// Dynamic instruction lines.
	instructionLines := []string{
		"- Continue toward the original goal",
		"- Prioritize the unresolved items listed above",
		"- No new user input was received",
		a.loopContinuationDecisionInstructionLine(),
		"- If the task is truly blocked, stop with <blocked>category: reason</blocked> using category in {credential_or_permission_missing, dependency_unavailable, required_input_missing, workspace_conflict, user_decision_required}",
		"- Choose the best reasonable path unless a real user decision is required",
		"- Only ask the user when a material ambiguity, permission boundary, or major tradeoff requires it",
	}
	if assessment.Action == LoopAssessmentActionVerify {
		instructionLines = append(instructionLines, "- Run the smallest relevant verification now, or include <verify-not-run>reason</verify-not-run> only if verification cannot be run")
	}
	if maxIter > 0 && maxIter-iter <= 2 {
		instructionLines = append(instructionLines, "- You are near the iteration limit: wrap up remaining work, do not start new subtasks or investigations")
	}
	if a.hasActiveSubAgents() {
		instructionLines = append(instructionLines, "- If a subagent appears stuck or blocked, escalate or cancel it rather than waiting indefinitely")
	}
	for _, reason := range reasons {
		if reason == "suspected_stall" {
			instructionLines = append(instructionLines, "- WARNING: You appear to be stalling. Do NOT summarize, suggest, or analyze again. Execute a concrete step NOW.")
			break
		}
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
