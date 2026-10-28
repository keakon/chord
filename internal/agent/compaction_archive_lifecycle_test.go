package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

func TestProduceCompactionDraftCapturesSummaryKeyFileRevision(t *testing.T) {
	t.Parallel()
	projectRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(projectRoot, "src"), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	path := filepath.Join(projectRoot, "src", "current_task.go")
	if err := os.WriteFile(path, []byte("package current\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	a := newTestMainAgent(t, projectRoot)
	a.globalConfig.Context.Compaction.Profile = config.CompactionProfileArchival
	a.SetProviderModelRef("sample/compact-model")
	providerCfg := llm.NewProviderConfig("sample", config.ProviderConfig{
		Type: "stub",
		Models: map[string]config.ModelConfig{
			"compact-model": {Limit: config.ModelLimit{Context: 16384, Output: 2048}},
		},
	}, []string{"test-key"})
	provider := &countingCompactionProvider{response: &message.Response{Content: validCompactionSummaryForTest("history-1.md")}}
	client := llm.NewClient(providerCfg, provider, "compact-model", 2048, "")
	a.SetModelSwitchFactory(func(string, []string, string) (*llm.Client, string, int, error) {
		return client, "compact-model", 16384, nil
	})
	snapshot := []message.Message{
		{Role: message.RoleUser, Content: "inspect current task"},
		{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{{ID: "read-1", Name: tools.NameRead, Args: json.RawMessage(`{"path":"src/current_task.go"}`)}}},
		{Role: message.RoleTool, ToolCallID: "read-1", Content: "READ_RESULT lines=1-1 total=1\npackage current"},
		{Role: message.RoleAssistant, Content: "continue"},
	}
	draft, err := a.produceCompactionDraftAsync(t.Context(), a.testCompactionInput(snapshot, false, 1, compactionTarget{sessionEpoch: a.sessionEpoch}, len(snapshot), compactionProfileArchival, "", nil, a.captureCompactionArchiveMeta()))
	if err != nil {
		t.Fatalf("produceCompactionDraftAsync: %v", err)
	}
	if len(draft.NewMessages) != 1 {
		t.Fatalf("len(NewMessages) = %d, want 1", len(draft.NewMessages))
	}
	want := computeFileHash(path)
	if got := draft.NewMessages[0].CompactionFileRevisions["src/current_task.go"]; got != want || got == "" {
		t.Fatalf("checkpoint revision = %q, want %q", got, want)
	}
}

func TestHandleCompactionReadyIgnoresStaleSessionDraft(t *testing.T) {
	t.Parallel()
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.startCompactionState(2, compactionTarget{sessionEpoch: 2, turnEpoch: 1}, compactionTriggerManual, continuationPlan{kind: compactionResumeIdle})
	a.sessionEpoch = 2
	a.turnEpoch = 1

	draft := &compactionDraft{
		PlanID:         1,
		Target:         compactionTarget{sessionEpoch: 1},
		NewMessages:    []message.Message{{Role: "user", Content: "[Context Summary]\nsummary"}},
		Index:          1,
		AbsHistoryPath: "/tmp/history-1.md",
		SummaryMode:    "truncate_only",
		ModelRef:       "fallback",
	}
	a.handleCompactionReady(Event{Type: EventCompactionReady, Payload: draft})

	if !a.IsCompactionRunning() {
		t.Fatal("stale draft should not clear compaction state")
	}
	if got := len(a.ctxMgr.Snapshot()); got != 0 {
		t.Fatalf("len(snapshot) = %d, want 0 for stale ready event", got)
	}
}

func TestHandleCompactionReadyIgnoresCurrentPlanFromOldSession(t *testing.T) {
	t.Parallel()
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.sessionEpoch = 1
	oldSessionEpoch := a.sessionEpoch
	a.startCompactionState(2, compactionTarget{sessionEpoch: oldSessionEpoch}, compactionTriggerManual, continuationPlan{kind: compactionResumeIdle})
	a.sessionEpoch++

	a.handleCompactionReady(Event{Type: EventCompactionReady, Payload: &compactionDraft{
		PlanID:      2,
		Target:      compactionTarget{sessionEpoch: oldSessionEpoch},
		NewMessages: []message.Message{{Role: "user", Content: "[Context Summary]\nold session"}},
	}})

	if !a.IsCompactionRunning() {
		t.Fatal("ready event from an old session must not release current compaction state")
	}
	if got := len(a.ctxMgr.Snapshot()); got != 0 {
		t.Fatalf("old-session ready event applied %d messages", got)
	}
}

func TestHandleCompactionFailedIgnoresCurrentPlanFromOldSession(t *testing.T) {
	t.Parallel()
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.sessionEpoch = 1
	oldSessionEpoch := a.sessionEpoch
	a.startCompactionState(2, compactionTarget{sessionEpoch: oldSessionEpoch}, compactionTriggerManual, continuationPlan{kind: compactionResumeIdle})
	a.sessionEpoch++

	a.handleCompactionFailed(Event{Type: EventCompactionFailed, Payload: &compactionFailure{
		planID: 2,
		target: compactionTarget{sessionEpoch: oldSessionEpoch},
		err:    fmt.Errorf("old session failure"),
	}})

	if !a.IsCompactionRunning() {
		t.Fatal("failure event from an old session must not release current compaction state")
	}
}

func TestStopCompactionForSessionSwitchCancelsWorkerWithoutBlocking(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	ctx, cancel := context.WithCancel(t.Context())
	a.beginCompactionState(1, compactionTarget{sessionEpoch: a.sessionEpoch}, compactionTriggerManual, continuationPlan{kind: compactionResumeIdle}, 0, cancel)

	if a.stopCompactionForSessionSwitch() {
		t.Fatal("session switch should be rejected while the compaction worker stops")
	}
	if ctx.Err() != context.Canceled {
		t.Fatalf("compaction context error = %v, want canceled", ctx.Err())
	}
	if !a.IsCompactionRunning() {
		t.Fatal("compaction state must remain active until its terminal event")
	}
}

func TestStopCompactionForSessionSwitchDiscardsReadyDraft(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	a.beginCompactionState(1, compactionTarget{sessionEpoch: a.sessionEpoch}, compactionTriggerManual, continuationPlan{kind: compactionResumeIdle}, 1, nil)
	a.compactionState.readyDraft = &compactionDraft{PlanID: 1, Target: compactionTarget{sessionEpoch: a.sessionEpoch}}

	if !a.stopCompactionForSessionSwitch() {
		t.Fatal("session switch should proceed after discarding a completed draft")
	}
	if a.IsCompactionRunning() {
		t.Fatal("completed compaction state should be cleared")
	}
	if got := a.mainSessionIdentity(a.sessionDir).WindowNumber; got != 0 {
		t.Fatalf("discarded draft advanced window: %d", got)
	}
}

func TestExportCompactionHistoryMetaPendingThenApplied(t *testing.T) {
	t.Parallel()
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	msgs := []message.Message{{Role: "user", Content: "hello"}}

	absPath, sourceRefs, sourceFingerprint, err := a.exportCompactionHistory(msgs, msgs, 1, nil, a.captureCompactionArchiveMeta())
	if err != nil {
		t.Fatalf("exportCompactionHistory: %v", err)
	}
	metaPath := compactionHistoryMetaPath(absPath)
	data, err := os.ReadFile(metaPath)
	if err != nil {
		t.Fatalf("read meta: %v", err)
	}
	var meta compactionHistoryMeta
	if err := json.Unmarshal(data, &meta); err != nil {
		t.Fatalf("unmarshal meta: %v", err)
	}
	if meta.Status != compactionHistoryPending {
		t.Fatalf("pending meta status = %q, want %q", meta.Status, compactionHistoryPending)
	}
	if got := a.mainSessionIdentity(a.sessionDir).WindowNumber; got != 0 {
		t.Fatalf("pending export advanced window: %d", got)
	}
	exportedAt := meta.ExportedAt
	if exportedAt.IsZero() {
		t.Fatal("expected ExportedAt to be set in pending meta")
	}
	a.ctxMgr.RestoreMessages(msgs)

	draft := &compactionDraft{
		PlanID:             1,
		Target:             compactionTarget{sessionEpoch: a.sessionEpoch, turnEpoch: a.turnEpoch},
		NewMessages:        []message.Message{{Role: "user", Content: "[Context Summary]\nsummary"}},
		HeadSplit:          len(msgs),
		Index:              1,
		AbsHistoryPath:     absPath,
		AbsHistoryMetaPath: metaPath,
		SourceRefs:         sourceRefs,
		SourceFingerprint:  sourceFingerprint,
		SummaryMode:        "structured_fallback",
		ModelRef:           "fallback",
	}
	if err := a.applyCompactionDraft(draft); err != nil {
		t.Fatalf("applyCompactionDraft: %v", err)
	}

	data, err = os.ReadFile(metaPath)
	if err != nil {
		t.Fatalf("read updated meta: %v", err)
	}
	if err := json.Unmarshal(data, &meta); err != nil {
		t.Fatalf("unmarshal updated meta: %v", err)
	}
	if meta.Status != compactionHistoryApplied {
		t.Fatalf("applied meta status = %q, want %q", meta.Status, compactionHistoryApplied)
	}
	if !meta.ExportedAt.Equal(exportedAt) {
		t.Fatalf("ExportedAt = %v, want preserved %v", meta.ExportedAt, exportedAt)
	}
	if meta.AppliedAt.IsZero() {
		t.Fatal("expected AppliedAt to be set")
	}
	if len(meta.SourceRefs) != len(msgs) || meta.SourceFingerprint != sourceFingerprint || meta.SourceGeneration == "" {
		t.Fatalf("applied metadata lost provenance: %#v", meta)
	}
}

func TestCleanupStalePendingCompactions(t *testing.T) {
	dir := t.TempDir()
	writeHistory := func(t *testing.T, n int, status string, exportedAt time.Time, withBackup bool) (historyPath, metaPath string) {
		t.Helper()
		historyName := fmt.Sprintf("history-%d.md", n)
		historyPath = filepath.Join(dir, historyName)
		if err := os.WriteFile(historyPath, []byte("Session Export\n"), 0o644); err != nil {
			t.Fatalf("write history %d: %v", n, err)
		}
		metaPath = compactionHistoryMetaPath(historyPath)
		meta := compactionHistoryMeta{Version: 1, HistoryFile: historyName, Status: status, ExportedAt: exportedAt}
		if status == compactionHistoryApplied {
			meta.AppliedAt = exportedAt.Add(time.Minute)
		}
		data, err := json.Marshal(meta)
		if err != nil {
			t.Fatalf("marshal meta %d: %v", n, err)
		}
		if err := os.WriteFile(metaPath, data, 0o644); err != nil {
			t.Fatalf("write meta %d: %v", n, err)
		}
		if withBackup {
			backupPath := filepath.Join(dir, fmt.Sprintf("main.pre-compress-%d.jsonl", n))
			if err := os.WriteFile(backupPath, []byte(`{"role":"user","content":"backup"}`+"\n"), 0o644); err != nil {
				t.Fatalf("write backup %d: %v", n, err)
			}
		}
		return historyPath, metaPath
	}

	old := time.Now().Add(-10 * time.Minute)
	fresh := time.Now()
	staleHistory, staleMeta := writeHistory(t, 1, compactionHistoryPending, old, false)
	freshHistory, freshMeta := writeHistory(t, 2, compactionHistoryPending, fresh, false)
	backupHistory, backupMeta := writeHistory(t, 3, compactionHistoryPending, old, true)
	appliedHistory, appliedMeta := writeHistory(t, 4, compactionHistoryApplied, old, false)

	cleanupStalePendingCompactions(dir, 5*time.Minute)

	for _, path := range []string{staleHistory, staleMeta} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("%s exists after stale pending cleanup; err=%v", filepath.Base(path), err)
		}
	}
	for _, path := range []string{freshHistory, freshMeta, backupHistory, backupMeta, appliedHistory, appliedMeta} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("%s should be preserved: %v", filepath.Base(path), err)
		}
	}
}
