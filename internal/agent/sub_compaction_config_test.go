package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
)

func TestSubAgentCompactionUsesCatalogAndExplicitOverrides(t *testing.T) {
	for _, tc := range []struct {
		name      string
		global    string
		model     string
		threshold float64
	}{
		{"catalog recommendation", "", "", 0.1},
		{"explicit global", "context:\n  compaction: {threshold: 0.4}\n", "", 0.4},
		{"explicit model", "context:\n  compaction: {threshold: 0.4}\n", "        compaction: {threshold: 0.6}\n", 0.6},
		{"explicit disabled", "", "        compaction: {threshold: 0}\n", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			body := tc.global + "providers:\n  anthropic:\n    preset: anthropic\n    models:\n      claude-haiku-5-5:\n        thinking: {type: adaptive, effort: medium}\n" + tc.model + "model_pools:\n  default: [anthropic/claude-haiku-5-5]\n"
			if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			rc, err := config.LoadResolvedConfig(path, "")
			if err != nil {
				t.Fatal(err)
			}
			if len(rc.Diagnostics) != 0 {
				t.Fatalf("diagnostics = %+v, want none", rc.Diagnostics)
			}
			cfg := rc.Config
			parent, sub := newMixedBatchTestSubAgent(t)
			parent.globalConfig = cfg
			provider := llm.NewProviderConfig("anthropic", cfg.Providers["anthropic"], []string{"key"})
			client := llm.NewClient(provider, stubProvider{}, "claude-haiku-5-5", 1024, "")
			sub.switchModel(client, "claude-haiku-5-5", 1000000)
			if got := sub.applyModelCompactionConfig(); got != tc.threshold {
				t.Fatalf("threshold = %v, want %v", got, tc.threshold)
			}
		})
	}
}

func TestSubAgentCompactionUsesSharedModelThreshold(t *testing.T) {
	for _, tc := range []struct {
		name    string
		global  float64
		model   *float64
		scale   int
		compact bool
	}{
		{"global inherited", 0.8, nil, 2, false},
		{"global early", 0.3, nil, 2, true},
		{"model early", 0.8, new(0.3), 2, true},
		{"model later", 0.3, new(0.8), 2, false},
		{"model disabled", 0.3, new(0.0), 2, false},
		{"global disabled", 0, nil, 2, false},
		{"full budget threshold", 0.8, new(1.0), 1, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			parent, sub := newMixedBatchTestSubAgent(t)
			messages := []message.Message{{Role: message.RoleUser, Content: "task"}}
			for range 12 {
				messages = append(messages,
					message.Message{Role: message.RoleAssistant, Content: strings.Repeat("analysis ", 160)},
					message.Message{Role: message.RoleUser, Content: "continue"},
				)
			}
			sub.ctxMgr.RestoreMessages(messages)
			estimated := estimateMessagesTokens(sub.ctxMgr, messages)
			mc := config.ModelConfig{Limit: config.ModelLimit{Context: estimated * 4, Input: estimated * tc.scale, Output: 256}}
			if tc.model != nil {
				mc.Compaction = &config.ModelCompactionConfig{Threshold: tc.model}
			}
			cfg := config.DefaultConfig()
			cfg.Context.Compaction.Threshold = tc.global
			cfg.Providers = map[string]config.ProviderConfig{
				"sample": {Type: config.ProviderTypeMessages, Models: map[string]config.ModelConfig{"worker": mc}},
			}
			parent.globalConfig = cfg
			client := llm.NewClient(llm.NewProviderConfig("sample", cfg.Providers["sample"], []string{"key"}), stubProvider{}, "worker", 256, "")
			sub.switchModel(client, "worker", mc.Limit.Context)
			// Discard the system-prompt change to isolate the threshold boundary.
			sub.ctxMgr.RestoreMessages(messages)
			prepared := sub.prepareContextForLLM(messages)
			if got := len(prepared) < len(messages); got != tc.compact {
				t.Fatalf("compacted = %v, want %v; message counts = %d/%d", got, tc.compact, len(messages), len(prepared))
			}
		})
	}
}

func TestSubAgentCompactionFollowsModelPoolCursorAndReservation(t *testing.T) {
	parent, sub := newMixedBatchTestSubAgent(t)
	cfg := config.DefaultConfig()
	cfg.Context.Compaction.Reserved = 100
	cfg.Providers = map[string]config.ProviderConfig{
		"sample": {
			Type: config.ProviderTypeMessages,
			Models: map[string]config.ModelConfig{
				"model-1": {Limit: config.ModelLimit{Context: 12000, Input: 10000, Output: 1000}, Compaction: &config.ModelCompactionConfig{Threshold: new(0.2)}},
				"model-2": {Limit: config.ModelLimit{Context: 24000, Input: 20000, Output: 2000}, Compaction: &config.ModelCompactionConfig{Threshold: new(0.6)}},
			},
		},
	}
	parent.globalConfig = cfg
	provider := llm.NewProviderConfig("sample", cfg.Providers["sample"], []string{"key"})
	client := llm.NewClient(provider, stubProvider{}, "model-1", 1000, "")
	sub.switchModel(client, "model-1", 12000)
	pool := []llm.FallbackModel{
		{ProviderConfig: provider, ProviderImpl: stubProvider{}, ModelID: "model-1", MaxTokens: 1000, ContextLimit: 12000},
		{ProviderConfig: provider, ProviderImpl: stubProvider{}, ModelID: "model-2", MaxTokens: 2000, ContextLimit: 24000},
	}
	for _, tc := range []struct {
		cursor    int
		threshold float64
		budget    int
	}{{0, 0.2, 9900}, {1, 0.6, 19900}, {0, 0.2, 9900}} {
		client.SetModelPool(pool, tc.cursor)
		if got := sub.applyModelCompactionConfig(); got != tc.threshold {
			t.Fatalf("cursor %d threshold = %v, want %v", tc.cursor, got, tc.threshold)
		}
		sub.prepareContextForLLM([]message.Message{
			{Role: message.RoleUser, Content: "task"},
			{Role: message.RoleAssistant, Content: "working"},
			{Role: message.RoleUser, Content: "continue"},
		})
		if got := sub.ctxMgr.GetUsableCompactionBudget(); got != tc.budget {
			t.Fatalf("cursor %d budget = %d, want %d", tc.cursor, got, tc.budget)
		}
	}
}
