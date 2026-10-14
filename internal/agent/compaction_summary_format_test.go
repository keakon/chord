package agent

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/agent/checkpointfmt"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

// TestModelDrivenSummaryOrderIsDeliberate pins the segment-order contract the
// model-driven builder intentionally breaks away from: its continuation-first
// layout (Next Step ahead of Progress, Constraints ahead of Active Objective)
// serves the resuming model, while validateCompactionSummary enforces the
// canonical order only on model-authored summaries. If the builder ever
// returns to canonical order — or the validator ever exempts model-driven
// bodies — this test names the place both sides must change together.
func TestModelDrivenSummaryOrderIsDeliberate(t *testing.T) {
	t.Parallel()
	agent := newTestMainAgent(t, t.TempDir())
	request := &modelDrivenCheckpointRequest{Args: tools.CompactContextArgs{
		ActiveObjective: "verify parser", NextStep: "run parser tests",
	}}
	bundle := modelDrivenBarrierSnapshot{snapshot: []message.Message{{Role: message.RoleUser, Content: "verify parser"}}}
	summary := agent.buildModelDrivenCheckpointSummary(bundle, bundle.snapshot, len(bundle.snapshot), request)
	if err := checkpointfmt.ValidateSummary(summary); err == nil {
		t.Fatal("model-driven summaries use continuation-first order and must stay outside the canonical-order validator")
	}
	nextStep := strings.Index(summary, "## Next Step")
	progress := strings.Index(summary, "## Progress")
	constraints := strings.Index(summary, "## User Constraints")
	objective := strings.Index(summary, "## Active Objective")
	if nextStep < 0 || progress < 0 || constraints < 0 || objective < 0 {
		t.Fatalf("model-driven summary lost a continuation section:\n%s", summary)
	}
	if !(constraints < objective && nextStep < progress) {
		t.Fatal("model-driven summary must keep Constraints before Active Objective and Next Step before Progress")
	}
}

func TestCompactionSummaryFromResponseContentStripsLeadingThinkBlock(t *testing.T) {
	summary := validCompactionSummaryForTest("history-1.md")
	got := compactionSummaryFromResponseContent("\n<think>provider failed to split reasoning</think>\n\n" + summary)
	if got != summary {
		t.Fatalf("compactionSummaryFromResponseContent() = %q, want %q", got, summary)
	}
}

func TestCompactionSummaryFromResponseContentStripsLeadingOrphanThinkClose(t *testing.T) {
	summary := validCompactionSummaryForTest("history-1.md")
	got := compactionSummaryFromResponseContent("</think>\n\n" + summary)
	if got != summary {
		t.Fatalf("compactionSummaryFromResponseContent() = %q, want %q", got, summary)
	}
	if err := checkpointfmt.ValidateSummary(got); err != nil {
		t.Fatalf("stripped summary should validate, got error: %v", err)
	}
}

func TestCompactionSummaryFromResponseContentKeepsLeadingUnclosedThink(t *testing.T) {
	summary := validCompactionSummaryForTest("history-1.md")
	// A leading <think> without a close is ambiguous: everything after it could
	// be reasoning, so it is left intact for validation to reject and repair.
	got := compactionSummaryFromResponseContent("<think>reasoning\n\n" + summary)
	if !strings.Contains(got, "<think>") {
		t.Fatalf("leading unclosed think tag should be preserved for validation, got %q", got)
	}
	if err := checkpointfmt.ValidateSummary(got); err == nil {
		t.Fatal("expected leading unclosed think tag to be rejected by validation")
	}
}

func TestCompactionSummaryFromResponseContentLeavesInlineThinkForValidation(t *testing.T) {
	summary := strings.Replace(validCompactionSummaryForTest("history-1.md"), "- continue current task", "- continue <think>bad</think> current task", 1)
	got := compactionSummaryFromResponseContent(summary)
	if got != summary {
		t.Fatalf("inline thinking tag should remain for validation, got %q", got)
	}
	if err := checkpointfmt.ValidateSummary(got); err == nil {
		t.Fatal("expected inline thinking tag to be rejected by validation")
	}
}

