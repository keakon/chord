package agent

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

func TestSubAgentPersistsThinkingBlocksWithAssistantToolCall(t *testing.T) {
	_, sub := newMixedBatchTestSubAgent(t)
	blocks := []message.ThinkingBlock{{Thinking: "plan", Signature: "sig"}, {Data: "encrypted"}}
	sub.handleLLMResponse(&llmResult{
		turnID: 1,
		resp: &message.Response{
			Content:        "\u200b\u200b",
			ThinkingBlocks: blocks,
			ToolCalls: convertCalls([]messageToolCall{
				mustJSONToolCall(t, "complete-1", "complete", map[string]any{"summary": "done"}),
			}),
		},
	})
	msgs := sub.ctxMgr.Snapshot()
	if len(msgs) == 0 {
		t.Fatal("expected assistant message in subagent context")
	}
	var got []message.ThinkingBlock
	for _, msg := range slices.Backward(msgs) {
		if msg.Role == message.RoleAssistant && len(msg.ThinkingBlocks) > 0 {
			got = msg.ThinkingBlocks
			break
		}
	}
	if len(got) != 2 || got[0] != blocks[0] || got[1] != blocks[1] {
		t.Fatalf("thinking blocks = %+v, want %+v", got, blocks)
	}
	for _, msg := range slices.Backward(msgs) {
		if msg.Role == message.RoleAssistant && len(msg.ThinkingBlocks) > 0 {
			if msg.Content != "" {
				t.Fatalf("assistant content = %q, want canonical empty string", msg.Content)
			}
			return
		}
	}
	t.Fatal("assistant message with thinking blocks disappeared")
}

