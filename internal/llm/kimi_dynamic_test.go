package llm

import (
	"encoding/json"
	"testing"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/modelcompat"
)

func kimiChatProviderConfig(model string, optIn bool) *ProviderConfig {
	modelCfg := config.ModelConfig{Limit: config.ModelLimit{Context: 128000, Output: 4096}}
	if optIn {
		modelCfg.Compat = &config.ModelCompatConfig{
			ChatCompletions: &config.ChatCompletionsCompatConfig{MCPSystemToolsMessage: new(true)},
		}
	}
	return NewProviderConfig("sample", config.ProviderConfig{
		Type:   config.ProviderTypeChatCompletions,
		APIURL: "https://example.invalid/v1",
		Models: map[string]config.ModelConfig{model: modelCfg},
	}, []string{"test-key"})
}

func TestSupportsKimiDynamicToolsProviderAndModelConfig(t *testing.T) {
	providerDefault := NewProviderConfig("sample", config.ProviderConfig{
		Type:   config.ProviderTypeChatCompletions,
		APIURL: "https://example.invalid/v1",
		Compat: &config.ProviderCompatConfig{ChatCompletions: &config.ChatCompletionsCompatConfig{MCPSystemToolsMessage: new(true)}},
		Models: map[string]config.ModelConfig{
			"model-1": {Limit: config.ModelLimit{Context: 128000, Output: 4096}},
		},
	}, []string{"test-key"})
	client := NewClient(providerDefault, &recordingProvider{}, "model-1", 512, "")
	if !client.SupportsKimiDynamicTools("sample/model-1") {
		t.Fatal("provider-level mcp_system_tools_message should be the default")
	}

	modelOverride := NewProviderConfig("sample", config.ProviderConfig{
		Type:   config.ProviderTypeChatCompletions,
		APIURL: "https://example.invalid/v1",
		Compat: &config.ProviderCompatConfig{ChatCompletions: &config.ChatCompletionsCompatConfig{MCPSystemToolsMessage: new(true)}},
		Models: map[string]config.ModelConfig{
			"model-1": {
				Limit:  config.ModelLimit{Context: 128000, Output: 4096},
				Compat: &config.ModelCompatConfig{ChatCompletions: &config.ChatCompletionsCompatConfig{MCPSystemToolsMessage: new(false)}},
			},
		},
	}, []string{"test-key"})
	client = NewClient(modelOverride, &recordingProvider{}, "model-1", 512, "")
	if client.SupportsKimiDynamicTools("sample/model-1") {
		t.Fatal("model-level false must override provider-level true")
	}
}

func TestSupportsKimiDynamicToolsOptInIsAuthoritative(t *testing.T) {
	for _, model := range []string{"model-1", "custom-compatible-model"} {
		client := NewClient(kimiChatProviderConfig(model, true), &recordingProvider{}, model, 512, "")
		if !client.SupportsKimiDynamicTools("sample/"+model) || !client.SupportsKimiDynamicTools("") {
			t.Fatalf("explicit opt-in did not enable %q", model)
		}
	}
	client := NewClient(kimiChatProviderConfig("dynamic-tools-model", false), &recordingProvider{}, "dynamic-tools-model", 512, "")
	if client.SupportsKimiDynamicTools("sample/dynamic-tools-model") {
		t.Fatal("model name alone must not enable dynamic tools")
	}
}

func TestAllPoolTargetsSupportKimiDynamicToolsRejectsMixedPool(t *testing.T) {
	primary := kimiChatProviderConfig("model-1", true)
	fallback := kimiChatProviderConfig("model-2", false)
	client := NewClient(primary, &recordingProvider{}, "model-1", 512, "")
	client.SetFallbackModels([]FallbackModel{{ProviderConfig: fallback, ModelID: "model-2", MaxTokens: 512}})
	if client.AllPoolTargetsSupportKimiDynamicTools() {
		t.Fatal("mixed capability pool must use top-level tools")
	}
}

func TestConvertMessagesToOpenAIRendersSystemToolsMessage(t *testing.T) {
	messages := []message.Message{
		{Role: message.RoleUser, Content: "hello"},
		message.NewSystemToolsMessage([]message.ToolDefinition{{
			Name:        "mcp_sample_lookup",
			Description: "lookup",
			InputSchema: map[string]any{"type": "object"},
		}}),
	}
	converted := convertMessagesToOpenAI("", modelcompat.WireFamilyOpenAIChat, modelcompat.ReasoningContinuityNone, messages)
	if len(converted) != 2 || converted[1].Role != "system" || len(converted[1].Tools) != 1 {
		t.Fatalf("converted dynamic tools = %#v", converted)
	}
	raw, err := json.Marshal(converted[1])
	if err != nil {
		t.Fatalf("marshal dynamic tools: %v", err)
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		t.Fatalf("unmarshal dynamic tools: %v", err)
	}
	if _, ok := object["content"]; ok {
		t.Fatalf("system tools message must omit content: %s", raw)
	}
	if _, ok := object["tools"]; !ok {
		t.Fatalf("system tools message missing tools: %s", raw)
	}
}

func TestConvertMessagesToOpenAIKeepsNullContentOnToolCallMessages(t *testing.T) {
	messages := []message.Message{
		{Role: message.RoleUser, Content: "hello"},
		{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{{ID: "call-1", Name: "lookup", Args: json.RawMessage(`{}`)}}},
	}
	converted := convertMessagesToOpenAI("", modelcompat.WireFamilyOpenAIChat, modelcompat.ReasoningContinuityNone, messages)
	if len(converted) != 2 || len(converted[1].ToolCalls) != 1 {
		t.Fatalf("converted messages = %#v", converted)
	}
	raw, err := json.Marshal(converted[1])
	if err != nil {
		t.Fatalf("marshal assistant tool-call message: %v", err)
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		t.Fatalf("unmarshal assistant tool-call message: %v", err)
	}
	// The content,omitempty tag exists for the system-tools message above; a
	// text-less assistant tool-call message must still serialize an explicit
	// null so the wire shape matches what strict gateways expect.
	content, ok := object["content"]
	if !ok {
		t.Fatalf("assistant tool-call message dropped content key: %s", raw)
	}
	if string(content) != "null" {
		t.Fatalf("assistant tool-call content = %s, want null", content)
	}
}
