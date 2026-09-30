package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/keakon/chord/internal/ctxmgr"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
	"github.com/keakon/chord/internal/worktree"
)

func checkpointWorklogShellArgs(t *testing.T, command string) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(map[string]string{"command": command})
	if err != nil {
		t.Fatalf("marshal shell args: %v", err)
	}
	return raw
}

func checkpointWorklogPatchArgs(t *testing.T, patch string) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(tools.ApplyPatchArgs{Patch: patch})
	if err != nil {
		t.Fatalf("marshal patch args: %v", err)
	}
	return raw
}

func checkpointWorklogPathArgs(t *testing.T, path string) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(map[string]string{"path": path})
	if err != nil {
		t.Fatalf("marshal path args: %v", err)
	}
	return raw
}

func checkpointWorklogWriteResult(callID, path string) message.Message {
	return message.Message{Role: message.RoleTool, ToolCallID: callID, ToolStatus: message.ToolStatusSuccess, Content: "ok", FileState: &message.ToolFileState{Writes: []message.TrackedFileState{{Path: path, Exists: true}}}}
}

func checkpointWorklogEstimateTokens(text string) int {
	return ctxmgr.EstimateMessagesTokens([]message.Message{{Role: message.RoleUser, Content: text}})
}

func TestBuildCheckpointWorklogExtractsMachineWork(t *testing.T) {
	patch := "*** Begin Patch\n" +
		"*** Update File: src/old.go\n" +
		"*** Move to: src/new.go\n" +
		"@@\n-old\n+new\n" +
		"*** Add File: docs/new.md\n" +
		"+# New\n" +
		"*** Delete File: tmp/old.txt\n" +
		"*** End Patch"
	head := []message.Message{
		{Role: message.RoleUser, Content: "do the work"},
		{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{
			{ID: "c1", Name: tools.NameWrite, Args: checkpointWorklogPathArgs(t, "internal/agent/a.go")},
			{ID: "c2", Name: tools.NameEdit, Args: checkpointWorklogPathArgs(t, "internal/agent/b.go")},
			{ID: "c3", Name: tools.NameDelete, Args: checkpointWorklogPathArgs(t, "docs/gone.md")},
			{ID: "c4", Name: tools.NameApplyPatch, Args: checkpointWorklogPatchArgs(t, patch)},
			{ID: "c5", Name: tools.NameShell, Args: checkpointWorklogShellArgs(t, "go test ./internal/agent/")},
			{ID: "c6", Name: tools.NameShell, Args: checkpointWorklogShellArgs(t, "go test ./internal/agent/")},
			{ID: "c7", Name: tools.NameShell, Args: checkpointWorklogShellArgs(t, `git commit -m "feat: worklog"`)},
		}},
		checkpointWorklogWriteResult("c1", "internal/agent/a.go"),
		checkpointWorklogWriteResult("c2", "internal/agent/b.go"),
		{Role: message.RoleTool, ToolCallID: "c3", ToolStatus: message.ToolStatusSuccess, FileState: &message.ToolFileState{Deletes: []message.TrackedFileState{{Path: "docs/gone.md"}}}},
		{Role: message.RoleTool, ToolCallID: "c4", ToolStatus: message.ToolStatusSuccess, FileState: &message.ToolFileState{
			Writes:  []message.TrackedFileState{{Path: "src/new.go", Exists: true}, {Path: "docs/new.md", Exists: true}},
			Deletes: []message.TrackedFileState{{Path: "src/old.go"}, {Path: "tmp/old.txt"}},
			Changes: []message.ToolFileChange{{Path: "src/old.go", TargetPath: "src/new.go"}},
		}},
		{Role: message.RoleTool, ToolCallID: "c5", ToolStatus: message.ToolStatusSuccess, Content: "ok"},
		{Role: message.RoleTool, ToolCallID: "c6", ToolStatus: message.ToolStatusSuccess, Content: "ok"},
		{Role: message.RoleTool, ToolCallID: "c7", ToolStatus: message.ToolStatusSuccess, Content: "[main 75e8edc8] feat: worklog\n 2 files changed"},
	}
	worklog := buildCheckpointWorklog(head)
	wantFiles := []checkpointWorklogFile{
		{Verb: tools.NameWrite, Path: "internal/agent/a.go"},
		{Verb: tools.NameEdit, Path: "internal/agent/b.go"},
		{Verb: tools.NameDelete, Path: "docs/gone.md"},
		{Verb: string(tools.MutationUpdate), Path: "src/old.go -> src/new.go"},
		{Verb: tools.NameWrite, Path: "docs/new.md"},
		{Verb: string(tools.MutationDelete), Path: "tmp/old.txt"},
	}
	if !reflect.DeepEqual(worklog.files, wantFiles) {
		t.Fatalf("files = %#v, want %#v", worklog.files, wantFiles)
	}
	wantCommands := []string{"[success] go test ./internal/agent/", `[success] git commit -m "feat: worklog"`}
	if !reflect.DeepEqual(worklog.commands, wantCommands) {
		t.Fatalf("commands = %#v, want %#v", worklog.commands, wantCommands)
	}
	wantCommits := []string{"[main 75e8edc8] feat: worklog"}
	if !reflect.DeepEqual(worklog.commits, wantCommits) {
		t.Fatalf("commits = %#v, want %#v", worklog.commits, wantCommits)
	}
	rendered := renderCheckpointWorklog(worklog)
	for _, want := range []string{
		checkpointWorklogHeading,
		"### Commits",
		"### Files touched",
		"### Shell tool outcomes",
		"- update src/old.go -> src/new.go",
		"- [main 75e8edc8] feat: worklog",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("rendered worklog missing %q:\n%s", want, rendered)
		}
	}
	if got := strings.Count(rendered, "go test ./internal/agent/"); got != 1 {
		t.Fatalf("duplicate shell command rendered %d times:\n%s", got, rendered)
	}
}

