package agent

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/agent/checkpointfmt"
	"github.com/keakon/chord/internal/agent/requesttrim"
	"github.com/keakon/chord/internal/ctxmgr"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

func TestBuildCompactionInputUsesProvidedEvidenceAndTail(t *testing.T) {
	head := []message.Message{
		{Role: "user", Content: "Improve extraction quality and prioritize candidate filtering."},
		{Role: "assistant", Content: "Classify the failure source before changing prompts or rules."},
		{Role: "tool", Content: "Error: patch not found"},
	}
	evidence := []evidenceItem{{Kind: evidenceUserCorrection, Title: "constraint", Excerpt: "do not hardcode"}}
	tail := []message.Message{{Role: "user", Content: "Continue and prioritize candidate containment handling."}}

	input, err := (&MainAgent{}).buildCompactionInputWithOptions(head, 8192, evidence, tail, compactionAnchors{})
	if err != nil {
		t.Fatalf("buildCompactionInput error: %v", err)
	}
	if got := len(input.EvidenceItems); got != 1 {
		t.Fatalf("len(EvidenceItems) = %d, want 1", got)
	}
	if input.EvidenceItems[0].Excerpt != "do not hardcode" {
		t.Fatalf("evidence excerpt = %q", input.EvidenceItems[0].Excerpt)
	}
	if len(input.RecentTail) != 1 || input.RecentTail[0].Content != tail[0].Content {
		t.Fatalf("recent tail mismatch: %+v", input.RecentTail)
	}
}

func TestBuildTruncateOnlySummaryIncludesFilesAndEvidenceSection(t *testing.T) {
	summary := buildTruncateOnlySummary(
		"history-1.md",
		fmt.Errorf("utility model unavailable"),
		[]string{"internal/agent/compaction.go"},
		nil,
		nil,
	)
	for _, heading := range checkpointfmt.RequiredHeadings() {
		if !strings.Contains(summary, heading) {
			t.Fatalf("truncate-only summary missing heading %q:\n%s", heading, summary)
		}
	}
	if !strings.Contains(summary, "- internal/agent/compaction.go") {
		t.Fatalf("truncate-only summary missing key file bullet:\n%s", summary)
	}
}

func TestRecordEvidenceFromMessageCapturesToolErrorAndDiff(t *testing.T) {
	a := &MainAgent{}
	a.recordEvidenceFromMessage(message.Message{Role: "tool", Content: "go test failed\n\nError: exit code 1", ToolDiff: "+ changed\n- old"})
	if got := a.evidence.len(); got != 2 {
		t.Fatalf("len(evidenceCandidates) = %d, want 2", got)
	}
}

func TestRecordEvidenceFromMessageCapturesToolStatusErrorWithoutErrorPrefix(t *testing.T) {
	a := &MainAgent{}
	a.recordEvidenceFromMessage(message.Message{
		Role:       "tool",
		Content:    "patch makes no changes. No files were modified",
		ToolStatus: message.ToolStatusError,
	})
	if got := a.evidence.len(); got != 1 {
		t.Fatalf("len(evidenceCandidates) = %d, want 1", got)
	}
	if a.evidence.snapshot()[0].Kind != evidenceToolError {
		t.Fatalf("evidence kind = %q, want %q", a.evidence.snapshot()[0].Kind, evidenceToolError)
	}
}

func TestSuccessfulToolOutputContainingErrorIsNotFailureEvidence(t *testing.T) {
	a := &MainAgent{}
	a.recordEvidenceFromMessage(message.Message{
		Role:       message.RoleTool,
		Content:    `source contains "Error:" as a string literal`,
		ToolStatus: message.ToolStatusSuccess,
	})
	if got := a.evidence.len(); got != 0 {
		t.Fatalf("successful output was recorded as failure evidence: %+v", a.evidence.snapshot())
	}
	if message.IsToolResultErrorMessage(message.Message{Role: message.RoleTool, Content: "Error: in documentation", ToolStatus: message.ToolStatusSuccess}) {
		t.Fatal("explicit success must override an Error: substring")
	}
}