func TestBuildCompactionRepairPrompt(t *testing.T) {
	original := "Archived history file: history-1.md\n\nTranscript:\n- user: fix compaction"
	prompt := buildCompactionRepairPrompt(original, fmt.Errorf("compaction summary next step is too vague"))
	if strings.Contains(prompt, "compaction summary next step is too vague") {
		t.Fatalf("repair prompt should not expose validation error:\n%s", prompt)
	}
	if strings.Contains(prompt, "previous") || strings.Contains(prompt, "invalid") || strings.Contains(prompt, "retry") {
		t.Fatalf("repair prompt should not expose retry/failure mechanics:\n%s", prompt)
	}
	if !strings.Contains(prompt, original) {
		t.Fatalf("repair prompt missing original input:\n%s", prompt)
	}
	if strings.Contains(prompt, "bad summary") {
		t.Fatalf("repair prompt should not include previous summary text:\n%s", prompt)
	}
	if !strings.Contains(prompt, "Do not restart work listed as completed") {
		t.Fatalf("repair prompt missing goal-preservation requirement:\n%s", prompt)
	}
}

func TestBuildCompactionRepairPromptListsRequiredHeadings(t *testing.T) {
	original := "Archived history file: history-1.md\n\nTranscript:\n- user: fix compaction"
	prompt := buildCompactionRepairPrompt(original, fmt.Errorf("compaction summary next step is too vague"))
	for _, heading := range checkpointfmt.RequiredHeadings() {
		if !strings.Contains(prompt, "- "+heading) {
			t.Fatalf("repair prompt missing required heading %q:\n%s", heading, prompt)
		}
	}
	if strings.Contains(prompt, "The missing sections are") {
		t.Fatalf("repair prompt should not name missing sections when validation failed for another reason:\n%s", prompt)
	}
}

func TestBuildCompactionRepairPromptNamesMissingSections(t *testing.T) {
	original := "Archived history file: history-1.md\n\nTranscript:\n- user: fix compaction"
	broken := strings.Replace(validCompactionSummaryForTest("history-1.md"), "## Progress\n- progress recorded\n\n", "", 1)
	if strings.Contains(broken, "## Progress") {
		t.Fatalf("test fixture still contains the removed section:\n%s", broken)
	}
	validationErr := checkpointfmt.ValidateSummary(broken)
	if validationErr == nil {
		t.Fatal("expected a summary without the Progress section to be rejected")
	}
	wrapped := fmt.Errorf("%w: %w", errInvalidCompactionSummary, validationErr)
	if !errors.Is(wrapped, errInvalidCompactionSummary) {
		t.Fatal("missing-section rejection should stay wrapped as an invalid compaction summary")
	}
	missing := compactionMissingHeadings(wrapped)
	if len(missing) != 1 || missing[0] != "## Progress" {
		t.Fatalf("missing headings = %v, want [## Progress]", missing)
	}
	prompt := buildCompactionRepairPrompt(original, wrapped)
	if !strings.Contains(prompt, "The missing sections are: ## Progress.") {
		t.Fatalf("repair prompt did not name the missing section:\n%s", prompt)
	}
}

func TestBuildStructuredFallbackSummaryIncludesSections(t *testing.T) {
	summary := buildStructuredFallbackSummary(
		"history-1.md",
		&compactionInput{RecentTailAnchor: "- user: continue", EvidenceItems: []evidenceItem{{Kind: evidenceUserCorrection, Excerpt: "do not hardcode"}}},
		fmt.Errorf("summary too short"),
		nil,
		nil,
		nil,
	)
	for _, heading := range checkpointfmt.RequiredHeadings() {
		if !strings.Contains(summary, heading) {
			t.Fatalf("fallback summary missing heading %q:\n%s", heading, summary)
		}
	}
	if !strings.Contains(summary, "history-1.md") {
		t.Fatalf("fallback summary missing archive path:\n%s", summary)
	}
}

