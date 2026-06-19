package agent

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/modelcompat"
	"github.com/keakon/chord/internal/recovery"
	"github.com/keakon/chord/internal/thinkingtranslate"
)

func TestCallLLMPromotesStreamingActivityOnToolUseStartWithoutStatusDelta(t *testing.T) {
	a := newReadyTestMainAgent(t)

	providerCfg := llm.NewProviderConfig("sample", config.ProviderConfig{
		Type: config.ProviderTypeChatCompletions,
		Models: map[string]config.ModelConfig{
			"test-model": {Limit: config.ModelLimit{Context: 128000, Output: 4096}},
		},
	}, []string{"test-key"})

	providerImpl := &blockingStreamProvider{
		calls: []scriptedStreamCall{{
			streams: []message.StreamDelta{{
				Type: "tool_use_start",
				ToolCall: &message.ToolCallDelta{
					ID:    "call-1",
					Name:  "Read",
					Input: `{"path":"README.md"}`,
				},
			}},
			resp: &message.Response{
				ToolCalls: []message.ToolCall{{
					ID:   "call-1",
					Name: "read",
					Args: []byte(`{"path":"README.md"}`),
				}},
				StopReason: "tool_use",
			},
		}},
	}

	client := llm.NewClient(providerCfg, providerImpl, "test-model", 4096, "sys")
	a.swapLLMClientWithRef(client, "test-model", 128000, "sample/test-model")

	_, err := a.callLLM(context.Background(), []message.Message{{Role: "user", Content: "hi"}})
	if err != nil {
		t.Fatalf("callLLM: %v", err)
	}

	events := drainAgentEvents(a.Events())
	var sawStreaming bool
	for _, evt := range events {
		act, ok := evt.(AgentActivityEvent)
		if !ok {
			continue
		}
		if act.Type == ActivityStreaming {
			sawStreaming = true
		}
	}
	if !sawStreaming {
		t.Fatal("expected streaming activity promoted from tool_use_start")
	}
}

func TestMainLLMStatusModelRefUpdatesRunningModelBeforeVisibleOutput(t *testing.T) {
	a := newReadyTestMainAgent(t)
	a.SetProviderModelRef("freemodel/gpt-5.5@xhigh")

	state := &mainLLMStreamState{}
	reducer := a.newMainLLMStreamReducer(nil, "freemodel/gpt-5.5@xhigh", "freemodel/gpt-5.5@xhigh", nil, false, state)
	reducer.Handle(message.StreamDelta{
		Type: "status",
		Status: &message.StatusDelta{
			Type:     "waiting_headers",
			ModelRef: "codex/gpt-5.5@xhigh",
		},
	})

	if got := a.RunningModelRef(); got != "codex/gpt-5.5@xhigh" {
		t.Fatalf("RunningModelRef = %q, want codex/gpt-5.5@xhigh", got)
	}
	events := drainAgentEvents(a.Events())
	var saw bool
	for _, evt := range events {
		changed, ok := evt.(RunningModelChangedEvent)
		if !ok {
			continue
		}
		if changed.ProviderModelRef == "freemodel/gpt-5.5@xhigh" && changed.RunningModelRef == "codex/gpt-5.5@xhigh" {
			saw = true
			break
		}
	}
	if !saw {
		t.Fatalf("missing RunningModelChangedEvent for codex retry; events=%#v", events)
	}
}