func TestUserFileContentDoesNotBecomeCompactionConstraint(t *testing.T) {
	msgs := []message.Message{
		{Role: message.RoleUser, Parts: []message.ContentPart{
			{Type: message.ContentPartText, Text: "Please inspect this plan."},
			{Type: message.ContentPartText, Text: `<file path="plan.md">` + "\nDo not change the public API.\n" + `</file>`},
		}},
		{Role: message.RoleAssistant, Content: "I will inspect it."},
		{Role: message.RoleUser, Content: "Continue with the analysis."},
	}
	items := collectEvidenceItems(msgs)
	for _, item := range items {
		if strings.Contains(item.Excerpt, "Do not change the public API") {
			t.Fatalf("file content leaked into evidence: %+v", item)
		}
	}
}

func TestEvidenceSelectionLimitsRepeatedToolErrors(t *testing.T) {
	msgs := make([]message.Message, 0, 4)
	for i := range 4 {
		msgs = append(msgs, message.Message{
			Role:       message.RoleTool,
			Content:    fmt.Sprintf("Error: failure %d", i),
			ToolStatus: message.ToolStatusError,
		})
	}
	items := selectEvidenceItems(msgs, 4096)
	count := 0
	for _, item := range items {
		if item.Kind == evidenceToolError {
			count++
		}
	}
	if count > maxToolErrorEvidenceItems {
		t.Fatalf("selected %d repeated tool errors, want at most %d: %+v", count, maxToolErrorEvidenceItems, items)
	}
}

func TestSelectEvidenceItemsSkipsToolErrorSupersededByLaterSuccess(t *testing.T) {
	msgs := []message.Message{
		{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{{ID: "call-1", Name: tools.NameRead, Args: json.RawMessage(`{"path":"src/a.go"}`)}}},
		{Role: message.RoleTool, ToolCallID: "call-1", Content: "Error: file not found", ToolStatus: message.ToolStatusError},
		{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{{ID: "call-2", Name: tools.NameRead, Args: json.RawMessage(`{"path":"src/a.go"}`)}}},
		{Role: message.RoleTool, ToolCallID: "call-2", Content: "file contents", ToolStatus: message.ToolStatusSuccess},
		{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{{ID: "call-3", Name: tools.NameEdit, Args: json.RawMessage(`{"path":"src/a.go"}`)}}},
		{Role: message.RoleTool, ToolCallID: "call-3", Content: "Error: patch failed", ToolStatus: message.ToolStatusError},
		{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{{ID: "call-4", Name: tools.NameRead, Args: json.RawMessage(`{"path":"src/b.go"}`)}}},
		{Role: message.RoleTool, ToolCallID: "call-4", Content: "Error: file missing again", ToolStatus: message.ToolStatusError},
	}

	kept := make(map[string]struct{})
	for _, item := range selectEvidenceItems(msgs, 4096) {
		if item.Kind == evidenceToolError {
			kept[item.SourceID] = struct{}{}
		}
	}
	if _, ok := kept["call-1"]; ok {
		t.Fatalf("superseded failure (call-1) must be dropped: %v", kept)
	}
	for _, want := range []string{"call-3", "call-4"} {
		if _, ok := kept[want]; !ok {
			t.Fatalf("unresolved failure %q must stay in the pack: %v", want, kept)
		}
	}
	if len(kept) != 2 {
		t.Fatalf("unexpected extra tool-error evidence: %v", kept)
	}
}

