package agent

import (
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/recovery"
	"github.com/keakon/chord/internal/tools"
)

func TestEnsureActiveBackgroundJobSnapshotRendersActiveJobsOnly(t *testing.T) {
	snapshotAt := time.Unix(1_700_000_000, 0)
	jobs := []recovery.BackgroundObjectState{
		{ID: "job-1", AgentID: "main-1", Description: "run the replay", StartedAt: snapshotAt.Add(-2 * time.Minute), MaxRuntimeSec: 600, Status: "running"},
		{ID: "job-2", AgentID: "main-1", Command: "go test ./...", StartedAt: snapshotAt.Add(-time.Minute), MaxRuntimeSec: 600, Status: "stopping"},
		{ID: "job-3", Description: "already done", StartedAt: snapshotAt.Add(-time.Hour), Status: "completed", FinishedAt: snapshotAt.Add(-30 * time.Minute)},
	}
	summary := ensureActiveBackgroundJobSnapshot("## Current User Request\n- continue", jobs, snapshotAt)
	for _, want := range []string{
		activeJobsSnapshotOpenPrefix + "2023-11-14T22:13:20Z)",
		"- job-1 | agent=main-1 | status=running | elapsed=2m00s | deadline=8m00s left | desc=run the replay",
		"- job-2 | agent=main-1 | status=stopping | elapsed=1m00s | deadline=9m00s left | desc=go test ./...",
		activeJobsSnapshotCloseTag,
	} {
		if !strings.Contains(summary, want) {
			t.Fatalf("snapshot missing %q:\n%s", want, summary)
		}
	}
	if strings.Contains(summary, "job-3") {
		t.Fatalf("a terminal job must not appear in the live snapshot:\n%s", summary)
	}
	// The machine block must not become a section: the checkpoint parsers split
	// on ATX headings and would otherwise read it as model-authored prose.
	headings := 0
	for line := range strings.SplitSeq(summary, "\n") {
		if strings.HasPrefix(line, "## ") {
			headings++
		}
	}
	if headings != 1 {
		t.Fatalf("snapshot introduced %d extra headings:\n%s", headings-1, summary)
	}
}

// deadlines are rendered against the capture instant, including one that
// already elapsed while the job is still being torn down.
func TestActiveBackgroundJobSnapshotMarksOverdueDeadlines(t *testing.T) {
	snapshotAt := time.Unix(1_700_000_000, 0)
	block := renderActiveBackgroundJobsSnapshot([]recovery.BackgroundObjectState{
		{ID: "job-9", Status: "stopping", StartedAt: snapshotAt.Add(-20 * time.Minute), MaxRuntimeSec: 600},
	}, snapshotAt)
	if !strings.Contains(block, "deadline=overdue") {
		t.Fatalf("block = %q, want the overdue marker", block)
	}
	if !strings.Contains(block, "elapsed=20m00s") {
		t.Fatalf("block = %q, want the live elapsed span", block)
	}
}

func TestEnsureActiveBackgroundJobSnapshotOmitsBlockWithoutActiveJobs(t *testing.T) {
	summary := "## Current User Request\n- continue"
	terminal := []recovery.BackgroundObjectState{{ID: "job-9", Status: "completed", FinishedAt: time.Now()}}
	if got := ensureActiveBackgroundJobSnapshot(summary, terminal, time.Now()); got != summary {
		t.Fatalf("got %q, want the summary unchanged without active jobs", got)
	}
	if got := ensureActiveBackgroundJobSnapshot("", nil, time.Now()); got != "" {
		t.Fatalf("got %q, want no block for an empty summary", got)
	}
}

func TestEnsureActiveBackgroundJobSnapshotReplacesThePreviousBlock(t *testing.T) {
	at := time.Unix(1_700_000_000, 0)
	first := ensureActiveBackgroundJobSnapshot("## Progress\n- did the work", []recovery.BackgroundObjectState{{ID: "job-1", Status: "running", StartedAt: at}}, at)
	second := ensureActiveBackgroundJobSnapshot(first, []recovery.BackgroundObjectState{{ID: "job-2", Status: "running", StartedAt: at.Add(time.Minute)}}, at.Add(time.Minute))
	if strings.Count(second, activeJobsSnapshotOpenPrefix) != 1 {
		t.Fatalf("block count = %d, want exactly one:\n%s", strings.Count(second, activeJobsSnapshotOpenPrefix), second)
	}
	if strings.Contains(second, "job-1") {
		t.Fatalf("the stale block survived the replace:\n%s", second)
	}
	if !strings.Contains(second, "job-2") || !strings.Contains(second, "## Progress") {
		t.Fatalf("replace dropped unrelated content:\n%s", second)
	}
}

func TestCarryStripsTheJobSnapshotBlock(t *testing.T) {
	at := time.Unix(1_700_000_000, 0)
	block := renderActiveBackgroundJobsSnapshot([]recovery.BackgroundObjectState{{ID: "job-1", Status: "running", StartedAt: at}}, at)
	content := message.CompactionSummaryHeader + "\n\n## Current User Request\n- continue\n\n" + block +
		"\n\n## Next Step\n- continue" + message.CompactionCompressedTag
	got := latestPriorCheckpointStrippedBody([]message.Message{{Role: message.RoleUser, Content: content, IsCompactionSummary: true}})
	if strings.Contains(got, activeJobsSnapshotOpenPrefix) || strings.Contains(got, "job-1") {
		t.Fatalf("the runtime-owned block must not be carried into the next checkpoint:\n%s", got)
	}
	if !strings.Contains(got, "## Current User Request") || !strings.Contains(got, "## Next Step") {
		t.Fatalf("stripping removed unrelated sections:\n%s", got)
	}
}

