package llm

import (
	"encoding/json"
	"testing"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/modelcompat"
)

func mcpDynamicProviderConfig(providerType, model string, chatOptIn, responsesOptIn bool) *ProviderConfig {
	modelCfg := config.ModelConfig{Limit: config.ModelLimit{Context: 128000, Output: 4096}}
	if chatOptIn || responsesOptIn {
		modelCfg.Compat = &config.ModelCompatConfig{}
		if chatOptIn {
			modelCfg.Compat.ChatCompletions = &config.ChatCompletionsCompatConfig{MCPSystemToolsMessage: new(true)}
		}
		if responsesOptIn {
			modelCfg.Compat.Responses = &config.ResponsesCompatConfig{MCPAdditionalTools: new(true)}
		}
	}
	return NewProviderConfig("sample", config.ProviderConfig{
		Type:   providerType,
		APIURL: "https://example.invalid/v1",
		Models: map[string]config.ModelConfig{model: modelCfg},
	}, []string{"test-key"})
}

func TestMCPDynamicCapabilitiesUseExplicitCompatFlags(t *testing.T) {
	chat := NewClient(mcpDynamicProviderConfig(config.ProviderTypeChatCompletions, "custom-model", true, false), &recordingProvider{}, "custom-model", 512, "")
	if !chat.SupportsKimiDynamicTools("sample/custom-model") || !chat.AllPoolTargetsSupportKimiDynamicTools() {
		t.Fatal("chat capability should follow mcp_system_tools_message without model-name gating")
	}
	if chat.SupportsResponsesAdditionalTools("sample/custom-model") {
		t.Fatal("chat target must not advertise Responses additional_tools")
	}

	responses := NewClient(mcpDynamicProviderConfig(config.ProviderTypeResponses, "custom-model", false, true), &recordingProvider{}, "custom-model", 512, "")
	if !responses.SupportsResponsesAdditionalTools("sample/custom-model") || !responses.AllPoolTargetsSupportResponsesAdditionalTools() {
		t.Fatal("Responses capability should follow mcp_additional_tools")
	}
	if responses.SupportsKimiDynamicTools("sample/custom-model") {
		t.Fatal("Responses target must not advertise Chat Completions dynamic tools")
	}
}

func TestMCPDynamicCapabilitiesRequireHomogeneousPool(t *testing.T) {
	primary := mcpDynamicProviderConfig(config.ProviderTypeChatCompletions, "model-1", true, false)
	fallback := mcpDynamicProviderConfig(config.ProviderTypeChatCompletions, "model-2", false, false)
	client := NewClient(primary, &recordingProvider{}, "model-1", 512, "")
	client.SetModelPool([]FallbackModel{
		{ProviderConfig: primary, ProviderImpl: &recordingProvider{}, ModelID: "model-1", MaxTokens: 512},
		{ProviderConfig: fallback, ProviderImpl: &recordingProvider{}, ModelID: "model-2", MaxTokens: 512},
	}, 0)
	if client.AllPoolTargetsSupportKimiDynamicTools() {
		t.Fatal("mixed capability pool must use the common top-level tool shape")
	}
}

func TestResponsesAdditionalToolsProviderAndModelConfig(t *testing.T) {
	provider := NewProviderConfig("sample", config.ProviderConfig{
		Type:   config.ProviderTypeResponses,
		APIURL: "https://example.invalid/v1/responses",
		Compat: &config.ProviderCompatConfig{Responses: &config.ResponsesCompatConfig{MCPAdditionalTools: new(true)}},
		Models: map[string]config.ModelConfig{
			"model-1": {Limit: config.ModelLimit{Context: 128000, Output: 4096}},
			"model-2": {
				Limit:  config.ModelLimit{Context: 128000, Output: 4096},
				Compat: &config.ModelCompatConfig{Responses: &config.ResponsesCompatConfig{MCPAdditionalTools: new(false)}},
			},
		},
	}, []string{"test-key"})
	client := NewClient(provider, &recordingProvider{}, "model-1", 512, "")
	if !client.SupportsResponsesAdditionalTools("sample/model-1") {
		t.Fatal("provider-level mcp_additional_tools should be the default")
	}
	if client.SupportsResponsesAdditionalTools("sample/model-2") {
		t.Fatal("model-level false must override provider-level true")
	}
}

