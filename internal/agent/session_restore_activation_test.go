package agent

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/analytics"
	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/thinkingtranslate"
	"github.com/keakon/chord/internal/tools"
)

func TestActivateLoadedSessionUsesLoadedStateWithoutRecomputingMerge(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	loaded := &loadedSessionState{
		SessionPath:            "/tmp/session-123",
		Messages:               []message.Message{{Role: "user", Content: "hi"}},
		TodoItems:              []tools.TodoItem{{ID: "todo-1", Status: "pending", Content: "from loaded"}},
		UsageStats:             analytics.SessionStats{InputTokens: 7, OutputTokens: 3, LLMCalls: 2},
		ContextUsage:           message.TokenUsage{InputTokens: 7, OutputTokens: 3},
		LastInputTokens:        11,
		LastTotalContextTokens: 29,
		ActiveRole:             "reviewer",
	}

	result := a.activateLoadedSession(loaded)
	if result.SessionPath != loaded.SessionPath || result.MessageCount != 1 || result.TodoCount != 1 {
		t.Fatalf("activateLoadedSession result = %+v, want loaded counts/path", result)
	}
	if got := a.GetTodos(); len(got) != 1 || got[0].Content != "from loaded" {
		t.Fatalf("GetTodos() = %+v, want loaded todos copied verbatim", got)
	}
	stats := a.GetUsageStats()
	if stats.LLMCalls != 2 || stats.InputTokens != 7 || stats.OutputTokens != 3 {
		t.Fatalf("GetUsageStats() = %+v, want loaded usage stats", stats)
	}
	current, _ := a.GetContextStats()
	if current != 11 {
		t.Fatalf("GetContextStats current = %d, want loaded input tokens 11", current)
	}
}

func TestActivateLoadedSessionDoesNotReplayThinkingTranslationsAcrossSessions(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.globalConfig = &config.Config{ThinkingTranslation: &config.ThinkingTranslationConfig{TargetLanguage: "zh-Hans"}}
	loaded := &loadedSessionState{
		SessionPath: "/tmp/session-789",
		Messages: []message.Message{
			{Role: "user", Content: "请用中文"},
			{Role: "assistant", ThinkingBlocks: []message.ThinkingBlock{{Thinking: "Hello reasoning"}}, Content: "done"},
		},
	}

	_ = a.activateLoadedSession(loaded)

	for _, evt := range drainAgentEvents(a.Events()) {
		if _, ok := evt.(ThinkingTranslatedEvent); ok {
			t.Fatal("did not expect ThinkingTranslatedEvent replay during restore without cross-session cache")
		}
	}
}

func TestThinkingTranslationServiceUsesProjectConfigOverride(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.globalConfig = &config.Config{
		ThinkingTranslation: &config.ThinkingTranslationConfig{TargetLanguage: "ja", ModelPool: "global-pool"},
		ModelPools:          map[string][]string{"global-pool": {"openai/global"}},
	}
	a.projectConfig = &config.Config{
		ThinkingTranslation: &config.ThinkingTranslationConfig{TargetLanguage: "zh-Hans", ModelPool: "project-pool"},
		ModelPools:          map[string][]string{"project-pool": {"openai/project"}},
	}
	a.modelSwitchFactory = func(providerModel string) (*llm.Client, string, int, error) {
		return newAuxModelPoolTestClient("openai", strings.TrimPrefix(providerModel, "openai/")), providerModel, 8192, nil
	}

	cfg := effectiveThinkingTranslationConfig(a.globalConfig, a.projectConfig)
	if cfg == nil {
		t.Fatal("effectiveThinkingTranslationConfig() = nil, want project override")
	}
	if cfg.TargetLanguage != "zh-Hans" || cfg.ModelPool != "project-pool" {
		t.Fatalf("effectiveThinkingTranslationConfig() = %+v, want project override", cfg)
	}
	if svc := a.thinkingTranslationService(); svc == nil {
		t.Fatal("thinkingTranslationService() = nil, want initialized service")
	} else {
		if svc.TargetLang != "zh-Hans" {
			t.Fatalf("svc.TargetLang = %q, want %q", svc.TargetLang, "zh-Hans")
		}
		if svc.ModelPool != "project-pool" {
			t.Fatalf("svc.ModelPool = %q, want %q", svc.ModelPool, "project-pool")
		}
	}
}

