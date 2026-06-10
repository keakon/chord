package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/identity"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

const repeatedToolCallInterceptThreshold = 3

type repeatedToolCallInterceptResult struct {
	toolResult string
	confirmErr error
}

func canonicalRepeatedToolCallArgs(raw json.RawMessage) string {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return "{}"
	}
	var decoded any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return trimmed
	}
	encoded, err := json.Marshal(decoded)
	if err != nil {
		return trimmed
	}
	return string(encoded)
}

func (a *MainAgent) recordRepeatedToolCall(tc message.ToolCall) int {
	if a == nil || !a.loopState.Enabled {
		return 0
	}
	fingerprint := loopToolCallFingerprint{
		Name: strings.TrimSpace(tc.Name),
		Args: canonicalRepeatedToolCallArgs(tc.Args),
	}
	a.loopState.RepeatedToolCallStreak = append(a.loopState.RepeatedToolCallStreak, fingerprint)
	if len(a.loopState.RepeatedToolCallStreak) > repeatedToolCallInterceptThreshold {
		a.loopState.RepeatedToolCallStreak = a.loopState.RepeatedToolCallStreak[len(a.loopState.RepeatedToolCallStreak)-repeatedToolCallInterceptThreshold:]
	}
	if len(a.loopState.RepeatedToolCallStreak) < repeatedToolCallInterceptThreshold {
		return 0
	}
	for i := 1; i < len(a.loopState.RepeatedToolCallStreak); i++ {
		if a.loopState.RepeatedToolCallStreak[i] != a.loopState.RepeatedToolCallStreak[0] {
			return 0
		}
	}
	return len(a.loopState.RepeatedToolCallStreak)
}

func (a *MainAgent) repeatedToolCallRejectResult(tc message.ToolCall, streak int) string {
	parts := []string{fmt.Sprintf("Tool call rejected automatically: detected %d consecutive identical `%s` tool calls with the same arguments.", streak, strings.TrimSpace(tc.Name))}
	reasons := a.currentLoopContinuationReasons("repeated_tool_call", "context_continue")
	if len(reasons) > 0 {
		parts = append(parts, "Loop exit conditions are not satisfied yet ("+strings.Join(reasons, ", ")+").")
	}
	if target := strings.TrimSpace(a.loopState.Target); target != "" {
		parts = append(parts, "Continue working toward the current loop target: "+target+".")
	} else {
		parts = append(parts, "Continue working toward the current loop target.")
	}
	parts = append(parts, "Do not repeat the same tool call unchanged again; explain why it did not make progress and choose a different concrete action.")
	return strings.Join(parts, " ")
}

func (a *MainAgent) repeatedToolCallConfirmResult(tc message.ToolCall, streak int) string {
	if a.loopState.MaxIterations > 0 {
		return fmt.Sprintf("Tool call requires user decision: detected %d consecutive identical `%s` tool calls with the same arguments and the automatic loop interception limit reached (%d). Deny this repeated call with a reason so the model can recover.", streak, strings.TrimSpace(tc.Name), a.loopState.MaxIterations)
	}
	return fmt.Sprintf("Tool call requires user decision: detected %d consecutive identical `%s` tool calls with the same arguments and automatic loop interception is disabled. Deny this repeated call with a reason so the model can recover.", streak, strings.TrimSpace(tc.Name))
}

func (a *MainAgent) maybeInterceptRepeatedToolCall(ctx context.Context, tc message.ToolCall) (*repeatedToolCallInterceptResult, bool) {
	if a == nil || !a.loopState.Enabled || strings.TrimSpace(tc.Name) == "" || tc.Name == tools.NameDone {
		return nil, false
	}
	streak := a.recordRepeatedToolCall(tc)
	if streak < repeatedToolCallInterceptThreshold {
		return nil, false
	}
	if a.shouldAutoInterceptLoopExit() {
		a.loopState.recordAutoExitIntercept()
		a.emitLoopStateChanged()
		return &repeatedToolCallInterceptResult{toolResult: a.repeatedToolCallRejectResult(tc, streak)}, true
	}
	if a.confirmFn == nil {
		return &repeatedToolCallInterceptResult{confirmErr: context.Canceled}, true
	}
	payload := struct {
		Reason string `json:"reason,omitempty"`
		Report string `json:"report"`
	}{
		Reason: a.repeatedToolCallConfirmResult(tc, streak),
		Report: strings.TrimSpace(a.repeatedToolCallRejectResult(tc, streak)),
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return &repeatedToolCallInterceptResult{confirmErr: err}, true
	}
	resp, err := a.AwaitForceDenyConfirm(ctx, tools.NameDone, string(encoded), 0, nil, nil, payload.Report)
	if err != nil {
		return &repeatedToolCallInterceptResult{confirmErr: err}, true
	}
	if resp.Approved || strings.TrimSpace(resp.DenyReason) == "" {
		return &repeatedToolCallInterceptResult{confirmErr: wrapToolRejectedByUser(tc.Name, a.repeatedToolCallConfirmResult(tc, streak))}, true
	}
	a.loopState.Iteration = 0
	a.loopState.RepeatedToolCallStreak = nil
	return &repeatedToolCallInterceptResult{toolResult: a.repeatedToolCallRejectResult(tc, streak)}, true
}

func (a *MainAgent) loopExitConditionsSatisfied() bool {
	if !a.loopState.Enabled {
		return false
	}
	if a.hasOpenTodos() {
		return false
	}
	if a.hasActiveSubAgents() {
		return false
	}
	return true
}