func TestActiveBackgroundJobsSnapshotBoundsRows(t *testing.T) {
	at := time.Unix(1_700_000_000, 0)
	long := strings.Repeat("x", activeJobsSnapshotItemChars*3)
	jobs := make([]recovery.BackgroundObjectState, 0, activeJobsSnapshotMaxItems+3)
	for i := range activeJobsSnapshotMaxItems + 3 {
		jobs = append(jobs, recovery.BackgroundObjectState{
			ID:          fmt.Sprintf("job-%d", i+1),
			Description: long,
			Status:      "running",
			StartedAt:   at,
		})
	}
	block := renderActiveBackgroundJobsSnapshot(jobs, at)
	if !strings.Contains(block, "not shown") {
		t.Fatalf("block = %q, want an omission note once the rows are capped", block)
	}
	if strings.Contains(block, long) {
		t.Fatal("a row must truncate its description instead of embedding it whole")
	}
	if !strings.Contains(block, "...(truncated)") {
		t.Fatalf("block = %q, want the truncation marker", block)
	}
	rows := 0
	for line := range strings.SplitSeq(block, "\n") {
		if strings.HasPrefix(line, "- job-") {
			rows++
		}
		if strings.Contains(line, "\n") || utf8.RuneCountInString(line) > activeJobsSnapshotItemChars*2 {
			t.Fatalf("row is not a single bounded line: %q", line)
		}
	}
	if rows > activeJobsSnapshotMaxItems {
		t.Fatalf("rendered %d rows, want at most %d", rows, activeJobsSnapshotMaxItems)
	}
	if rows == 0 {
		t.Fatal("the snapshot rendered no rows")
	}
}

func TestSingleLineSnapshotTextCollapsesWhitespace(t *testing.T) {
	got := singleLineSnapshotText("first\nsecond\tthird", 100)
	if got != "first second third" {
		t.Fatalf("singleLineSnapshotText = %q, want whitespace collapsed", got)
	}
}

// The model-driven checkpoint renders the snapshot from the frozen barrier
// states, so a job the model never mentioned still reaches the durable body —
// while a terminal job stays out of the live list.
func TestModelDrivenCheckpointCarriesTheActiveJobSnapshot(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.ctxMgr.Append(message.Message{Role: message.RoleUser, Content: "keep working"})
	a.ctxMgr.Append(message.Message{Role: message.RoleAssistant, Content: "working"})
	snapshot := a.ctxMgr.Snapshot()
	now := time.Now()
	bundle := modelDrivenBarrierSnapshot{
		snapshot: snapshot,
		backgroundObjects: []recovery.BackgroundObjectState{
			{ID: "job-live", AgentID: "main-1", Description: "run the replay", Status: "running", StartedAt: now.Add(-2 * time.Minute), MaxRuntimeSec: 600},
			{ID: "job-done", Description: "already finished", Status: "completed", StartedAt: now.Add(-time.Hour), FinishedAt: now.Add(-time.Minute)},
		},
	}
	req := e2eCheckpointRequest("keep working", nil, nil, nil, "stage-1", "active", "provisional")
	summary := a.buildModelDrivenCheckpointSummary(bundle, snapshot, len(snapshot), req)
	if !strings.Contains(summary, "job-live") || !strings.Contains(summary, "deadline=") {
		t.Fatalf("checkpoint missing the live job snapshot:\n%s", summary)
	}
	if strings.Contains(summary, "job-done") {
		t.Fatalf("checkpoint must not carry a terminal job:\n%s", summary)
	}
	// The builder renders once, and both the preflight savings check and the
	// applied body read that same string.
	if got := strings.Count(summary, activeJobsSnapshotOpenPrefix); got != 1 {
		t.Fatalf("block count = %d, want exactly one:\n%s", got, summary)
	}
}

// The usage-driven draft path applies the same snapshot, so a summarizer that
// never mentions the running job cannot drop it from the checkpoint.
func TestProduceCompactionDraftCarriesTheActiveJobSnapshot(t *testing.T) {
	projectRoot := t.TempDir()
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

	restoreJobs := tools.ResetJobRegistryForTest()
	t.Cleanup(restoreJobs)
	t.Cleanup(func() { tools.StopAllJobsForShutdown() })
	if _, err := tools.ExecuteJobForTest(tools.WithAgentID(t.Context(), a.instanceID), "sleep 60", "live build", new(600)); err != nil {
		t.Fatalf("ExecuteJobForTest: %v", err)
	}

	snapshot := []message.Message{
		{Role: "user", Content: "u1"},
		{Role: "assistant", Content: "a1"},
		{Role: "user", Content: "u2"},
		{Role: "assistant", Content: "a2"},
		{Role: "user", Content: "u3"},
		{Role: "assistant", Content: "a3"},
	}
	draft, err := a.produceCompactionDraftAsync(t.Context(), snapshot, false, 1, compactionTarget{sessionEpoch: a.sessionEpoch}, len(snapshot), compactionProfileArchival, "", nil, a.captureCompactionArchiveMeta())
	if err != nil {
		t.Fatalf("produceCompactionDraftAsync: %v", err)
	}
	if draft.Skip || len(draft.NewMessages) == 0 {
		t.Fatalf("draft = %+v, want one summary message", draft)
	}
	body := draft.NewMessages[0].Content
	if !strings.Contains(body, "live build") || !strings.Contains(body, activeJobsSnapshotOpenPrefix) {
		t.Fatalf("checkpoint missing the live job snapshot:\n%s", body)
	}
}