func TestBuildCheckpointWorklogStartsAfterLatestCheckpoint(t *testing.T) {
	head := []message.Message{
		{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{
			{ID: "old-write", Name: tools.NameWrite, Args: checkpointWorklogPathArgs(t, "before.go")},
			{ID: "old-shell", Name: tools.NameShell, Args: checkpointWorklogShellArgs(t, "rm -rf old")},
		}},
		{Role: message.RoleTool, ToolCallID: "old-write", Content: "ok"},
		{Role: message.RoleUser, IsCompactionSummary: true, Content: "## Progress\n- older work"},
		{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{
			{ID: "new-write", Name: tools.NameWrite, Args: checkpointWorklogPathArgs(t, "after.go")},
		}},
		checkpointWorklogWriteResult("new-write", "after.go"),
	}
	worklog := buildCheckpointWorklog(head)
	wantFiles := []checkpointWorklogFile{{Verb: tools.NameWrite, Path: "after.go"}}
	if !reflect.DeepEqual(worklog.files, wantFiles) {
		t.Fatalf("files = %#v, want only post-checkpoint work %#v", worklog.files, wantFiles)
	}
	if len(worklog.commands) != 0 {
		t.Fatalf("commands = %#v, want none from before the checkpoint", worklog.commands)
	}
}

func TestBuildCheckpointWorklogIsDeterministic(t *testing.T) {
	head := []message.Message{
		{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{
			{ID: "c1", Name: tools.NameEdit, Args: checkpointWorklogPathArgs(t, "a.go")},
			{ID: "c2", Name: tools.NameShell, Args: checkpointWorklogShellArgs(t, "go build ./...")},
			{ID: "c3", Name: tools.NameShell, Args: checkpointWorklogShellArgs(t, "git commit -am wip")},
		}},
		{Role: message.RoleTool, ToolCallID: "c3", ToolStatus: message.ToolStatusSuccess, Content: "[main 0a1b2c3d] wip"},
	}
	first := buildCheckpointWorklog(head)
	second := buildCheckpointWorklog(head)
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("same transcript produced different worklogs:\n%#v\n%#v", first, second)
	}
	if renderCheckpointWorklog(first) != renderCheckpointWorklog(second) {
		t.Fatal("same transcript rendered different worklog text")
	}
}

