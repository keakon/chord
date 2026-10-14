package agent

import (
	"context"
	"testing"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/contextdiag"
	"github.com/keakon/chord/internal/ctxmgr"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
)

func TestMainContextDiagnosticsRequestAndHistory(t *testing.T) {
	a := newReadyTestMainAgent(t)
	cfg := llm.NewProviderConfig("sample", config.ProviderConfig{Type: config.ProviderTypeChatCompletions, Models: map[string]config.ModelConfig{"test-model": {Limit: config.ModelLimit{Context: 128000, Output: 4096}}}}, []string{"sample-key"})
	p := &blockingStreamProvider{calls: []scriptedStreamCall{{resp: &message.Response{Content: "sample", Usage: &message.TokenUsage{InputTokens: 10}}}}}
	client := llm.NewClient(cfg, p, "test-model", 4096, "system")
	a.swapLLMClientWithRef(client, "test-model", 128000, "sample/test-model")
	a.ctxMgr.Append(message.Message{Role: message.RoleUser, Content: "sample"})
	if _, err := a.callLLMForRequest(context.Background(), snapshotLLMRequest(a.ctxMgr), 0); err != nil {
		t.Fatal(err)
	}
	v := a.FocusedContextDiagnostics()
	if !v.Available || v.Historical || v.Snapshot.State != contextdiag.Completed || v.Snapshot.Usage.FullInput != 10 {
		t.Fatalf("snapshot %+v", v)
	}
	a.ctxMgr.Append(message.Message{Role: message.RoleAssistant, Content: "sample"})
	if a.FocusedContextDiagnostics().Historical {
		t.Fatal("append invalidated snapshot")
	}
	a.ctxMgr.RestoreMessages(a.ctxMgr.Snapshot())
	if !a.FocusedContextDiagnostics().Historical {
		t.Fatal("rewrite not marked historical")
	}
	before := v.Snapshot.ID.Binding.Epoch
	a.contextDiagnostics.invalidate()
	a.contextDiagnostics.reset()
	if a.FocusedContextDiagnostics().Available {
		t.Fatal("new binding inherited snapshot")
	}
	d := a.contextDiagnostics.begin("sample", "main", 0)
	if d == nil {
		t.Fatal("new binding unavailable")
	}
	if a.contextDiagnostics.collector == nil || diagnosticsEpoch.Load() <= before {
		t.Fatal("epoch not advanced")
	}
}
func TestDiagnosticReductionFrozenRatioAndUnavailableWorker(t *testing.T) {
	a := &MainAgent{}
	before := []message.Message{{Role: message.RoleUser, Content: "sample sample sample"}}
	after := []message.Message{{Role: message.RoleUser, Content: "sample"}}
	a.freezeDiagnosticReduction(before, after, 0.5)
	r := a.diagnosticReduction()
	if r.Before != int64(ctxmgr.EstimateMessagesTokensWithRatio(before, 0.5)) || r.After != int64(ctxmgr.EstimateMessagesTokensWithRatio(after, 0.5)) {
		t.Fatalf("ratio mismatch %+v", r)
	}
	var runtime contextDiagnosticsRuntime
	observer := runtime.begin("sample", "worker", 0)
	if observer == nil {
		t.Fatal("no worker observer")
	}
	runtime.invalidate()
	if runtime.begin("sample", "worker", 0) != nil {
		t.Fatal("retired runtime resurrected")
	}
}
