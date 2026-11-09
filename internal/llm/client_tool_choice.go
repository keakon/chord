package llm

import (
	"context"
	"encoding/json"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/config"
)

type toolChoiceRequestKey struct{}

// The provider records the final body, including overrides and cache hits.
// Only a rejected request needs to decode it; successful streams pay no scan.
type toolChoiceRequest struct {
	body []byte
}

func recordToolChoiceRequest(ctx context.Context, body []byte) {
	if request, _ := ctx.Value(toolChoiceRequestKey{}).(*toolChoiceRequest); request != nil {
		request.body = body
	}
}

func (r *toolChoiceRequest) forced(provider *ProviderConfig, model string, tuning RequestTuning) bool {
	var choice, field string
	switch provider.Type() {
	case config.ProviderTypeGenerateContent:
		choice, field = tuning.Gemini.ToolChoice, "toolConfig"
	case config.ProviderTypeMessages:
		choice, field = tuning.Anthropic.ToolChoice, "tool_choice"
	case config.ProviderTypeChatCompletions, config.ProviderTypeResponses:
		choice, field = tuning.OpenAI.ToolChoice, "tool_choice"
	default:
		return false
	}
	if choice != "required" || tuning.HostedTool != nil {
		return false
	}
	// A tuning-only probe cannot remove a choice owned by request overrides.
	overrides := provider.RequestOverrides(model)
	if _, exists := overrides.Body[field]; exists {
		return false
	}
	for source, target := range overrides.RenameBodyFields {
		if source == field || target != nil && *target == field {
			return false
		}
	}
	var body struct {
		Tools      []json.RawMessage `json:"tools"`
		ToolChoice json.RawMessage   `json:"tool_choice"`
		ToolConfig *geminiToolConfig `json:"toolConfig"`
	}
	if json.Unmarshal(r.body, &body) != nil || len(body.Tools) == 0 {
		return false
	}
	if provider.Type() == config.ProviderTypeGenerateContent {
		return body.ToolConfig != nil && body.ToolConfig.FunctionCallingConfig != nil && body.ToolConfig.FunctionCallingConfig.Mode == "ANY"
	}
	if provider.Type() == config.ProviderTypeMessages {
		var choice anthropicToolChoice
		return json.Unmarshal(body.ToolChoice, &choice) == nil && choice.Type == "any"
	}
	var wireChoice string
	return json.Unmarshal(body.ToolChoice, &wireChoice) == nil && wireChoice == "required"
}

// The probe preserves the client's tool surface. Removing a wire constraint
// must not enable native tools that the original forced call excluded.
func (c *Client) probeWithoutForcedToolChoice(a streamTargetAttempt) (streamTargetAttemptResult, int, error) {
	probe := a
	probe.ctx = context.WithValue(a.ctx, nativePolicyContextKey{}, (*NativeToolPolicy)(nil))
	probe.target.tuning.OpenAI.ToolChoice = ""
	probe.target.tuning.Anthropic.ToolChoice = ""
	probe.target.tuning.Gemini.ToolChoice = ""
	probe.fallbackEnabled = false
	probe.fallbackModels = nil
	probe.hasNextTarget = false
	log.Warnf("retrying request without forced tool choice after terminal 400 provider=%v model=%v", a.target.provider.Name(), a.target.modelID)
	result, tokens, err := c.completeStreamTarget(probe)
	if err == nil && result.resp != nil {
		a.target.provider.markForcedChoiceRefused(a.target.modelID)
		log.Warnf("request succeeded without forced tool choice; downgrading it for this target provider=%v model=%v", a.target.provider.Name(), a.target.modelID)
	}
	return result, tokens, err
}