func TestRenderCheckpointWorklogCapsBlocksAtTheirTails(t *testing.T) {
	var worklog checkpointWorklog
	for i := range 25 {
		worklog.files = append(worklog.files, checkpointWorklogFile{Verb: tools.NameEdit, Path: fmt.Sprintf("file-%02d.go", i)})
	}
	for i := range 15 {
		worklog.commands = append(worklog.commands, fmt.Sprintf("cmd-%02d", i))
	}
	for i := range 10 {
		worklog.commits = append(worklog.commits, fmt.Sprintf("[main 00000%02d] subject-%02d", i, i))
	}
	rendered := renderCheckpointWorklog(worklog)
	for _, want := range []string{
		"(5 earlier omitted)",
		"(3 earlier omitted)",
		"(2 earlier omitted)",
		"- edit file-24.go",
		"- cmd-14",
		"[main 0000009] subject-09",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("rendered worklog missing %q:\n%s", want, rendered)
		}
	}
	for _, dropped := range []string{"- edit file-04.go", "- cmd-02", "subject-01"} {
		if strings.Contains(rendered, dropped) {
			t.Fatalf("rendered worklog kept dropped entry %q:\n%s", dropped, rendered)
		}
	}
	// The section budget is the sum of its block caps; 400 runes cover the
	// heading, intro, subheadings and omission notes.
	perRow := compactWorklogItemChars + len("- ") + len("…")
	budget := compactWorklogMaxCommits*perRow +
		compactWorklogMaxFiles*(perRow+len("edit ")) +
		compactWorklogMaxCommands*perRow + 400
	if got := utf8.RuneCountInString(rendered); got > budget {
		t.Fatalf("rendered worklog is %d runes, over the %d-rune budget", got, budget)
	}
}

func TestRenderCheckpointWorklogFlattensAndTruncatesRows(t *testing.T) {
	long := strings.Repeat("x", compactWorklogItemChars*3)
	worklog := checkpointWorklog{commands: []string{
		"echo hi\n## Forged Heading",
		long,
	}}
	rendered := renderCheckpointWorklog(worklog)
	if strings.Contains(rendered, "\n## Forged Heading") {
		t.Fatalf("newline inside a command forged a heading:\n%s", rendered)
	}
	if !strings.Contains(rendered, "…") {
		t.Fatalf("long command was not truncated:\n%s", rendered)
	}
	for line := range strings.SplitSeq(rendered, "\n") {
		if strings.HasPrefix(line, "- ") && utf8.RuneCountInString(line) > compactWorklogItemChars+len("- ")+len("…") {
			t.Fatalf("rendered line exceeds the item cap (%d runes): %q", utf8.RuneCountInString(line), line)
		}
	}
}

