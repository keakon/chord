package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/keakon/golog"
	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/agent/requestshape"
	"github.com/keakon/chord/internal/agent/requesttrim"
	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/logtest"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

func TestRefreshVisibleContextReductionStatsDoesNotRememberPreparedRequest(t *testing.T) {
	a := &MainAgent{projectConfig: &config.Config{Context: config.ContextConfig{Reduction: config.ContextReductionConfig{}}}}
	largeReadOutput := strings.Repeat("large read output ", requesttrim.DefaultPolicy().ReadLikeOutputBytes)
	msgs := []message.Message{
		{Role: "user", Content: "u1"},
		{Role: "assistant", ToolCalls: []message.ToolCall{{ID: "tc1", Name: tools.NameWebFetch, Args: json.RawMessage(`{"url":"https://example.com/a.go"}`)}}},
		{Role: "tool", ToolCallID: "tc1", Content: largeReadOutput},
		{Role: "user", Content: "u2"},
		{Role: "user", Content: "u3"},
	}

	a.refreshVisibleContextReductionStats(msgs)
	stats := a.currentMainContextReductionStats()
	if stats.Bytes <= 0 || stats.CurrentBytes <= 0 {
		t.Fatalf("visible reduction stats = %+v, want reduced bytes and current surface", stats)
	}
	a.loopReductionMu.Lock()
	lastPreparedTurnID := a.lastPreparedLLMTurnID
	lastPreparedPrefixLen := len(a.lastPreparedLLMRequestPrefix)
	a.loopReductionMu.Unlock()
	if lastPreparedTurnID != 0 || lastPreparedPrefixLen != 0 {
		t.Fatalf("refresh should not remember a prepared request, turn=%d prefix_len=%d", lastPreparedTurnID, lastPreparedPrefixLen)
	}
}

func TestActivateLoadedSessionRefreshesVisibleContextReductionStats(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.projectConfig = &config.Config{Context: config.ContextConfig{Reduction: config.ContextReductionConfig{}}}
	largeReadOutput := strings.Repeat("large read output ", requesttrim.DefaultPolicy().ReadLikeOutputBytes)
	msgs := []message.Message{
		{Role: "user", Content: "u1"},
		{Role: "assistant", ToolCalls: []message.ToolCall{{ID: "tc1", Name: tools.NameWebFetch, Args: json.RawMessage(`{"url":"https://example.com/a.go"}`)}}},
		{Role: "tool", ToolCallID: "tc1", Content: largeReadOutput},
		{Role: "user", Content: "u2"},
		{Role: "user", Content: "u3"},
	}

	result := a.activateLoadedSession(&loadedSessionState{SessionPath: a.sessionDir, Messages: msgs})
	if result.MessageCount != len(msgs) {
		t.Fatalf("MessageCount = %d, want %d", result.MessageCount, len(msgs))
	}
	// The scan now runs off the restore path on a deferred loop event; the
	// test harness never starts the loop, so invoke the handler directly.
	a.handleRefreshReductionStats()
	stats := a.currentMainContextReductionStats()
	if stats.Bytes <= 0 || stats.CurrentBytes <= 0 {
		t.Fatalf("restored visible reduction stats = %+v, want reduced bytes and current surface", stats)
	}
	if gotRaw := a.GetContextBytes(); stats.CurrentBytes >= gotRaw {
		t.Fatalf("CurrentBytes = %d, want less than raw restored bytes %d", stats.CurrentBytes, gotRaw)
	}
}

func TestTopContextContributorsIncludesToolNames(t *testing.T) {
	msgs := []message.Message{
		{Role: "user", Content: "small"},
		{Role: "assistant", ToolCalls: []message.ToolCall{{ID: "tc1", Name: tools.NameGrep, Args: json.RawMessage(`{"pattern":"TODO"}`)}}},
		{Role: "tool", ToolCallID: "tc1", Content: strings.Repeat("match\n", 100)},
	}

	contributors := topContextContributors(nil, msgs, 1)
	if len(contributors) != 1 {
		t.Fatalf("len(contributors) = %d, want 1", len(contributors))
	}
	if contributors[0].Role != "tool" || contributors[0].Tool != tools.NameGrep || contributors[0].Index != 2 {
		t.Fatalf("top contributor = %+v, want Grep tool result at index 2", contributors[0])
	}
	if !strings.Contains(contextContributorLabel(contributors[0]), "tool/"+tools.NameGrep) {
		t.Fatalf("label missing tool name: %q", contextContributorLabel(contributors[0]))
	}
}

