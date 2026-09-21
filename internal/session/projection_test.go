package session

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/message"
)

func projectionFixtureMetadata() map[string]string {
	return map[string]string{
		MetadataKeySessionID:  "test-session",
		MetadataKeyInstanceID: "main-1",
	}
}

func mustExportForProjection(t *testing.T, msgs []message.Message) *ExportedSession {
	t.Helper()
	exported, err := Export(msgs, nil, projectionFixtureMetadata())
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	return exported
}

func TestProjectSegmentsTurnsOnUserMessages(t *testing.T) {
	msgs := []message.Message{
		{Role: message.RoleUser, Content: "first request"},
		{
			Role:    message.RoleAssistant,
			Content: "working on it",
			ToolCalls: []message.ToolCall{
				{ID: "call-1", Name: "read", Args: json.RawMessage(`{"path":"sample.txt"}`)},
			},
		},
		{
			Role:           message.RoleTool,
			Content:        "file content",
			ToolCallID:     "call-1",
			ToolStatus:     message.ToolStatusSuccess,
			ToolDurationMs: 12,
		},
		{Role: message.RoleAssistant, Content: "done with first"},
		{Role: message.RoleUser, Content: "second request"},
		{Role: message.RoleAssistant, Content: "done with second"},
	}
	turns, err := Project(mustExportForProjection(t, msgs))
	if err != nil {
		t.Fatalf("Project: %v", err)
	}
	if len(turns) != 2 {
		t.Fatalf("len(turns) = %d, want 2", len(turns))
	}
	if turns[0].Trigger != TriggerUserMessage || turns[1].Trigger != TriggerUserMessage {
		t.Fatalf("triggers = %q, %q, want user_message", turns[0].Trigger, turns[1].Trigger)
	}
	if turns[0].UserText.Text != "first request" || turns[0].UserText.Truncated {
		t.Fatalf("turn 0 user text = %#v", turns[0].UserText)
	}
	if turns[0].AssistantFinalText.Text != "done with first" {
		t.Fatalf("turn 0 assistant text = %#v", turns[0].AssistantFinalText)
	}
	if len(turns[0].ToolCalls) != 1 {
		t.Fatalf("len(turn 0 tool calls) = %d, want 1", len(turns[0].ToolCalls))
	}
	call := turns[0].ToolCalls[0]
	if call.Name != "read" || call.Status != ProjectedStatusSuccess {
		t.Fatalf("tool call = %#v", call)
	}
	sum := sha256.Sum256([]byte("file content"))
	if call.ResultDigest != hex.EncodeToString(sum[:]) {
		t.Fatalf("result digest = %q, want sha256 of content", call.ResultDigest)
	}
	if !strings.Contains(call.Ref, "test-session#2:call-1") {
		t.Fatalf("tool ref = %q, want session pointer", call.Ref)
	}
	if call.Rejected || call.ResultUnknown {
		t.Fatalf("ordinary success must not be rejected/unknown: %#v", call)
	}
	if turns[0].Provenance.MessageIndexRange != [2]int{0, 3} {
		t.Fatalf("turn 0 range = %v, want [0 3]", turns[0].Provenance.MessageIndexRange)
	}
	if turns[0].Provenance.SessionID != "test-session" || turns[0].Provenance.InstanceID != "main-1" {
		t.Fatalf("turn 0 provenance = %#v", turns[0].Provenance)
	}
}

func TestProjectUnknownTriggerWithoutLeadingUser(t *testing.T) {
	msgs := []message.Message{
		{Role: message.RoleAssistant, Content: "continued work"},
		{Role: message.RoleUser, Content: "real request"},
	}
	turns, err := Project(mustExportForProjection(t, msgs))
	if err != nil {
		t.Fatalf("Project: %v", err)
	}
	if len(turns) != 2 {
		t.Fatalf("len(turns) = %d, want 2", len(turns))
	}
	if turns[0].Trigger != TriggerUnknown {
		t.Fatalf("turn 0 trigger = %q, want unknown", turns[0].Trigger)
	}
	if turns[0].UserText.Text != "" {
		t.Fatalf("unknown turn must have empty user text: %#v", turns[0].UserText)
	}
	if turns[0].AssistantFinalText.Text != "continued work" {
		t.Fatalf("turn 0 assistant text = %#v", turns[0].AssistantFinalText)
	}
}