func TestCheckpointWorklogUsesRecordedOutcomes(t *testing.T) {
	for _, tc := range []struct {
		name     string
		tool     string
		args     json.RawMessage
		result   message.Message
		files    []checkpointWorklogFile
		commands []string
		commits  []string
	}{
		{name: "failed edit", tool: tools.NameEdit, args: checkpointWorklogPathArgs(t, "a.go"), result: message.Message{ToolStatus: message.ToolStatusError, Content: "Error: text not found"}},
		{name: "partial deletion", tool: tools.NameDelete, args: json.RawMessage(`{"paths":["a.go","b.go"]}`), result: message.Message{ToolStatus: message.ToolStatusError, FileState: &message.ToolFileState{Deletes: []message.TrackedFileState{{Path: "a.go"}}}}, files: []checkpointWorklogFile{{Verb: tools.NameDelete, Path: "a.go"}}},
		{name: "cancelled mutation with effects", tool: tools.NameWrite, args: checkpointWorklogPathArgs(t, "requested.go"), result: message.Message{ToolStatus: message.ToolStatusCancelled, FileState: &message.ToolFileState{Writes: []message.TrackedFileState{{Path: "actual.go", Exists: true}}}}, files: []checkpointWorklogFile{{Verb: tools.NameWrite, Path: "actual.go"}}},
		{name: "not started shell", tool: tools.NameShell, args: checkpointWorklogShellArgs(t, "go test ./..."), result: message.Message{ToolStatus: message.ToolStatusCancelled, ToolRecoveryState: message.ToolRecoveryStateNotStarted}},
		{name: "unknown shell", tool: tools.NameShell, args: checkpointWorklogShellArgs(t, "git commit -m sample"), result: message.Message{ToolStatus: message.ToolStatusError, ToolRecoveryState: message.ToolRecoveryStateOutcomeUnknown, Content: "[main 1234567] sample"}, commands: []string{"[outcome_unknown] git commit -m sample"}},
		{name: "failed check", tool: tools.NameShell, args: checkpointWorklogShellArgs(t, "go test ./..."), result: message.Message{ToolStatus: message.ToolStatusError}, commands: []string{"[error] go test ./..."}},
		{name: "confirmed shell arguments", tool: tools.NameShell, args: checkpointWorklogShellArgs(t, "go test ./..."), result: message.Message{ToolStatus: message.ToolStatusSuccess, Audit: &message.ToolArgsAudit{EffectiveArgsJSON: string(checkpointWorklogShellArgs(t, "go test ./internal/tools"))}}, commands: []string{"[success] go test ./internal/tools"}},
		{name: "commit then later shell failure", tool: tools.NameShell, args: checkpointWorklogShellArgs(t, "git commit -m sample && go test ./..."), result: message.Message{ToolStatus: message.ToolStatusError, Content: "[main 1234567] sample\nError: check failed"}, commands: []string{"[error] git commit -m sample && go test ./..."}, commits: []string{"[main 1234567] sample"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.result.Role, tc.result.ToolCallID = message.RoleTool, "call"
			head := []message.Message{{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{{ID: "call", Name: tc.tool, Args: tc.args}}}, tc.result}
			got := buildCheckpointWorklog(head)
			if !reflect.DeepEqual(got.files, tc.files) || !reflect.DeepEqual(got.commands, tc.commands) || !reflect.DeepEqual(got.commits, tc.commits) {
				t.Fatalf("worklog = %#v; want files=%#v commands=%#v commits=%#v", got, tc.files, tc.commands, tc.commits)
			}
		})
	}
}