func doneAutoRejectionReasonLine(reason string) string {
	switch strings.TrimSpace(reason) {
	case "open_todos":
		return "open TODO items remain"
	case "subagents_active":
		return "active subagents are still running"
	default:
		return strings.TrimSpace(reason)
	}
}

func (a *MainAgent) loopExitRejectionToolResult() string {
	reasons := a.currentLoopContinuationReasons()
	if len(reasons) == 0 {
		return "Done rejected automatically: loop exit conditions are not satisfied yet. Finish the remaining work before calling Done again."
	}
	lines := make([]string, 0, len(reasons))
	for _, reason := range reasons {
		line := doneAutoRejectionReasonLine(reason)
		if line == "" {
			continue
		}
		lines = append(lines, line)
	}
	if len(lines) == 0 {
		return "Done rejected automatically: loop exit conditions are not satisfied yet. Finish the remaining work before calling Done again."
	}
	return "Done rejected automatically: loop exit conditions are not satisfied yet: " + strings.Join(lines, "; ") + ". Finish the remaining work before calling Done again."
}

func (a *MainAgent) loopExitInterceptLimitResult() string {
	if a.loopState.MaxIterations > 0 {
		return "Done requires user decision: automatic Done interception limit reached (" + strconv.Itoa(a.loopState.MaxIterations) + "). Approve exit or deny to continue."
	}
	return "Done requires user decision: automatic Done interception is disabled. Approve exit or deny to continue."
}

func (a *MainAgent) awaitDoneConfirmation(ctx context.Context, reason, argsJSON, assistantContent string) (ConfirmResponse, error) {
	report := strings.TrimSpace(assistantContent)
	if parsed, err := tools.ParseDoneArgs(json.RawMessage(argsJSON)); err == nil {
		report = parsed.Report
	}
	payload := struct {
		Reason string `json:"reason,omitempty"`
		Report string `json:"report"`
	}{Reason: strings.TrimSpace(reason), Report: report}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return ConfirmResponse{}, err
	}
	return a.AwaitConfirm(ctx, tools.NameDone, string(encoded), 0, nil, nil, report)
}

func (a *MainAgent) persistLoopDoneToolResult(callID, result string) {
	if a == nil {
		return
	}
	result = strings.TrimSpace(result)
	if callID == "" || result == "" {
		return
	}
	toolMsg := message.Message{Role: message.RoleTool, Content: result, ToolCallID: callID, ToolStatus: string(ToolResultStatusSuccess)}
	a.ctxMgr.Append(toolMsg)
	if a.recovery != nil {
		a.persistAsync(identity.MainAgentID, toolMsg)
	}
	if a.turn != nil {
		a.recordEvidenceFromMessage(toolMsg)
	}
}

func (a *MainAgent) appendLoopContinuationAndContinue(callID, argsJSON, result string) {
	if a.turn == nil {
		return
	}
	result = strings.TrimSpace(result)
	if result == "" {
		result = "Continue the loop."
	}
	a.persistLoopDoneToolResult(callID, result)
	assessment := &LoopAssessment{
		Action:  LoopAssessmentActionContinue,
		Message: result,
		Reasons: []string{"done_rejected", "context_continue"},
	}
	note := a.buildLoopContinuationNote(assessment)
	if note != nil {
		a.loopState.DeferContinuationPromptUntilDone = false
		a.pendingLoopContinuation = note
		a.emitLoopContinuationNote(note, false)
	}
	a.emitToTUI(ToolCallUpdateEvent{ID: callID, Name: tools.NameDone, ArgsJSON: argsJSON, ArgsStreamingDone: true, AgentID: "main"})
	a.emitToTUI(ToolResultEvent{CallID: callID, Name: tools.NameDone, ArgsJSON: argsJSON, Result: result, Status: ToolResultStatusSuccess})
}

func (a *MainAgent) shouldAutoInterceptLoopExit() bool {
	if !a.loopState.Enabled {
		return false
	}
	if a.loopState.MaxIterations <= 0 {
		return true
	}
	return a.loopState.Iteration < a.loopState.MaxIterations
}

func (a *MainAgent) autoRejectLoopExitAndContinue(callID, argsJSON, result string) bool {
	if !a.shouldAutoInterceptLoopExit() {
		return false
	}
	a.loopState.recordAutoExitIntercept()
	a.appendLoopContinuationAndContinue(callID, argsJSON, result)
	a.emitLoopStateChanged()
	return true
}

func (a *MainAgent) shouldRequireToolCallInLoop() bool {
	if a == nil || a.llmClient == nil || !a.loopState.Enabled {
		return false
	}
	provider := a.llmClient.ProviderConfig()
	if provider == nil {
		return false
	}
	// Conservative gate: only enable request-level tool_choice on provider
	// families that are known to carry tool_choice in this codebase.
	switch provider.Type() {
	case config.ProviderTypeMessages:
		return true
	case config.ProviderTypeGenerateContent:
		return true
	case config.ProviderTypeChatCompletions, config.ProviderTypeChatCompletionsLegacy, config.ProviderTypeResponses:
		return true
	default:
		return false
	}
}

func (a *MainAgent) applyLoopToolChoiceRequirement(tuning llm.RequestTuning) llm.RequestTuning {
	if !a.shouldRequireToolCallInLoop() {
		return tuning
	}
	parallelFalse := false
	tuning.OpenAI.ParallelToolCalls = &parallelFalse
	tuning.OpenAI.ToolChoice = "required"
	tuning.Anthropic.ToolChoice = "required"
	tuning.Gemini.ToolChoice = "required"
	return tuning
}
