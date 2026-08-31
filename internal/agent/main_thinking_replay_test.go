package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
)

// newThinkingReplayClient builds an llm.Client whose model pool cursor targets
// a DeepSeek-style thinking model with visible reasoning replay.
func newThinkingReplayClient(t *testing.T) *llm.Client {
	t.Helper()
	providerCfg := llm.NewProviderConfig("deepseek", config.ProviderConfig{
		Type: config.ProviderTypeChatCompletions,
		Models: map[string]config.ModelConfig{
			"deepseek-v4-pro": {Compat: &config.ModelCompatConfig{
				ReasoningContinuity: &config.ReasoningContinuityCompatConfig{Mode: "openai_visible"},
			}},
		},
	}, []string{"k"})
	return llm.NewClient(providerCfg, &recordingLengthRecoveryProvider{}, "deepseek-v4-pro", 1024, "")
}

// newPlainThinkingClient builds an llm.Client targeting a plain chat model
// without visible reasoning replay support.
func newPlainThinkingClient(t *testing.T) *llm.Client {
	t.Helper()
	providerCfg := llm.NewProviderConfig("openai", config.ProviderConfig{
		Type: config.ProviderTypeChatCompletions,
		Models: map[string]config.ModelConfig{
			"gpt-4o": {},
		},
	}, []string{"k"})
	return llm.NewClient(providerCfg, &recordingLengthRecoveryProvider{}, "gpt-4o", 1024, "")
}

func TestStashTruncatedThinkingReplayDeepSeek(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.llmClient = newThinkingReplayClient(t)
	a.newTurn()

	a.stashTruncatedThinkingReplay(&LLMResponsePayload{
		ReasoningContent: "deep thinking about the problem",
		StopReason:       "length",
	})
	if a.pendingThinkingReplayPrefix == nil {
		t.Fatal("expected thinking replay prefix to be stashed for DeepSeek-style target")
	}
	if a.pendingThinkingReplayPrefix.Content != "" {
		t.Fatalf("prefix content = %q, want empty (thinking-only)", a.pendingThinkingReplayPrefix.Content)
	}
	if a.pendingThinkingReplayPrefix.Kind != message.KindThinkingReplayPrefix {
		t.Fatalf("prefix kind = %q, want %q", a.pendingThinkingReplayPrefix.Kind, message.KindThinkingReplayPrefix)
	}
	if a.pendingThinkingReplayPrefix.ReasoningContent != "deep thinking about the problem" {
		t.Fatalf("prefix reasoning = %q", a.pendingThinkingReplayPrefix.ReasoningContent)
	}
	if a.pendingThinkingReplayRef != "deepseek/deepseek-v4-pro" {
		t.Fatalf("bound ref = %q, want deepseek/deepseek-v4-pro", a.pendingThinkingReplayRef)
	}
}

func TestTakePendingThinkingReplayPrefixDropsOnTurnChange(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.llmClient = newThinkingReplayClient(t)
	a.newTurn()
	a.stashTruncatedThinkingReplay(&LLMResponsePayload{ReasoningContent: "old turn reasoning", StopReason: "length"})

	a.newTurn()
	if prefix := a.takePendingThinkingReplayPrefix(); prefix != nil {
		t.Fatalf("expected prefix dropped after turn change, got %+v", prefix)
	}
}

func TestStashTruncatedThinkingReplaySkipsWhenNoReasoning(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.llmClient = newThinkingReplayClient(t)
	a.newTurn()

	a.stashTruncatedThinkingReplay(&LLMResponsePayload{StopReason: "length"})
	if a.pendingThinkingReplayPrefix != nil {
		t.Fatal("expected no prefix when response has no reasoning text")
	}
	a.stashTruncatedThinkingReplay(&LLMResponsePayload{ReasoningContent: "   ", StopReason: "length"})
	if a.pendingThinkingReplayPrefix != nil {
		t.Fatal("expected no prefix when reasoning text is blank")
	}
}