func TestCallLLMLogsPreparedReductionStatsWhenFocusedSubAgentExists(t *testing.T) {
	var buf bytes.Buffer
	logger := logtest.NewLogger(&buf, golog.DebugLevel)
	log.SetDefaultLogger(logger)
	defer log.SetDefaultLogger(logtest.NewLogger(nil, golog.InfoLevel))

	a := newReadyTestMainAgent(t)
	a.newTurn("")
	a.projectConfig = &config.Config{Context: config.ContextConfig{Reduction: config.ContextReductionConfig{
		ShellSuccessAgeTurns: 1,
		ShellSuccessBytes:    10,
	}}}
	providerCfg := llm.NewProviderConfig("test", config.ProviderConfig{
		Type: config.ProviderTypeMessages,
		Models: map[string]config.ModelConfig{
			"model": {Limit: config.ModelLimit{Context: 8192, Output: 1024}},
		},
	}, []string{"test-key"})
	provider := &captureMessagesProvider{}
	a.llmClient = llm.NewClient(providerCfg, provider, "model", 1024, "")
	a.providerModelRef = "test/model"
	a.llmMu.Lock()
	a.runningModelRef = "test/model"
	a.llmMu.Unlock()
	focusTestSubAgent(t, a, "sub-1", "sub", "")

	largeOutput := strings.Repeat("test output line\n", 500)
	msgs := []message.Message{
		{Role: "user", Content: "u1"},
		{Role: "assistant", ToolCalls: []message.ToolCall{{ID: "tc1", Name: tools.NameShell, Args: json.RawMessage(`{"command":"npm test"}`)}}},
		{Role: "tool", ToolCallID: "tc1", Content: largeOutput},
		{Role: "user", Content: "u2"},
		{Role: "user", Content: "u3"},
		{Role: "user", Content: "u4"},
	}

	if _, err := a.callLLMForRequest(context.Background(), llmRequestInput{Messages: msgs}, 0); err != nil {
		t.Fatalf("callLLM: %v", err)
	}
	logOutput := buf.String()
	if !strings.Contains(logOutput, "LLM request context contributors") {
		t.Fatalf("expected request context debug log, got %q", logOutput)
	}
	if !strings.Contains(logOutput, "reduction_messages=1") {
		t.Fatalf("expected main prepared reduction stats in log, got %q", logOutput)
	}
	if !strings.Contains(logOutput, "reduction_saved_delta=") ||
		!strings.Contains(logOutput, "reduction_protect_reason=") ||
		!strings.Contains(logOutput, "reduction_reuse_reason=") ||
		!strings.Contains(logOutput, "previous_model=") ||
		!strings.Contains(logOutput, "model_changed=") {
		t.Fatalf("expected extended reduction debug fields in log, got %q", logOutput)
	}
	if strings.Contains(logOutput, "provider_key_index=") {
		t.Fatalf("expected single-key provider not to emit provider_key_index, got %q", logOutput)
	}
	if strings.Contains(logOutput, "reduction_messages=0") {
		t.Fatalf("expected focused subagent not to zero-out main request stats, got %q", logOutput)
	}
	if got := a.GetContextReductionStats(); !isZeroContextReductionStats(got) {
		t.Fatalf("focused subagent stats = %+v, want zero-valued subagent stats to confirm log did not come from focus getter", got)
	}
	if len(provider.messages) == 0 {
		t.Fatal("expected provider to receive prepared messages")
	}
}