func TestCallLLMClosesThinkingBeforeFirstText(t *testing.T) {
	a := newReadyTestMainAgent(t)
	a.newTurn()
	if a.turn == nil {
		t.Fatal("expected active turn")
	}

	providerCfg := llm.NewProviderConfig("sample", config.ProviderConfig{
		Type: config.ProviderTypeChatCompletions,
		Models: map[string]config.ModelConfig{
			"test-model": {Limit: config.ModelLimit{Context: 128000, Output: 4096}},
		},
	}, []string{"test-key"})
	providerImpl := &blockingStreamProvider{
		calls: []scriptedStreamCall{{
			streams: []message.StreamDelta{
				{Type: "thinking", Text: "analyzing"},
				{Type: "text", Text: "answer"},
				{Type: "thinking", Text: "late reasoning"},
			},
			resp: &message.Response{Content: "answer", StopReason: "stop"},
		}},
	}

	client := llm.NewClient(providerCfg, providerImpl, "test-model", 4096, "sys")
	a.swapLLMClientWithRef(client, "test-model", 128000, "sample/test-model")

	if _, err := a.callLLM(context.Background(), []message.Message{{Role: "user", Content: "hi"}}); err != nil {
		t.Fatalf("callLLM: %v", err)
	}

	events := drainAgentEvents(a.Events())
	var streamEvents []AgentEvent
	for _, evt := range events {
		switch evt.(type) {
		case ThinkingStartedEvent, StreamThinkingDeltaEvent, StreamThinkingEvent, StreamTextEvent:
			streamEvents = append(streamEvents, evt)
		}
	}
	if len(streamEvents) != 4 {
		t.Fatalf("stream event count = %d, want 4; events=%#v", len(streamEvents), streamEvents)
	}
	if _, ok := streamEvents[0].(ThinkingStartedEvent); !ok {
		t.Fatalf("streamEvents[0] = %T, want ThinkingStartedEvent", streamEvents[0])
	}
	thinkDelta, ok := streamEvents[1].(StreamThinkingDeltaEvent)
	if !ok {
		t.Fatalf("streamEvents[1] = %T, want StreamThinkingDeltaEvent", streamEvents[1])
	}
	if thinkDelta.Text != "analyzing" {
		t.Fatalf("thinking delta text = %q, want analyzing", thinkDelta.Text)
	}
	if _, ok := streamEvents[2].(StreamThinkingEvent); !ok {
		t.Fatalf("streamEvents[2] = %T, want StreamThinkingEvent", streamEvents[2])
	}
	text, ok := streamEvents[3].(StreamTextEvent)
	if !ok {
		t.Fatalf("streamEvents[3] = %T, want StreamTextEvent", streamEvents[3])
	}
	if text.Text != "answer" {
		t.Fatalf("text = %q, want answer", text.Text)
	}
}
func TestCallLLMTranslatesEachStreamingThinkingBlock(t *testing.T) {
	a := newReadyTestMainAgent(t)
	a.ctxMgr.Append(message.Message{Role: "user", Content: "hi"})
	a.newTurn()
	if a.turn == nil {
		t.Fatal("expected active turn")
	}

	svc, err := thinkingtranslate.NewService()
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	svc.TargetLang = "zh-Hans"
	svc.ModelPool = "translation"
	var translatedMu sync.Mutex
	var translated []string
	svc.SetTranslator(agentTestChunkTranslator{translate: func(ctx context.Context, targetLang, chunk string) (string, error) {
		if targetLang != "zh-Hans" {
			t.Fatalf("targetLang = %q, want zh-Hans", targetLang)
		}
		translatedMu.Lock()
		translated = append(translated, chunk)
		translatedMu.Unlock()
		return "翻译:" + chunk, nil
	}})
	a.thinkingTranslateSvc = svc

	providerCfg := llm.NewProviderConfig("sample", config.ProviderConfig{
		Type: config.ProviderTypeChatCompletions,
		Models: map[string]config.ModelConfig{
			"test-model": {Limit: config.ModelLimit{Context: 128000, Output: 4096}},
		},
	}, []string{"test-key"})
	providerImpl := &blockingStreamProvider{
		calls: []scriptedStreamCall{{
			streams: []message.StreamDelta{
				{Type: "thinking", Text: "First streaming thought."},
				{Type: "thinking_end"},
				{Type: "thinking", Text: "Second streaming thought."},
				{Type: "thinking_end"},
			},
			resp: &message.Response{Content: "answer", StopReason: "stop"},
		}},
	}

	client := llm.NewClient(providerCfg, providerImpl, "test-model", 4096, "sys")
	a.swapLLMClientWithRef(client, "test-model", 128000, "sample/test-model")

	if _, err := a.callLLM(context.Background(), a.GetMessages()); err != nil {
		t.Fatalf("callLLM: %v", err)
	}
	a.outputWg.Wait()

	translatedMu.Lock()
	translatedCount := len(translated)
	translatedSet := make(map[string]bool, len(translated))
	for _, chunk := range translated {
		translatedSet[chunk] = true
	}
	translatedMu.Unlock()
	if translatedCount != 2 {
		t.Fatalf("translator calls = %d, want 2; chunks=%#v", translatedCount, translated)
	}
	if !translatedSet["First streaming thought."] || !translatedSet["Second streaming thought."] {
		t.Fatalf("translated chunks = %#v", translated)
	}

	var got []ThinkingTranslatedEvent
	for _, evt := range drainAgentEvents(a.Events()) {
		if translated, ok := evt.(ThinkingTranslatedEvent); ok {
			got = append(got, translated)
		}
	}
	if len(got) != 2 {
		t.Fatalf("ThinkingTranslatedEvent count = %d, want 2; events=%#v", len(got), got)
	}
	byBlock := make(map[int]ThinkingTranslatedEvent, len(got))
	for _, evt := range got {
		byBlock[evt.BlockIndex] = evt
	}
	for i, want := range []string{"First streaming thought.", "Second streaming thought."} {
		evt, ok := byBlock[i]
		if !ok {
			t.Fatalf("missing ThinkingTranslatedEvent for block %d; events=%#v", i, got)
		}
		if evt.MessageID != "msgidx:1" {
			t.Fatalf("block %d MessageID = %q, want msgidx:1", i, evt.MessageID)
		}
		if evt.Translated != "翻译:"+want {
			t.Fatalf("block %d Translated = %q, want %q", i, evt.Translated, "翻译:"+want)
		}
	}
}