// The supersede filter must match the operation's target, not just the tool
// name: a successful read of another file says nothing about the failure, and
// a shell command's effect is not attributable to a path at all.
func TestToolFailureSupersededOnlyOnMatchingTargets(t *testing.T) {
	call := func(id, name, args string) message.Message {
		return message.Message{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{{ID: id, Name: name, Args: json.RawMessage(args)}}}
	}
	failure := func(id string) message.Message {
		return message.Message{Role: message.RoleTool, ToolCallID: id, Content: "Error: file not found", ToolStatus: message.ToolStatusError}
	}
	success := func(id string) message.Message {
		return message.Message{Role: message.RoleTool, ToolCallID: id, Content: "file contents", ToolStatus: message.ToolStatusSuccess}
	}

	tests := []struct {
		name string
		msgs []message.Message
		want string
	}{
		{
			name: "same tool same target supersedes",
			msgs: []message.Message{
				call("fail", tools.NameRead, `{"path":"src/a.go"}`), failure("fail"),
				call("ok", tools.NameRead, `{"path":"src/a.go"}`), success("ok"),
			},
			want: "fail",
		},
		{
			name: "same tool different target keeps the failure",
			msgs: []message.Message{
				call("fail", tools.NameRead, `{"path":"src/a.go"}`), failure("fail"),
				call("ok", tools.NameRead, `{"path":"src/b.go"}`), success("ok"),
			},
		},
		{
			name: "unattributable tool keeps the failure",
			msgs: []message.Message{
				call("fail", tools.NameShell, `{"command":"go build ./..."}`), failure("fail"),
				call("ok", tools.NameShell, `{"command":"go build ./..."}`), success("ok"),
			},
		},
		{
			name: "later successful write covering the same file supersedes the patch failure",
			msgs: []message.Message{
				call("fail", tools.NameEdit, `{"path":"src/a.go"}`), failure("fail"),
				call("ok", tools.NameEdit, `{"paths":["src/a.go"]}`), success("ok"),
			},
			want: "fail",
		},
		{
			name: "accepted compact_context retry supersedes the rejection",
			msgs: []message.Message{
				call("fail", tools.NameCompactContext, `{"active_objective":"a","next_step":"b"}`), failure("fail"),
				call("ok", tools.NameCompactContext, `{"active_objective":"a","next_step":"b"}`), success("ok"),
			},
			want: "fail",
		},
		{
			// The accepted retry's result is deferred to the barrier, so the
			// snapshot that decides retention holds the declaration without a
			// result. That is still the retry that replaced the rejection.
			name: "in-flight compact_context retry supersedes the rejection",
			msgs: []message.Message{
				call("fail", tools.NameCompactContext, `{"active_objective":"a","next_step":"b"}`), failure("fail"),
				call("ok", tools.NameCompactContext, `{"active_objective":"a","next_step":"b"}`),
			},
			want: "fail",
		},
		{
			name: "unanswered declaration of another tool does not supersede",
			msgs: []message.Message{
				call("fail", tools.NameCompactContext, `{"active_objective":"a","next_step":"b"}`), failure("fail"),
				call("ok", tools.NameRead, `{"path":"src/a.go"}`),
			},
		},
		{
			// An answered call is not in flight: a cancelled result is
			// unresolved, so the rejection is still the newest outcome.
			name: "answered compact_context call with a cancelled result keeps the failure",
			msgs: []message.Message{
				call("fail", tools.NameCompactContext, `{"active_objective":"a","next_step":"b"}`), failure("fail"),
				call("ok", tools.NameCompactContext, `{"active_objective":"a","next_step":"b"}`),
				{Role: message.RoleTool, ToolCallID: "ok", Content: "cancelled", ToolStatus: message.ToolStatusCancelled},
			},
		},
		{
			name: "compact_context rejection with no later acceptance keeps the failure",
			msgs: []message.Message{
				call("fail", tools.NameCompactContext, `{"active_objective":"a","next_step":"b"}`), failure("fail"),
				call("ok", tools.NameCompactContext, `{"active_objective":"a","next_step":"b"}`), failure("ok"),
			},
		},
		{
			name: "another tool's success does not supersede a compact_context rejection",
			msgs: []message.Message{
				call("fail", tools.NameCompactContext, `{"active_objective":"a","next_step":"b"}`), failure("fail"),
				call("ok", tools.NameRead, `{"path":"src/a.go"}`), success("ok"),
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := toolFailureSupersededByLaterSuccess(tt.msgs)
			if tt.want == "" {
				if len(got) != 0 {
					t.Fatalf("superseded = %v, want none", got)
				}
				return
			}
			if _, ok := got[tt.want]; !ok || len(got) != 1 {
				t.Fatalf("superseded = %v, want exactly %q", got, tt.want)
			}
		})
	}
}

