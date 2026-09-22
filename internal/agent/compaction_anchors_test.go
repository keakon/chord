package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

func anchorEvidence(excerpts ...string) []evidenceItem {
	items := make([]evidenceItem, 0, len(excerpts))
	for _, excerpt := range excerpts {
		items = append(items, buildEvidenceItem(evidenceUserCorrection, "User correction / constraint", "why", "src", excerpt))
	}
	return items
}

func checkpointWithAnchors(t *testing.T, anchors compactionAnchors) message.Message {
	t.Helper()
	content := buildCompactionCheckpointMessage(
		withCompactionAnchors("## Current User Request\n- do the thing", anchors),
		[]string{"history-1.md"},
		"model_summary",
		nil,
	)
	return message.Message{Role: message.RoleUser, IsCompactionSummary: true, Content: content}
}

func TestCompactionAnchorsRoundTripThroughCheckpoint(t *testing.T) {
	anchors := buildCompactionAnchors(compactionAnchors{}, "refactor the parser into two passes", anchorEvidence("不要改动公开 API", "must not add new dependencies"))
	msg := checkpointWithAnchors(t, anchors)

	got := latestCompactionAnchors([]message.Message{{Role: message.RoleUser, Content: "unrelated"}, msg})
	if got.OriginalRequest != anchors.OriginalRequest {
		t.Fatalf("OriginalRequest = %q, want %q", got.OriginalRequest, anchors.OriginalRequest)
	}
	if len(got.Constraints) != len(anchors.Constraints) {
		t.Fatalf("Constraints = %v, want %v", got.Constraints, anchors.Constraints)
	}
	for i := range got.Constraints {
		if got.Constraints[i] != anchors.Constraints[i] {
			t.Fatalf("Constraints[%d] = %q, want %q", i, got.Constraints[i], anchors.Constraints[i])
		}
	}
}

func TestCompactionAnchorsKeepOriginalRequestAcrossCompactions(t *testing.T) {
	first := buildCompactionAnchors(compactionAnchors{}, "original: build the import pipeline", nil)
	if first.OriginalRequest == "" {
		t.Fatal("first compaction did not record the original request")
	}

	// A later compaction sees a history that already starts with a checkpoint,
	// so the "first user message" it can observe is the checkpoint itself. The
	// carried-forward value must win.
	second := buildCompactionAnchors(first, "[Context Summary] ...", nil)
	if second.OriginalRequest != first.OriginalRequest {
		t.Fatalf("OriginalRequest = %q, want it preserved as %q", second.OriginalRequest, first.OriginalRequest)
	}

	third := buildCompactionAnchors(second, "", nil)
	if third.OriginalRequest != first.OriginalRequest {
		t.Fatalf("OriginalRequest lost after a third compaction: %q", third.OriginalRequest)
	}
}

func TestCompactionAnchorsAppendNewConstraintsAndDeduplicate(t *testing.T) {
	first := buildCompactionAnchors(compactionAnchors{}, "req", anchorEvidence("不要改动公开 API"))
	second := buildCompactionAnchors(first, "req", anchorEvidence("不要改动公开   API", "只能用标准库"))
	if len(second.Constraints) != 2 {
		t.Fatalf("Constraints = %v, want the duplicate collapsed and the new one appended", second.Constraints)
	}
	if second.Constraints[0] != first.Constraints[0] {
		t.Fatalf("existing constraint rewritten: %q -> %q", first.Constraints[0], second.Constraints[0])
	}
	if !strings.Contains(second.Constraints[1], "只能用标准库") {
		t.Fatalf("new constraint missing: %v", second.Constraints)
	}
}