func TestStreamingThinkingTranslationCancelledOnRollback(t *testing.T) {
	a := newReadyTestMainAgent(t)

	svc, err := thinkingtranslate.NewService()
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	svc.TargetLang = "zh-Hans"
	svc.ModelPool = "translation"
	firstStarted := make(chan struct{})
	firstCancelled := make(chan struct{})
	var firstStartedOnce sync.Once
	var firstCancelledOnce sync.Once
	var mu sync.Mutex
	calls := 0
	svc.SetTranslator(agentTestChunkTranslator{translate: func(ctx context.Context, targetLang, chunk string) (string, error) {
		mu.Lock()
		calls++
		call := calls
		mu.Unlock()
		if call == 1 {
			firstStartedOnce.Do(func() { close(firstStarted) })
			<-ctx.Done()
			firstCancelledOnce.Do(func() { close(firstCancelled) })
			return "", ctx.Err()
		}
		return "翻译:" + chunk, nil
	}})
	a.thinkingTranslateSvc = svc

	a.scheduleStreamingThinkingTranslation(1, 0, "Failed streaming thought.")
	select {
	case <-firstStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for first translation to start")
	}

	a.cancelStreamingThinkingTranslations(1)
	select {
	case <-firstCancelled:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for rollback to cancel first translation")
	}
	a.outputWg.Wait()

	entries, err := recovery.LoadThinkingTranslations(a.sessionDir)
	if err != nil {
		t.Fatalf("LoadThinkingTranslations: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("entries len = %d, want no stale rollback translation: %#v", len(entries), entries)
	}
	for _, evt := range drainAgentEvents(a.Events()) {
		if translated, ok := evt.(ThinkingTranslatedEvent); ok {
			t.Fatalf("unexpected stale ThinkingTranslatedEvent after rollback: %#v", translated)
		}
	}

	a.scheduleStreamingThinkingTranslation(1, 0, "Retried streaming thought.")
	a.outputWg.Wait()

	var got []ThinkingTranslatedEvent
	for _, evt := range drainAgentEvents(a.Events()) {
		if translated, ok := evt.(ThinkingTranslatedEvent); ok {
			got = append(got, translated)
		}
	}
	if len(got) != 1 {
		t.Fatalf("ThinkingTranslatedEvent count after retry = %d, want 1: %#v", len(got), got)
	}
	if got[0].MessageID != "msgidx:1" || got[0].BlockIndex != 0 || got[0].Translated != "翻译:Retried streaming thought." {
		t.Fatalf("retry translation event = %#v", got[0])
	}
	entries, err = recovery.LoadThinkingTranslations(a.sessionDir)
	if err != nil {
		t.Fatalf("LoadThinkingTranslations retry: %v", err)
	}
	if len(entries) != 1 || entries[0].Translated != "翻译:Retried streaming thought." {
		t.Fatalf("entries after retry = %#v, want retry translation", entries)
	}
}

func TestHandleLLMResponsePersistsOpenAIChatReasoningWithChatCompletionsProvenance(t *testing.T) {
	a := newReadyTestMainAgent(t)
	providerCfg := llm.NewProviderConfig("deepseek", config.ProviderConfig{
		Type: config.ProviderTypeChatCompletions,
		Models: map[string]config.ModelConfig{
			"deepseek-v4-pro": {Limit: config.ModelLimit{Context: 128000, Output: 4096}},
		},
	}, []string{"test-key"})
	client := llm.NewClient(providerCfg, stubProvider{}, "deepseek-v4-pro", 4096, "sys")
	a.swapLLMClientWithRef(client, "deepseek-v4-pro", 128000, "deepseek/deepseek-v4-pro")
	a.newTurn()
	if a.turn == nil {
		t.Fatal("expected active turn")
	}

	payload := &LLMResponsePayload{
		ReasoningContent: "I need to inspect the repo before reading files.",
		ToolCalls: []message.ToolCall{{
			ID:   "call-1",
			Name: "read",
			Args: []byte(`{"path":"README.md"}`),
		}},
		StopReason: "tool_calls",
	}

	a.handleLLMResponse(Event{Type: EventLLMResponse, TurnID: a.turn.ID, Payload: payload})

	msgs := a.GetMessages()
	if len(msgs) == 0 {
		t.Fatal("expected assistant message appended to context")
	}
	last := msgs[len(msgs)-1]
	if last.Role != "assistant" {
		t.Fatalf("last role = %q, want assistant", last.Role)
	}
	if last.ReasoningContent != "I need to inspect the repo before reading files." {
		t.Fatalf("ReasoningContent = %q", last.ReasoningContent)
	}
	if last.Provenance == nil {
		t.Fatal("expected assistant provenance")
	}
	if last.Provenance.WireFamily != modelcompat.WireFamilyOpenAIChat {
		t.Fatalf("WireFamily = %q, want %q", last.Provenance.WireFamily, modelcompat.WireFamilyOpenAIChat)
	}
}

func TestCallLLMOnlyEmitsOneStreamingActivityWhenStatusAlsoArrives(t *testing.T) {
	a := newReadyTestMainAgent(t)

	providerCfg := llm.NewProviderConfig("sample", config.ProviderConfig{
		Type: config.ProviderTypeChatCompletions,
		Models: map[string]config.ModelConfig{
			"test-model": {Limit: config.ModelLimit{Context: 128000, Output: 4096}},
		},
	}, []string{"test-key"})

	providerImpl := &blockingStreamProvider{
		calls: []scriptedStreamCall{{
			streams: []message.StreamDelta{
				{Type: "status", Status: &message.StatusDelta{Type: "waiting_token"}},
				{Type: "tool_use_start", ToolCall: &message.ToolCallDelta{ID: "call-1", Name: "read", Input: `{"path":"README.md"}`}},
				{Type: "status", Status: &message.StatusDelta{Type: "streaming"}},
			},
			resp: &message.Response{
				ToolCalls: []message.ToolCall{{
					ID:   "call-1",
					Name: "read",
					Args: []byte(`{"path":"README.md"}`),
				}},
				StopReason: "tool_use",
			},
		}},
	}

	client := llm.NewClient(providerCfg, providerImpl, "test-model", 4096, "sys")
	a.swapLLMClientWithRef(client, "test-model", 128000, "sample/test-model")

	_, err := a.callLLM(context.Background(), []message.Message{{Role: "user", Content: "hi"}})
	if err != nil {
		t.Fatalf("callLLM: %v", err)
	}

	events := drainAgentEvents(a.Events())
	streamingCount := 0
	for _, evt := range events {
		act, ok := evt.(AgentActivityEvent)
		if ok && act.Type == ActivityStreaming {
			streamingCount++
		}
	}
	if streamingCount != 1 {
		t.Fatalf("streaming activity count = %d, want 1", streamingCount)
	}
}