func TestPrepareMessagesForLLM_ResetsReductionStatsWhenNothingReduced(t *testing.T) {
	a := &MainAgent{}
	largeOutput := strings.Repeat("test output line\n", 500)
	msgs := []message.Message{
		{Role: "user", Content: "u1"},
		{Role: "assistant", ToolCalls: []message.ToolCall{
			{ID: "tc1", Name: "shell", Args: json.RawMessage(`{"command":"npm test"}`)},
		}},
		{Role: "tool", ToolCallID: "tc1", Content: largeOutput},
		{Role: "user", Content: "u2"},
		{Role: "user", Content: "u3"},
		{Role: "user", Content: "u4"},
	}
	_ = a.prepareMessagesForLLM(msgs)
	if stats := a.GetContextReductionStats(); stats.Messages == 0 || stats.Bytes == 0 {
		t.Fatalf("expected initial reduction stats, got %+v", stats)
	}
	_ = a.prepareMessagesForLLM([]message.Message{{Role: "user", Content: "short"}})
	if stats := a.GetContextReductionStats(); !isZeroContextReductionStats(stats) {
		t.Fatalf("expected stats reset when request has no reduction, got %+v", stats)
	}
}

func TestPrepareMessagesForLLM_RecordsSkipAndOverCompressionStats(t *testing.T) {
	a := newReductionTestMainAgent(t, t.TempDir())
	a.projectConfig = &config.Config{
		Context: config.ContextConfig{Reduction: config.ContextReductionConfig{
			HighRiskProtectAgeTurns: 4,
			ReadLikeAgeTurns:        1,
			ReadLikeOutputBytes:     80,
			StaleAgeTurns:           9,
			StaleOutputBytes:        80,
			ShellSuccessAgeTurns:    9,
			ShellSuccessBytes:       1 << 20,
			MinIncrementalTokens:    1,
		}},
	}
	readContent := "READ_RESULT lines=1-120 total=120\n" + strings.Repeat("line content for read result\n", 120)
	freshHighRisk := "panic: boom\n" + strings.Repeat("stack frame\n", 40)
	largeFreshUnmatched := strings.Repeat("fresh unmatched output\n", 20)
	a.newTurn("")

	// Pass 1: the only copy of the fetch ages out and is genuinely summarized
	// away. A same-pass duplicate would collapse as repeated instead, which is
	// not discard evidence — the reread signal is inherently cross-request.
	fetchArgs := json.RawMessage(`{"url":"https://example.com/spec"}`)
	msgs := []message.Message{
		{Role: "user", Content: "u1"},
		{Role: "assistant", ToolCalls: []message.ToolCall{{ID: "tc1", Name: tools.NameWebFetch, Args: fetchArgs}}},
		{Role: "tool", ToolCallID: "tc1", Content: readContent},
		{Role: "user", Content: "u2"},
	}
	if prepared := a.prepareMessagesForLLM(msgs); prepared[2].Content == readContent {
		t.Fatal("fixture fetch must be summarized on the first pass")
	}

	// Pass 2: fresh high-risk and large-but-unmatched outputs feed the
	// skip-reason stats, then the model re-fetches the same URL. The re-fetch
	// is the newest batch, so the fresh copy is kept by age and the discarded
	// evidence surfaces as the reread over-compression signal.
	msgs = append(msgs,
		message.Message{Role: "assistant", ToolCalls: []message.ToolCall{{ID: "tc3", Name: tools.NameShell, Args: json.RawMessage(`{"command":"go test ./..."}`)}}},
		message.Message{Role: "tool", ToolCallID: "tc3", Content: freshHighRisk},
		message.Message{Role: "assistant", ToolCalls: []message.ToolCall{{ID: "tc4", Name: tools.NameShell, Args: json.RawMessage(`{"command":"printf lots"}`)}}},
		message.Message{Role: "tool", ToolCallID: "tc4", Content: largeFreshUnmatched},
		message.Message{Role: "assistant", ToolCalls: []message.ToolCall{{ID: "tc2", Name: tools.NameWebFetch, Args: fetchArgs}}},
		message.Message{Role: "tool", ToolCallID: "tc2", Content: readContent},
	)

	a.prepareMessagesForLLM(msgs)
	stats := a.GetContextReductionStats()
	if stats.OverCompression[contextReductionOverCompressionReread] == 0 {
		t.Fatalf("expected reread over-compression signal, stats=%+v", stats)
	}
	// The aggregate says reduction went too far somewhere; the per-tool key is
	// what makes the signal actionable — it names whose summary provoked the
	// re-fetch, which is the input a per-shape retention tune needs.
	if got := stats.OverCompressionByTool[contextReductionOverCompressionReread+"/"+tools.NameWebFetch]; got == 0 {
		t.Fatalf("reread signal not attributed to %s: over_compression_by_tool=%+v", tools.NameWebFetch, stats.OverCompressionByTool)
	}
	// The aggregate stays summable: the per-tool breakdown lives in its own map,
	// so totalling OverCompression cannot double-count the same event.
	if got := stats.OverCompression[contextReductionOverCompressionReread]; got != 1 {
		t.Fatalf("aggregate reread count = %d, want exactly 1: over_compression=%+v", got, stats.OverCompression)
	}
	if stats.SkippedByReason[contextReductionSkipRecentHighRisk] == 0 {
		t.Fatalf("expected recent high-risk skip reason, stats=%+v", stats)
	}
	if stats.SkippedByReason[contextReductionSkipLargeUnreduced] == 0 {
		t.Fatalf("expected large-but-unreduced skip reason, stats=%+v", stats)
	}
}

