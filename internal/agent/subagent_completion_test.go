package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

func TestStructuredCompleteEnvelopeParsedFromCompleteTool(t *testing.T) {
	parent, sub := newMixedBatchTestSubAgent(t)
	sub.handleLLMResponse(&llmResult{
		turnID: 1,
		resp: &message.Response{ToolCalls: convertCalls([]messageToolCall{
			mustJSONToolCall(t, "call-1", "Complete", map[string]any{
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
	if strings.TrimSpace(read) != "research body" {
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
	if body != "new body" {
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
	if out != "artifact body" {
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
	a.explicitUserTurnCount = 5
	a.taskRecords["task-1"] = &DurableTaskRecord{
		TaskID:             "task-1",
		AgentDefName:       "explorer",
		State:              string(SubAgentStateCompleted),
		ResumePolicy:       taskResumePolicyCompletedFollowUpOnly,
		PlanTaskRef:        "P1",
		SemanticTaskKey:    "coordination-snapshot",
		LastSummary:        "research complete",
		LastUpdatedTurn:    5,
		LastArtifactRefs:   []tools.ArtifactRef{{ID: "art-1", Type: "research_report", RelPath: "artifacts/subagents/worker-1/report.md"}},
		LastCompletion:     &CompletionEnvelope{Summary: "research complete", FilesChanged: []string{"internal/a.go"}, VerificationRun: []string{"go test ./internal/a"}},
		ExpectedWriteScope: tools.WriteScope{Files: []string{"internal/a.go"}},
	}
	block := a.buildCoordinationSnapshotOverlay()
	for _, want := range []string{"SubAgent coordination snapshot", "task_id: task-1", "artifact_refs: artifacts/subagents/worker-1/report.md(research_report)", "files_changed: internal/a.go", "verification_run: go test ./internal/a", "write_scope: file:internal/a.go"} {
		if !strings.Contains(block, want) {
			t.Fatalf("snapshot missing %q:\n%s", want, block)
		}
	}
}

func TestCoordinationSnapshotMarksRunningWorkerStallButNotWaitingPrimary(t *testing.T) {
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
		State:            string(SubAgentStateWaitingPrimary),
		LastSummary:      "needs decision",
		LastUpdatedTurn:  a.explicitUserTurnCount,
	}
	a.taskRecords[waiting.TaskID] = waiting

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
		t.Fatalf("waiting_primary should not be marked stalled:\n%s", block)
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
