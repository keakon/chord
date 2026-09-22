package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

func editMatchFailureError() error {
	return errors.New("old_string not found in file, even after punctuation/whitespace tolerance. Closest match is at line 3 (96% similar, 5 character difference)")
}

func editRetryPayload(name, argsJSON string, err error) *ToolResultPayload {
	return &ToolResultPayload{
		Name:     name,
		ArgsJSON: argsJSON,
		Error:    err,
	}
}

// applyAdvice wraps the free function with the agent's own streak map, so the
// tests read like the call sites in main_handlers_tools.go / sub_event_tools.go.
func applyAdvice(a *MainAgent, contextResult string, payload *ToolResultPayload, isError bool) string {
	return appendEditRetryAdvice(&a.editMatchFailStreak, contextResult, payload.Name, payload.ArgsJSON, a.toolExecutionPipeline().effectiveToolBaseDir(), payload.Error, isError)
}

// mustPatchArgs marshals a patch into the production {"patch": ...} payload
// shape apply_patch actually carries, instead of a fake top-level {"path":...}.
func mustPatchArgs(t *testing.T, patch string) string {
	t.Helper()
	b, err := json.Marshal(struct {
		Patch string `json:"patch"`
	}{Patch: patch})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestAppendEditRetryAdviceFirstFailureNoNote(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	payload := editRetryPayload(tools.NameEdit, `{"path":"demo.md"}`, editMatchFailureError())
	out := applyAdvice(a, "Error: old_string not found", payload, true)
	if strings.Contains(out, "repeated approximate-match") {
		t.Fatalf("out = %q, want no advisory on the first failure", out)
	}
}

func TestAppendEditRetryAdviceSecondFailureAddsNote(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	payload := editRetryPayload(tools.NameEdit, `{"path":"demo.md"}`, editMatchFailureError())
	first := applyAdvice(a, "Error: old_string not found", payload, true)
	second := applyAdvice(a, first, payload, true)
	if !strings.Contains(second, "2 repeated approximate-match failures for demo.md") {
		t.Fatalf("out = %q, want advisory on the second failure", second)
	}
	if !strings.Contains(second, "read the target range fresh") {
		t.Fatalf("out = %q, want fresh-read steering", second)
	}
}

func TestAppendEditRetryAdviceSuccessResetsStreak(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	payload := editRetryPayload(tools.NameEdit, `{"path":"demo.md"}`, editMatchFailureError())
	success := editRetryPayload(tools.NameEdit, `{"path":"demo.md"}`, nil)
	_ = applyAdvice(a, "ok", payload, true)    // streak 1
	_ = applyAdvice(a, "ok", success, false)   // reset
	out := applyAdvice(a, "ok", payload, true) // streak 1 again
	out = applyAdvice(a, out, payload, true)   // streak 2
	if !strings.Contains(out, "2 repeated approximate-match failures") {
		t.Fatalf("out = %q, want advisory only after two failures since the last success", out)
	}
}

func TestAppendEditRetryAdviceSuccessfulReadResetsStreak(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	failure := editRetryPayload(tools.NameEdit, `{"path":"demo.md"}`, editMatchFailureError())
	_ = applyAdvice(a, "err", failure, true)
	read := editRetryPayload(tools.NameRead, `{"path":"demo.md","offset":1,"limit":20}`, nil)
	_ = applyAdvice(a, "ok", read, false)
	out := applyAdvice(a, "err", failure, true)
	if strings.Contains(out, "repeated approximate-match") {
		t.Fatalf("out = %q, want the successful read to start a fresh failure streak", out)
	}
}

func TestAppendEditRetryAdviceIgnoresNonMatchFailure(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	// Ambiguous multi-match needs more context, not a fresh read.
	payload := editRetryPayload(tools.NameEdit, `{"path":"demo.md"}`, errors.New("old_string found 2 times, provide more context or set replace_all to true"))
	out := applyAdvice(a, "Error: ambiguous", payload, true)
	out = applyAdvice(a, out, payload, true)
	if strings.Contains(out, "repeated approximate-match") {
		t.Fatalf("out = %q, want no advisory for non-approximate failures", out)
	}
}

// A non-approximate failure in between does not reset the streak: the count
// is approximate-match failures for the target since its last success (or
// turn start), not strictly consecutive tool results, so the advisory still
// fires at the second approximate failure.
func TestAppendEditRetryAdviceNonMatchFailureBetweenDoesNotResetStreak(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	approx := editRetryPayload(tools.NameEdit, `{"path":"demo.md"}`, editMatchFailureError())
	ambiguous := editRetryPayload(tools.NameEdit, `{"path":"demo.md"}`, errors.New("old_string found 2 times, provide more context or set replace_all to true"))
	out := applyAdvice(a, "Error: old_string not found", approx, true) // streak 1
	out = applyAdvice(a, out, ambiguous, true)                         // neither incremented nor reset
	out = applyAdvice(a, out, approx, true)                            // streak 2 -> advisory
	if !strings.Contains(out, "2 repeated approximate-match failures for demo.md") {
		t.Fatalf("out = %q, want advisory at the second approximate failure despite an intervening non-approximate failure", out)
	}
}

func TestAppendEditRetryAdviceTracksPathsIndependently(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	one := editRetryPayload(tools.NameEdit, `{"path":"a.md"}`, editMatchFailureError())
	two := editRetryPayload(tools.NameEdit, `{"path":"b.md"}`, editMatchFailureError())
	out := applyAdvice(a, "err", one, true) // a streak 1
	out = applyAdvice(a, out, two, true)    // b streak 1
	out = applyAdvice(a, out, one, true)    // a streak 2 -> advisory
	if !strings.Contains(out, "for a.md") || strings.Contains(out, "for b.md") {
		t.Fatalf("out = %q, want advisory only for the twice-failing path", out)
	}
}

func TestAppendEditRetryAdviceIgnoresOtherTools(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	payload := &ToolResultPayload{
		Name:     tools.NameWrite,
		ArgsJSON: `{"path":"demo.md"}`,
		Error:    errors.New("permission denied"),
	}
	out := applyAdvice(a, "Error: permission denied", payload, true)
	out = applyAdvice(a, out, payload, true)
	if strings.Contains(out, "repeated approximate-match") {
		t.Fatalf("out = %q, want no advisory for non-edit tools", out)
	}
}

// apply_patch carries its target path inside the patch text, not as a
// top-level field; the advisory must parse the patch to track per-path
// streaks against the production payload shape.
func TestAppendEditRetryAdviceApplyPatchParsesPatchPath(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	patch := "*** Begin Patch\n" +
		"*** Update File: demo.md\n" +
		"@@\n" +
		"-### 3.2.1 Heading\n" +
		"+### 3.2.1 Renamed\n" +
		"*** End Patch"
	payload := &ToolResultPayload{
		Name:     tools.NameApplyPatch,
		ArgsJSON: mustPatchArgs(t, patch),
		Error:    errors.New("hunk not found (1/1); the expected text is only part of current line 3"),
	}
	out := applyAdvice(a, "Error: hunk not found", payload, true)
	out = applyAdvice(a, out, payload, true)
	if !strings.Contains(out, "2 repeated approximate-match failures for demo.md") {
		t.Fatalf("out = %q, want advisory parsed from the apply_patch patch text", out)
	}
}

// A multi-file patch cannot be localized to one failing target from the
// arguments alone, so the advisory is skipped rather than keying on the
// wrong file.
func TestAppendEditRetryAdviceApplyPatchMultiFileSkips(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	patch := "*** Begin Patch\n" +
		"*** Update File: a.md\n" +
		"@@\n" +
		"-a\n" +
		"+b\n" +
		"*** Update File: b.md\n" +
		"@@\n" +
		"-c\n" +
		"+d\n" +
		"*** End Patch"
	payload := &ToolResultPayload{
		Name:     tools.NameApplyPatch,
		ArgsJSON: mustPatchArgs(t, patch),
		Error:    errors.New("hunk not found (1/1)"),
	}
	out := applyAdvice(a, "Error: hunk not found", payload, true)
	out = applyAdvice(a, out, payload, true)
	if strings.Contains(out, "repeated approximate-match") {
		t.Fatalf("out = %q, want no advisory for a multi-file patch", out)
	}
}

func TestAppendEditRetryAdviceCapsAfterLimit(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	payload := editRetryPayload(tools.NameEdit, `{"path":"demo.md"}`, editMatchFailureError())
	// Up to the cap, each failure from the threshold on appends a note.
	for i := 1; i <= editRetryAdviceCap; i++ {
		out := applyAdvice(a, "err", payload, true)
		if i >= editRetryAdviceThreshold {
			if !strings.Contains(out, fmt.Sprintf("%d repeated", i)) {
				t.Fatalf("streak %d: out = %q, want note", i, out)
			}
		}
	}
	// Beyond the cap the note is suppressed so it cannot accumulate without
	// bound; the streak itself still counts (a later success would reset it).
	before := "err"
	after := applyAdvice(a, before, payload, true)
	if after != before {
		t.Fatalf("beyond cap: out = %q, want no new note (input unchanged)", after)
	}
}

// A successful result on a turn that tracked no failures needs no per-path
// work: the streak map is nil, so there is nothing to clear, and the advisory
// must not try to resolve the target path (which for apply_patch parses the
// whole patch payload) on the hot success path. Regression guard for the
// `!isError && *streaks == nil` early return.
func TestAppendEditRetryAdviceNilStreakSuccessNoNote(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	// Success with no prior failures: the map stays nil and the result is
	// returned unchanged, with no note and no path parsing.
	if out := applyAdvice(a, "ok", editRetryPayload(tools.NameEdit, `{"path":"demo.md"}`, nil), false); out != "ok" {
		t.Fatalf("edit success out = %q, want unchanged (no path work, no note)", out)
	}
	// apply_patch success: the path lives inside a patch payload that must
	// not be parsed on a clean success.
	patch := "*** Begin Patch\n*** Update File: demo.md\n@@\n-a\n+b\n*** End Patch"
	patchSuccess := &ToolResultPayload{
		Name:     tools.NameApplyPatch,
		ArgsJSON: mustPatchArgs(t, patch),
	}
	if out := applyAdvice(a, "ok", patchSuccess, false); out != "ok" {
		t.Fatalf("apply_patch success out = %q, want unchanged", out)
	}
}

// Integration: the MainAgent's real handleToolResult wiring (not the free
// function) must append the advisory to the model-facing context result
// stored in ctxMgr while the TUI display result stays untouched.
func TestMainAgentHandleToolResultAppendsAdvisoryToContextOnly(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.newTurn()
	turnID := a.turn.ID
	callID := "edit-fail-1"
	argsJSON := `{"path":"demo.md"}`
	a.turn.PendingToolCalls.Store(3)
	a.turn.TotalToolCalls.Store(3)
	a.turn.recordPendingToolCall(PendingToolCall{CallID: callID, Name: tools.NameEdit, ArgsJSON: argsJSON})

	fail := func() {
		a.handleToolResult(Event{Type: EventToolResult, TurnID: turnID, Payload: &ToolResultPayload{
			CallID:   callID,
			Name:     tools.NameEdit,
			ArgsJSON: argsJSON,
			Error:    editMatchFailureError(),
			Result:   "",
		}})
	}
	fail()
	msgs := a.GetMessages()
	last := msgs[len(msgs)-1]
	if strings.Contains(last.Content, "repeated approximate-match failures") {
		t.Fatalf("first failure: tool message content = %q, want no advisory yet", last.Content)
	}
	fail()
	msgs = a.GetMessages()
	last = msgs[len(msgs)-1]
	if !strings.Contains(last.Content, "2 repeated approximate-match failures for demo.md") {
		t.Fatalf("second failure: tool message content = %q, want advisory in context result", last.Content)
	}
	if !strings.Contains(last.Content, "old_string not found") {
		t.Fatalf("second failure: tool message content = %q, want the underlying error text to survive alongside the note", last.Content)
	}
	// The display result pushed to the TUI never carries the advisory.
	display := collectToolResultEventResults(t, a, callID)
	for _, r := range display {
		if strings.Contains(r, "repeated approximate-match failures") {
			t.Fatalf("TUI display result = %q, want advisory confined to the context result", r)
		}
	}
}

// Integration: SubAgent's real handleToolResult wiring appends the advisory
// to its own model-facing context result on repeated failures.
func TestSubAgentHandleToolResultAppendsAdvisoryToContext(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	sub := newControllableTestSubAgent(t, a, "edit-retry")
	sub.newTurn()
	turnID := sub.turn.ID
	callID := "sub-edit-fail-1"
	argsJSON := `{"path":"demo.md"}`
	sub.turn.PendingToolCalls.Store(3)
	sub.turn.TotalToolCalls.Store(3)
	sub.turn.recordPendingToolCall(PendingToolCall{CallID: callID, Name: tools.NameEdit, ArgsJSON: argsJSON})

	fail := func() {
		sub.handleToolResult(&toolResult{
			CallID:   callID,
			Name:     tools.NameEdit,
			ArgsJSON: argsJSON,
			Error:    editMatchFailureError(),
			TurnID:   turnID,
		})
	}
	fail()
	fail()
	msgs := sub.GetMessages()
	last := msgs[len(msgs)-1]
	if !strings.Contains(last.Content, "2 repeated approximate-match failures for demo.md") {
		t.Fatalf("subagent tool message content = %q, want advisory in context result", last.Content)
	}
	display := collectToolResultEventResults(t, a, callID)
	for _, r := range display {
		if strings.Contains(r, "repeated approximate-match failures") {
			t.Fatalf("subagent TUI display result = %q, want advisory confined to the context result", r)
		}
	}
}

// A fresh turn must not inherit the previous turn's failure streak: two
// failures before the reset, then two failures after it, each pair produces
// exactly one advisory at the second failure.
func TestEditRetryStreakResetsOnNewTurn(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	payload := editRetryPayload(tools.NameEdit, `{"path":"demo.md"}`, editMatchFailureError())
	firstTurn := applyAdvice(a, "err", payload, true)    // streak 1
	firstTurn = applyAdvice(a, firstTurn, payload, true) // streak 2 -> note
	if !strings.Contains(firstTurn, "2 repeated") {
		t.Fatalf("first turn second failure = %q, want advisory", firstTurn)
	}
	a.newTurn()
	after := applyAdvice(a, "err", payload, true) // streak restarts at 1
	if strings.Contains(after, "repeated") {
		t.Fatalf("after new turn first failure = %q, want no advisory (streak reset)", after)
	}
	after = applyAdvice(a, after, payload, true) // streak 2 -> note again
	if !strings.Contains(after, "2 repeated") {
		t.Fatalf("after new turn second failure = %q, want advisory again", after)
	}
}

func TestMainAgentApplyPatchRetryRequiresSuccessfulRead(t *testing.T) {
	projectRoot := t.TempDir()
	path := filepath.Join(projectRoot, "demo.txt")
	if err := os.WriteFile(path, []byte("current\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	a := newTestMainAgent(t, projectRoot)
	a.tools.Register(tools.ReadTool{BaseDir: projectRoot})
	a.tools.Register(tools.ApplyPatchTool{BaseDir: projectRoot})
	a.newTurn()

	patch := "*** Begin Patch\n" +
		"*** Update File: demo.txt\n" +
		"@@\n" +
		"-expected\n" +
		"+replacement\n" +
		"*** End Patch"
	argsJSON := mustPatchArgs(t, patch)
	first, firstErr := a.executeToolCall(context.Background(), message.ToolCall{
		ID:   "patch-1",
		Name: tools.NameApplyPatch,
		Args: json.RawMessage(argsJSON),
	})
	if firstErr == nil || !strings.Contains(firstErr.Error(), "hunk not found") {
		t.Fatalf("first error = %v, want ordinary hunk-not-found diagnostic", firstErr)
	}
	a.applyPatchRetry.observeResult(tools.NameApplyPatch, first.EffectiveArgsJSON, projectRoot, firstErr)

	blocked, blockedErr := a.executeToolCall(context.Background(), message.ToolCall{
		ID:   "patch-2",
		Name: tools.NameApplyPatch,
		Args: json.RawMessage(argsJSON),
	})
	if blockedErr == nil || !strings.Contains(blockedErr.Error(), "this exact patch already failed") {
		t.Fatalf("second error = %v, want unchanged-patch rejection", blockedErr)
	}
	if !blocked.ExecStartedAt.IsZero() {
		t.Fatalf("blocked retry execution started at %v, want rejection before execution", blocked.ExecStartedAt)
	}

	if err := os.WriteFile(path, []byte("expected\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	readArgs := `{"path":"demo.txt","offset":1,"limit":20}`
	readResult, readErr := a.executeToolCall(context.Background(), message.ToolCall{
		ID:   "read-1",
		Name: tools.NameRead,
		Args: json.RawMessage(readArgs),
	})
	if readErr != nil {
		t.Fatalf("read current target: %v", readErr)
	}
	a.applyPatchRetry.observeResult(tools.NameRead, readResult.EffectiveArgsJSON, projectRoot, nil)

	if _, err := a.executeToolCall(context.Background(), message.ToolCall{
		ID:   "patch-3",
		Name: tools.NameApplyPatch,
		Args: json.RawMessage(argsJSON),
	}); err != nil {
		t.Fatalf("retry after successful read: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "replacement\n" {
		t.Fatalf("file content = %q, want replacement", got)
	}
}

func TestApplyPatchRetryAllowsChangedPatchWithoutRead(t *testing.T) {
	projectRoot := t.TempDir()
	stalePatch := "*** Begin Patch\n*** Update File: demo.txt\n@@\n-old\n+new\n*** End Patch"
	revisedPatch := "*** Begin Patch\n*** Update File: demo.txt\n@@\n-current\n+new\n*** End Patch"
	var guard applyPatchRetryGuard
	guard.observeResult(
		tools.NameApplyPatch,
		mustPatchArgs(t, stalePatch),
		projectRoot,
		errors.New("hunk not found (1/1)"),
	)
	if err := guard.reject(tools.NameApplyPatch, json.RawMessage(mustPatchArgs(t, revisedPatch)), projectRoot); err != nil {
		t.Fatalf("changed patch rejected: %v", err)
	}
}

// Path spellings that resolve to the same file must share one streak: the
// advisory keys on the resolved path, so `demo.md`, `./demo.md` and the
// absolute path all increment the same counter.
func TestEditRetryAdviceUnifiesPathSpellings(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	root := a.contentRoot
	rel := editRetryPayload(tools.NameEdit, `{"path":"demo.md"}`, editMatchFailureError())
	dotRel := editRetryPayload(tools.NameEdit, `{"path":"./demo.md"}`, editMatchFailureError())
	abs := editRetryPayload(tools.NameEdit, fmt.Sprintf(`{"path":%q}`, filepath.Join(root, "demo.md")), editMatchFailureError())
	out := applyAdvice(a, "err", rel, true)
	out = applyAdvice(a, out, dotRel, true) // same resolved path -> streak 2
	if !strings.Contains(out, "2 repeated approximate-match failures for demo.md") {
		t.Fatalf("rel then ./rel = %q, want advisory on the second spelling", out)
	}
	out = applyAdvice(a, out, abs, true) // streak 3, still one counter
	if !strings.Contains(out, "3 repeated approximate-match failures for demo.md") {
		t.Fatalf("rel, ./rel then abs = %q, want a single streak reaching 3", out)
	}
}

// The pipeline's canonical path extraction accepts the legacy filePath alias
// and string-wrapped JSON args; the advisory must track both spellings as
// the same target.
func TestEditRetryAdviceSupportsFileAliasAndWrappedArgs(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	aliased := editRetryPayload(tools.NameEdit, `{"filePath":"demo.md"}`, editMatchFailureError())
	wrapped := editRetryPayload(tools.NameEdit, `"{\"path\":\"demo.md\"}"`, editMatchFailureError())
	canonical := editRetryPayload(tools.NameEdit, `{"path":"demo.md"}`, editMatchFailureError())
	out := applyAdvice(a, "err", aliased, true)
	out = applyAdvice(a, out, wrapped, true) // same target -> streak 2
	if !strings.Contains(out, "2 repeated approximate-match failures for demo.md") {
		t.Fatalf("filePath then wrapped = %q, want one streak across spellings", out)
	}
	out = applyAdvice(a, out, canonical, true) // streak 3
	if !strings.Contains(out, "3 repeated approximate-match failures for demo.md") {
		t.Fatalf("filePath, wrapped then canonical = %q, want a single streak reaching 3", out)
	}
}

// collectToolResultEventResults drains the TUI output channel for
// ToolResultEvent display results belonging to a tool call.
func collectToolResultEventResults(t *testing.T, a *MainAgent, callID string) []string {
	t.Helper()
	var results []string
	for {
		select {
		case evt := <-a.outputCh:
			if tre, ok := evt.(ToolResultEvent); ok && tre.CallID == callID {
				results = append(results, tre.Result)
			}
			if len(results) >= 2 {
				return results
			}
		default:
			return results
		}
	}
}

// A target rewrite by another tool (edit/write/shell) or an external editor
// is as fresh a basis for a retry as a successful Read: the guard snapshots
// the target's content hash when the failure arms the block, and an
// unchanged-patch retry that finds the file changed is allowed to execute —
// it can now match the new content, and if it fails again the failure re-arms
// the guard on the new revision.
func TestApplyPatchRetryUnblocksAfterTargetRewriteWithoutRead(t *testing.T) {
	projectRoot := t.TempDir()
	path := filepath.Join(projectRoot, "demo.txt")
	if err := os.WriteFile(path, []byte("current\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	a := newTestMainAgent(t, projectRoot)
	a.tools.Register(tools.ReadTool{BaseDir: projectRoot})
	a.tools.Register(tools.ApplyPatchTool{BaseDir: projectRoot})
	a.newTurn()

	patch := "*** Begin Patch\n" +
		"*** Update File: demo.txt\n" +
		"@@\n" +
		"-old\n" +
		"+replacement\n" +
		"*** End Patch"
	argsJSON := mustPatchArgs(t, patch)
	first, firstErr := a.executeToolCall(context.Background(), message.ToolCall{
		ID:   "patch-1",
		Name: tools.NameApplyPatch,
		Args: json.RawMessage(argsJSON),
	})
	if firstErr == nil || !strings.Contains(firstErr.Error(), "hunk not found") {
		t.Fatalf("first error = %v, want ordinary hunk-not-found diagnostic", firstErr)
	}
	a.applyPatchRetry.observeResult(tools.NameApplyPatch, first.EffectiveArgsJSON, projectRoot, firstErr)

	blocked, blockedErr := a.executeToolCall(context.Background(), message.ToolCall{
		ID:   "patch-2",
		Name: tools.NameApplyPatch,
		Args: json.RawMessage(argsJSON),
	})
	if blockedErr == nil || !strings.Contains(blockedErr.Error(), "this exact patch already failed") {
		t.Fatalf("second error = %v, want unchanged-patch rejection while the file is unchanged", blockedErr)
	}
	if !blocked.ExecStartedAt.IsZero() {
		t.Fatalf("blocked retry execution started at %v, want rejection before execution", blocked.ExecStartedAt)
	}

	// The model's own earlier edit/write (or an external editor) rewrote the
	// file to the content this patch expects — note there is no Read here.
	if err := os.WriteFile(path, []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := a.executeToolCall(context.Background(), message.ToolCall{
		ID:   "patch-3",
		Name: tools.NameApplyPatch,
		Args: json.RawMessage(argsJSON),
	}); err != nil {
		t.Fatalf("retry after the target file changed: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "replacement\n" {
		t.Fatalf("file content = %q, want replacement", got)
	}
}