func TestPrepareMessagesForLLM_ClassifiesRereadRevisionStats(t *testing.T) {
	for _, tc := range []struct {
		name    string
		changed bool
		wantKey string
		reject  string
	}{
		{
			name:    "same revision",
			changed: false,
			wantKey: contextReductionOverCompressionRereadSameRevision,
			reject:  contextReductionOverCompressionRereadChangedRevision,
		},
		{
			name:    "changed revision",
			changed: true,
			wantKey: contextReductionOverCompressionRereadChangedRevision,
			reject:  contextReductionOverCompressionRereadSameRevision,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			projectRoot := t.TempDir()
			a := newReductionTestMainAgent(t, projectRoot)
			a.projectConfig = &config.Config{Context: config.ContextConfig{Reduction: config.ContextReductionConfig{
				MinToolResultsPrune:  1,
				ReadLikeAgeTurns:     1,
				ReadLikeOutputBytes:  40,
				MinIncrementalTokens: 1,
			}}}
			a.newTurn("")
			filePath := filepath.Join(projectRoot, "a.go")
			writeRevision := func(content string) string {
				if err := os.WriteFile(filePath, []byte(content), 0o644); err != nil {
					t.Fatal(err)
				}
				hash, _, _, err := verifiedCurrentFileHash(filePath)
				if err != nil {
					t.Fatal(err)
				}
				return hash
			}
			firstHash := writeRevision("package a\n")
			readState := func(revision string) *message.ToolFileState {
				return &message.ToolFileState{Reads: []message.TrackedFileState{{Path: filePath, SHA256: revision, Exists: true}}}
			}
			content := "READ_RESULT lines=1-100 total=100\n" + strings.Repeat("source line\n", 100)
			firstMessages := []message.Message{
				{Role: message.RoleUser, Content: "u1"},
				{Role: message.RoleAssistant, RequestBatch: 1, ToolCalls: []message.ToolCall{{ID: "r1", Name: tools.NameRead, Args: json.RawMessage(`{"path":"a.go"}`)}}},
				{Role: message.RoleTool, ToolCallID: "r1", ToolStatus: "success", Content: content, FileState: readState(firstHash)},
				{Role: message.RoleAssistant, RequestBatch: 2, ToolCalls: []message.ToolCall{{ID: "e1", Name: tools.NameEdit, Args: json.RawMessage(`{"path":"a.go","old_string":"x","new_string":"y"}`)}}},
				{Role: message.RoleTool, ToolCallID: "e1", ToolStatus: "success", Content: "edited"},
			}
			setTestRequestBatch(a, firstMessages, 2)
			first := a.prepareMessagesForLLM(firstMessages)
			if !strings.Contains(first[2].Content, "truncated=") {
				t.Fatalf("first request did not reduce the read: %q", first[2].Content)
			}
			lastHash := firstHash
			if tc.changed {
				lastHash = writeRevision("package a\n\n// changed\n")
			}
			messages := append(append([]message.Message(nil), firstMessages...),
				message.Message{Role: message.RoleAssistant, RequestBatch: 3, ToolCalls: []message.ToolCall{{ID: "r2", Name: tools.NameRead, Args: json.RawMessage(`{"path":"a.go"}`)}}},
				message.Message{Role: message.RoleTool, ToolCallID: "r2", ToolStatus: "success", Content: content, FileState: readState(lastHash)},
			)
			setTestRequestBatch(a, messages, 3)
			a.prepareMessagesForLLM(messages)
			stats := a.GetContextReductionStats()
			if stats.OverCompression[contextReductionOverCompressionReread] == 0 || stats.OverCompression[tc.wantKey] == 0 {
				t.Fatalf("reread stats = %+v, want aggregate and %q", stats.OverCompression, tc.wantKey)
			}
			if stats.OverCompression[tc.reject] != 0 {
				t.Fatalf("reread stats = %+v, did not want %q", stats.OverCompression, tc.reject)
			}
		})
	}
}