func TestNewAuxModelPoolClientFallsBackAcrossRefs(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	calls := make([]string, 0, 2)
	a.modelSwitchFactory = func(providerModel string) (*llm.Client, string, int, error) {
		calls = append(calls, providerModel)
		switch providerModel {
		case "bad/ref":
			return nil, "", 0, fmt.Errorf("boom")
		case "good/ref":
			return newAuxModelPoolTestClient("good", "ref"), providerModel, 8192, nil
		default:
			return nil, "", 0, fmt.Errorf("unexpected ref %q", providerModel)
		}
	}

	client, err := a.newAuxModelPoolClient([]string{"bad/ref", "good/ref"}, 0, 2048)
	if err != nil {
		t.Fatalf("newAuxModelPoolClient() error = %v, want nil", err)
	}
	if client == nil {
		t.Fatal("newAuxModelPoolClient() returned nil client")
	}
	if !reflect.DeepEqual(calls, []string{"bad/ref", "good/ref"}) {
		t.Fatalf("modelSwitchFactory calls = %v, want ordered fallback", calls)
	}
	if got := client.OutputTokenMax(); got != 2048 {
		t.Fatalf("client.OutputTokenMax() = %d, want 2048", got)
	}
}

func TestNewAuxModelPoolClientReturnsFirstErrorWhenAllRefsFail(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.modelSwitchFactory = func(providerModel string) (*llm.Client, string, int, error) {
		return nil, "", 0, fmt.Errorf("failed %s", providerModel)
	}

	_, err := a.newAuxModelPoolClient([]string{"first/ref", "second/ref"}, 0, 0)
	if err == nil {
		t.Fatal("newAuxModelPoolClient() error = nil, want first failure")
	}
	if got := err.Error(); got != "failed first/ref" {
		t.Fatalf("newAuxModelPoolClient() error = %q, want %q", got, "failed first/ref")
	}
}

func TestActivateLoadedSessionKeepsRepairedEmptyHistoryCleared(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	loaded := &loadedSessionState{
		SessionPath:            "/tmp/session-456",
		Messages:               []message.Message{{Role: "tool", ToolCallID: "ghost", Content: "orphan"}},
		LastInputTokens:        11,
		LastTotalContextTokens: 29,
	}

	result := a.activateLoadedSession(loaded)
	if result.MessageCount != 0 {
		t.Fatalf("activateLoadedSession result.MessageCount = %d, want 0 after orphan repair", result.MessageCount)
	}
	if got := len(a.GetMessages()); got != 0 {
		t.Fatalf("len(GetMessages()) = %d, want 0 after orphan repair", got)
	}
	current, _ := a.GetContextStats()
	if current != 0 {
		t.Fatalf("GetContextStats current = %d, want 0 after orphan repair", current)
	}
	if got := a.ctxMgr.LastTotalContextTokens(); got != 0 {
		t.Fatalf("LastTotalContextTokens() = %d, want 0 after orphan repair", got)
	}
}

func TestThinkingTranslationIgnoresCanceledTurnContext(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	svc, err := thinkingtranslate.NewService()
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	svc.TargetLang = "zh-Hans"
	svc.ModelPool = "translation"
	calls := 0
	svc.SetTranslator(agentTestChunkTranslator{translate: func(ctx context.Context, targetLang, chunk string) (string, error) {
		if targetLang != "zh-Hans" {
			t.Fatalf("targetLang = %q, want zh-Hans", targetLang)
		}
		calls++
		return "翻译:" + chunk, nil
	}})
	a.thinkingTranslateSvc = svc

	turnCtx, cancel := context.WithCancel(context.Background())
	cancel()
	a.turn = &Turn{Ctx: turnCtx, Cancel: cancel}
	a.ctxMgr.Append(message.Message{Role: "user", Content: "请用中文回复。"})
	a.ctxMgr.Append(message.Message{
		Role: "assistant",
		ThinkingBlocks: []message.ThinkingBlock{
			{Thinking: "First thinking block."},
			{Thinking: "Second thinking block."},
		},
	})

	a.maybeTranslateLatestThinkingAfterIdle(1)
	a.outputWg.Wait()

	if calls != 2 {
		t.Fatalf("translator calls = %d, want 2", calls)
	}
	gotEvents := 0
	for _, evt := range drainAgentEvents(a.Events()) {
		if translated, ok := evt.(ThinkingTranslatedEvent); ok {
			gotEvents++
			if translated.TargetLang != "zh-Hans" {
				t.Fatalf("translated.TargetLang = %q, want zh-Hans", translated.TargetLang)
			}
		}
	}
	if gotEvents != 2 {
		t.Fatalf("ThinkingTranslatedEvent count = %d, want 2", gotEvents)
	}
}

type agentTestChunkTranslator struct {
	translate func(ctx context.Context, targetLang, chunk string) (string, error)
}

func (s agentTestChunkTranslator) TranslateChunk(ctx context.Context, targetLang, chunk string) (string, error) {
	return s.translate(ctx, targetLang, chunk)
}