func TestProjectNilSessionErrors(t *testing.T) {
	if _, err := Project(nil); err == nil {
		t.Fatal("nil session must fail instead of emitting empty output")
	}
}

func TestProjectMailboxStarterIsInferred(t *testing.T) {
	msgs := []message.Message{
		{Role: message.RoleUser, Content: "worker handoff", Kind: message.KindSubAgentMailbox},
		{Role: message.RoleAssistant, Content: "ack"},
	}
	turns, err := Project(mustExportForProjection(t, msgs))
	if err != nil {
		t.Fatalf("Project: %v", err)
	}
	if len(turns) != 1 || turns[0].Trigger != TriggerInferred {
		t.Fatalf("turns = %#v, want one inferred turn", turns)
	}
	if turns[0].UserText.Text != "worker handoff" {
		t.Fatalf("mailbox content stays reachable: %#v", turns[0].UserText)
	}
}

func TestProjectSyntheticSignalsStayInTurn(t *testing.T) {
	msgs := []message.Message{
		{Role: message.RoleUser, Content: "run the task"},
		{Role: message.RoleAssistant, Content: "partial"},
		{Role: message.RoleUser, Content: "pressure reminder", Kind: message.KindContextNotice},
		{Role: message.RoleUser, Content: "job finished", Kind: message.KindBackgroundResult},
	}
	turns, err := Project(mustExportForProjection(t, msgs))
	if err != nil {
		t.Fatalf("Project: %v", err)
	}
	// Context notice attaches to the open turn; the background result opens
	// an inferred turn.
	if len(turns) != 2 {
		t.Fatalf("len(turns) = %d, want 2: %#v", len(turns), turns)
	}
	if turns[0].Trigger != TriggerUserMessage {
		t.Fatalf("turn 0 trigger = %q, want user_message", turns[0].Trigger)
	}
	if turns[0].UserText.Text != "run the task" {
		t.Fatalf("in-turn synthetic must not replace user text: %#v", turns[0].UserText)
	}
	if turns[1].Trigger != TriggerInferred {
		t.Fatalf("turn 1 trigger = %q, want inferred", turns[1].Trigger)
	}
}

func TestProjectCompactionSummaryIsBoundaryNotFact(t *testing.T) {
	msgs := []message.Message{
		{Role: message.RoleUser, Content: "original request"},
		{Role: message.RoleAssistant, Content: "original work"},
		{Role: message.RoleUser, Content: "[Context Summary]\ncondensed history", IsCompactionSummary: true},
		{Role: message.RoleAssistant, Content: "after compaction"},
	}
	turns, err := Project(mustExportForProjection(t, msgs))
	if err != nil {
		t.Fatalf("Project: %v", err)
	}
	if len(turns) != 2 {
		t.Fatalf("len(turns) = %d, want 2", len(turns))
	}
	if !turns[1].CompactionBoundary {
		t.Fatal("summary turn must set compaction_boundary")
	}
	if turns[1].UserText.Text != "" {
		t.Fatalf("summary text must not read as a user request: %#v", turns[1].UserText)
	}
	if turns[1].UserText.Ref == "" {
		t.Fatal("boundary turn must keep a ref to the summary marker")
	}
	if turns[1].AssistantFinalText.Text != "after compaction" {
		t.Fatalf("boundary turn assistant text = %#v", turns[1].AssistantFinalText)
	}
}