func TestContextReductionStatsSnapshotsCloneMaps(t *testing.T) {
	a := newReductionTestMainAgent(t, t.TempDir())
	a.newTurn("")
	turnID := a.currentTurnID()
	preparedMessages := []message.Message{{Role: "user", Content: "u"}}

	preparedStats := ContextReductionStats{
		Messages:        1,
		Bytes:           10,
		ByToolAndRule:   map[string]ContextReductionBucket{tools.NameShell + "/stale": {Messages: 1, Bytes: 10}},
		SkippedByReason: map[string]int{contextReductionSkipRecentHighRisk: 1},
		OverCompression: map[string]int{contextReductionOverCompressionResearch: 1},
	}
	a.setContextReductionStats(preparedStats)
	a.updatePreparedLLMRequestSurface(turnID, preparedMessages)

	a.loopReductionMu.Lock()
	a.contextReductionStats.ByToolAndRule[tools.NameShell+"/stale"] = ContextReductionBucket{Messages: 99, Bytes: 99}
	a.contextReductionStats.SkippedByReason[contextReductionSkipRecentHighRisk] = 99
	a.contextReductionStats.OverCompression[contextReductionOverCompressionResearch] = 99
	a.loopReductionMu.Unlock()
	if got := a.preparedContextReductionStatsForTurn(turnID); !contextReductionStatsEqual(got, preparedStats) {
		t.Fatalf("prepared stats snapshot = %+v, want independent clone %+v", got, preparedStats)
	}

	surfaceStats := ContextReductionStats{
		Messages:        2,
		Bytes:           20,
		ByToolAndRule:   map[string]ContextReductionBucket{tools.NameRead + "/read_like": {Messages: 2, Bytes: 20}},
		SkippedByReason: map[string]int{contextReductionSkipLargeUnreduced: 2},
		OverCompression: map[string]int{contextReductionOverCompressionReread: 2},
	}
	a.loopReductionMu.Lock()
	a.lastPreparedLLMTurnID = turnID
	a.lastPreparedLLMRequestShape = requestshape.Shapes(preparedMessages)
	a.lastPreparedLLMRequestPrefix = cloneMessageSliceForRequestShape(preparedMessages)
	a.lastPreparedReductionStats = cloneContextReductionStats(surfaceStats)
	a.loopReductionMu.Unlock()

	// The borrowed surface view must not let a caller mutate the agent's own
	// stats maps through the returned clone.
	surface, ok := a.stableReductionSurfaceCandidate(turnID)
	if !ok {
		t.Fatal("stableReductionSurfaceCandidate() not available, want the stored surface")
	}
	snapshot := cloneContextReductionStats(surface.Stats)
	snapshot.ByToolAndRule[tools.NameRead+"/read_like"] = ContextReductionBucket{Messages: 55, Bytes: 55}
	snapshot.SkippedByReason[contextReductionSkipLargeUnreduced] = 55
	snapshot.OverCompression[contextReductionOverCompressionReread] = 55
	if got := a.preparedContextReductionStatsForTurn(turnID); !contextReductionStatsEqual(got, surfaceStats) {
		t.Fatalf("prepared surface stats after snapshot mutation = %+v, want independent clone %+v", got, surfaceStats)
	}
}

