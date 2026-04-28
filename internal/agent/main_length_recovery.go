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

func (a *MainAgent) beginLengthRecoveryRetry(toolName string, turnID uint64, turnCtx context.Context) {
	a.turn.InLengthRecovery = true
	recoveryPrompt := lengthRecoveryPrompt(toolName)
	parallelFalse := false
	a.llmClient.SetNextRequestTuningOverride(llm.RequestTuning{
		OpenAI: llm.OpenAITuning{ParallelToolCalls: &parallelFalse},
	})
	// Use request-scoped overlay instead of durable ctxMgr append so the
	// recovery prompt does not survive compaction. See
	// docs/architecture/prompt-and-context-engineering.md.
	a.pendingRecoveryPrompt = recoveryPrompt
	a.discardSpeculativeStreamToolsAndClearToolTrace(a.turn)
	a.prepareSubAgentMailboxBatchForTurnContinuation()
	a.beginMainLLMAfterPreparation(turnCtx, turnID, "")
}