func TestEvidenceToolErrorPackSkipsSupersededTrackerCandidate(t *testing.T) {
	a := &MainAgent{}
	a.ctxMgr = ctxmgr.NewManager(100000, 0.5)
	msgs := []message.Message{
		{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{{ID: "call-1", Name: tools.NameRead, Args: json.RawMessage(`{"path":"src/a.go"}`)}}},
		{Role: message.RoleTool, ToolCallID: "call-1", Content: "Error: file not found", ToolStatus: message.ToolStatusError},
		{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{{ID: "call-2", Name: tools.NameRead, Args: json.RawMessage(`{"path":"src/a.go"}`)}}},
		{Role: message.RoleTool, ToolCallID: "call-2", Content: "file contents", ToolStatus: message.ToolStatusSuccess},
		{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{{ID: "call-3", Name: tools.NameEdit, Args: json.RawMessage(`{"path":"src/a.go"}`)}}},
		{Role: message.RoleTool, ToolCallID: "call-3", Content: "Error: patch failed", ToolStatus: message.ToolStatusError},
	}
	a.ctxMgr.RestoreMessages(msgs)
	for _, msg := range msgs {
		if msg.Role != message.RoleTool || !message.IsToolResultErrorMessage(msg) {
			continue
		}
		a.addToolEvidenceCandidate(buildEvidenceItem(
			evidenceToolError,
			"Latest failing tool result",
			"This looks like a current blocker; preserving the exact error helps the next continuation avoid guessing.",
			"tool result",
			requesttrim.TextSnippet(strings.TrimSpace(msg.Content), 800),
		), msg)
	}

	kept := make(map[string]struct{})
	for _, item := range a.evidenceItemsForCompaction(4096) {
		if item.Kind == evidenceToolError {
			kept[item.SourceID] = struct{}{}
		}
	}
	if _, ok := kept["call-1"]; ok {
		t.Fatalf("tracker pack kept the superseded failure (call-1): %v", kept)
	}
	if _, ok := kept["call-3"]; !ok {
		t.Fatalf("tracker pack lost the unresolved failure (call-3): %v", kept)
	}
	if len(kept) != 1 {
		t.Fatalf("unexpected extra tool-error evidence: %v", kept)
	}
}

func TestCollectCompactionEvidenceClassifiesTypedMailbox(t *testing.T) {
	items := selectEvidenceItems([]message.Message{
		{Role: message.RoleUser, Content: "real user request"},
		{Role: message.RoleUser, Content: "completed mailbox", Kind: message.KindSubAgentMailbox, Mailbox: &message.MailboxMetadata{MessageID: "worker-1-1", Kind: string(SubAgentMailboxKindCompleted)}},
	}, 10000)
	var userRequests, completions int
	for _, item := range items {
		switch item.Kind {
		case evidenceUserRequest:
			userRequests++
		case evidenceSubAgentDone:
			completions++
		}
	}
	if userRequests != 1 || completions != 1 {
		t.Fatalf("evidence counts = user:%d completion:%d, want 1 each; items=%+v", userRequests, completions, items)
	}
}

// --------------------------------------------------------------------------
// Evidence sequencing, recent-tail degradation, correction-marker precision
// --------------------------------------------------------------------------

func TestEvidenceTrackerSequenceKeepsGrowingAfterSaturation(t *testing.T) {
	a := &MainAgent{}
	total := maxEvidenceCandidates + 12
	for i := range total {
		a.recordEvidenceFromMessage(message.Message{
			Role:    message.RoleUser,
			Content: fmt.Sprintf("implement feature number %d in the parser", i),
		})
	}

	items := a.evidence.snapshot()
	if len(items) != maxEvidenceCandidates {
		t.Fatalf("tracker len = %d, want %d", len(items), maxEvidenceCandidates)
	}
	for i := 1; i < len(items); i++ {
		if items[i].Sequence <= items[i-1].Sequence {
			t.Fatalf("sequence stopped increasing at %d: %d <= %d", i, items[i].Sequence, items[i-1].Sequence)
		}
	}

	// A saturated tracker must still resolve the newest request, not the
	// oldest survivor of the retained window.
	want := fmt.Sprintf("implement feature number %d in the parser", total-1)
	var picked string
	for _, item := range evidenceItemsFromCandidates(items, 200000) {
		if item.Kind == evidenceUserRequest {
			picked = item.Excerpt
		}
	}
	if picked != want {
		t.Fatalf("latest user request = %q, want %q", picked, want)
	}
}