func TestDynamicMCPWireItemsPreserveMessagePosition(t *testing.T) {
	tools := []message.ToolDefinition{{
		Name:        "mcp_sample_lookup",
		Description: "lookup",
		InputSchema: map[string]any{"type": "object"},
	}}
	messages := []message.Message{
		{Role: message.RoleUser, Content: "first"},
		message.NewSystemToolsMessage(tools),
		{Role: message.RoleAssistant, Content: "done"},
		{Role: message.RoleUser, Content: "second"},
	}

	chat := convertMessagesToOpenAI("", "openai_chat", "", messages)
	if len(chat) != 4 || len(chat[1].Tools) != 1 || chat[1].Tools[0].Function.Name != "mcp_sample_lookup" {
		t.Fatalf("chat dynamic mount = %#v", chat)
	}
	raw, err := json.Marshal(chat[1])
	if err != nil {
		t.Fatalf("marshal chat mount: %v", err)
	}
	var object map[string]any
	if err := json.Unmarshal(raw, &object); err != nil {
		t.Fatalf("unmarshal chat mount: %v", err)
	}
	if _, exists := object["content"]; exists {
		t.Fatalf("chat mount must omit content: %s", raw)
	}

	responses := convertMessagesToResponses("", messages)
	if len(responses) != 4 || responses[1].Type != "additional_tools" || responses[1].Role != responsesAdditionalToolsRole || len(responses[1].Tools) != 1 {
		t.Fatalf("Responses dynamic mount = %#v", responses)
	}
}

func TestResponsesAdditionalToolsMatchesWebSocketIncrementalBaseline(t *testing.T) {
	toolDefs := []message.ToolDefinition{{
		Name:        "mcp_sample_lookup",
		Description: "lookup",
		InputSchema: map[string]any{"type": "object"},
	}}
	firstMessages := []message.Message{
		{Role: message.RoleUser, Content: "first"},
		message.NewSystemToolsMessage(toolDefs),
	}
	firstInput := convertMessagesToResponses("", firstMessages)
	output := []responsesOutputEntry{{
		Type:    "message",
		Role:    "assistant",
		Content: []responsesContentBlock{{Type: "output_text", Text: "done"}},
	}}
	_, baselineLen, baselineSig := codexWSBuildBaseline(firstInput, responsesOutputToInputItems(output), false)
	response := &message.Response{}
	collectResponsesOutput(response, output)

	nextMessages := []message.Message{
		{Role: message.RoleUser, Content: "first"},
		message.NewSystemToolsMessage(toolDefs),
		{Role: message.RoleAssistant, ResponsesOutput: response.ResponsesOutput},
		{Role: message.RoleUser, Content: "second"},
	}
	nextInput := convertMessagesToResponses("", nextMessages)
	if len(nextInput) <= baselineLen {
		t.Fatalf("next input length = %d, baseline = %d", len(nextInput), baselineLen)
	}
	if got := responsesInputPrefixSignature(nextInput, baselineLen); got != baselineSig {
		t.Fatalf("additional_tools moved across turns: got %q want %q", got, baselineSig)
	}

	normalized, _ := modelcompat.NormalizeForTarget(firstMessages, modelcompat.TargetModel{
		WireFamily: modelcompat.WireFamilyOpenAIResponses,
	}, modelcompat.NormalizeOptions{StructuredTools: true})
	if len(normalized) != 2 || len(normalized[1].MCPTools) != 1 {
		t.Fatalf("normalization dropped request-only MCP tools: %#v", normalized)
	}
}