func TestProjectRejectedCompactCallStaysInTurn(t *testing.T) {
	msgs := []message.Message{
		{Role: message.RoleUser, Content: "compress now"},
		{
			Role:    message.RoleAssistant,
			Content: "",
			ToolCalls: []message.ToolCall{
				{ID: "cc-1", Name: "compact_context", Args: json.RawMessage(`{}`)},
			},
		},
		{
			Role:       message.RoleTool,
			Content:    "Context checkpoint rejected: unknown evidence ID",
			ToolCallID: "cc-1",
			ToolStatus: message.ToolStatusError,
		},
		{
			Role:    message.RoleAssistant,
			Content: "",
			ToolCalls: []message.ToolCall{
				{ID: "cc-2", Name: "compact_context", Args: json.RawMessage(`{}`)},
			},
		},
		{
			Role:       message.RoleTool,
			Content:    "checkpoint accepted",
			ToolCallID: "cc-2",
			ToolStatus: message.ToolStatusSuccess,
		},
	}
	turns, err := Project(mustExportForProjection(t, msgs))
	if err != nil {
		t.Fatalf("Project: %v", err)
	}
	if len(turns) != 1 {
		t.Fatalf("rejected compact call must not cut a new turn: got %d turns", len(turns))
	}
	if len(turns[0].ToolCalls) != 2 {
		t.Fatalf("len(tool calls) = %d, want 2", len(turns[0].ToolCalls))
	}
	// Declined calls persist as ordinary error results with no structural
	// decline marker, so the projection reports the error without guessing a
	// rejection source.
	if turns[0].ToolCalls[0].Status != ProjectedStatusError || turns[0].ToolCalls[0].Rejected {
		t.Fatalf("declined call = %#v, want error without rejected flag", turns[0].ToolCalls[0])
	}
}

func TestProjectRecoveryBarrierAndUnknownAreExplicit(t *testing.T) {
	msgs := []message.Message{
		{Role: message.RoleUser, Content: "restore check"},
		{
			Role:    message.RoleAssistant,
			Content: "",
			ToolCalls: []message.ToolCall{
				{ID: "never", Name: "write", Args: json.RawMessage(`{}`)},
				{ID: "started", Name: "shell", Args: json.RawMessage(`{}`)},
			},
		},
		{
			Role:              message.RoleTool,
			Content:           "session restored before the tool started",
			ToolCallID:        "never",
			ToolStatus:        message.ToolStatusError,
			ToolRecoveryState: message.ToolRecoveryStateNotStarted,
		},
		{
			Role:              message.RoleTool,
			Content:           "session restored after the tool started",
			ToolCallID:        "started",
			ToolStatus:        message.ToolStatusError,
			ToolRecoveryState: message.ToolRecoveryStateOutcomeUnknown,
		},
	}
	turns, err := Project(mustExportForProjection(t, msgs))
	if err != nil {
		t.Fatalf("Project: %v", err)
	}
	if len(turns) != 1 || len(turns[0].ToolCalls) != 2 {
		t.Fatalf("turns = %#v", turns)
	}
	barrier := turns[0].ToolCalls[0]
	if !barrier.Rejected || barrier.RejectionSource != RejectionSourceRecoveryBarrier {
		t.Fatalf("barrier call = %#v, want rejected recovery_barrier", barrier)
	}
	unknown := turns[0].ToolCalls[1]
	if !unknown.ResultUnknown {
		t.Fatalf("outcome_unknown call = %#v, want result_unknown", unknown)
	}
	if unknown.Rejected {
		t.Fatalf("outcome_unknown must not read as rejected: %#v", unknown)
	}
	if unknown.Status != ProjectedStatusError {
		t.Fatalf("outcome_unknown keeps its persisted status: %#v", unknown)
	}
}

