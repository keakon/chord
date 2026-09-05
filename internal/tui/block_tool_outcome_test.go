package tui

import (
	"strings"
	"testing"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/tools"
)

// outcomeCardFixture builds a finished card for one tool in one terminal
// state, so the shared-envelope assertions below can sweep the whole card set
// instead of testing one renderer at a time.
func outcomeCardFixture(name, args, result string, status agent.ToolResultStatus) *Block {
	return &Block{
		ID:            1,
		Type:          BlockToolCall,
		ToolName:      name,
		Content:       args,
		RawArgs:       args,
		ResultDone:    true,
		ResultStatus:  status,
		ResultContent: result,
	}
}

var outcomeCardArgs = map[string]string{
	tools.NameShell:          `{"command":"git commit --amend","description":"Recommit"}`,
	tools.NameRead:           `{"path":"internal/tui/a.go"}`,
	tools.NameWrite:          `{"path":"internal/tui/a.go","content":"package tui\n"}`,
	tools.NameEdit:           `{"path":"internal/tui/a.go","old_string":"foo","new_string":"bar"}`,
	tools.NameGrep:           `{"pattern":"foo","path":"internal"}`,
	tools.NameWebFetch:       `{"url":"https://example.com"}`,
	tools.NameSkill:          `{"skill":"code-review"}`,
	tools.NameSpawn:          `{"command":"npm run dev"}`,
	tools.NameTodoWrite:      `{"todos":[{"content":"a","status":"pending"}]}`,
	tools.NameQuestion:       `{"question":"Which?","options":[{"label":"main"}]}`,
	tools.NameComplete:       `{"summary":"Landed it."}`,
	tools.NameEscalate:       `{"reason":"Need approval."}`,
	tools.NameDelegate:       `{"agent_type":"reviewer","description":"review"}`,
	tools.NameCancel:         `{"reason":"user interrupted"}`,
	tools.NameNotify:         `{"message":"build finished"}`,
	tools.NameHandoff:        `{"plan_path":".chord/plans/20260905-x.md","summary":"hand off"}`,
	tools.NameCompactContext: `{"active_objective":"O","completed":["a"],"next_step":"N"}`,
}

// TestToolCardsNeverRepeatTheErrorLabel pins the shared envelope: the tool
// result arrives wrapped as "Error: <message>", so a card that prints its own
// "↳ Error:" header must strip the prefix instead of showing "Error: Error:".
func TestToolCardsNeverRepeatTheErrorLabel(t *testing.T) {
	ApplyTheme(DefaultTheme())
	for name, args := range outcomeCardArgs {
		block := outcomeCardFixture(name, args, "Error: the tool refused the call", agent.ToolResultStatusError)
		plain := stripANSI(strings.Join(block.Render(120, ""), "\n"))
		if strings.Contains(plain, "Error: Error:") {
			t.Errorf("%s card repeats the error label:\n%s", name, plain)
		}
		if !strings.Contains(plain, "the tool refused the call") {
			t.Errorf("%s card drops the error message:\n%s", name, plain)
		}
	}
}

// TestToolCardsReportCancellationOnce pins the other half: "Cancelled" is a
// label, not a body, so no card may print it twice (the generic card printed
// a summary row and an envelope row, and the prose-control and
// compact_context cards printed "↳ Cancelled:" followed by "Cancelled").
func TestToolCardsReportCancellationOnce(t *testing.T) {
	ApplyTheme(DefaultTheme())
	for name, args := range outcomeCardArgs {
		for _, content := range []string{"Cancelled", "cancelled"} {
			block := outcomeCardFixture(name, args, content, agent.ToolResultStatusCancelled)
			plain := stripANSI(strings.Join(block.Render(120, ""), "\n"))
			if got := strings.Count(strings.ToLower(plain), "cancelled"); got != 1 {
				t.Errorf("%s card mentions the cancellation %d times (result %q):\n%s", name, got, content, plain)
			}
		}
	}
}

// TestCollapsedToolCardsKeepTheOutcomeOnOneLine pins the collapsed shape: a
// failure must read at a glance without opening the card, so the collapsed
// envelope is a single "↳ Error: <summary>" row rather than a header plus an
// indented body.
func TestCollapsedToolCardsKeepTheOutcomeOnOneLine(t *testing.T) {
	ApplyTheme(DefaultTheme())
	for _, name := range []string{tools.NameShell, tools.NameWebFetch, tools.NameSkill, tools.NameSpawn, tools.NameCompactContext} {
		block := outcomeCardFixture(name, outcomeCardArgs[name], "Error: exit code 1", agent.ToolResultStatusError)
		plain := stripANSI(strings.Join(block.Render(120, ""), "\n"))
		if !strings.Contains(plain, "↳ Error: exit code 1") {
			t.Errorf("%s collapsed card does not fold the failure onto one row:\n%s", name, plain)
		}
	}
}

// TestShellCardKeepsExitCodeAndLabelsOutputHonestly pins the two shell
// regressions: the expanded card reported "Exit: error" while the collapsed
// card knew the exit code, and every failing run's output was labelled
// "Stderr:" even though the runtime hands the card one merged stream.
func TestShellCardKeepsExitCodeAndLabelsOutputHonestly(t *testing.T) {
	ApplyTheme(DefaultTheme())
	block := outcomeCardFixture(tools.NameShell, outcomeCardArgs[tools.NameShell],
		"--- FAIL: TestFoo\n\nError: exit code 1", agent.ToolResultStatusError)
	block.ToolCallDetailExpanded = true

	plain := stripANSI(strings.Join(block.Render(120, ""), "\n"))

	if !strings.Contains(plain, "↳ Exit: 1") {
		t.Fatalf("expected the expanded shell card to keep the exit code, got:\n%s", plain)
	}
	if strings.Contains(plain, "Stderr:") || strings.Contains(plain, "Stdout:") {
		t.Fatalf("expected the merged stream to be labelled Output, got:\n%s", plain)
	}
	if !strings.Contains(plain, "↳ Output:") || !strings.Contains(plain, "--- FAIL: TestFoo") {
		t.Fatalf("expected the failing output under ↳ Output:, got:\n%s", plain)
	}
	// Section headers carry the ↳ marker like every other card body.
	if !strings.Contains(plain, "↳ Command:") {
		t.Fatalf("expected the command section header to carry ↳, got:\n%s", plain)
	}
}

// TestShellCancelledCardReportsTheCancellation pins that the expanded shell
// card still reports a cancelled run: its result body is dropped by the
// stream split, so only the shared envelope can carry the state.
func TestShellCancelledCardReportsTheCancellation(t *testing.T) {
	ApplyTheme(DefaultTheme())
	block := outcomeCardFixture(tools.NameShell, outcomeCardArgs[tools.NameShell], "Cancelled", agent.ToolResultStatusCancelled)
	block.ToolCallDetailExpanded = true

	plain := stripANSI(strings.Join(block.Render(120, ""), "\n"))

	if !strings.Contains(plain, "↳ Cancelled") {
		t.Fatalf("expected the expanded shell card to report the cancellation, got:\n%s", plain)
	}
}