func TestCheckpointWorklogBudgetPreservesRecoverySections(t *testing.T) {
	var calls []message.ToolCall
	var results []message.Message
	for i := range 40 {
		id := fmt.Sprintf("call-%d", i)
		path := fmt.Sprintf("file-%02d-", i) + strings.Repeat("x", 300)
		calls = append(calls, message.ToolCall{ID: id, Name: tools.NameWrite, Args: checkpointWorklogPathArgs(t, path)})
		results = append(results, checkpointWorklogWriteResult(id, path))
	}
	calls = append(calls, message.ToolCall{ID: "commit", Name: tools.NameShell, Args: checkpointWorklogShellArgs(t, "git commit -m sample")})
	results = append(results, message.Message{Role: message.RoleTool, ToolCallID: "commit", ToolStatus: message.ToolStatusSuccess, Content: "[main 1234567] " + strings.Repeat("x", 30000)})
	head := append([]message.Message{{Role: message.RoleAssistant, ToolCalls: calls}}, results...)
	worklog := buildCheckpointWorklog(head)
	rendered := renderCheckpointWorklog(worklog)
	for row := range strings.SplitSeq(rendered, "\n") {
		if strings.HasPrefix(row, "- ") && utf8.RuneCountInString(row) > compactWorklogItemChars+4 {
			t.Fatalf("unbounded row has %d runes", utf8.RuneCountInString(row))
		}
	}
	// One rune per token also exercises a more expensive calibration than
	// the usual byte estimator, independent of the model-authored budget.
	section := boundedCheckpointWorklog(worklog, compactWorklogMaxTokens, utf8.RuneCountInString)
	if got := utf8.RuneCountInString(section); got > compactWorklogMaxTokens {
		t.Fatalf("worklog tokens = %d, over %d", got, compactWorklogMaxTokens)
	}
	if !strings.Contains(section, "file-39-") || !strings.Contains(section, "Earlier worklog entries omitted") {
		t.Fatalf("bounded worklog lost newest mutation or omission notice: %s", section)
	}
	core := "## Current User Request\n- request\n\n## Runtime Recovery State\n- outcome unknown\n\n## Todo State\n- pending\n\n## Progress\n- verified"
	repository := checkpointRepositoryStateHeading + "\n" + strings.Repeat("r", 1200)
	summary := applyCheckpointMachineState(core, head, repository, utf8.RuneCountInString)
	for _, want := range []string{"- request", "- outcome unknown", "- pending", "- verified"} {
		if !strings.Contains(summary, want) {
			t.Fatalf("machine sections displaced recovery core %q", want)
		}
	}
	if added := utf8.RuneCountInString(summary) - utf8.RuneCountInString(core); added > compactCheckpointMachineMaxTokens+4 {
		t.Fatalf("machine sections added %d tokens, over %d", added, compactCheckpointMachineMaxTokens)
	}
}

func TestCheckpointWorklogDeduplicatesAtLatestOccurrence(t *testing.T) {
	head := []message.Message{{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{
		{ID: "a1", Name: tools.NameEdit}, {ID: "b", Name: tools.NameEdit}, {ID: "a2", Name: tools.NameEdit},
	}}, checkpointWorklogWriteResult("a1", "a.go"), checkpointWorklogWriteResult("b", "b.go"), checkpointWorklogWriteResult("a2", "a.go")}
	got := buildCheckpointWorklog(head)
	want := []checkpointWorklogFile{{Verb: tools.NameEdit, Path: "b.go"}, {Verb: tools.NameEdit, Path: "a.go"}}
	if !reflect.DeepEqual(got.files, want) {
		t.Fatalf("files = %#v; want most recent distinct mutations %#v", got.files, want)
	}
}

func TestPriorCheckpointCarryStripsMachineSnapshots(t *testing.T) {
	body := "## Progress\n- semantic progress\n\n" + checkpointWorklogHeading + "\n- write prior.go\n\n" + checkpointRepositoryStateHeading + "\n- HEAD: earlier\n\n## Key Decisions\n- decision"
	content := buildCompactionCheckpointMessage(body, nil, compactionSummaryModeModelDriven, nil, "")
	got := latestPriorCheckpointBody([]message.Message{{Role: message.RoleUser, Content: content, IsCompactionSummary: true}})
	if strings.Contains(got, checkpointWorklogHeading) || strings.Contains(got, checkpointRepositoryStateHeading) || strings.Contains(got, "earlier") {
		t.Fatalf("prior machine snapshots carried again: %s", got)
	}
	for _, want := range []string{"semantic progress", "decision"} {
		if !strings.Contains(got, want) {
			t.Fatalf("stripping machine snapshots lost %q: %s", want, got)
		}
	}
}

