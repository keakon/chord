package agent

import (
	"context"
	"fmt"
	"strings"

	"github.com/keakon/chord/internal/llm"
)

const maxLengthRecoveryAttempts = 2

func truncatedToolName(calls []string) string {
	for _, name := range calls {
		if strings.TrimSpace(name) != "" {
			return name
		}
	}
	return ""
}

func lengthRecoveryPrompt(toolName string) string {
	toolName = strings.TrimSpace(toolName)
	if toolName == "" {
		return "System note: the previous response was cut off by the output limit while generating tool arguments. Continue directly without apology or recap. Choose exactly one minimal next step. If you need a tool, call exactly one tool with complete JSON arguments and keep the arguments as short as possible. Do not combine code edits, tests, and documentation in the same response."
	}
	return fmt.Sprintf("System note: the previous response was cut off by the output limit while generating arguments for tool %q. Continue directly without apology or recap. Choose exactly one minimal next step. If you need a tool, call exactly one tool with complete JSON arguments and keep the arguments as short as possible. Do not combine code edits, tests, and documentation in the same response.", toolName)
}

// noOutputLengthRecoveryPrompt addresses truncation that consumed the whole
// output budget on reasoning, before any tool call or visible text. Telling the
// model to shorten tool arguments would not help here, so it is steered to stop
// reasoning and commit to an action instead.
func noOutputLengthRecoveryPrompt() string {
	return "System note: the previous response was cut off by the output limit while still reasoning, so it produced no visible reply. Stop analysing and act now. Continue directly without apology or recap. Do one minimal concrete step: either call exactly one tool with short complete JSON arguments, or give a short direct answer. Do not re-derive prior conclusions."
}

func autoContinuePrompt() string {
	return "System note: context compaction completed successfully. Continue the active coding task directly without apology or recap. Prefer the smallest next concrete step, and preserve the constraints and decisions captured in the compacted context summary."
}

func autoContinueReplayPrompt(userIntent string) string {
	userIntent = strings.TrimSpace(userIntent)
	if userIntent == "" {
		return ""
	}
	return fmt.Sprintf("System note: after compaction, keep the current task anchored to the latest user intent. The latest user request was: %q. Continue that request directly without apology or recap, unless newer queued user input in this turn supersedes it.", userIntent)
}

// beginLengthRecoveryRetry retries the turn with recoveryPrompt injected as a
// request-scoped overlay. Callers choose the prompt because truncation while
// generating tool arguments and truncation before producing anything need
// different guidance.
func (a *MainAgent) beginLengthRecoveryRetry(recoveryPrompt string, turnID uint64, turnCtx context.Context) {
	a.turn.InLengthRecovery = true
	parallelFalse := false
	a.applyMainLLMRequestTuningOverride(llm.RequestTuning{
		OpenAI: llm.OpenAITuning{ParallelToolCalls: &parallelFalse},
	})
	// Use request-scoped overlay instead of durable ctxMgr append so the
	// recovery prompt does not survive compaction. See

	a.pendingRecoveryPrompt = recoveryPrompt
	a.armLengthRecoveryResume(recoveryPrompt)
	a.discardSpeculativeStreamToolsAndClearToolTrace(a.turn, "length_recovery")
	a.prepareSubAgentMailboxBatchForTurnContinuation()
	a.beginMainLLMAfterPreparation(turnCtx, turnID, "")
}