func TestProjectFileAttribution(t *testing.T) {
	msgs := []message.Message{
		{Role: message.RoleUser, Content: "edit files"},
		{
			Role:    message.RoleAssistant,
			Content: "",
			ToolCalls: []message.ToolCall{
				{ID: "ok", Name: "edit", Args: json.RawMessage(`{}`)},
				{ID: "part", Name: "write", Args: json.RawMessage(`{}`)},
				{ID: "sh", Name: "shell", Args: json.RawMessage(`{}`)},
			},
		},
		{
			Role:       message.RoleTool,
			Content:    "edited",
			ToolCallID: "ok",
			ToolStatus: message.ToolStatusSuccess,
			FileState: &message.ToolFileState{
				Changes: []message.ToolFileChange{{Path: "sample.go", Added: 2, Removed: 1}},
			},
		},
		{
			Role:       message.RoleTool,
			Content:    "partially written",
			ToolCallID: "part",
			ToolStatus: message.ToolStatusError,
			FileState: &message.ToolFileState{
				Changes: []message.ToolFileChange{{Path: "partial.go", Added: 1}},
			},
		},
		{
			Role:       message.RoleTool,
			Content:    "command output",
			ToolCallID: "sh",
			ToolStatus: message.ToolStatusSuccess,
		},
	}
	turns, err := Project(mustExportForProjection(t, msgs))
	if err != nil {
		t.Fatalf("Project: %v", err)
	}
	if len(turns) != 1 {
		t.Fatalf("len(turns) = %d, want 1", len(turns))
	}
	changes := turns[0].FileChanges
	if len(changes) != 2 {
		t.Fatalf("file changes = %#v, want 2 tool-attributed entries", changes)
	}
	if changes[0].Path != "sample.go" || changes[0].Op != FileOpWrite || changes[0].Attribution != AttributionExact {
		t.Fatalf("success change = %#v", changes[0])
	}
	if changes[1].Path != "partial.go" || changes[1].Attribution != AttributionPartial {
		t.Fatalf("partial change = %#v", changes[1])
	}
	// A shell result without FileState records no file facts: no record does
	// not mean no change.
	for _, change := range changes {
		if change.Path == "" {
			t.Fatalf("change with empty path: %#v", change)
		}
	}
}

func TestProjectMoveAndDeleteOps(t *testing.T) {
	msgs := []message.Message{
		{Role: message.RoleUser, Content: "move files"},
		{
			Role:      message.RoleAssistant,
			Content:   "",
			ToolCalls: []message.ToolCall{{ID: "m", Name: "edit", Args: json.RawMessage(`{}`)}},
		},
		{
			Role:       message.RoleTool,
			Content:    "moved",
			ToolCallID: "m",
			ToolStatus: message.ToolStatusSuccess,
			FileState: &message.ToolFileState{
				Changes: []message.ToolFileChange{
					{Path: "old.go", TargetPath: "new.go"},
					{Path: "gone.go", Deleted: true},
				},
			},
		},
	}
	turns, err := Project(mustExportForProjection(t, msgs))
	if err != nil {
		t.Fatalf("Project: %v", err)
	}
	changes := turns[0].FileChanges
	if len(changes) != 2 {
		t.Fatalf("changes = %#v", changes)
	}
	if changes[0].Op != FileOpMove || changes[0].TargetPath != "new.go" {
		t.Fatalf("move = %#v", changes[0])
	}
	if changes[1].Op != FileOpDelete {
		t.Fatalf("delete = %#v", changes[1])
	}
}

func TestProjectFileFallbacksWithoutChanges(t *testing.T) {
	msgs := []message.Message{
		{Role: message.RoleUser, Content: "track writes"},
		{
			Role:      message.RoleAssistant,
			Content:   "",
			ToolCalls: []message.ToolCall{{ID: "w", Name: "write", Args: json.RawMessage(`{}`)}},
		},
		{
			Role:       message.RoleTool,
			Content:    "written",
			ToolCallID: "w",
			// Legacy record without a terminal status: attribution unknown.
			FileState: &message.ToolFileState{
				Writes:  []message.TrackedFileState{{Path: "tracked.go", Exists: true}},
				Deletes: []message.TrackedFileState{{Path: "removed.go", Exists: false}},
			},
		},
		{
			Role:      message.RoleAssistant,
			Content:   "",
			ToolCalls: []message.ToolCall{{ID: "legacy", Name: "edit", Args: json.RawMessage(`{}`)}},
		},
		{
			Role:             message.RoleTool,
			Content:          "edited",
			ToolCallID:       "legacy",
			ToolStatus:       message.ToolStatusSuccess,
			ToolChangedPaths: []string{"legacy.go"},
		},
	}
	turns, err := Project(mustExportForProjection(t, msgs))
	if err != nil {
		t.Fatalf("Project: %v", err)
	}
	changes := turns[0].FileChanges
	if len(changes) != 3 {
		t.Fatalf("changes = %#v, want tracked write/delete plus legacy path", changes)
	}
	if changes[0].Path != "tracked.go" || changes[0].Op != FileOpWrite || changes[0].Attribution != AttributionUnknown {
		t.Fatalf("tracked write = %#v", changes[0])
	}
	if changes[1].Path != "removed.go" || changes[1].Op != FileOpDelete {
		t.Fatalf("tracked delete = %#v", changes[1])
	}
	// A legacy path list without FileState cannot say which op ran.
	if changes[2].Path != "legacy.go" || changes[2].Op != FileOpUnknown || changes[2].Attribution != AttributionUnknown {
		t.Fatalf("legacy path = %#v", changes[2])
	}
}

