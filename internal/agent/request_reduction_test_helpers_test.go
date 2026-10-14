package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/ctxmgr"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/tools"
)

func contextReductionStatsEqual(a, b ContextReductionStats) bool {
	if a.Messages != b.Messages ||
		a.Bytes != b.Bytes ||
		a.TokensBefore != b.TokensBefore ||
		a.TokensAfter != b.TokensAfter ||
		a.TokensSaved != b.TokensSaved ||
		a.Protected != b.Protected ||
		a.ReusedStable != b.ReusedStable ||
		len(a.ByToolAndRule) != len(b.ByToolAndRule) ||
		len(a.SkippedByReason) != len(b.SkippedByReason) ||
		len(a.OverCompression) != len(b.OverCompression) {
		return false
	}
	for key, av := range a.ByToolAndRule {
		if bv, ok := b.ByToolAndRule[key]; !ok || bv != av {
			return false
		}
	}
	for key, av := range a.SkippedByReason {
		if bv, ok := b.SkippedByReason[key]; !ok || bv != av {
			return false
		}
	}
	for key, av := range a.OverCompression {
		if bv, ok := b.OverCompression[key]; !ok || bv != av {
			return false
		}
	}
	return true
}

func isShellSuccessSummary(content string) bool {
	return (strings.Contains(content, "[Older "+tools.NameShell+" success summarized for this request to save context;") ||
		strings.Contains(content, "[Older "+tools.NameShell+" go test success summarized for this request to save context;")) &&
		strings.Contains(content, "bytes=") &&
		strings.Contains(content, "lines=")
}

func focusTestSubAgent(t *testing.T, a *MainAgent, instanceID, providerName, preset string) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	providerCfg := llm.NewProviderConfig(providerName, config.ProviderConfig{
		Preset: preset,
		Type:   config.ProviderTypeMessages,
		Models: map[string]config.ModelConfig{
			"model": {Limit: config.ModelLimit{Context: 8192, Output: 1024}},
		},
	}, []string{"test-key"})
	sub := &SubAgent{
		instanceID: instanceID,
		llmClient:  llm.NewClient(providerCfg, stubProvider{}, "model", 1024, ""),
		parent:     a,
		parentCtx:  ctx,
		cancel:     cancel,
		recovery:   a.recoveryManager(),
		ctxMgr:     ctxmgr.NewManager(100, 0),
	}
	a.subs.mu.Lock()
	a.subs.subAgents[instanceID] = sub
	a.subs.mu.Unlock()
	a.focusedAgent.Store(sub)
}
