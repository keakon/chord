package agent

import (
	"context"
	"encoding/json"
	"errors"
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
	sub.verificationLedger = []verificationLedgerEntry{{
		ToolCallID: "verify-1",
		Command:    "go test ./internal/a",
		Status:     "passed",
		Summary:    "ok",
	}}
	sub.handleLLMResponse(&llmResult{
		turnID: 1,
		resp: &message.Response{ToolCalls: convertCalls([]messageToolCall{
			mustJSONToolCall(t, "call-1", "complete", map[string]any{
				"summary":               "done",
				"files_changed":         []string{"internal/a.go"},
				"verification_run":      []string{"go test ./internal/a"},
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
	if got := strings.Join(env.VerificationRun, ","); got != "go test ./internal/a" {
		t.Fatalf("verification_run = %q", got)
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

func TestCompleteRejectsArtifactOutsideSessionBoundary(t *testing.T) {
	parent, sub := newMixedBatchTestSubAgent(t)
	sub.handleLLMResponse(&llmResult{
		turnID: 1,
		resp: &message.Response{ToolCalls: convertCalls([]messageToolCall{
			mustJSONToolCall(t, "call-1", "complete", map[string]any{
				"summary":   "done",
				"artifacts": []map[string]any{{"rel_path": "../outside.txt"}},
			}),
		})},
	})
	select {
	case evt := <-parent.eventCh:
		if evt.Type != EventAgentError || !strings.Contains(evt.Payload.(error).Error(), "artifact path escapes") {
			t.Fatalf("event = %#v", evt)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for invalid Complete error")
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
	sub.tools.Register(dummyMutatingTool{dummyTool: dummyTool{name: "OpaqueMutation"}})
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
	_, sub := newMixedBatchTestSubAgent(t)
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
	if sub.idleTimer != nil {
		t.Fatal("unparseable thinking toolcall entered idle wait")
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

// Terminal recovery and completion-verification retry both issue an LLM request
// from inside handleLLMResponse, after runLoop's finishLLMRequest already cleared
// the in-flight gate. They must re-arm it: otherwise runLoop sees an idle
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
		{
			name: "completion verification retry",
			trigger: func(t *testing.T, sub *SubAgent) {
				sub.retryCompletionVerification(errors.New("verification commands did not run"))
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
	sub.turn.SubAgentTerminalRecoveryCount = 1
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
		LastCompletion:     &CompletionEnvelope{Summary: "research complete", FilesChanged: []string{"internal/a.go"}, VerificationRun: []string{"go test ./internal/a"}, VerificationRecords: []VerificationRecord{{ToolCallID: "verify-1", Command: "go test ./internal/a", Status: "passed", Summary: "ok"}}},
		ExpectedWriteScope: tools.WriteScope{Files: []string{"internal/a.go"}},
	}
	block := a.buildCoordinationSnapshotOverlay()
	for _, want := range []string{"SubAgent coordination snapshot", "task_id: task-1", "artifact_refs: artifacts/subagents/worker-1/report.md(research_report)", "files_changed: internal/a.go", "verification_run: go test ./internal/a", "verification:", "go test ./internal/a [passed]: ok", "write_scope: file:internal/a.go"} {
		if !strings.Contains(block, want) {
			t.Fatalf("snapshot missing %q:\n%s", want, block)
		}
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
}

func mustMarshalJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