func TestBuildStructuredFallbackSummaryWithoutAnchorDoesNotInventActiveWork(t *testing.T) {
	summary := buildStructuredFallbackSummary(
		"history-1.md",
		&compactionInput{RecentTailAnchor: "- (none)", GoalAnchor: "- Latest user request not confidently recoverable."},
		fmt.Errorf("model unavailable"),
		nil,
		[]tools.TodoItem{
			{ID: "1", Status: "completed", Content: "ship completed docs update"},
			{ID: "2", Status: "in_progress", Content: "debug older implementation issue"},
		},
		nil,
	)
	for _, forbidden := range []string{
		"Active/relevant to latest request: not relevance-filtered",
		"Continue from the archived history and preserved evidence",
		"ship completed docs update\n## SubAgent State",
	} {
		if strings.Contains(summary, forbidden) {
			t.Fatalf("fallback summary contains target-drift pattern %q:\n%s", forbidden, summary)
		}
	}
	if !strings.Contains(summary, "No active objective can be recovered safely") {
		t.Fatalf("fallback summary should make unrecoverable objective explicit:\n%s", summary)
	}
	if !strings.Contains(summary, "Before modifying files or continuing old work") {
		t.Fatalf("fallback next step should require recovering latest request first:\n%s", summary)
	}
	if !strings.Contains(summary, "do not act on completed/background or stale/superseded todos") {
		t.Fatalf("fallback next step should guard against stale todo restart:\n%s", summary)
	}
}

func TestEnsureCompactionSummaryKeyFilesAppendsMissingPaths(t *testing.T) {
	summary := "## Goal\n- continue\n\n## User Constraints\n- none\n\n## Progress\n- progress\n\n## Key Decisions\n- decisions\n\n## Files and Evidence\n- Archived history: history-1.md\n\n## Todo State\n- none\n\n## SubAgent State\n- none\n\n## Open Problems\n- none\n\n## Next Step\n- continue"
	got := ensureCompactionSummaryKeyFiles(summary, []string{"internal/agent/compaction.go", "docs/architecture/context-management.md"})
	if !strings.Contains(got, "- internal/agent/compaction.go") {
		t.Fatalf("summary missing injected key file:\n%s", got)
	}
	if !strings.Contains(got, "- docs/architecture/context-management.md") {
		t.Fatalf("summary missing injected docs file:\n%s", got)
	}
}

func TestEnsureCompactionSummaryKeyFilesIgnoresProseMentionWhenCheckingExistingBullets(t *testing.T) {
	summary := "## Goal\n- continue\n\n## User Constraints\n- none\n\n## Progress\n- progress\n\n## Key Decisions\n- decisions\n\n## Files and Evidence\n- Archived history: history-1.md\n- See internal/agent/compaction.go for the latest draft context.\n\n## Todo State\n- none\n\n## SubAgent State\n- none\n\n## Open Problems\n- none\n\n## Next Step\n- continue"
	got := ensureCompactionSummaryKeyFiles(summary, []string{"internal/agent/compaction.go"})
	if !strings.Contains(got, "\n- internal/agent/compaction.go\n") {
		t.Fatalf("summary should still inject standalone bullet for key file:\n%s", got)
	}
}

func TestExtractCompactionKeyFilesIgnoresInvalidLines(t *testing.T) {
	projectRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(projectRoot, "internal", "agent"), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(projectRoot, "docs", "architecture"), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(projectRoot, "internal", "agent", "compaction.go"), []byte("package agent\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := os.WriteFile(filepath.Join(projectRoot, "docs", "architecture", "context-management.md"), []byte("# doc\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	content := buildCompactionCheckpointMessage(
		"## Goal\n- continue\n\n## User Constraints\n- none\n\n## Progress\n- progress\n\n## Key Decisions\n- decisions\n\n## Files and Evidence\n- Archived history: history-1.md\n- internal/agent/compaction.go\n- `docs/architecture/context-management.md`\n- the key file is internal/tui/app.go because ...\n\n## Todo State\n- none\n\n## SubAgent State\n- none\n\n## Open Problems\n- none\n\n## Next Step\n- continue",
		[]string{".chord/sessions/test/history-1.md"},
		"model_summary",
		nil,
	)
	got := extractCompactionKeyFiles(content, projectRoot)
	if len(got) != 2 {
		t.Fatalf("len(got) = %d, want 2 (%v)", len(got), got)
	}
	if got[0] != "internal/agent/compaction.go" || got[1] != "docs/architecture/context-management.md" {
		t.Fatalf("got = %#v", got)
	}
}