func TestStructuredCompleteEnvelopeParsedFromCompleteTool(t *testing.T) {
	parent, sub := newMixedBatchTestSubAgent(t)
	artifactPath := filepath.Join(sub.sessionDir, "artifacts", "subagents", "worker-1", "report.md")
	if err := os.MkdirAll(filepath.Dir(artifactPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(artifactPath, []byte("report"), 0o600); err != nil {
		t.Fatal(err)
	}
	sub.handleLLMResponse(&llmResult{
		turnID: 1,
		resp: &message.Response{ToolCalls: convertCalls([]messageToolCall{
			mustJSONToolCall(t, "call-1", "complete", map[string]any{
				"summary":               "done",
				"files_changed":         []string{"internal/a.go"},
				"remaining_limitations": []string{"e2e not run"},
				"known_risks":           []string{"manual QA still useful"},
				"follow_up_recommended": []string{"review"},
				"artifacts":             []map[string]any{{"id": "art-1", "type": "research_report", "rel_path": "artifacts/subagents/worker-1/report.md"}},
			}),
		})},
	})

	evt := <-parent.eventCh
	if evt.Type != EventAgentDone {
		t.Fatalf("event.Type = %q, want %q", evt.Type, EventAgentDone)
	}
	result, ok := evt.Payload.(*AgentResult)
	if !ok || result.Envelope == nil {
		t.Fatalf("payload = %#v, want AgentResult with envelope", evt.Payload)
	}
	env := result.Envelope
	if result.Summary != "done" || env.Summary != "done" {
		t.Fatalf("summary = %q envelope=%q", result.Summary, env.Summary)
	}
	if got := strings.Join(env.FilesChanged, ","); got != "internal/a.go" {
		t.Fatalf("files_changed = %q", got)
	}
	if got := strings.Join(env.ReportedFilesChanged, ","); got != "internal/a.go" {
		t.Fatalf("reported_files_changed = %q", got)
	}
	if len(env.ActualFilesChanged) != 0 || env.FileAttributionIncomplete {
		t.Fatalf("runtime attribution = files %#v incomplete=%v, want empty and complete", env.ActualFilesChanged, env.FileAttributionIncomplete)
	}
	if got := strings.Join(env.RemainingLimitations, ","); got != "e2e not run" {
		t.Fatalf("remaining_limitations = %q", got)
	}
	if got := strings.Join(env.KnownRisks, ","); got != "manual QA still useful" {
		t.Fatalf("known_risks = %q", got)
	}
	if len(env.Artifacts) != 1 || env.Artifacts[0].RelPath != "artifacts/subagents/worker-1/report.md" {
		t.Fatalf("artifacts = %#v", env.Artifacts)
	}
}

func TestCompleteSchemaAndParserAcceptTypedResult(t *testing.T) {
	parent, sub := newMixedBatchTestSubAgent(t)
	completeTool, ok := sub.tools.Get(tools.NameComplete)
	if !ok {
		t.Fatal("Complete tool missing")
	}
	properties := completeTool.Parameters()["properties"].(map[string]any)
	for _, field := range []string{"result_type", "result", "result_ref"} {
		if properties[field] == nil {
			t.Fatalf("Complete schema missing %s", field)
		}
	}
	sub.handleLLMResponse(&llmResult{
		turnID: 1,
		resp: &message.Response{ToolCalls: convertCalls([]messageToolCall{
			mustJSONToolCall(t, "call-1", "complete", map[string]any{
				"summary": "done", "result_type": "type/test", "result": map[string]any{"value": 1},
			}),
		})},
	})
	select {
	case evt := <-parent.eventCh:
		if evt.Type != EventAgentDone {
			t.Fatalf("event = %#v", evt)
		}
		result := evt.Payload.(*AgentResult)
		if result.Envelope == nil || result.Envelope.ResultType != "type/test" || result.Envelope.ResultRef == nil || string(result.Envelope.Result) != `{"value":1}` {
			t.Fatalf("envelope = %#v", result.Envelope)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for typed Complete")
	}
}

// TestCompleteAcceptsResultRefFromSaveArtifactResultMode pins the merged
// result-mode-in-save_artifact contract end to end: the ResultRef returned by
// SaveArtifactTool's result mode must pass Complete's result_ref validation
// unchanged when the agent hands it back.
func TestCompleteAcceptsResultRefFromSaveArtifactResultMode(t *testing.T) {
	parent, sub := newMixedBatchTestSubAgent(t)
	ctx := tools.WithSessionDir(context.Background(), sub.sessionDir)
	out, err := (tools.SaveArtifactTool{}).Execute(ctx, mustMarshalJSON(t, map[string]any{
		"result_type": "type/test", "result": map[string]any{"value": 1},
	}))
	if err != nil {
		t.Fatalf("SaveArtifact result mode: %v", err)
	}
	var ref tools.ResultRef
	if err := json.Unmarshal([]byte(out), &ref); err != nil {
		t.Fatalf("unmarshal ResultRef: %v", err)
	}
	sub.handleLLMResponse(&llmResult{
		turnID: 1,
		resp: &message.Response{ToolCalls: convertCalls([]messageToolCall{
			mustJSONToolCall(t, "call-1", "complete", map[string]any{
				"summary": "done", "result_type": "type/test",
				"result_ref": map[string]any{
					"id": ref.ID, "result_type": ref.ResultType, "rel_path": ref.RelPath,
					"sha256": ref.SHA256, "size_bytes": ref.SizeBytes,
				},
			}),
		})},
	})
	select {
	case evt := <-parent.eventCh:
		if evt.Type != EventAgentDone {
			t.Fatalf("event = %#v", evt)
		}
		result := evt.Payload.(*AgentResult)
		if result.Envelope == nil || result.Envelope.ResultRef == nil || result.Envelope.ResultRef.ID != ref.ID {
			t.Fatalf("envelope = %#v, want result_ref %s", result.Envelope, ref.ID)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for typed Complete with save_artifact result_ref")
	}
}

func TestSubAgentCompletionMergesObservedFileState(t *testing.T) {
	_, sub := newMixedBatchTestSubAgent(t)
	changedPath := filepath.Join(sub.workDir, "internal", "observed.go")
	sub.recordTaskToolChanges(&toolResult{
		Name:      tools.NameWrite,
		ArgsJSON:  `{"path":"internal/observed.go","content":"package observed"}`,
		FileState: &message.ToolFileState{Writes: []message.TrackedFileState{{Path: changedPath, Exists: true}}},
	}, false)

	result := sub.enrichCompletionResult(&AgentResult{Summary: "done", Envelope: &CompletionEnvelope{
		Summary:      "done",
		FilesChanged: []string{"internal/reported.go"},
	}})
	if result == nil || result.Envelope == nil {
		t.Fatalf("result = %#v, want completion envelope", result)
	}
	env := result.Envelope
	if got := strings.Join(env.ReportedFilesChanged, ","); got != "internal/reported.go" {
		t.Fatalf("reported_files_changed = %q", got)
	}
	if got := strings.Join(env.ActualFilesChanged, ","); got != "internal/observed.go" {
		t.Fatalf("actual_files_changed = %q", got)
	}
	if got := strings.Join(env.FilesChanged, ","); got != "internal/reported.go,internal/observed.go" {
		t.Fatalf("files_changed = %q", got)
	}
	if env.FileAttributionIncomplete {
		t.Fatal("file attribution unexpectedly incomplete")
	}
}

func TestSubAgentCompletionMarksUnobservableMutationIncomplete(t *testing.T) {
	_, sub := newMixedBatchTestSubAgent(t)
	sub.tools.Register(dummyMutatingTool{name: "OpaqueMutation"})
	sub.recordTaskToolChanges(&toolResult{Name: "OpaqueMutation", ArgsJSON: `{}`}, false)

	result := sub.enrichCompletionResult(&AgentResult{Summary: "done"})
	if result == nil || result.Envelope == nil || !result.Envelope.FileAttributionIncomplete {
		t.Fatalf("result = %#v, want incomplete file attribution", result)
	}
	if len(result.Envelope.ActualFilesChanged) != 0 || len(result.Envelope.FilesChanged) != 0 {
		t.Fatalf("unobservable mutation invented paths: %#v", result.Envelope)
	}
}

func TestSubAgentFailedMutationPreservesObservedPathsAndIncompleteState(t *testing.T) {
	_, sub := newMixedBatchTestSubAgent(t)
	changedPath := filepath.Join(sub.workDir, "internal", "partial.go")
	files, incomplete := sub.recordTaskToolChanges(&toolResult{
		Name:      tools.NameWrite,
		ArgsJSON:  `{"path":"internal/partial.go","content":"partial"}`,
		FileState: &message.ToolFileState{Writes: []message.TrackedFileState{{Path: changedPath, Exists: true}}},
	}, true)
	if got := strings.Join(files, ","); got != "internal/partial.go" || incomplete {
		t.Fatalf("failed mutation attribution = files %q incomplete=%v", got, incomplete)
	}

	_, incomplete = sub.recordTaskToolChanges(&toolResult{
		Name:     tools.NameWrite,
		ArgsJSON: `{"path":"internal/unknown.go","content":"partial"}`,
	}, true)
	if !incomplete {
		t.Fatal("failed mutation without file state should mark attribution incomplete")
	}
	result := sub.enrichCompletionResult(&AgentResult{Summary: "partial failure"})
	if result == nil || result.Envelope == nil {
		t.Fatalf("result = %#v, want completion envelope", result)
	}
	if got := strings.Join(result.Envelope.ActualFilesChanged, ","); got != "internal/partial.go" {
		t.Fatalf("actual_files_changed = %q", got)
	}
	if !result.Envelope.FileAttributionIncomplete {
		t.Fatal("completion should preserve incomplete failed mutation attribution")
	}
}

func TestSubAgentRestoreMessagesRebuildsFileAttribution(t *testing.T) {
	_, sub := newMixedBatchTestSubAgent(t)
	legacyPath := filepath.Join(sub.workDir, "internal", "legacy.go")
	sub.RestoreMessages([]message.Message{
		{Role: "tool", ToolCallID: "ok", ToolStatus: message.ToolStatusSuccess, ToolChangedPaths: []string{"internal/observed.go"}},
		{Role: "tool", ToolCallID: "legacy", ToolStatus: message.ToolStatusSuccess, FileState: &message.ToolFileState{Writes: []message.TrackedFileState{{Path: legacyPath, Exists: true}}}},
		{Role: "tool", ToolCallID: "opaque", ToolStatus: message.ToolStatusSuccess, FileAttributionIncomplete: true},
		{Role: "tool", ToolCallID: "failed", ToolStatus: message.ToolStatusError, ToolChangedPaths: []string{"internal/ignored.go"}, FileAttributionIncomplete: true},
	})

	result := sub.enrichCompletionResult(&AgentResult{Summary: "restored"})
	if result == nil || result.Envelope == nil {
		t.Fatalf("result = %#v, want completion envelope", result)
	}
	if got := strings.Join(result.Envelope.ActualFilesChanged, ","); got != "internal/ignored.go,internal/legacy.go,internal/observed.go" {
		t.Fatalf("restored actual_files_changed = %q", got)
	}
	if !result.Envelope.FileAttributionIncomplete {
		t.Fatal("restored file attribution should remain incomplete")
	}
}

func TestSubAgentPureTextGetsSingleTerminalRecoveryRequest(t *testing.T) {
	_, sub := newMixedBatchTestSubAgent(t)
	providerCfg := llm.NewProviderConfig("test", config.ProviderConfig{
		Type: config.ProviderTypeChatCompletions,
		Models: map[string]config.ModelConfig{
			"model": {Limit: config.ModelLimit{Context: 8192, Output: 1024}},
		},
	}, []string{"key"})
	provider := &blockingStreamProvider{calls: []scriptedStreamCall{{resp: &message.Response{ToolCalls: convertCalls([]messageToolCall{mustJSONToolCall(t, "complete-1", "complete", map[string]any{"summary": "done"})})}}}}
	sub.llmClient = llm.NewClient(providerCfg, provider, "model", 1024, "sys")

	sub.handleLLMResponse(&llmResult{turnID: 1, resp: &message.Response{Content: "I finished the work."}})
	waitForSubAgentLLMResult(t, sub, time.Second)

	seen, _ := provider.snapshot()
	if len(seen) != 1 {
		t.Fatalf("provider request count = %d, want 1", len(seen))
	}
	last := seen[0][len(seen[0])-1]
	if last.Role != "user" || !strings.Contains(last.Content, "call Complete") {
		t.Fatalf("terminal recovery message = %#v", last)
	}
	if sub.turn.SubAgentTerminalRecoveryCount != 1 {
		t.Fatalf("terminal recovery count = %d, want 1", sub.turn.SubAgentTerminalRecoveryCount)
	}
}

func TestSubAgentUnparseableThinkingToolcallGetsTerminalRecoveryRequest(t *testing.T) {
	parent, sub := newMixedBatchTestSubAgent(t)
	enabled := true
	providerCfg := llm.NewProviderConfig("test", config.ProviderConfig{
		Type: config.ProviderTypeChatCompletions,
		Compat: &config.ProviderCompatConfig{
			ThinkingToolcall: &config.ThinkingToolcallCompatConfig{Enabled: &enabled},
		},
		Models: map[string]config.ModelConfig{
			"model": {Limit: config.ModelLimit{Context: 8192, Output: 1024}},
		},
	}, []string{"key"})
	provider := &blockingStreamProvider{calls: []scriptedStreamCall{{resp: &message.Response{ToolCalls: convertCalls([]messageToolCall{mustJSONToolCall(t, "complete-1", "complete", map[string]any{"summary": "done"})})}}}}
	sub.llmClient = llm.NewClient(providerCfg, provider, "model", 1024, "sys")

	sub.handleLLMResponse(&llmResult{turnID: 1, resp: &message.Response{
		ReasoningContent:          "<|tool_calls_section_begin|>not a valid pseudo call<|tool_calls_section_end|>",
		StopReason:                "stop",
		ThinkingToolcallMarkerHit: true,
	}})
	waitForSubAgentLLMResult(t, sub, time.Second)

	seen, _ := provider.snapshot()
	if len(seen) != 1 {
		t.Fatalf("provider request count = %d, want 1", len(seen))
	}
	last := seen[0][len(seen[0])-1]
	if last.Role != "user" || !strings.Contains(last.Content, "call Complete") {
		t.Fatalf("terminal recovery message = %#v", last)
	}
	if sub.turn.SubAgentTerminalRecoveryCount != 1 {
		t.Fatalf("terminal recovery count = %d, want 1", sub.turn.SubAgentTerminalRecoveryCount)
	}
	// The terminal recovery budget is one per turn: a second text-only reply
	// must fail with EventAgentError for owner notification instead of
	// parking in an idle wait.
	sub.handleLLMResponse(&llmResult{turnID: 1, resp: &message.Response{Content: "still not calling a coordination tool"}})
	// The unparseable-drift recovery above already emitted an agent_log event;
	// skip unrelated events queued before the terminal failure.
	deadline := time.After(time.Second)
	for {
		select {
		case evt := <-parent.eventCh:
			if evt.Type != EventAgentError || evt.SourceID != sub.instanceID {
				continue
			}
			if err, ok := evt.Payload.(error); !ok || !strings.Contains(err.Error(), "without a coordination tool") {
				t.Fatalf("error payload = %#v", evt.Payload)
			}
			return
		case <-deadline:
			t.Fatal("timed out waiting for text-only terminal failure after recovery")
		}
	}
}

func TestSubAgentRequestsRequiredToolChoiceWhenProviderSupportsIt(t *testing.T) {
	_, sub := newMixedBatchTestSubAgent(t)
	providerCfg := llm.NewProviderConfig("test", config.ProviderConfig{
		Type: config.ProviderTypeChatCompletions,
		Models: map[string]config.ModelConfig{
			"model": {Limit: config.ModelLimit{Context: 8192, Output: 1024}},
		},
	}, []string{"key"})
	provider := &blockingStreamProvider{calls: []scriptedStreamCall{{resp: &message.Response{ToolCalls: convertCalls([]messageToolCall{mustJSONToolCall(t, "complete-1", "complete", map[string]any{"summary": "done"})})}}}}
	sub.llmClient = llm.NewClient(providerCfg, provider, "model", 1024, "sys")

	sub.asyncCallLLMWithFlightMarked(sub.turn, sub.ctxMgr.Snapshot())
	waitForSubAgentLLMResult(t, sub, time.Second)

	_, seen := provider.snapshot()
	if len(seen) != 1 {
		t.Fatalf("provider request count = %d, want 1", len(seen))
	}
	if seen[0].OpenAI.ToolChoice != "required" || seen[0].Anthropic.ToolChoice != "required" || seen[0].Gemini.ToolChoice != "required" {
		t.Fatalf("request tuning = %#v, want required tool choice", seen[0])
	}
	if seen[0].OpenAI.ParallelToolCalls != nil {
		t.Fatalf("parallel tool calls = %#v, want no required-tool override", seen[0].OpenAI.ParallelToolCalls)
	}
}

func TestSubAgentRequestCarriesTurnResponsesState(t *testing.T) {
	_, sub := newMixedBatchTestSubAgent(t)
	sub.turn.LLMResponsesState = llm.NewResponsesTurnState()
	providerCfg := llm.NewProviderConfig("test", config.ProviderConfig{
		Type: config.ProviderTypeChatCompletions,
		Models: map[string]config.ModelConfig{
			"model": {Limit: config.ModelLimit{Context: 8192, Output: 1024}},
		},
	}, []string{"key"})
	provider := &blockingStreamProvider{calls: []scriptedStreamCall{{resp: &message.Response{Content: "done"}}}}
	sub.llmClient = llm.NewClient(providerCfg, provider, "model", 1024, "sys")

	sub.asyncCallLLMWithFlightMarked(sub.turn, sub.ctxMgr.Snapshot())
	waitForSubAgentLLMResult(t, sub, time.Second)

	seen := provider.turnStatesSnapshot()
	if len(seen) != 1 {
		t.Fatalf("provider request count = %d, want 1", len(seen))
	}
	if seen[0] != sub.turn.LLMResponsesState {
		t.Fatalf("request turn state = %p, want SubAgent turn state %p", seen[0], sub.turn.LLMResponsesState)
	}
}

func TestSubAgentInterruptedStreamGetsSingleTerminalRecoveryRequest(t *testing.T) {
	_, sub := newMixedBatchTestSubAgent(t)
	providerCfg := llm.NewProviderConfig("test", config.ProviderConfig{
		Type: config.ProviderTypeChatCompletions,
		Models: map[string]config.ModelConfig{
			"model": {Limit: config.ModelLimit{Context: 8192, Output: 1024}},
		},
	}, []string{"key"})
	provider := &blockingStreamProvider{calls: []scriptedStreamCall{{resp: &message.Response{ToolCalls: convertCalls([]messageToolCall{mustJSONToolCall(t, "complete-1", "complete", map[string]any{"summary": "done"})})}}}}
	sub.llmClient = llm.NewClient(providerCfg, provider, "model", 1024, "sys")
	sub.turn.appendPartialText("partial result")

	sub.handleLLMResponse(&llmResult{turnID: 1, err: io.ErrUnexpectedEOF})
	waitForSubAgentLLMResult(t, sub, time.Second)

	seen, _ := provider.snapshot()
	if len(seen) != 1 {
		t.Fatalf("provider request count = %d, want 1", len(seen))
	}
	if len(seen[0]) < 2 || seen[0][0].Role != "assistant" || seen[0][0].Content != "partial result" {
		t.Fatalf("recovery context = %#v, want interrupted assistant text", seen[0])
	}
	last := seen[0][len(seen[0])-1]
	if last.Role != "user" || !strings.Contains(last.Content, "transient transport error") {
		t.Fatalf("transport recovery message = %#v", last)
	}
}

// Terminal recovery issues an LLM request from inside handleLLMResponse,
// after runLoop's finishLLMRequest already cleared the in-flight gate. It must
// re-arm it: otherwise runLoop sees an idle
// sub-agent and can consume queued input — newTurn then cancels the recovery
// request's context and its result is silently dropped — or park the sub-agent
// mid-request.
func TestSubAgentRecoveryRequestsKeepInFlightGateClosed(t *testing.T) {
	tests := []struct {
		name    string
		trigger func(t *testing.T, sub *SubAgent)
	}{
		{
			name: "terminal recovery after pure text",
			trigger: func(t *testing.T, sub *SubAgent) {
				sub.handleLLMResponse(&llmResult{turnID: 1, resp: &message.Response{Content: "I finished the work."}})
			},
		},
		{
			name: "terminal recovery after interrupted stream",
			trigger: func(t *testing.T, sub *SubAgent) {
				sub.handleLLMResponse(&llmResult{turnID: 1, err: io.ErrUnexpectedEOF})
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Hold the provider inside CompleteStream so the assertions observe a
			// genuinely in-flight request.
			provider := &blockingStreamProvider{
				calls:      []scriptedStreamCall{{holdAfterStreams: true, resp: &message.Response{Content: "held"}}},
				streamedCh: make(chan struct{}),
				releaseCh:  make(chan struct{}),
			}
			providerCfg := llm.NewProviderConfig("test", config.ProviderConfig{
				Type:   config.ProviderTypeChatCompletions,
				Models: map[string]config.ModelConfig{"model": {Limit: config.ModelLimit{Context: 8192, Output: 1024}}},
			}, []string{"key"})
			_, sub := newMixedBatchTestSubAgent(t)
			sub.llmMu.Lock()
			sub.llmClient = llm.NewClient(providerCfg, provider, "model", 1024, "sys")
			sub.llmMu.Unlock()
			// runLoop clears the gate via finishLLMRequest before dispatching the
			// response, so the recovery path starts from the cleared state.
			sub.llmRequestInFlight.Store(false)

			tc.trigger(t, sub)

			select {
			case <-provider.streamedCh:
			case <-time.After(2 * time.Second):
				t.Fatal("recovery request never reached the provider")
			}
			if !sub.llmRequestInFlight.Load() {
				t.Error("in-flight gate is open during the recovery request; runLoop can consume queued input and cancel it via newTurn")
			}
			if sub.canStartUserTurn() {
				t.Error("canStartUserTurn() = true while the recovery request is in flight")
			}

			// Release the provider so the async goroutine completes its session-dir
			// writes before TempDir cleanup.
			close(provider.releaseCh)
			waitForSubAgentLLMResult(t, sub, 2*time.Second)
		})
	}
}

func TestSubAgentTerminalRecoveryIsBounded(t *testing.T) {
	parent, sub := newMixedBatchTestSubAgent(t)
	// Transport interruptions draw on their own budget, so exhaust that one:
	// the terminal nudge is reserved for a reply that stopped at plain text.
	sub.turn.SubAgentStreamResumeCount = maxSubAgentStreamResumes
	sub.handleLLMResponse(&llmResult{turnID: 1, err: io.ErrUnexpectedEOF})

	select {
	case evt := <-parent.eventCh:
		if evt.Type != EventAgentError || evt.SourceID != sub.instanceID {
			t.Fatalf("event = %#v, want SubAgent EventAgentError", evt)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for bounded recovery failure")
	}
}

func TestSubAgentSecondPureTextFailsForOwnerNotification(t *testing.T) {
	parent, sub := newMixedBatchTestSubAgent(t)
	sub.turn.SubAgentTerminalRecoveryCount = 1
	sub.handleLLMResponse(&llmResult{turnID: 1, resp: &message.Response{Content: "still no coordination tool"}})

	select {
	case evt := <-parent.eventCh:
		if evt.Type != EventAgentError || evt.SourceID != sub.instanceID {
			t.Fatalf("event = %#v, want SubAgent EventAgentError", evt)
		}
		if err, ok := evt.Payload.(error); !ok || !strings.Contains(err.Error(), "without a coordination tool") {
			t.Fatalf("error payload = %#v", evt.Payload)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for text-only terminal failure")
	}
}

func TestSaveArtifactToolWritesSessionArtifactAndReadArtifactReadsIt(t *testing.T) {
	sessionDir := t.TempDir()
	ctx := tools.WithTaskID(tools.WithAgentID(tools.WithSessionDir(context.Background(), sessionDir), "worker-1"), "task-1")
	out, err := (tools.SaveArtifactTool{}).Execute(ctx, mustMarshalJSON(t, map[string]any{
		"filename":    "../research.md",
		"type":        "research_report",
		"description": "repo discovery",
		"content":     "research body",
		"mode":        "overwrite",
	}))
	if err != nil {
		t.Fatalf("SaveArtifact: %v", err)
	}
	var ref tools.ArtifactRef
	if err := json.Unmarshal([]byte(out), &ref); err != nil {
		t.Fatalf("unmarshal ref: %v", err)
	}
	if ref.Type != "research_report" || !strings.HasPrefix(ref.RelPath, "artifacts/subagents/worker_1/task_1/") {
		t.Fatalf("artifact ref = %#v", ref)
	}
	read, err := (tools.ReadArtifactTool{}).Execute(ctx, mustMarshalJSON(t, map[string]any{"path": ref.RelPath}))
	if err != nil {
		t.Fatalf("ReadArtifact saved artifact: %v", err)
	}
	if !strings.Contains(read, "ARTIFACT_RESULT lines=1-1 total=1 sha256=") || !strings.HasSuffix(strings.TrimSpace(read), "research body") {
		t.Fatalf("artifact body = %q", read)
	}
	// Default mode=create should fail on second write.
	if _, err := (tools.SaveArtifactTool{}).Execute(ctx, mustMarshalJSON(t, map[string]any{
		"filename": "report.md",
		"content":  "first",
		"mode":     "create",
	})); err != nil {
		t.Fatalf("SaveArtifact(create) first: %v", err)
	}
	if _, err := (tools.SaveArtifactTool{}).Execute(ctx, mustMarshalJSON(t, map[string]any{
		"filename": "report.md",
		"content":  "second",
		"mode":     "create",
	})); err == nil {
		t.Fatalf("SaveArtifact(create) second succeeded, want error")
	}
	// Append should work.
	if _, err := (tools.SaveArtifactTool{}).Execute(ctx, mustMarshalJSON(t, map[string]any{
		"filename": "report.md",
		"content":  "second",
		"mode":     "append",
	})); err != nil {
		t.Fatalf("SaveArtifact(append): %v", err)
	}
	refOut, err := (tools.SaveArtifactTool{}).Execute(ctx, mustMarshalJSON(t, map[string]any{
		"filename": "report.md",
		"content":  "third",
		"mode":     "append",
	}))
	if err != nil {
		t.Fatalf("SaveArtifact(append) second: %v", err)
	}
	var ref2 tools.ArtifactRef
	if err := json.Unmarshal([]byte(refOut), &ref2); err != nil {
		t.Fatalf("unmarshal ref: %v", err)
	}
	body, err := (tools.ReadArtifactTool{}).Execute(ctx, mustMarshalJSON(t, map[string]any{"path": ref2.RelPath}))
	if err != nil {
		t.Fatalf("ReadArtifact appended: %v", err)
	}
	if !strings.Contains(body, "first") || !strings.Contains(body, "second") || !strings.Contains(body, "third") {
		t.Fatalf("append body missing parts: %q", body)
	}
}

func TestSaveArtifactOverwriteReplacesExistingContent(t *testing.T) {
	sessionDir := t.TempDir()
	ctx := tools.WithTaskID(tools.WithAgentID(tools.WithSessionDir(context.Background(), sessionDir), "worker-2"), "task-2")
	if _, err := (tools.SaveArtifactTool{}).Execute(ctx, mustMarshalJSON(t, map[string]any{
		"filename": "report.md",
		"content":  "old body",
		"mode":     "create",
	})); err != nil {
		t.Fatalf("SaveArtifact(create): %v", err)
	}
	out, err := (tools.SaveArtifactTool{}).Execute(ctx, mustMarshalJSON(t, map[string]any{
		"filename": "report.md",
		"content":  "new body",
		"mode":     "overwrite",
	}))
	if err != nil {
		t.Fatalf("SaveArtifact(overwrite): %v", err)
	}
	var ref tools.ArtifactRef
	if err := json.Unmarshal([]byte(out), &ref); err != nil {
		t.Fatalf("unmarshal ref: %v", err)
	}
	body, err := (tools.ReadArtifactTool{}).Execute(ctx, mustMarshalJSON(t, map[string]any{"path": ref.RelPath}))
	if err != nil {
		t.Fatalf("ReadArtifact overwrite: %v", err)
	}
	body = strings.TrimSpace(body)
	if !strings.HasSuffix(body, "new body") {
		t.Fatalf("overwrite body = %q, want %q", body, "new body")
	}
	if strings.Contains(body, "old body") {
		t.Fatalf("overwrite retained old body: %q", body)
	}
}

func TestMailboxArtifactRefsMergeAndDedupe(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.enqueueSubAgentMailbox(SubAgentMailboxMessage{
		MessageID: "worker-1-1",
		AgentID:   "worker-1",
		TaskID:    "task-1",
		Kind:      SubAgentMailboxKindCompleted,
		Priority:  SubAgentMailboxPriorityUrgent,
		Summary:   "done",
		Completion: &CompletionEnvelope{
			Summary: "done",
			Artifacts: []tools.ArtifactRef{
				{ID: "art-1", Type: "research_report", RelPath: "artifacts/subagents/worker-1/task-1/report.md"},
				{ID: "art-1", Type: "research_report", RelPath: "artifacts/subagents/worker-1/task-1/report.md"},
				{ID: "art-2", Type: "verification_log", RelPath: "artifacts/subagents/worker-1/task-1/verify.log"},
			},
		},
	})
	msgs, err := loadSubAgentMailboxMessages(a.sessionDir)
	if err != nil {
		t.Fatalf("loadSubAgentMailboxMessages: %v", err)
	}
	last := msgs[len(msgs)-1]
	if last.Completion == nil {
		t.Fatal("expected completion envelope")
	}
	if got := len(last.Completion.Artifacts); got != 2 {
		t.Fatalf("artifact refs count = %d, want 2; refs=%#v", got, last.Completion.Artifacts)
	}
	if last.Completion.Artifacts[0].RelPath != "artifacts/subagents/worker-1/task-1/report.md" {
		t.Fatalf("first artifact ref = %#v", last.Completion.Artifacts[0])
	}
	if last.Completion.Artifacts[1].RelPath != "artifacts/subagents/worker-1/task-1/verify.log" {
		t.Fatalf("second artifact ref = %#v", last.Completion.Artifacts[1])
	}
}

func TestReadArtifactToolRejectsPathEscapeAndReadsSessionArtifact(t *testing.T) {
	sessionDir := t.TempDir()
	artifactRel := filepath.ToSlash(filepath.Join("artifacts", "subagents", "worker-1", "report.md"))
	artifactAbs := filepath.Join(sessionDir, filepath.FromSlash(artifactRel))
	if err := os.MkdirAll(filepath.Dir(artifactAbs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(artifactAbs, []byte("artifact body"), 0o644); err != nil {
		t.Fatal(err)
	}
	tool := tools.ReadArtifactTool{}
	out, err := tool.Execute(tools.WithSessionDir(context.Background(), sessionDir), mustMarshalJSON(t, map[string]any{"path": artifactRel}))
	if err != nil {
		t.Fatalf("ReadArtifact valid path: %v", err)
	}
	if !strings.Contains(out, "ARTIFACT_RESULT lines=1-1 total=1 sha256=") || !strings.HasSuffix(strings.TrimSpace(out), "artifact body") {
		t.Fatalf("artifact content = %q", out)
	}
	for _, bad := range []string{"../secret.md", filepath.ToSlash(filepath.Join("subagents", "worker-1", "report.md")), filepath.ToSlash(filepath.Join("artifacts", "..", "secret.md")), artifactAbs} {
		if _, err := tool.Execute(tools.WithSessionDir(context.Background(), sessionDir), mustMarshalJSON(t, map[string]any{"path": bad})); err == nil {
			t.Fatalf("ReadArtifact path %q succeeded, want error", bad)
		}
	}
}

func TestCoordinationSnapshotIncludesDurableCompletionAndArtifact(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.explicitUserTurnCount.Store(5)
	a.subs.taskRecords["task-1"] = &DurableTaskRecord{
		TaskID:             "task-1",
		AgentDefName:       "explorer",
		State:              string(SubAgentStateCompleted),
		ResumePolicy:       taskResumePolicyNotify,
		PlanTaskRef:        "P1",
		SemanticTaskKey:    "coordination-snapshot",
		LastSummary:        "research complete",
		LastUpdatedTurn:    5,
		LastArtifactRefs:   []tools.ArtifactRef{{ID: "art-1", Type: "research_report", RelPath: "artifacts/subagents/worker-1/report.md"}},
		LastCompletion:     &CompletionEnvelope{Summary: "research complete", FilesChanged: []string{"internal/a.go"}},
		ExpectedWriteScope: tools.WriteScope{Files: []string{"internal/a.go"}},
	}
	block := a.buildCoordinationSnapshotOverlay()
	for _, want := range []string{"SubAgent coordination snapshot", "task_id: task-1", "agent_type: explorer", "artifact_refs: artifacts/subagents/worker-1/report.md(research_report)", "files_changed: internal/a.go", "write_scope: file:internal/a.go"} {
		if !strings.Contains(block, want) {
			t.Fatalf("snapshot missing %q:\n%s", want, block)
		}
	}
}

// TestCoordinationSnapshotCapsCompletionLists pins the per-list length cap on
// the files_changed / remaining_limitations / known_risks
// lines: an unbounded list from one verbose completion could otherwise dominate
// the overlay token budget under the 8-task ceiling. The truncated tail is
// reported as an explicit "...N more" hint so the reader knows the list
// continues past the cap.
func TestCoordinationSnapshotCapsCompletionLists(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.explicitUserTurnCount.Store(7)
	a.subs.taskRecords["task-1"] = &DurableTaskRecord{
		TaskID:          "task-1",
		State:           string(SubAgentStateCompleted),
		ResumePolicy:    taskResumePolicyNotify,
		LastSummary:     "big completion",
		LastUpdatedTurn: 7,
		LastCompletion: &CompletionEnvelope{
			FilesChanged:         []string{"f-1", "f-2", "f-3", "f-4", "f-5"},
			RemainingLimitations: []string{"l-1", "l-2", "l-3", "l-4", "l-5", "l-6"},
			KnownRisks:           []string{"r-1", "r-2", "r-3", "r-4"},
		},
	}
	block := a.buildCoordinationSnapshotOverlay()
	for _, want := range []string{
		"files_changed: f-1, f-2, f-3, ...2 more",
		"remaining_limitations: l-1, l-2, l-3, ...3 more",
		"known_risks: r-1, r-2, r-3, ...1 more",
	} {
		if !strings.Contains(block, want) {
			t.Fatalf("snapshot missing capped list %q:\n%s", want, block)
		}
	}
	for _, leaked := range []string{"f-4", "l-4", "r-4"} {
		if strings.Contains(block, leaked) {
			t.Fatalf("snapshot leaked list item past the cap (%q):\n%s", leaked, block)
		}
	}
}

// TestCoordinationSnapshotOmitsCompletionDeliveredByInjectedMailbox pins the
// same-request dedupe between the completed mailbox and the coordination
// snapshot: when the mailbox that recorded a terminal completion is already
// part of the request (delivered in the pending batch or durable in the
// conversation), the snapshot must not list the completion a second time. The
// mailbox text is the single expression of the fact then.
func TestCoordinationSnapshotOmitsCompletionDeliveredByInjectedMailbox(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.explicitUserTurnCount.Store(6)
	a.subs.taskRecords["task-1"] = &DurableTaskRecord{
		TaskID:          "task-1",
		State:           string(SubAgentStateCompleted),
		ResumePolicy:    taskResumePolicyNotify,
		LastMailboxID:   "worker-1-1",
		LastSummary:     "research complete",
		LastUpdatedTurn: 6,
		LastCompletion:  &CompletionEnvelope{Summary: "research complete", FilesChanged: []string{"internal/a.go"}},
	}

	block := a.buildCoordinationSnapshotOverlayForRequest(map[string]struct{}{"worker-1-1": {}})
	if strings.Contains(block, "task_id: task-1") {
		t.Fatalf("completed task re-listed although its mailbox is in the request:\n%s", block)
	}

	// Control: without the mailbox in the request the same completion is still
	// listed, so the snapshot remains the fallback that keeps terminal
	// completions visible when no mailbox text expresses them.
	block = a.buildCoordinationSnapshotOverlay()
	if !strings.Contains(block, "task_id: task-1") || !strings.Contains(block, "files_changed: internal/a.go") {
		t.Fatalf("snapshot missing completion without injected mailbox:\n%s", block)
	}
}

func TestCoordinationSnapshotMarksRunningWorkerStallButNotWaitingMain(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	running := newControllableTestSubAgent(t, a, "task-running")
	running.semHeld = true
	running.setState(SubAgentStateRunning, "working")
	running.runtimeState.stateChangedAt = time.Now().Add(-coordinationSnapshotStallAfter - time.Minute)
	a.syncTaskRecordFromSub(running, "")

	waiting := &DurableTaskRecord{
		TaskID:           "task-waiting",
		AgentDefName:     "worker",
		LatestInstanceID: "worker-waiting",
		State:            string(SubAgentStateWaitingMain),
		LastSummary:      "needs decision",
		LastUpdatedTurn:  a.explicitUserTurnCount.Load(),
	}
	a.subs.taskRecords[waiting.TaskID] = waiting

	// Overlay formatting is read-only; stall markers are refreshed at the
	// request-dispatch boundary (buildTurnOverlayMessages), so the test invokes
	// the same refresh explicitly before rendering the snapshot.
	a.updateSubAgentStallMarkers()
	block := a.buildCoordinationSnapshotOverlay()
	if !strings.Contains(block, "task_id: task-running") || !strings.Contains(block, "suspected_stall: running with no recent state/progress update") {
		t.Fatalf("snapshot missing running stall:\n%s", block)
	}
	idx := strings.Index(block, "task_id: task-waiting")
	if idx < 0 {
		t.Fatalf("snapshot missing waiting task:\n%s", block)
	}
	waitingSection := block[idx:]
	if strings.Contains(waitingSection, "suspected_stall:") && !strings.Contains(waitingSection, "task_id: task-running") {
		t.Fatalf("waiting_main should not be marked stalled:\n%s", block)
	}
	// The aligned labels render as a per-record header suffix, so pin the exact
	// formatted substring to lock agent_type/agent_id values and order.
	if !strings.Contains(waitingSection, "task_id: task-waiting agent_type: worker agent_id: worker-waiting") {
		t.Fatalf("waiting task missing agent_type/agent_id labels:\n%s", block)
	}
}

func mustMarshalJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// TestCompletedMailboxTextExpressesContentOnce pins the mailbox-text dedupe:
// mailbox producers set Summary and Payload to the same string for terminal
// events, and the model-facing text must not write the identical content on
// both lines. The typed-result handle that the coordination snapshot would
// otherwise surface is part of the mailbox text instead, so eliding the task
// from the snapshot (same-request dedupe) loses nothing.
func TestCompletedMailboxTextExpressesContentOnce(t *testing.T) {
	summary := "alpha refactor landed"
	msg := &SubAgentMailboxMessage{
		MessageID: "msg-1", AgentID: "worker-1", TaskID: "task-1",
		Kind: SubAgentMailboxKindCompleted, Summary: summary, Payload: summary,
		Completion: &CompletionEnvelope{
			Summary: summary, FilesChanged: []string{"internal/a.go"},
			ResultType: "type/report", ResultRef: &tools.ResultRef{ID: "sha-1", RelPath: "artifacts/results/result.json"},
		},
	}
	text := formatSubAgentMailboxInjectionText(msg)
	if n := strings.Count(text, summary); n != 1 {
		t.Fatalf("completion content appears %d times, want once:\n%s", n, text)
	}
	if strings.Contains(text, "\n- payload: ") {
		t.Fatalf("payload line duplicates an identical summary:\n%s", text)
	}
	for _, want := range []string{"result_type: type/report", "result_ref: artifacts/results/result.json"} {
		if !strings.Contains(text, want) {
			t.Fatalf("mailbox text missing %q:\n%s", want, text)
		}
	}
}

// TestMailboxTextKeepsDistinctPayload guards the other half of the dedupe
// contract: a payload that genuinely differs from the summary is still
// rendered, so distinct message bodies are not lost by the equal-text elision.
func TestMailboxTextKeepsDistinctPayload(t *testing.T) {
	msg := &SubAgentMailboxMessage{
		MessageID: "msg-2", AgentID: "worker-1", TaskID: "task-1",
		Kind: SubAgentMailboxKindProgress, Summary: "short headline", Payload: "long detail body for the mailbox",
	}
	text := formatSubAgentMailboxInjectionText(msg)
	for _, want := range []string{"- summary: short headline", "- payload: long detail body for the mailbox"} {
		if !strings.Contains(text, want) {
			t.Fatalf("mailbox text missing %q:\n%s", want, text)
		}
	}
}

// TestMainMailboxAndSnapshotDoNotDoubleBillSameCompletion exercises the
// request-assembly path (buildTurnOverlayMessages): a completion whose
// completed mailbox is delivered in the same request must produce exactly the
// mailbox overlay — the coordination snapshot must not list the task again —
// and a later request in the same turn that already carries the durable
// mailbox message in its conversation must stay deduplicated too.
func TestMainMailboxAndSnapshotDoNotDoubleBillSameCompletion(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.explicitUserTurnCount.Store(8)
	summary := "complete refactor alpha"
	a.subs.taskRecords["task-1"] = &DurableTaskRecord{
		TaskID:          "task-1",
		State:           string(SubAgentStateCompleted),
		ResumePolicy:    taskResumePolicyNotify,
		LastMailboxID:   "worker-1-1",
		LastSummary:     summary,
		LastUpdatedTurn: 8,
		LastCompletion:  &CompletionEnvelope{Summary: summary, FilesChanged: []string{"internal/a.go"}},
	}
	a.pendingSubAgentMailboxes = []*SubAgentMailboxMessage{{
		MessageID: "worker-1-1", AgentID: "worker-1", TaskID: "task-1",
		Kind: SubAgentMailboxKindCompleted, Summary: summary, Payload: summary,
	}}

	overlays := a.buildTurnOverlayMessages()
	if len(overlays) != 1 || overlays[0].Kind != message.KindSubAgentMailbox {
		t.Fatalf("overlays = %#v, want exactly the completed mailbox (no snapshot repeat)", overlays)
	}
	if strings.Contains(overlays[0].Content, "SubAgent coordination snapshot") {
		t.Fatalf("mailbox overlay unexpectedly contains the coordination snapshot:\n%s", overlays[0].Content)
	}
	if n := strings.Count(overlays[0].Content, summary); n != 1 {
		t.Fatalf("completion summary appears %d times in the mailbox overlay, want once:\n%s", n, overlays[0].Content)
	}

	// A later request in the same turn already carries the durable mailbox
	// message in its conversation, so the snapshot stays deduplicated.
	for _, overlay := range a.buildTurnOverlayMessages() {
		if strings.Contains(overlay.Content, "task_id: task-1") || strings.Contains(overlay.Content, "SubAgent coordination snapshot") {
			t.Fatalf("later request re-listed the completed task despite its durable mailbox:\n%s", overlay.Content)
		}
	}
}

// The following completion-rejection tests cover rejectInvalidCompleteArguments
// and the degraded typed-result delivery, which remain live code.

func TestSubAgentInvalidCompleteGetsRejectedToolResultAndBoundedFollowUp(t *testing.T) {
	tests := []struct {
		name       string
		args       any
		wantReason string
	}{
		{name: "json parse error", args: map[string]any{"summary": 123}, wantReason: "cannot unmarshal number"},
		{name: "blank summary", args: map[string]any{"summary": "   "}, wantReason: "summary is required"},
		{name: "artifact outside session", args: map[string]any{"summary": "done", "artifacts": []map[string]any{{"rel_path": "../outside.txt"}}}, wantReason: "artifact path escapes"},
		{name: "typed result without result_type", args: map[string]any{"summary": "done", "result": map[string]any{"value": 1}}, wantReason: "result or result_ref requires result_type"},
		{name: "result_type without result or result_ref", args: map[string]any{"summary": "done", "result_type": "type/test"}, wantReason: "result_type requires result or result_ref"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			parent, sub := newMixedBatchTestSubAgent(t)
			providerCfg := llm.NewProviderConfig("test", config.ProviderConfig{
				Type:   config.ProviderTypeChatCompletions,
				Models: map[string]config.ModelConfig{"model": {Limit: config.ModelLimit{Context: 8192, Output: 1024}}},
			}, []string{"key"})
			// The follow-up request is sent with a forced required-tool-choice
			// tuning, so the scripted response must return a tool call (mirroring
			// the terminal-recovery tests) or the client cannot finalize it.
			provider := &blockingStreamProvider{calls: []scriptedStreamCall{{resp: &message.Response{ToolCalls: convertCalls([]messageToolCall{mustJSONToolCall(t, "retry-1", "complete", map[string]any{"summary": "done"})})}}}}
			sub.llmClient = llm.NewClient(providerCfg, provider, "model", 1024, "sys")

			sub.handleLLMResponse(&llmResult{turnID: 1, resp: &message.Response{ToolCalls: convertCalls([]messageToolCall{
				mustJSONToolCall(t, "call-1", "complete", tc.args),
			})}})

			// Must not be terminal: the worker gets one bounded follow-up request
			// instead of failing on the spot.
			select {
			case evt := <-parent.eventCh:
				t.Fatalf("invalid Complete terminated the task with %#v instead of a bounded follow-up", evt)
			default:
			}
			if result := waitForSubAgentLLMResult(t, sub, time.Second); result.err != nil {
				t.Fatalf("follow-up request failed: %v", result.err)
			}
			if got := sub.turn.SubAgentCompletionRecoveryCount; got != 1 {
				t.Fatalf("completion recovery count = %d, want 1", got)
			}
			if got := sub.turn.SubAgentTerminalRecoveryCount; got != 0 {
				t.Fatalf("terminal (pure-text) recovery count = %d, want 0 (rejected Complete must not consume the wrap-up nudge)", got)
			}

			// The rejected Complete call got a tool result so the transcript keeps
			// its tool-call pairing.
			msgs := sub.ctxMgr.Snapshot()
			foundRejected := false
			for _, msg := range msgs {
				if msg.Role != "tool" || msg.ToolCallID != "call-1" {
					continue
				}
				foundRejected = strings.HasPrefix(msg.Content, "Completion rejected:") && strings.Contains(msg.Content, tc.wantReason)
				break
			}
			if !foundRejected {
				t.Fatalf("missing rejected tool result containing %q in %#v", tc.wantReason, msgs)
			}

			// The follow-up request carried the fix-it nudge for the model.
			seen, _ := provider.snapshot()
			if len(seen) != 1 {
				t.Fatalf("provider request count = %d, want 1 bounded follow-up", len(seen))
			}
			last := seen[0][len(seen[0])-1]
			if last.Role != "user" || !strings.Contains(last.Content, "Completion was rejected:") {
				t.Fatalf("follow-up request tail = %#v, want rejection nudge", last)
			}
		})
	}
}

func TestSubAgentInvalidCompleteRetryHasOwnBudgetAfterPureTextRecovery(t *testing.T) {
	// The pure-text wrap-up nudge and the rejected-Complete follow-up used to
	// share one budget, so a text-only reply followed by a rejected Complete
	// failed with a "after retry" error that was actually the first attempt.
	// Each recovery class now gets its own single follow-up.
	parent, sub := newMixedBatchTestSubAgent(t)
	providerCfg := llm.NewProviderConfig("test", config.ProviderConfig{
		Type:   config.ProviderTypeChatCompletions,
		Models: map[string]config.ModelConfig{"model": {Limit: config.ModelLimit{Context: 8192, Output: 1024}}},
	}, []string{"key"})
	provider := &blockingStreamProvider{calls: []scriptedStreamCall{{resp: &message.Response{ToolCalls: convertCalls([]messageToolCall{mustJSONToolCall(t, "retry-1", "complete", map[string]any{"summary": "done"})})}}}}
	sub.llmClient = llm.NewClient(providerCfg, provider, "model", 1024, "sys")
	// Simulate a text-only reply that already spent the wrap-up nudge.
	sub.turn.SubAgentTerminalRecoveryCount = 1

	sub.handleLLMResponse(&llmResult{turnID: 1, resp: &message.Response{ToolCalls: convertCalls([]messageToolCall{
		mustJSONToolCall(t, "call-1", "complete", map[string]any{"summary": "   "}),
	})}})

	select {
	case evt := <-parent.eventCh:
		t.Fatalf("rejected Complete terminated despite the pure-text budget already being spent: %#v", evt)
	default:
	}
	if result := waitForSubAgentLLMResult(t, sub, time.Second); result.err != nil {
		t.Fatalf("follow-up request failed: %v", result.err)
	}
	if got := sub.turn.SubAgentCompletionRecoveryCount; got != 1 {
		t.Fatalf("completion recovery count = %d, want 1 (the split budget must still be available)", got)
	}

	// The corrected follow-up call completes normally: the worker kept its
	// second chance after one pure-text reply and one rejected Complete.
	sub.handleLLMResponse(&llmResult{turnID: 1, resp: &message.Response{ToolCalls: convertCalls([]messageToolCall{
		mustJSONToolCall(t, "call-2", "complete", map[string]any{"summary": "done"}),
	})}})
	select {
	case evt := <-parent.eventCh:
		if evt.Type != EventAgentDone {
			t.Fatalf("event.Type = %q, want EventAgentDone", evt.Type)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for corrected Complete")
	}
}

func TestSubAgentCoReturnedInvalidCompleteRejectedAfterSiblingsSettle(t *testing.T) {
	parent, sub := newMixedBatchTestSubAgent(t)
	providerCfg := llm.NewProviderConfig("test", config.ProviderConfig{
		Type:   config.ProviderTypeChatCompletions,
		Models: map[string]config.ModelConfig{"model": {Limit: config.ModelLimit{Context: 8192, Output: 1024}}},
	}, []string{"key"})
	provider := &blockingStreamProvider{calls: []scriptedStreamCall{{resp: &message.Response{ToolCalls: convertCalls([]messageToolCall{mustJSONToolCall(t, "retry-1", "complete", map[string]any{"summary": "done"})})}}}}
	sub.llmClient = llm.NewClient(providerCfg, provider, "model", 1024, "sys")

	sub.handleLLMResponse(&llmResult{turnID: 1, resp: &message.Response{ToolCalls: convertCalls([]messageToolCall{
		mustJSONToolCall(t, "call-1", "complete", map[string]any{"summary": "done", "result": map[string]any{"value": 1}}),
		mustJSONToolCall(t, "call-2", "Dummy", map[string]any{"value": "x"}),
	})}})
	if sub.pendingComplete != nil || sub.pendingCompleteCallID != "" {
		t.Fatalf("invalid Complete must not become a pending completion: %#v", sub.pendingComplete)
	}
	if sub.pendingRejectedCompleteErr == nil || sub.pendingRejectedCompleteCallID != "call-1" {
		t.Fatalf("pending rejected completion = %q/%v, want call-1 with the typed-result error", sub.pendingRejectedCompleteCallID, sub.pendingRejectedCompleteErr)
	}

	// The sibling tool settles first; only then is the rejected Complete
	// appended and the bounded follow-up issued.
	sub.handleToolResult(&toolResult{CallID: "call-2", Name: "Dummy", ArgsJSON: `{"value":"x"}`, Result: "ok", TurnID: 1})
	select {
	case evt := <-parent.eventCh:
		t.Fatalf("invalid Complete terminated the task with %#v instead of a bounded follow-up", evt)
	default:
	}
	if result := waitForSubAgentLLMResult(t, sub, time.Second); result.err != nil {
		t.Fatalf("follow-up request failed: %v", result.err)
	}
	if got := sub.turn.SubAgentCompletionRecoveryCount; got != 1 {
		t.Fatalf("completion recovery count = %d, want 1", got)
	}
	if sub.pendingRejectedCompleteErr != nil || sub.pendingRejectedCompleteCallID != "" {
		t.Fatalf("pending rejected completion not cleared after rejection: %q/%v", sub.pendingRejectedCompleteCallID, sub.pendingRejectedCompleteErr)
	}
	msgs := sub.ctxMgr.Snapshot()
	foundRejected := false
	for _, msg := range msgs {
		if msg.Role != "tool" || msg.ToolCallID != "call-1" {
			continue
		}
		foundRejected = strings.HasPrefix(msg.Content, "Completion rejected:") && strings.Contains(msg.Content, "result or result_ref requires result_type")
		break
	}
	if !foundRejected {
		t.Fatalf("missing rejected tool result in %#v", msgs)
	}
}

func TestSubAgentRepeatedInvalidCompleteFailsAfterRecoveryBudgetSpent(t *testing.T) {
	parent, sub := newMixedBatchTestSubAgent(t)
	sub.turn.SubAgentCompletionRecoveryCount = 1
	sub.handleLLMResponse(&llmResult{turnID: 1, resp: &message.Response{ToolCalls: convertCalls([]messageToolCall{
		mustJSONToolCall(t, "call-1", "complete", map[string]any{"summary": "   "}),
	})}})

	select {
	case evt := <-parent.eventCh:
		if evt.Type != EventAgentError {
			t.Fatalf("event.Type = %q, want %q", evt.Type, EventAgentError)
		}
		if err, ok := evt.Payload.(error); !ok || !strings.Contains(err.Error(), "summary is required") {
			t.Fatalf("error payload = %#v, want rejected-after-retry error with the arg cause", evt.Payload)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for bounded recovery failure")
	}
}

// TestSubAgentDeferredCompletionRetainsStructuredEnvelope pins that a
// completion deferred for outstanding join-children keeps its structured
// envelope (files, risks, follow-ups) intact on the pending intent until the
// children settle and delivery resumes.
func TestSubAgentDeferredCompletionRetainsStructuredEnvelope(t *testing.T) {
	parent, sub := newMixedBatchTestSubAgent(t)
	parent.subs.mu.Lock()
	parent.subs.taskRecords["child-1"] = &DurableTaskRecord{
		TaskID:           "child-1",
		OwnerTaskID:      sub.taskID,
		JoinToOwner:      true,
		State:            string(SubAgentStateRunning),
		LatestInstanceID: "worker-child",
	}
	parent.subs.mu.Unlock()

	sub.handleLLMResponse(&llmResult{
		turnID: 1,
		resp: &message.Response{ToolCalls: convertCalls([]messageToolCall{
			mustJSONToolCall(t, "call-1", "complete", map[string]any{
				"summary":               "final summary",
				"files_changed":         []string{"internal/a.go"},
				"known_risks":           []string{"manual QA"},
				"follow_up_recommended": []string{"review"},
			}),
		})},
	})

	pending := sub.PendingCompleteIntent()
	if pending == nil || pending.Envelope == nil {
		t.Fatalf("PendingCompleteIntent() = %#v, want structured envelope", pending)
	}
	if got := pending.Envelope.FilesChanged; len(got) != 1 || got[0] != "internal/a.go" {
		t.Fatalf("pending files_changed = %#v", got)
	}
	if !slices.Contains(pending.Envelope.KnownRisks, "manual QA") || !slices.Contains(pending.Envelope.FollowUpRecommended, "review") {
		t.Fatalf("pending envelope = %#v, want the declared risks and follow-ups preserved", pending.Envelope)
	}
}

func TestCoordinationSnapshotDoesNotDeadlockOnWaitingDescendant(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	sub := newControllableTestSubAgent(t, a, "task-parent")
	sub.instanceID = "worker-parent"
	sub.setState(SubAgentStateWaitingDescendant, "waiting for child")
	sub.runtimeState.stateChangedAt = time.Now().Add(-coordinationSnapshotStallAfter - time.Minute)
	a.subs.mu.Lock()
	delete(a.subs.subAgents, "worker-1")
	a.subs.subAgents[sub.instanceID] = sub
	a.subs.taskRecords[sub.taskID] = &DurableTaskRecord{
		TaskID:           sub.taskID,
		LatestInstanceID: sub.instanceID,
		State:            string(SubAgentStateWaitingDescendant),
	}
	a.subs.mu.Unlock()
	done := make(chan string, 1)
	go func() {
		done <- a.buildCoordinationSnapshotOverlay()
	}()
	select {
	case out := <-done:
		if out == "" {
			t.Fatal("snapshot unexpectedly empty")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("buildCoordinationSnapshotOverlay appears deadlocked")
	}
}

// The typed-result group is optional metadata. Once the model has spent its one
// correction and still cannot pair the fields, destroying a finished task is a
// worse outcome than delivering it without the group, so the completion settles
// with the summary intact and the drop recorded as a limitation.
func TestUnpairedTypedResultSettlesDegradedAfterRecoveryBudgetSpent(t *testing.T) {
	parent, sub := newMixedBatchTestSubAgent(t)
	sub.turn.SubAgentCompletionRecoveryCount = 1
	sub.handleLLMResponse(&llmResult{turnID: 1, resp: &message.Response{ToolCalls: convertCalls([]messageToolCall{
		mustJSONToolCall(t, "call-1", "complete", map[string]any{
			"summary":               "implemented the parser fix",
			"files_changed":         []string{"internal/parser/parse.go"},
			"remaining_limitations": []string{"docs not updated"},
			"result":                map[string]any{"value": 1},
		}),
	})}})

	select {
	case evt := <-parent.eventCh:
		if evt.Type != EventAgentDone {
			t.Fatalf("event.Type = %q, want %q (an unpaired typed result must not destroy the delivery)", evt.Type, EventAgentDone)
		}
		result, ok := evt.Payload.(*AgentResult)
		if !ok {
			t.Fatalf("payload = %#v, want *AgentResult", evt.Payload)
		}
		if result.Summary != "implemented the parser fix" {
			t.Fatalf("summary = %q, want the model's summary preserved", result.Summary)
		}
		if result.Envelope == nil {
			t.Fatal("envelope = nil, want the structured fields preserved")
		}
		if !slices.Contains(result.Envelope.FilesChanged, "internal/parser/parse.go") {
			t.Fatalf("files_changed = %#v, want the declared file preserved", result.Envelope.FilesChanged)
		}
		if result.Envelope.ResultType != "" || len(result.Envelope.Result) != 0 || result.Envelope.ResultRef != nil {
			t.Fatalf("typed result = (%q, %s, %#v), want it stripped", result.Envelope.ResultType, result.Envelope.Result, result.Envelope.ResultRef)
		}
		if !slices.Contains(result.Envelope.RemainingLimitations, "docs not updated") {
			t.Fatalf("remaining_limitations = %#v, want the model's own limitations kept", result.Envelope.RemainingLimitations)
		}
		if !slices.Contains(result.Envelope.RemainingLimitations, droppedTypedResultLimitation) {
			t.Fatalf("remaining_limitations = %#v, want the dropped-result note appended", result.Envelope.RemainingLimitations)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for the degraded completion")
	}
}