func TestProjectTextBudgetMarksTruncation(t *testing.T) {
	msgs := []message.Message{
		{Role: message.RoleUser, Content: strings.Repeat("a", 100)},
		{Role: message.RoleAssistant, Content: "ok"},
	}
	exported := mustExportForProjection(t, msgs)
	limits := DefaultProjectionLimits()
	limits.MaxUserTextRunes = 10
	turns, err := ProjectWithLimits(exported, limits)
	if err != nil {
		t.Fatalf("ProjectWithLimits: %v", err)
	}
	if turns[0].UserText.Text != strings.Repeat("a", 10) || !turns[0].UserText.Truncated {
		t.Fatalf("user text = %#v, want truncated at 10 runes", turns[0].UserText)
	}
	if turns[0].UserText.Ref == "" {
		t.Fatal("truncated text must carry a ref")
	}
	if turns[0].AssistantFinalText.Truncated || turns[0].AssistantFinalText.Ref != "" {
		t.Fatalf("uncut text must not carry flags: %#v", turns[0].AssistantFinalText)
	}
}

func TestProjectTotalBudgetErrors(t *testing.T) {
	msgs := []message.Message{
		{Role: message.RoleUser, Content: "request"},
		{Role: message.RoleAssistant, Content: "response"},
	}
	exported := mustExportForProjection(t, msgs)
	limits := DefaultProjectionLimits()
	limits.MaxTotalBytes = 10
	if _, err := ProjectWithLimits(exported, limits); err == nil {
		t.Fatal("over-budget projection must fail instead of silently dropping turns")
	}
}

func TestProjectOrphanResultsAreDeterministic(t *testing.T) {
	msgs := []message.Message{
		{Role: message.RoleUser, Content: "go"},
		{Role: message.RoleAssistant, Content: "done"},
		{Role: message.RoleTool, Content: "out-b", ToolCallID: "orphan-b", ToolStatus: message.ToolStatusSuccess},
		{Role: message.RoleTool, Content: "out-a", ToolCallID: "orphan-a", ToolStatus: message.ToolStatusSuccess},
	}
	exported := mustExportForProjection(t, msgs)
	var first []byte
	for i := range 200 {
		turns, err := Project(exported)
		if err != nil {
			t.Fatalf("Project: %v", err)
		}
		data, err := MarshalProjectedTurnsJSONL(turns)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if first == nil {
			first = data
			continue
		}
		if string(data) != string(first) {
			t.Fatalf("iteration %d differs: orphan order must follow message index", i)
		}
	}
	turns, err := Project(exported)
	if err != nil {
		t.Fatalf("Project: %v", err)
	}
	if len(turns) != 1 || len(turns[0].ToolCalls) != 2 {
		t.Fatalf("turns = %#v", turns)
	}
	if turns[0].ToolCalls[0].CallID != "orphan-b" || turns[0].ToolCalls[1].CallID != "orphan-a" {
		t.Fatalf("orphans must render in message order: %#v", turns[0].ToolCalls)
	}
}

