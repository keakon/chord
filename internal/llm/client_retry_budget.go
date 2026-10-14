package llm

import (
	"encoding/json"
	"sync"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/ctxmgr"
	"github.com/keakon/chord/internal/message"
)

// estimateInputTokens provides a rough token estimate from messages when no
// API-reported usage is available (e.g. the cursor-start model failed on every attempt).
// Uses the shared per-message estimator instead of a parallel implementation.
//
// This stays on the plain bytes/3 bound rather than the session-calibrated
// ratio, for two reasons: this package cannot depend on session-level
// calibration, and the caller (clampEffectiveMaxTokens) is an admission gate,
// where bytes/3 — roughly the densest realistic token/byte ratio — is the
// conservative direction.
func estimateInputTokens(messages []message.Message) int {
	total := 0
	for _, msg := range messages {
		total += ctxmgr.EstimateMessageTokens(msg)
	}
	return total
}

// toolEstimateMemo caches the schema-marshal estimate for the most recent tool
// slice. Tool definitions are frozen once built, so one entry keyed by sliceID
// serves the per-attempt and per-request estimators; a rebuilt slice just
// recomputes.
var toolEstimateMemo struct {
	sync.Mutex
	toolsPtr *message.ToolDefinition
	toolsLen int
	tokens   int
}

func estimateToolDefinitionTokens(tools []message.ToolDefinition) int {
	if len(tools) == 0 {
		return 0
	}
	toolEstimateMemo.Lock()
	if toolEstimateMemo.toolsPtr == sliceID(tools) && toolEstimateMemo.toolsLen == len(tools) {
		tokens := toolEstimateMemo.tokens
		toolEstimateMemo.Unlock()
		return tokens
	}
	toolEstimateMemo.Unlock()
	total := 0
	for _, tool := range tools {
		n := len(tool.Name) + len(tool.Description)
		if schemaBytes, err := json.Marshal(tool.InputSchema); err == nil {
			n += len(schemaBytes)
		}
		total += n
	}
	if total == 0 {
		return 0
	}
	tokens := total / 3
	toolEstimateMemo.Lock()
	toolEstimateMemo.toolsPtr, toolEstimateMemo.toolsLen, toolEstimateMemo.tokens = sliceID(tools), len(tools), tokens
	toolEstimateMemo.Unlock()
	return tokens
}

func estimateRequestInputTokens(systemPrompt string, messages []message.Message, tools []message.ToolDefinition) int {
	total := estimateInputTokens(messages) + estimateToolDefinitionTokens(tools)
	if systemPrompt != "" {
		total += len(systemPrompt) / 3
	}
	if total < 1 {
		return 1
	}
	return total
}

// EstimateRequestInputTokens returns the same rough request-side token estimate
// used by clampEffectiveMaxTokens. It includes system prompt, messages, and
// tool schema overhead.
func EstimateRequestInputTokens(systemPrompt string, messages []message.Message, tools []message.ToolDefinition) int {
	return estimateRequestInputTokens(systemPrompt, messages, tools)
}

// clampEffectiveMaxTokens returns the output budget the model can accept for
// this request, the estimated request input, and whether the observed input
// leaves room for at least one output token inside the model's context window.
// A false third result means the observed input side alone fills the window: no
// output budget is left for the provider to honour, so the request must not be
// sent. Output-budget clamping (model output cap, outputCapSetting/thinking
// floor, contextCap) is preserved; only the refusal gate is usage-only.
func clampEffectiveMaxTokens(
	model config.ModelConfig,
	effectiveMaxTokens int,
	outputCapSetting int,
	tuning RequestTuning,
	systemPrompt string,
	messages []message.Message,
	tools []message.ToolDefinition,
	lastInputTokens int,
) (int, int, bool) {
	if model.Limit.Output > 0 && model.Limit.Output < effectiveMaxTokens {
		effectiveMaxTokens = model.Limit.Output
	}

	outputCap := outputCapSetting
	if outputCap <= 0 {
		outputCap = DefaultOutputTokenMax
	}
	if outputCap < effectiveMaxTokens {
		minForThinking := 0
		if tuning.Anthropic.ThinkingBudget > 0 {
			minForThinking = tuning.Anthropic.ThinkingBudget + 1024
		}
		if outputCap < minForThinking {
			outputCap = minForThinking
		}
		if outputCap < effectiveMaxTokens {
			effectiveMaxTokens = outputCap
		}
	}

	inputEstimate := max(estimateRequestInputTokens(systemPrompt, messages, tools), lastInputTokens)
	inputFits := true
	if model.Limit.Context > 0 {
		buffer := max(model.Limit.Context/100, 256)
		contextCap := max(model.Limit.Context-inputEstimate-buffer, 1)
		if contextCap < effectiveMaxTokens {
			effectiveMaxTokens = contextCap
		}
		// Usage-only admission: the byte-based estimate must not refuse a
		// request on its own, and it must not force a refusal through the
		// max() above either. Only a provider-observed prompt size
		// (lastInputTokens) may refuse locally; without an observation the
		// request is always sent and the provider is the authority.
		inputFits = lastInputTokens <= 0 || lastInputTokens+buffer < model.Limit.Context
	}
	return effectiveMaxTokens, inputEstimate, inputFits
}