func TestCompactionAnchorsConstraintsStayBounded(t *testing.T) {
	anchors := compactionAnchors{}
	total := compactAnchorsMaxConstraints + 10
	for i := range total {
		anchors = buildCompactionAnchors(anchors, "req", anchorEvidence(fmt.Sprintf("必须保留约束 %d", i)))
	}
	if len(anchors.Constraints) != compactAnchorsMaxConstraints {
		t.Fatalf("len(Constraints) = %d, want %d", len(anchors.Constraints), compactAnchorsMaxConstraints)
	}
	// Overflow drops the middle: the earliest ground rules and the newest
	// task-local constraints both survive.
	if !strings.Contains(anchors.Constraints[0], "约束 0") {
		t.Fatalf("oldest constraint dropped: %q", anchors.Constraints[0])
	}
	newest := anchors.Constraints[len(anchors.Constraints)-1]
	if !strings.Contains(newest, fmt.Sprintf("约束 %d", total-1)) {
		t.Fatalf("newest constraint dropped: %q", newest)
	}
	if !strings.Contains(anchors.OmittedNote, "1 earlier constraint(s) omitted") {
		t.Fatalf("overflow note missing or wrong: %q", anchors.OmittedNote)
	}
	rendered := renderCompactionAnchors(anchors)
	if !strings.Contains(rendered, anchors.OmittedNote) {
		t.Fatalf("rendered anchors omit the overflow note: %q", rendered)
	}
	for line := range strings.SplitSeq(rendered, "\n") {
		if strings.Contains(line, "earlier constraint(s) omitted") && strings.HasPrefix(line, "- ") {
			t.Fatalf("overflow note must not parse as a constraint bullet: %q", line)
		}
	}
	// The note round-trips through the checkpoint and is carried into the next
	// compaction when nothing new overflows.
	parsed := latestCompactionAnchors([]message.Message{checkpointWithAnchors(t, anchors)})
	if parsed.OmittedNote != anchors.OmittedNote {
		t.Fatalf("OmittedNote = %q after round trip, want %q", parsed.OmittedNote, anchors.OmittedNote)
	}
	carried := buildCompactionAnchors(parsed, "req", nil)
	if carried.OmittedNote != parsed.OmittedNote {
		t.Fatalf("OmittedNote not carried forward: %q -> %q", parsed.OmittedNote, carried.OmittedNote)
	}
}

func TestCompactionAnchorsEmptyRendersNothing(t *testing.T) {
	if got := renderCompactionAnchors(compactionAnchors{}); got != "" {
		t.Fatalf("renderCompactionAnchors(empty) = %q, want empty", got)
	}
	summary := "## Current User Request\n- do the thing"
	if got := withCompactionAnchors(summary, compactionAnchors{}); got != summary {
		t.Fatalf("withCompactionAnchors(empty) = %q, want the summary unchanged", got)
	}
	if got := latestCompactionAnchors(nil); !got.empty() {
		t.Fatalf("latestCompactionAnchors(nil) = %+v, want empty", got)
	}
}

func TestCompactionAnchorsDoNotForgeMarkdownSections(t *testing.T) {
	anchors := buildCompactionAnchors(compactionAnchors{}, "## Files and Evidence", anchorEvidence("### 不要 use tabs"))
	rendered := renderCompactionAnchors(anchors)
	for line := range strings.SplitSeq(rendered, "\n") {
		if strings.HasPrefix(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "- ")), "#") {
			t.Fatalf("anchor line can forge a Markdown heading: %q", line)
		}
	}
}