func TestStashTruncatedThinkingReplayIsBoundedToFirstRecovery(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.llmClient = newThinkingReplayClient(t)
	a.newTurn()

	a.stashTruncatedThinkingReplay(&LLMResponsePayload{ReasoningContent: "repeated reasoning", StopReason: "length"})
	a.takePendingThinkingReplayPrefix()
	a.stashTruncatedThinkingReplay(&LLMResponsePayload{ReasoningContent: "repeated reasoning", StopReason: "length"})
	if a.pendingThinkingReplayPrefix != nil {
		t.Fatal("expected later recovery round to use the ordinary prompt without another reasoning replay")
	}
}

func TestStashTruncatedThinkingReplaySkipsUnsupportedTarget(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.llmClient = newPlainThinkingClient(t)
	a.newTurn()

	a.stashTruncatedThinkingReplay(&LLMResponsePayload{
		ReasoningContent: "thinking on a plain chat model",
		StopReason:       "length",
	})
	if a.pendingThinkingReplayPrefix != nil {
		t.Fatal("expected no prefix when target does not support visible reasoning replay")
	}
}

func TestTakePendingThinkingReplayPrefixRefMatch(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.llmClient = newThinkingReplayClient(t)
	a.newTurn()
	a.stashTruncatedThinkingReplay(&LLMResponsePayload{ReasoningContent: "continued reasoning", StopReason: "length"})

	prefix := a.takePendingThinkingReplayPrefix()
	if prefix == nil {
		t.Fatal("expected prefix when ref matches and target supports replay")
	}
	if prefix.ReasoningContent != "continued reasoning" {
		t.Fatalf("prefix reasoning = %q", prefix.ReasoningContent)
	}
	// One-shot: a second take must be empty.
	if again := a.takePendingThinkingReplayPrefix(); again != nil {
		t.Fatal("expected prefix to be consumed after one take")
	}
}

func TestTakePendingThinkingReplayPrefixDropsOnModelSwitch(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.llmClient = newThinkingReplayClient(t)
	a.newTurn()
	a.stashTruncatedThinkingReplay(&LLMResponsePayload{ReasoningContent: "reasoning bound to deepseek", StopReason: "length"})

	// User switches the model before the recovery request: cursor now targets a
	// different model, so the prefix must be dropped rather than replayed to a
	// model that did not produce it.
	a.llmClient = newPlainThinkingClient(t)
	if prefix := a.takePendingThinkingReplayPrefix(); prefix != nil {
		t.Fatalf("expected prefix dropped on model switch, got %+v", prefix)
	}
	if a.pendingThinkingReplayPrefix != nil {
		t.Fatal("expected prefix cleared after dropping")
	}
}

func TestTakePendingThinkingReplayPrefixDropsOnCapabilityLost(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.llmClient = newThinkingReplayClient(t)
	a.newTurn()
	a.stashTruncatedThinkingReplay(&LLMResponsePayload{ReasoningContent: "reasoning", StopReason: "length"})

	// Same provider/model ref but the model config no longer enables visible
	// reasoning (e.g. provider compat changed): capability is re-checked at
	// request boundary and must drop the prefix.
	plain := newPlainThinkingClient(t)
	a.llmClient = plain
	if prefix := a.takePendingThinkingReplayPrefix(); prefix != nil {
		t.Fatalf("expected prefix dropped on capability loss, got %+v", prefix)
	}
}

// TestHandleLLMResponseTruncatedNoOutputStashesThinking drives the real
// truncation handler: a max_tokens response that spent its whole budget on
// reasoning (no visible content, no tool calls) must stash the thinking for a
// DeepSeek-style target and enter length recovery.
func TestHandleLLMResponseTruncatedNoOutputStashesThinking(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.llmClient = newThinkingReplayClient(t)
	a.newTurn()

	a.handleLLMResponse(Event{Type: EventLLMResponse, TurnID: a.turn.ID, Payload: &LLMResponsePayload{
		ReasoningContent: "113KB of truncated reasoning",
		StopReason:       "max_tokens",
	}})

	if a.pendingThinkingReplayPrefix == nil {
		t.Fatal("expected truncated thinking stashed by truncation handler")
	}
	if a.pendingThinkingReplayPrefix.ReasoningContent != "113KB of truncated reasoning" {
		t.Fatalf("stashed reasoning = %q", a.pendingThinkingReplayPrefix.ReasoningContent)
	}
	if !a.turn.InLengthRecovery {
		t.Fatal("expected turn to enter length recovery")
	}
}

