package agent

import (
	"strings"
	"testing"
	"time"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
)

func TestCacheExpectationTracksTextBeforeAttachment(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	cfg := llm.NewProviderConfig("sample", config.ProviderConfig{Type: config.ProviderTypeResponses, Models: map[string]config.ModelConfig{"gpt-6.1-sol": {}}}, nil)
	a.llmClient = llm.NewClient(cfg, &recordingLoopTuningProvider{}, "gpt-6.1-sol", 4096, "sys")
	history := []message.Message{
		{Role: message.RoleUser, Parts: []message.ContentPart{{Type: message.ContentPartText, Text: strings.Repeat("Sample ", 60)}, {Type: message.ContentPartPDF, Data: []byte("%PDF-sample")}}},
		{Role: message.RoleUser, Kind: message.KindTurnOverlay, Content: "Sample hint"},
	}
	hash := a.computeToolDefinitionHash()
	a.noteCacheExpectation("sample/gpt-6.1-sol", history, 1, hash, time.Now(), nil)
	history[0].Parts[1].Data = []byte("%PDF-other")
	diag := a.noteCacheExpectation("sample/gpt-6.1-sol", history, 1, hash, time.Now(), nil)
	if diag["cache_messages"] != "1" || diag["cache_expected_tokens"] == "0" || diag["cache_prefix_divergence"] != "1" {
		t.Fatalf("attachment outside boundary affected cached text: %v", diag)
	}
	history[0].Parts[0].Text = strings.Repeat("Changed ", 60)
	diag = a.noteCacheExpectation("sample/gpt-6.1-sol", history, 1, hash, time.Now(), nil)
	if diag["cache_prefix_divergence"] != "0" || diag["cache_divergence_kind"] != "rewrite" {
		t.Fatalf("text rewrite before boundary went undetected: %v", diag)
	}
}
