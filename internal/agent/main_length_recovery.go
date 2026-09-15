package agent

import (
	"context"
	"fmt"
	"strings"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/modelcompat"
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
	return "System note: context compaction completed successfully. Continue the active coding task directly without apology or recap. Prefer the smallest next concrete step, and preserve the constraints and decisions captured in the compacted context summary. " + checkpointVerificationGuidance
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
	// The recovery request is a dispatch boundary: carry queued user input and
	// the pending mailbox batch exactly like the tool-batch closeout. Consuming
	// an empty queue is a no-op, so the compaction-resume caller that already
	// merged queued input before entering here is unaffected.
	a.mergePendingInputsForTurnContinuation()
	a.beginMainLLMAfterPreparation(turnCtx, turnID, "")
}

// stashTruncatedThinkingReplay captures the visible reasoning text of a
// response whose whole output budget was spent on thinking before any visible
// reply. On the next recovery request the text is replayed as a wire-only
// assistant prefix so a thinking-mode backend (DeepSeek family) continues from
// its truncated reasoning instead of starting over. The prefix is bound to the
// producing model ref and never enters durable history.
func (a *MainAgent) stashTruncatedThinkingReplay(payload *LLMResponsePayload) {
	a.clearPendingThinkingReplay()
	if payload == nil {
		return
	}
	// Only the first no-output truncation in a turn gets a reasoning replay.
	// Later bounded recovery rounds keep the ordinary recovery prompt instead
	// of repeatedly resubmitting an unsupported gateway-specific message shape.
	if a.turn == nil || a.turn.thinkingReplayAttempted {
		return
	}
	reasoning := strings.TrimSpace(payload.ReasoningContent)
	if reasoning == "" {
		return
	}
	if a.llmClient == nil {
		return
	}
	boundTurnID := a.currentTurnID()
	if boundTurnID == 0 {
		return
	}
	boundRef := a.llmClient.NextRequestModelRef()
	if boundRef == "" || !a.llmClient.SupportsThinkingReplay(boundRef) {
		return
	}
	providerID, modelID := splitModelRefParts(boundRef)
	if providerID == "" {
		return
	}
	log.Infof("stashing truncated thinking replay reasoning_len=%v bound_ref=%q", len(reasoning), boundRef)
	a.turn.thinkingReplayAttempted = true
	a.pendingThinkingReplayPrefix = &message.Message{
		Role:             message.RoleAssistant,
		Content:          "",
		ReasoningContent: reasoning,
		Kind:             message.KindThinkingReplayPrefix,
		// The wire-only prefix must survive message normalization for the
		// producing target: visible reasoning replay is gated on provenance
		// matching the target provider, so bind the prefix to the model that
		// produced it.
		Provenance: &message.MessageProvenance{
			ProviderID: providerID,
			ModelID:    modelID,
			WireFamily: modelcompat.WireFamilyOpenAIChat,
		},
	}
	a.pendingThinkingReplayRef = boundRef
	a.pendingThinkingReplayTurnID = boundTurnID
}

// splitModelRefParts splits a "provider/model" model ref into its parts,
// dropping any inline variant suffix (e.g. "provider/model@variant").
func splitModelRefParts(ref string) (providerID, modelID string) {
	ref = strings.TrimSpace(ref)
	if i := strings.Index(ref, "/"); i > 0 {
		providerID = ref[:i]
		modelID = ref[i+1:]
		if j := strings.Index(modelID, "@"); j >= 0 {
			modelID = modelID[:j]
		}
	}
	return providerID, modelID
}

// takePendingThinkingReplayPrefix consumes the stashed truncated-thinking
// prefix for a one-shot wire-only replay. It returns nil (and drops the prefix)
// when the model pool no longer targets the model that produced it, the target
// lost visible-reasoning replay support, or nothing was stashed. The returned
// message must only be injected into the request wire, never appended to
// ctxMgr.
func (a *MainAgent) takePendingThinkingReplayPrefix() *message.Message {
	if a.pendingThinkingReplayPrefix == nil {
		return nil
	}
	prefix := a.pendingThinkingReplayPrefix
	boundRef := a.pendingThinkingReplayRef
	boundTurnID := a.pendingThinkingReplayTurnID
	a.clearPendingThinkingReplay()
	if a.llmClient == nil {
		return nil
	}
	if currentTurnID := a.currentTurnID(); currentTurnID == 0 || currentTurnID != boundTurnID {
		log.Infof("dropping truncated thinking replay prefix turn_changed bound_turn=%v current_turn=%v", boundTurnID, currentTurnID)
		return nil
	}
	currentRef := a.llmClient.NextRequestModelRef()
	if currentRef == "" || currentRef != boundRef {
		log.Infof("dropping truncated thinking replay prefix model_changed bound_ref=%q current_ref=%q", boundRef, currentRef)
		return nil
	}
	if !a.llmClient.SupportsThinkingReplay(currentRef) {
		log.Infof("dropping truncated thinking replay prefix capability_lost ref=%q", currentRef)
		return nil
	}
	return prefix
}

// clearPendingThinkingReplay discards any stashed truncated-thinking prefix.
func (a *MainAgent) clearPendingThinkingReplay() {
	a.pendingThinkingReplayPrefix = nil
	a.pendingThinkingReplayRef = ""
	a.pendingThinkingReplayTurnID = 0
}