func TestEvidenceTrackerSequenceIgnoresCallerSuppliedValue(t *testing.T) {
	var tracker evidenceCandidateTracker
	tracker.add(evidenceItem{Kind: evidenceToolError, Excerpt: "first", Sequence: 999})
	tracker.add(evidenceItem{Kind: evidenceToolError, Excerpt: "second", Sequence: 1})
	items := tracker.snapshot()
	if len(items) != 2 {
		t.Fatalf("len(items) = %d, want 2", len(items))
	}
	if items[0].Sequence != 1 || items[1].Sequence != 2 {
		t.Fatalf("sequences = %d, %d; want 1, 2", items[0].Sequence, items[1].Sequence)
	}
}

// TestFilterCompactionEvidenceForArchivalKeepsReferencableConstraints guards
// the archival profile's evidence keep-set: the archival summary is generated
// once and never re-scans the archived content, so a declarative constraint
// dropped from the evidence pack would be lost for the rest of the session.
// The keep-set also covers every kind a committed checkpoint may cite as
// acceptance evidence (user corrections, stated constraints, tool diffs), so
// a validated ID is always visible inside the checkpoint.
func TestFilterCompactionEvidenceForArchivalKeepsStatedConstraints(t *testing.T) {
	items := []evidenceItem{
		{Kind: evidenceUserCorrection, Title: "User correction / constraint", Excerpt: "do not change the public API", Priority: 100, Sequence: 1},
		{Kind: evidenceStatedConstraint, Title: "Stated constraint", Excerpt: "keep the existing output format", Priority: 90, Sequence: 2},
		{Kind: evidenceToolDiff, Title: "Recent code diff in main.go", Excerpt: "diff", Priority: 90, Sequence: 3},
		{Kind: evidenceSubAgentDone, Title: "SubAgent completion summary", Excerpt: "done", Priority: 80, Sequence: 4},
	}
	kept := filterCompactionEvidenceForArchival(items)
	kinds := make([]evidenceKind, 0, len(kept))
	for _, item := range kept {
		kinds = append(kinds, item.Kind)
	}
	if len(kept) != 3 || kinds[0] != evidenceUserCorrection || kinds[1] != evidenceStatedConstraint || kinds[2] != evidenceToolDiff {
		t.Fatalf("kept kinds = %v, want the user correction, stated constraint and tool diff preserved", kinds)
	}
}

func TestFilterCompactionEvidenceForArchivalDropsInvalidatedEvidence(t *testing.T) {
	items := []evidenceItem{
		{Kind: evidenceUserCorrection, Excerpt: "keep this", Validity: evidenceValidityValid},
		{Kind: evidenceUserCorrection, Excerpt: "source changed", Validity: evidenceValidityInvalidated},
		{Kind: evidenceStatedConstraint, Excerpt: "keep this too"},
	}
	kept := filterCompactionEvidenceForArchival(items)
	if len(kept) != 2 {
		t.Fatalf("kept evidence = %#v, want only valid evidence", kept)
	}
}

func TestRenderEvidenceArtifactIncludesValidityAndRecovery(t *testing.T) {
	content := renderEvidenceArtifactContent([]evidenceItem{{
		Title: "Observed state", Excerpt: "value", Validity: evidenceValidityValid, Recovery: "reread source", SourceID: "call-1", Revisions: map[string]string{"main.go": "abc"},
	}})
	if !strings.Contains(content, "Validity: valid") || !strings.Contains(content, "Recovery: reread source") || !strings.Contains(content, "Source ID: call-1") || !strings.Contains(content, "Revision: main.go=abc") {
		t.Fatalf("evidence artifact = %q", content)
	}
}

func mustTodoIndex(t *testing.T, id string) int {
	t.Helper()
	n, err := strconv.Atoi(strings.TrimPrefix(id, "t"))
	if err != nil {
		t.Fatalf("bad todo id %q: %v", id, err)
	}
	return n
}

func TestEvidenceItemIDIsShortAndStable(t *testing.T) {
	item := evidenceItem{Kind: evidenceToolDiff, Key: "tool\x00" + strings.Repeat("large", 500)}
	first := evidenceItemID(item)
	second := evidenceItemID(item)
	if first != second || !strings.HasPrefix(first, "ev-") || len(first) != 15 {
		t.Fatalf("evidence ID = %q, want stable short ev ID", first)
	}
	if strings.ContainsRune(first, '\x00') {
		t.Fatalf("evidence ID contains internal separator: %q", first)
	}
}
