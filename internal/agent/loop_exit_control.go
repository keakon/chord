package agent

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

func (a *MainAgent) loopExitConditionsSatisfied(content string) bool {
	if !a.loopState.Enabled {
		return false
	}
	if a.hasOpenTodos() {
		return false
	}
	if a.hasActiveSubAgents() {
		return false
	}
	return a.loopVerificationSatisfied(content)
}

func (a *MainAgent) loopExitRejectionToolResult() string {
	reasons := a.currentLoopContinuationReasons()
	if len(reasons) == 0 {
		return "Done rejected: loop exit conditions are not satisfied yet. Continue working toward the current loop target."
	}
	return "Done rejected: loop exit conditions are not satisfied yet (" + strings.Join(reasons, ", ") + "). Continue working toward the current loop target."
}

func (a *MainAgent) awaitLoopExitConfirmation(ctx context.Context, pending *loopExitResult) (ConfirmResponse, error) {
	if pending == nil {
		return ConfirmResponse{Approved: false}, nil
	}
	payload := struct {
		Reason string `json:"reason,omitempty"`
	}{Reason: strings.TrimSpace(pending.Reason)}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return ConfirmResponse{}, err
	}
	return a.AwaitConfirm(ctx, "Done", string(encoded), 0, nil, nil)
}

func (a *MainAgent) appendLoopContinuationAndContinue(callID, argsJSON, result string) {
	if a.turn == nil {
		return
	}
	result = strings.TrimSpace(result)
	if result == "" {
		result = "Continue the loop."
	}
	assessment := &LoopAssessment{
		Action:  LoopAssessmentActionContinue,
		Message: result,
		Reasons: []string{"done_rejected", "context_continue"},
	}
	note := a.buildLoopContinuationNote(assessment)
	if note != nil {
		a.loopState.DeferContinuationPromptUntilDone = false
		a.pendingLoopContinuation = note
		a.appendLoopNoticeMessage(note.Title, note.Text)
		a.emitToTUI(LoopNoticeEvent{Title: note.Title, Text: note.Text, DedupKey: note.DedupKey})
	}
	a.emitToTUI(ToolCallUpdateEvent{ID: callID, Name: tools.NameDone, ArgsJSON: argsJSON, ArgsStreamingDone: true, AgentID: "main"})
	a.emitToTUI(ToolResultEvent{CallID: callID, Name: tools.NameDone, ArgsJSON: argsJSON, Result: result, Status: ToolResultStatusSuccess})
	msg := message.Message{Role: "assistant", Content: result, Kind: "loop_notice"}
	a.ctxMgr.Append(msg)
	if a.recovery != nil {
		a.persistAsync("main", msg)
	}
}

func (a *MainAgent) shouldRequireToolCallInLoop() bool {
	if a == nil || a.llmClient == nil || !a.loopState.Enabled {
		return false
	}
	provider := a.llmClient.ProviderConfig()
	if provider == nil {
		return false
	}
	// Conservative gate: only enable request-level tool_choice on OpenAI-compatible
	// provider families that are known to carry tool_choice in this codebase.
	switch provider.Type() {
	case "chat-completions", "chat_completions", "responses":
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
	return tuning
}