func TestBuildCompactionCheckpointMessageListsAllHistoryRefs(t *testing.T) {
	content := buildCompactionCheckpointMessage(
		"## Current User Request\n- continue\n\n## Active Objective\n- continue\n\n## Background Goals\n- none\n\n## User Constraints\n- none\n\n## Progress\n- progress\n\n## Key Decisions\n- decisions\n\n## Files and Evidence\n- Archived history for this compaction: history-3.md\n\n## Todo State\n- Active/relevant to latest request: (none)\n- Completed/background: (none)\n- Stale/superseded: (none)\n\n## SubAgent State\n- none\n\n## Open Problems\n- none\n\n## Next Step\n- continue",
		[]string{".chord/sessions/test/history-1.md", ".chord/sessions/test/history-2.md", ".chord/sessions/test/history-3.md"},
		"model_summary",
		nil,
	)
	for _, want := range []string{"Archived history files (read the matching file with the read tool", "history-1.md", "history-2.md", "history-3.md"} {
		if !strings.Contains(content, want) {
			t.Fatalf("checkpoint missing %q:\n%s", want, content)
		}
	}
}

func TestExtractCompactionKeyFilesFromTruncateOnlySummary(t *testing.T) {
	projectRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(projectRoot, "internal", "agent"), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(projectRoot, "internal", "agent", "compaction.go"), []byte("package agent\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	summary := buildTruncateOnlySummary(
		"history-1.md",
		fmt.Errorf("utility model unavailable"),
		[]string{"internal/agent/compaction.go"},
		nil,
		nil,
	)
	content := buildCompactionCheckpointMessage(summary, []string{".chord/sessions/test/history-1.md"}, "truncate_only", nil)
	got := extractCompactionKeyFiles(content, projectRoot)
	if len(got) != 1 || got[0] != "internal/agent/compaction.go" {
		t.Fatalf("got = %#v, want internal/agent/compaction.go", got)
	}
}

// A checkpoint must not grow without bound. The runtime TODO snapshot and the
// archived history map are the only parts that would otherwise gain content
// every compaction, and that growth feeds back into the model-driven low-gain
// gate, which compares projected against current size.
func TestCheckpointGrowingSectionsAreBounded(t *testing.T) {
	todos := make([]tools.TodoItem, 0, 40)
	for i := range 40 {
		status := "completed"
		if i >= 38 {
			status = "pending"
		}
		todos = append(todos, tools.TodoItem{
			ID:      fmt.Sprintf("t%d", i),
			Status:  status,
			Content: strings.Repeat("todo detail ", 40),
		})
	}
	kept, omitted := boundedTodoSnapshotItems(todos)
	if len(kept) != compactTodoSnapshotMaxItems || omitted != len(todos)-compactTodoSnapshotMaxItems {
		t.Fatalf("kept=%d omitted=%d, want %d kept", len(kept), omitted, compactTodoSnapshotMaxItems)
	}
	for _, want := range []string{"t38", "t39"} {
		if !slices.ContainsFunc(kept, func(item tools.TodoItem) bool { return item.ID == want }) {
			t.Fatalf("unfinished todo %s must survive the cap, kept=%v", want, kept)
		}
	}
	if !slices.IsSortedFunc(kept, func(a, b tools.TodoItem) int {
		return mustTodoIndex(t, a.ID) - mustTodoIndex(t, b.ID)
	}) {
		t.Fatalf("kept todos must stay in their original order, got %v", kept)
	}
	summary := "## Todo State\n- see snapshot\n\n## Next Step\n- continue\n"
	rendered := ensureCompactionTodoSnapshot(summary, todos)
	if !strings.Contains(rendered, "earlier completed/cancelled todos omitted") {
		t.Fatalf("snapshot must disclose the omission, got %q", rendered)
	}
	if strings.Contains(rendered, strings.Repeat("todo detail ", 40)) {
		t.Fatal("snapshot must truncate long todo content")
	}

	refs := make([]string, 0, 30)
	for i := 1; i <= 30; i++ {
		refs = append(refs, fmt.Sprintf("/tmp/session/history-%d.md", i))
	}
	lines := formatHistoryMapLines(refs, nil)
	if len(lines) != compactHistoryMapMaxEntries+1 {
		t.Fatalf("history map lines = %d, want %d entries plus one omission note", len(lines), compactHistoryMapMaxEntries)
	}
	if !strings.Contains(lines[0], "earlier archives omitted") {
		t.Fatalf("history map must disclose the omission, got %q", lines[0])
	}
	if !strings.Contains(lines[len(lines)-1], "history-30.md") {
		t.Fatalf("history map must keep the newest archive, got %q", lines[len(lines)-1])
	}
}