func TestEnsureCheckpointMachineSectionPlacement(t *testing.T) {
	summary := "## Current User Request\n- do x\n\n## Progress\n- built\n\n## Key Decisions\n- decided"
	withWorklog := ensureCheckpointWorklogSection(summary, "## Checkpoint Worklog\n- write a.go")
	progress := strings.Index(withWorklog, checkpointProgressHeading)
	worklog := strings.Index(withWorklog, checkpointWorklogHeading)
	decisions := strings.Index(withWorklog, "## Key Decisions")
	if progress < 0 || worklog < 0 || decisions < 0 || !(progress < worklog && worklog < decisions) {
		t.Fatalf("worklog not placed between Progress and Key Decisions:\n%s", withWorklog)
	}
	withBoth := ensureCheckpointRepositoryStateSection(withWorklog, "## Repository State\n- branch: main")
	repo := strings.Index(withBoth, checkpointRepositoryStateHeading)
	if !(worklog < repo && repo < strings.Index(withBoth, "## Key Decisions")) {
		t.Fatalf("repository state not placed after the worklog:\n%s", withBoth)
	}

	replaced := ensureCheckpointWorklogSection(withBoth, "## Checkpoint Worklog\n- write b.go")
	if got := strings.Count(replaced, checkpointWorklogHeading); got != 1 {
		t.Fatalf("re-ensured worklog rendered %d sections:\n%s", got, replaced)
	}
	if strings.Contains(replaced, "write a.go") || !strings.Contains(replaced, "write b.go") {
		t.Fatalf("stale worklog body survived the replace:\n%s", replaced)
	}

	appended := ensureCheckpointMachineSection("## Current User Request\n- x", checkpointWorklogHeading, "## Checkpoint Worklog\n- z", checkpointProgressHeading)
	if !strings.HasSuffix(appended, "## Checkpoint Worklog\n- z") {
		t.Fatalf("missing anchor must append the section:\n%s", appended)
	}

	removed := ensureCheckpointWorklogSection(withBoth, "")
	if strings.Contains(removed, checkpointWorklogHeading) || strings.Contains(removed, "write a.go") {
		t.Fatalf("empty section must remove the machine section:\n%s", removed)
	}
	if !strings.Contains(removed, checkpointRepositoryStateHeading) || !strings.Contains(removed, "## Key Decisions") {
		t.Fatalf("removal disturbed neighboring sections:\n%s", removed)
	}
	if got := ensureCheckpointWorklogSection(removed, ""); got != removed {
		t.Fatalf("empty section with no existing section changed the summary:\n%s", got)
	}
}

func TestApplyCheckpointMachineStateAddsBothSections(t *testing.T) {
	if !worktree.GitAvailable() {
		t.Skip("git not available")
	}
	repo := newWorktreeTestRepo(t)
	head := []message.Message{
		{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{
			{ID: "c1", Name: tools.NameEdit, Args: checkpointWorklogPathArgs(t, "internal/agent/a.go")},
		}},
		checkpointWorklogWriteResult("c1", "internal/agent/a.go"),
	}
	summary := applyCheckpointMachineState("## Current User Request\n- do x\n\n## Progress\n- built", head, buildCheckpointRepositoryState(t.Context(), repo), checkpointWorklogEstimateTokens)
	if !strings.Contains(summary, "- edit internal/agent/a.go") {
		t.Fatalf("worklog missing from the composed summary:\n%s", summary)
	}
	if !strings.Contains(summary, "- branch: main") {
		t.Fatalf("repository state missing from the composed summary:\n%s", summary)
	}
	if !(strings.Index(summary, checkpointProgressHeading) < strings.Index(summary, checkpointWorklogHeading)) {
		t.Fatalf("worklog must follow Progress:\n%s", summary)
	}
	if !(strings.Index(summary, checkpointWorklogHeading) < strings.Index(summary, checkpointRepositoryStateHeading)) {
		t.Fatalf("repository state must follow the worklog:\n%s", summary)
	}
}