func TestCheckpointWithAnchorsStillExtractsKeyFiles(t *testing.T) {
	projectRoot := t.TempDir()
	keyFile := filepath.Join(projectRoot, "parser.go")
	if err := os.WriteFile(keyFile, []byte("package main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	anchors := buildCompactionAnchors(compactionAnchors{}, "refactor the parser", anchorEvidence("不要改动公开 API"))
	summary := "## Current User Request\n- keep going\n\n## Files and Evidence\n- parser.go\n\n## Next Step\n- run the tests"
	content := buildCompactionCheckpointMessage(withCompactionAnchors(summary, anchors), []string{"history-1.md"}, "model_summary", nil)

	if got := extractCompactionKeyFiles(content, projectRoot); len(got) != 1 || got[0] != "parser.go" {
		t.Fatalf("extractCompactionKeyFiles = %v, want [parser.go]", got)
	}
	if !strings.Contains(content, "refactor the parser") {
		t.Fatalf("checkpoint lost the original request anchor: %q", content)
	}
}

func TestCompactionPromptCarriesSessionAnchors(t *testing.T) {
	anchors := buildCompactionAnchors(compactionAnchors{}, "build the import pipeline", anchorEvidence("只能用标准库"))
	input := &compactionInput{Transcript: "transcript", SessionAnchors: anchors}
	prompt := buildCompactionPromptWithKeyFiles(input, "history-1.md", nil, nil, nil, nil)

	if !strings.Contains(prompt, "Durable session anchors (carried forward verbatim in the checkpoint):") {
		t.Fatalf("prompt missing the session-anchor section:\n%s", prompt)
	}
	if !strings.Contains(prompt, "build the import pipeline") || !strings.Contains(prompt, "只能用标准库") {
		t.Fatalf("prompt missing anchor content:\n%s", prompt)
	}

	empty := buildCompactionPromptWithKeyFiles(&compactionInput{Transcript: "t"}, "history-1.md", nil, nil, nil, nil)
	if !strings.Contains(empty, "(none yet; this is the first compaction of the session)") {
		t.Fatalf("prompt missing the empty-anchor placeholder:\n%s", empty)
	}
}

// TestCompactionReductionScratchCarriesReductionSemantics requires that the
// compaction input is reduced through a scratch agent that must follow the same
// reduction semantics as the main request — tool registry (read-only shell
// verdicts), project root (read path resolution), and immutable snapshots of
// the recall-protection sets — without leaking its own bookkeeping back into
// the live agent.
func TestCompactionReductionScratchCarriesReductionSemantics(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	projectRoot := t.TempDir()
	a.contentRoot = projectRoot
	a.recalledReductionInputs = map[string]struct{}{"read|/work/a.go": {}}
	a.lastPreparedLLMDiscardedInputs = map[string]string{"shell|go test": "summarized"}
	scratch := a.compactionReductionScratch()
	if scratch.contentRoot != projectRoot {
		t.Fatalf("scratch projectRoot = %q, want %q", scratch.contentRoot, projectRoot)
	}
	if scratch.tools != a.tools {
		t.Fatal("scratch must carry the tool registry for read-only shell classification")
	}
	if _, ok := scratch.recalledReductionInputs["read|/work/a.go"]; !ok {
		t.Fatalf("scratch missing recalled inputs: %v", scratch.recalledReductionInputs)
	}
	if _, ok := scratch.lastPreparedLLMDiscardedInputs["shell|go test"]; !ok {
		t.Fatalf("scratch missing discarded inputs: %v", scratch.lastPreparedLLMDiscardedInputs)
	}
	// Mutations on the scratch (e.g. a recalled input registered during its
	// pass) must not leak into the live agent's set.
	scratch.noteRecalledReductionInput("read|/work/b.go")
	if _, ok := a.recalledReductionInputs["read|/work/b.go"]; ok {
		t.Fatal("scratch recall bookkeeping leaked into the live agent")
	}
}

// TestEvidenceRebuildsFromCompactedMessagesTail requires that, after
// compaction the runtime evidence candidates are rebuilt from the preserved
// tail (checkpoint + tail), not just the checkpoint. The summary is exempt, but
// the latest user request / correction / tool error in the tail must re-enter
// the candidates so the next compaction cannot erode them away one round at a
// time.
func TestEvidenceRebuildsFromCompactedMessagesTail(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	checkpoint := message.Message{Role: message.RoleUser, IsCompactionSummary: true, Content: "[Context Summary]\n## Current User Request\n- old archived request"}
	tail := []message.Message{
		{Role: message.RoleUser, Content: "don't change the public API"},
		{Role: message.RoleTool, Content: "Done rejected: keep the existing behavior"},
		{Role: message.RoleTool, Content: "command failed\n\nError: exit code 1"},
	}
	compacted := append([]message.Message{checkpoint}, tail...)
	a.resetRuntimeEvidenceFromMessages(compacted)
	items := a.evidence.snapshot()
	var haveCorrection, haveRejection, haveError bool
	for _, item := range items {
		switch item.Kind {
		case evidenceUserCorrection:
			haveCorrection = true
		case evidenceDoneRejected:
			haveRejection = true
		case evidenceToolError:
			haveError = true
		}
	}
	if !haveCorrection || !haveRejection || !haveError {
		t.Fatalf("evidence candidates missing tail signals: correction=%v rejection=%v error=%v items=%+v", haveCorrection, haveRejection, haveError, items)
	}
	for _, item := range items {
		if strings.Contains(item.Excerpt, "Context Summary") {
			t.Fatalf("checkpoint must not become an evidence candidate: %+v", item)
		}
	}
}

// TestEvidenceRebuildsAfterAppliedCompaction covers the same requirement through
// a real apply: applyCompactionDraft must rebuild the candidates from the whole
// compacted list, so a draft wired to rebuild from the checkpoint alone (or from
// the pre-compaction snapshot) is caught here rather than one round later.
func TestEvidenceRebuildsAfterAppliedCompaction(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.ctxMgr.Append(message.Message{Role: message.RoleUser, Content: "archived opening request"})
	a.ctxMgr.Append(message.Message{Role: message.RoleUser, Content: "continue fixing the build errors"})
	a.ctxMgr.Append(message.Message{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{{ID: "shell1", Name: tools.NameShell, Args: json.RawMessage(`{"command":"go build"}`)}}})
	a.ctxMgr.Append(message.Message{Role: message.RoleTool, ToolCallID: "shell1", Content: "Error: undefined foo", ToolStatus: string(ToolResultStatusError)})

	checkpoint := buildCompactionCheckpointMessage("## Current User Request\n- continue", []string{"history-1.md: build errors"}, "truncate_only", nil)
	draft := &compactionDraft{
		NewMessages:    []message.Message{{Role: message.RoleUser, Content: checkpoint, IsCompactionSummary: true}},
		HeadSplit:      1,
		Index:          1,
		AbsHistoryPath: filepath.Join(a.sessionDir, "history-1.md"),
		SummaryMode:    "truncate_only",
		PlanID:         1,
	}
	if err := a.applyCompactionDraft(draft); err != nil {
		t.Fatalf("applyCompactionDraft: %v", err)
	}

	var haveRequest, haveError bool
	for _, item := range a.evidence.snapshot() {
		switch item.Kind {
		case evidenceUserRequest:
			haveRequest = true
		case evidenceToolError:
			haveError = true
		}
	}
	if !haveRequest || !haveError {
		t.Fatalf("preserved tail signals missing after apply: request=%v error=%v", haveRequest, haveError)
	}
}

// TestCompactionDraftEmbedsAndInheritsSessionAnchors covers the wiring end to
// end: the draft's checkpoint must carry the anchors, and a second compaction
// over that checkpoint must inherit them instead of re-deriving them from a
// history that now begins with a summary.
func TestCompactionDraftEmbedsAndInheritsSessionAnchors(t *testing.T) {
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
	provider := &countingCompactionProvider{
		response: &message.Response{Content: validCompactionSummaryForTest("history-1.md")},
	}
	client := llm.NewClient(providerCfg, provider, "compact-model", 2048, "")
	a.SetModelSwitchFactory(func(string, []string, string) (*llm.Client, string, int, error) {
		return client, "compact-model", 16384, nil
	})

	snapshot := []message.Message{
		{Role: message.RoleUser, Content: "port the config loader to the new schema"},
		{Role: message.RoleAssistant, Content: "a1"},
		{Role: message.RoleUser, Content: "不要改动公开 API"},
		{Role: message.RoleAssistant, Content: "a2"},
	}
	a.resetRuntimeEvidenceFromMessages(snapshot)

	evidenceItems := a.evidenceItemsForCompaction(a.ctxMgr.GetMaxTokens())
	draft, err := a.produceCompactionDraftAsync(t.Context(), snapshot, false, 1,
		compactionTarget{sessionEpoch: a.sessionEpoch}, len(snapshot), compactionProfileArchival,
		"port the config loader to the new schema", evidenceItems, a.captureCompactionArchiveMeta())
	if err != nil {
		t.Fatalf("produceCompactionDraftAsync: %v", err)
	}
	if draft.Skip || len(draft.NewMessages) == 0 {
		t.Fatalf("unexpected draft: %+v", draft)
	}
	checkpoint := draft.NewMessages[0]
	if !strings.Contains(checkpoint.Content, message.CompactionAnchorsOpenTag) {
		t.Fatalf("checkpoint carries no anchors block:\n%s", checkpoint.Content)
	}
	anchors := latestCompactionAnchors(draft.NewMessages)
	if !strings.Contains(anchors.OriginalRequest, "port the config loader") {
		t.Fatalf("OriginalRequest = %q", anchors.OriginalRequest)
	}
	if len(anchors.Constraints) == 0 || !strings.Contains(anchors.Constraints[0], "不要改动公开 API") {
		t.Fatalf("Constraints = %v, want the recorded correction", anchors.Constraints)
	}

	// Second round: the history now starts with the checkpoint, so a naive
	// re-derivation would replace the original request with summary text.
	next := append([]message.Message{checkpoint}, []message.Message{
		{Role: message.RoleAssistant, Content: "continuing"},
		{Role: message.RoleUser, Content: "now add the migration command"},
		{Role: message.RoleAssistant, Content: "done"},
	}...)
	secondEvidence := a.evidenceItemsForCompaction(a.ctxMgr.GetMaxTokens())
	secondDraft, err := a.produceCompactionDraftAsync(t.Context(), next, false, 2,
		compactionTarget{sessionEpoch: a.sessionEpoch}, len(next), compactionProfileArchival,
		checkpoint.Content, secondEvidence, a.captureCompactionArchiveMeta())
	if err != nil {
		t.Fatalf("second produceCompactionDraftAsync: %v", err)
	}
	secondAnchors := latestCompactionAnchors(secondDraft.NewMessages)
	if secondAnchors.OriginalRequest != anchors.OriginalRequest {
		t.Fatalf("OriginalRequest = %q, want it inherited as %q", secondAnchors.OriginalRequest, anchors.OriginalRequest)
	}
	if len(secondAnchors.Constraints) == 0 || !strings.Contains(secondAnchors.Constraints[0], "不要改动公开 API") {
		t.Fatalf("Constraints = %v, want the first-round constraint inherited", secondAnchors.Constraints)
	}
}

// TestCompactionInputHonorsConfiguredReductionPolicy pins the fix for a silent
// inconsistency: the summarize input is reduced before it reaches the
// compaction model, and that reduction used to run with hardcoded defaults, so
// a session that had deliberately raised its retention thresholds still had its
// durable summary built from default-trimmed tool output.
func TestCompactionInputHonorsConfiguredReductionPolicy(t *testing.T) {
	body := strings.Repeat("build step output line\n", 400)
	head := []message.Message{
		{Role: message.RoleUser, Content: "run the build"},
		{
			Role:         message.RoleAssistant,
			RequestBatch: 1,
			ToolCalls:    []message.ToolCall{{ID: "sh-1", Name: tools.NameShell, Args: json.RawMessage(`{"command":"go build ./..."}`)}},
		},
		{Role: message.RoleTool, ToolCallID: "sh-1", Content: body},
		{Role: message.RoleUser, Content: "keep going"},
		{Role: message.RoleAssistant, RequestBatch: 5, Content: "continuing"},
	}

	defaults := newTestMainAgent(t, t.TempDir())
	reduced, err := defaults.buildCompactionInputWithOptions(head, 200000, nil, nil, compactionAnchors{})
	if err != nil {
		t.Fatalf("buildCompactionInputWithOptions: %v", err)
	}
	if strings.Contains(reduced.Transcript, body) {
		t.Fatal("default policy should still reduce a large aged shell success output")
	}

	retaining := newTestMainAgent(t, t.TempDir())
	retaining.projectConfig = &config.Config{Context: config.ContextConfig{Reduction: config.ContextReductionConfig{
		ShellSuccessBytes: 1 << 20,
		StaleOutputBytes:  1 << 20,
	}}}
	kept, err := retaining.buildCompactionInputWithOptions(head, 200000, nil, nil, compactionAnchors{})
	if err != nil {
		t.Fatalf("buildCompactionInputWithOptions: %v", err)
	}
	if !strings.Contains(kept.Transcript, body) {
		t.Fatalf("configured retention thresholds were ignored when building the compaction input:\n%s", kept.Transcript)
	}
}

func TestCompactionReductionScratchDoesNotShareAgentState(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.setPreparedStablePrefixLen(7)
	scratch := a.compactionReductionScratch()
	if scratch == a {
		t.Fatal("compaction reduction must not run on the live agent")
	}
	if scratch.globalConfig != a.globalConfig || scratch.projectConfig != a.projectConfig {
		t.Fatal("scratch agent must carry the configured reduction policy")
	}
	// Reducing through the scratch agent must not disturb the live agent's
	// prepared-surface bookkeeping for the in-flight main request.
	_ = scratch.prepareMessagesForLLM([]message.Message{{Role: message.RoleUser, Content: "hi"}})
	if got := a.consumePreparedStablePrefixLen(); got != 7 {
		t.Fatalf("live prepared prefix len = %d, want 7", got)
	}
}