// TestHandleLLMResponseTruncatedNoOutputPlainModelKeepsDiscard verifies the
// plain-model path keeps discarding the truncated response (no prefix).
func TestHandleLLMResponseTruncatedNoOutputPlainModelKeepsDiscard(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.llmClient = newPlainThinkingClient(t)
	a.newTurn()

	a.handleLLMResponse(Event{Type: EventLLMResponse, TurnID: a.turn.ID, Payload: &LLMResponsePayload{
		ReasoningContent: "reasoning on a plain chat model",
		StopReason:       "max_tokens",
	}})

	if a.pendingThinkingReplayPrefix != nil {
		t.Fatal("expected no prefix for plain chat model")
	}
	if !a.turn.InLengthRecovery {
		t.Fatal("expected turn to enter length recovery regardless")
	}
}

// TestCallLLMInjectsThinkingReplayPrefixWireOnly assembles a real LLM request
// and verifies the stashed prefix is injected as a wire-only assistant message
// right before the recovery overlay, and that neither is persisted to ctxMgr.
func TestCallLLMInjectsThinkingReplayPrefixWireOnly(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.llmClient = newThinkingReplayClient(t)
	a.newTurn()
	a.stashTruncatedThinkingReplay(&LLMResponsePayload{ReasoningContent: "wire-only reasoning", StopReason: "length"})
	a.pendingRecoveryPrompt = "continue from where you stopped"

	capture := &captureMessagesProvider{}
	a.llmClient = llm.NewClient(
		llm.NewProviderConfig("deepseek", config.ProviderConfig{
			Type: config.ProviderTypeChatCompletions,
			Models: map[string]config.ModelConfig{
				"deepseek-v4-pro": {Compat: &config.ModelCompatConfig{
					ReasoningContinuity: &config.ReasoningContinuityCompatConfig{Mode: "openai_visible"},
				}},
			},
		}, []string{"k"}),
		capture, "deepseek-v4-pro", 1024, "",
	)
	a.markAgentsMDReady()
	a.MarkSkillsReady()
	a.markMCPReady()

	_, err := a.callLLM(context.Background(), []message.Message{
		{Role: "user", Content: "original question"},
	})
	if err != nil {
		t.Fatalf("callLLM: %v", err)
	}
	if len(capture.messages) == 0 {
		t.Fatal("expected at least one captured request")
	}
	msgs := capture.messages
	// Find the assistant prefix and confirm it precedes the recovery overlay.
	var prefixIdx, overlayIdx = -1, -1
	for i, m := range msgs {
		if m.Role == "assistant" && m.ReasoningContent == "wire-only reasoning" && m.Content == "" {
			prefixIdx = i
		}
		if m.Role == "user" && strings.Contains(m.Content, "continue from where you stopped") {
			overlayIdx = i
		}
	}
	if prefixIdx < 0 {
		t.Fatalf("expected wire-only assistant thinking prefix in request messages: %+v", msgs)
	}
	if overlayIdx < 0 {
		t.Fatalf("expected recovery overlay in request messages: %+v", msgs)
	}
	if prefixIdx > overlayIdx {
		t.Fatalf("prefix (idx %d) must precede recovery overlay (idx %d)", prefixIdx, overlayIdx)
	}
	// Nothing wire-only may reach ctxMgr durable history.
	snapshot := a.ctxMgr.Snapshot()
	for _, m := range snapshot {
		if m.Role == "assistant" && strings.TrimSpace(m.ReasoningContent) != "" && m.Content == "" {
			t.Fatalf("wire-only thinking prefix leaked into ctxMgr: %+v", m)
		}
		if strings.Contains(m.Content, "continue from where you stopped") {
			t.Fatalf("recovery overlay leaked into ctxMgr: %+v", m)
		}
	}
}