func TestProjectIsDeterministic(t *testing.T) {
	msgs := []message.Message{
		{Role: message.RoleUser, Content: "request"},
		{
			Role:      message.RoleAssistant,
			Content:   "answer",
			ToolCalls: []message.ToolCall{{ID: "c1", Name: "read", Args: json.RawMessage(`{}`)}},
		},
		{Role: message.RoleTool, Content: "output", ToolCallID: "c1", ToolStatus: message.ToolStatusSuccess},
	}
	exported := mustExportForProjection(t, msgs)
	first, err := Project(exported)
	if err != nil {
		t.Fatalf("Project: %v", err)
	}
	second, err := Project(exported)
	if err != nil {
		t.Fatalf("Project: %v", err)
	}
	a, err := MarshalProjectedTurnsJSONL(first)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	b, err := MarshalProjectedTurnsJSONL(second)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(a) != string(b) {
		t.Fatal("same session projected twice must be byte-identical")
	}
	if len(a) == 0 || a[len(a)-1] != '\n' {
		t.Fatal("JSONL output must end each turn with a newline")
	}
}

func TestExportNewFieldsRoundTrip(t *testing.T) {
	msgs := []message.Message{{
		Role:                      message.RoleTool,
		Content:                   "edited",
		Kind:                      "custom-kind",
		ToolCallID:                "call-9",
		ToolStatus:                message.ToolStatusSuccess,
		FileState:                 &message.ToolFileState{Changes: []message.ToolFileChange{{Path: "sample.go", Added: 1}}},
		FileAttributionIncomplete: true,
		ToolChangedPaths:          []string{"sample.go"},
	}}
	exported, err := Export(msgs, nil, nil)
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	if exported.Messages[0].Kind != "custom-kind" {
		t.Fatalf("kind not exported: %#v", exported.Messages[0])
	}
	if exported.Messages[0].FileState == nil || len(exported.Messages[0].FileState.Changes) != 1 {
		t.Fatalf("file state not exported: %#v", exported.Messages[0])
	}
	restored := exported.ToMessages()
	if restored[0].Kind != "custom-kind" || restored[0].FileState == nil || !restored[0].FileAttributionIncomplete {
		t.Fatalf("restored = %#v", restored[0])
	}
	if len(restored[0].ToolChangedPaths) != 1 {
		t.Fatalf("restored changed paths = %#v", restored[0])
	}
	// The extended export still validates as v1 and survives a file round-trip.
	data, err := json.Marshal(exported)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	imported, err := ImportFromBytes(data)
	if err != nil {
		t.Fatalf("ImportFromBytes: %v", err)
	}
	if imported.Version != CurrentVersion || imported.Messages[0].Kind != "custom-kind" {
		t.Fatalf("imported = %#v", imported.Messages[0])
	}
}

func TestImportReadsV1FixtureWithoutNewFields(t *testing.T) {
	raw := `{"version":"1","created_at":"2026-09-21T00:00:00Z","messages":[{"role":"user","content":"hello","timestamp":"2026-09-21T00:00:00Z"}]}`
	imported, err := ImportFromBytes([]byte(raw))
	if err != nil {
		t.Fatalf("ImportFromBytes: %v", err)
	}
	if len(imported.Messages) != 1 || imported.Messages[0].Kind != "" || imported.Messages[0].FileState != nil {
		t.Fatalf("v1 message = %#v", imported.Messages[0])
	}
	turns, err := Project(imported)
	if err != nil {
		t.Fatalf("Project: %v", err)
	}
	if len(turns) != 1 || turns[0].Trigger != TriggerUserMessage {
		t.Fatalf("turns = %#v", turns)
	}
}

func TestLegacyReaderIgnoresNewExportFields(t *testing.T) {
	exported, err := Export([]message.Message{{
		Role:      message.RoleTool,
		Content:   "edited",
		Kind:      "custom-kind",
		FileState: &message.ToolFileState{Changes: []message.ToolFileChange{{Path: "sample.go"}}},
	}}, nil, nil)
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	data, err := json.Marshal(exported.Messages[0])
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	// A v1-era reader shape without the new fields must still decode: the
	// standard JSON decoder ignores unknown fields, so old builds keep
	// reading new files.
	var legacy struct {
		Role       string `json:"role"`
		Content    string `json:"content"`
		ToolCallID string `json:"tool_call_id"`
	}
	if err := json.Unmarshal(data, &legacy); err != nil {
		t.Fatalf("legacy unmarshal: %v", err)
	}
	if legacy.Role != "tool" || legacy.Content != "edited" {
		t.Fatalf("legacy = %#v", legacy)
	}
}