func TestSetIdleAndDrainPendingKeepsVisibleContextReductionStats(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.newTurn("")
	want := ContextReductionStats{Messages: 2, Bytes: 2048, TokensBefore: 1000, TokensAfter: 700, TokensSaved: 300}
	a.setContextReductionStats(want)
	a.rememberPreparedLLMRequest(a.currentTurnID(), []message.Message{{Role: "user", Content: "u"}}, []message.Message{{Role: "user", Content: "u"}}, nil, nil, 0, a.contextReductionPolicy())

	a.setIdleAndDrainPending()

	if got := a.GetContextReductionStats(); !contextReductionStatsEqual(got, want) {
		t.Fatalf("visible reduction stats after idle = %+v, want %+v", got, want)
	}
	a.loopReductionMu.Lock()
	stableSurfaceRetained := a.lastPreparedLLMTurnID != 0 && len(a.lastPreparedLLMRequestPrefix) != 0
	transientStateCleared := a.wrapUpGraceTurnID == 0 && a.wrapUpGraceRemaining == 0
	a.loopReductionMu.Unlock()
	if !stableSurfaceRetained {
		t.Fatal("idle should retain the stable request surface for cross-turn cache reuse")
	}
	if !transientStateCleared {
		t.Fatal("idle should clear turn-local reduction state")
	}
}

func TestHandleCompactionReadyClearsPreparedReductionStats(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.ctxMgr.Append(message.Message{Role: "user", Content: "one"})
	a.ctxMgr.Append(message.Message{Role: "assistant", Content: "two"})
	a.EnableLoopMode("finish")
	a.loopReductionMu.Lock()
	a.lastPreparedLLMTurnID = 99
	a.lastPreparedLLMRequestPrefix = []message.Message{{Role: "tool", Content: "old prepared prefix"}}
	a.lastPreparedReductionStats = ContextReductionStats{Messages: 3, Bytes: 4096}
	a.contextReductionStats = ContextReductionStats{Messages: 3, Bytes: 4096}
	a.loopReductionMu.Unlock()
	a.startCompactionState(1, compactionTarget{sessionEpoch: a.sessionEpoch}, compactionTriggerManual, continuationPlan{kind: compactionResumeIdle})
	a.compactionState.headSplit = 2

	draft := &compactionDraft{
		NewMessages:    []message.Message{{Role: "user", Content: "[Context Summary]", IsCompactionSummary: true}},
		HeadSplit:      2,
		Index:          1,
		AbsHistoryPath: filepath.Join(a.sessionDir, "history-1.md"),
		SummaryMode:    "truncate_only",
		PlanID:         1,
		Target:         compactionTarget{sessionEpoch: a.sessionEpoch},
		Manual:         true,
	}

	a.handleCompactionReady(Event{Type: EventCompactionReady, Payload: draft})

	if got := a.GetContextReductionStats(); !isZeroContextReductionStats(got) {
		t.Fatalf("context reduction stats after compaction = %+v, want zero", got)
	}
	a.loopReductionMu.Lock()
	defer a.loopReductionMu.Unlock()
	if len(a.lastPreparedLLMRequestPrefix) != 0 || !isZeroContextReductionStats(a.lastPreparedReductionStats) || a.lastPreparedLLMTurnID != 0 {
		t.Fatalf("last prepared reduction snapshot not cleared: turn=%d prefix=%#v stats=%+v", a.lastPreparedLLMTurnID, a.lastPreparedLLMRequestPrefix, a.lastPreparedReductionStats)
	}
}