func TestBuildCheckpointRepositoryStateReportsBuildTimeStatus(t *testing.T) {
	if !worktree.GitAvailable() {
		t.Skip("git not available")
	}
	repo := newWorktreeTestRepo(t)
	state := buildCheckpointRepositoryState(t.Context(), repo)
	if !strings.Contains(state, checkpointRepositoryStateHeading) {
		t.Fatalf("repository state missing its heading:\n%s", state)
	}
	head := agentTestGitOutput(t, repo, "rev-parse", "HEAD")
	if !strings.Contains(state, "- branch: main") {
		t.Fatalf("repository state missing the branch:\n%s", state)
	}
	if !strings.Contains(state, "- HEAD: "+head[:12]) {
		t.Fatalf("repository state missing the abbreviated HEAD %q:\n%s", head[:12], state)
	}
	if !strings.Contains(state, "- working tree: clean") {
		t.Fatalf("freshly cloned repo must report a clean tree:\n%s", state)
	}

	if err := os.WriteFile(filepath.Join(repo, "wip.txt"), []byte("wip\n"), 0o644); err != nil {
		t.Fatalf("seed dirty file: %v", err)
	}
	if state = buildCheckpointRepositoryState(t.Context(), repo); !strings.Contains(state, "- working tree: uncommitted changes") {
		t.Fatalf("dirty tree not reported:\n%s", state)
	}

	runAgentTestGit(t, repo, "checkout", "-q", "--detach")
	if state = buildCheckpointRepositoryState(t.Context(), repo); !strings.Contains(state, "- branch: (detached HEAD)") {
		t.Fatalf("detached HEAD not reported:\n%s", state)
	}

	if got := buildCheckpointRepositoryState(t.Context(), t.TempDir()); got != "" {
		t.Fatalf("non-repository directory reported state:\n%s", got)
	}
	if got := buildCheckpointRepositoryState(t.Context(), ""); got != "" {
		t.Fatalf("empty workdir reported state:\n%s", got)
	}
}

func TestModelDrivenCheckpointBuilderIncludesMachineSections(t *testing.T) {
	if !worktree.GitAvailable() {
		t.Skip("git not available")
	}
	projectRoot := newWorktreeTestRepo(t)
	a := newTestMainAgent(t, projectRoot)
	a.newTurn()
	a.ctxMgr.Append(message.Message{Role: message.RoleUser, Content: "keep working"})
	a.ctxMgr.Append(message.Message{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{
		{ID: "c1", Name: tools.NameEdit, Args: checkpointWorklogPathArgs(t, "internal/agent/a.go")},
		{ID: "c2", Name: tools.NameShell, Args: checkpointWorklogShellArgs(t, "go test ./internal/agent/")},
	}})
	a.ctxMgr.Append(checkpointWorklogWriteResult("c1", "internal/agent/a.go"))
	a.ctxMgr.Append(message.Message{Role: message.RoleTool, ToolCallID: "c2", ToolStatus: message.ToolStatusSuccess, Content: "ok"})
	snapshot := a.ctxMgr.Snapshot()
	bundle := modelDrivenBarrierSnapshot{
		snapshot:              snapshot,
		maxTokens:             a.ctxMgr.GetMaxTokens(),
		prepareReducedRequest: a.compactionReductionScratch().prepareMessagesForLLM,
		workDir:               projectRoot,
	}
	req := &modelDrivenCheckpointRequest{ToolCallID: "cc-1", Args: tools.CompactContextArgs{ActiveObjective: "objective", NextStep: "next"}}
	builder := a.newModelDrivenCheckpointBuilder(t.Context(), bundle, snapshot, len(snapshot), req)
	for _, want := range []string{
		checkpointWorklogHeading,
		"- edit internal/agent/a.go",
		"- [success] go test ./internal/agent/",
		checkpointRepositoryStateHeading,
		"- branch: main",
	} {
		if !strings.Contains(builder.summaryText, want) {
			t.Fatalf("builder summary missing %q:\n%s", want, builder.summaryText)
		}
	}
	content, _ := builder.render("")
	for _, want := range []string{checkpointWorklogHeading, "- edit internal/agent/a.go", checkpointRepositoryStateHeading} {
		if !strings.Contains(content, want) {
			t.Fatalf("rendered checkpoint missing %q:\n%s", want, content)
		}
	}
}
