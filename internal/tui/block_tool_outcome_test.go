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
	for _, name := range []string{tools.NameShell, tools.NameWebFetch, tools.NameSkill, tools.NameSpawn} {
		block := outcomeCardFixture(name, outcomeCardArgs[name], "Error: exit code 1", agent.ToolResultStatusError)
		plain := stripANSI(strings.Join(block.Render(120, ""), "\n"))
		if !strings.Contains(plain, "↳ Error: exit code 1") {
			t.Errorf("%s collapsed card does not fold the failure onto one row:\n%s", name, plain)
		}
	}
}

// TestCollapsedReadErrorWithSuggestionsShowsFullBody pins the multi-line outcome
// rule: a "file not found" error that carries a "Did you mean:" suggestion list
// must render its whole body even when the card is collapsed. The old one-row
// summary joined "file not found:" with "Did you mean:" and truncated the rest,
// hiding the very path the user needed — so collapses are no longer a guessing
// game. Shell-style single-line errors still fold to one row (see
// TestCollapsedToolCardsKeepTheOutcomeOnOneLine).
func TestCollapsedReadErrorWithSuggestionsShowsFullBody(t *testing.T) {
	ApplyTheme(DefaultTheme())
	block := outcomeCardFixture(tools.NameRead, outcomeCardArgs[tools.NameRead],
		"file not found: internal/tools/ignore.go\nDid you mean:\n- internal/tools/ignore.go",
		agent.ToolResultStatusError)
	block.Collapsed = true

	plain := stripANSI(strings.Join(block.Render(120, ""), "\n"))
	for _, want := range []string{
		"↳ Error:",
		"file not found: internal/tools/ignore.go",
		"Did you mean:",
		"- internal/tools/ignore.go",
	} {
		if !strings.Contains(plain, want) {
			t.Fatalf("expected collapsed read error to contain %q, got:\n%s", want, plain)
		}
	}
	// The prompt header must not be collapsed into the cause on one row; the
	// suggestion list stays on its own lines so the suggested path is visible.
	if strings.Contains(plain, "· Did you mean:") {
		t.Fatalf("expected the collapsed read error to keep the body on separate lines, got:\n%s", plain)
	}
}

// TestCollapsedToggleableCardShowsDisclosureMarker pins the marker rule: the
// marker states what the toggle can do, so a collapsed card that opens into a
// fuller body carries ▸ even when the collapsed body already fits.
func TestCollapsedToggleableCardShowsDisclosureMarker(t *testing.T) {
	ApplyTheme(DefaultTheme())
	for _, name := range []string{tools.NameWebFetch, tools.NameSkill, tools.NameSpawn, tools.NameShell} {
		block := outcomeCardFixture(name, outcomeCardArgs[name], "Error: 404 Not Found", agent.ToolResultStatusError)
		collapsed := stripANSI(strings.Join(block.Render(120, ""), "\n"))
		if !strings.Contains(collapsed, "✗ ▸ "+name) {
			t.Errorf("%s collapsed card is missing the ▸ disclosure marker:\n%s", name, collapsed)
		}
		block.ToolCallDetailExpanded = true
		block.InvalidateCache()
		expanded := stripANSI(strings.Join(block.Render(120, ""), "\n"))
		if !strings.Contains(expanded, "✗ ▾ "+name) {
			t.Errorf("%s expanded card is missing the ▾ disclosure marker:\n%s", name, expanded)
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

// TestCardSubjectsRideTheHeader pins the header/body split for the cards that
// still index by an argument: the argument that says what a call is about
// belongs on the header line, and the body must not repeat it. Path-shaped
// subjects go up whole, with their qualifier in the option group beside them.
func TestCardSubjectsRideTheHeader(t *testing.T) {
	ApplyTheme(DefaultTheme())
	cases := []struct {
		name       string
		tool       string
		args       string
		result     string
		wantHeader string
		// once must not appear a second time anywhere in the card.
		once string
	}{
		{
			name:       "delete paths",
			tool:       tools.NameDelete,
			args:       `{"paths":["tmp/a.go","tmp/b.go"],"reason":"clean up"}`,
			result:     "delete completed.\n\nDeleted (2):\n- tmp/a.go\n- tmp/b.go",
			wantHeader: "delete 2 files (clean up)",
			once:       "clean up",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			block := outcomeCardFixture(c.tool, c.args, c.result, "")
			// Collapsed: an expanded card legitimately repeats a short prose
			// subject under its own section, since the header only ever
			// carries the first sentence of it.
			block.Collapsed = true
			plain := stripANSI(strings.Join(block.Render(120, ""), "\n"))
			if !strings.Contains(plain, c.wantHeader) {
				t.Fatalf("expected header %q, got:\n%s", c.wantHeader, plain)
			}
			if c.once != "" && strings.Count(plain, c.once) != 1 {
				t.Fatalf("expected %q only on the header, got:\n%s", c.once, plain)
			}
		})
	}
}

// TestAlwaysExpandedCardsKeepProseOffTheHeader pins the other half of the
// split: report-style cards (complete / delegate / notify / compact_context)
// render the whole prose in their body, so their header is the bare tool name
// and the subject must not appear twice.
func TestAlwaysExpandedCardsKeepProseOffTheHeader(t *testing.T) {
	ApplyTheme(DefaultTheme())
	cases := []struct {
		name    string
		tool    string
		args    string
		subject string
	}{
		{
			name:    "complete summary",
			tool:    tools.NameComplete,
			args:    `{"summary":"Unified the card surfaces. Follow-up work remains."}`,
			subject: "Unified the card surfaces.",
		},
		{
			name:    "delegate description",
			tool:    tools.NameDelegate,
			args:    `{"agent_type":"reviewer","description":"Audit the tool card styles"}`,
			subject: "Audit the tool card styles",
		},
		{
			name:    "notify message",
			tool:    tools.NameNotify,
			args:    `{"message":"build finished","kind":"info"}`,
			subject: "build finished",
		},
		{
			name:    "compact_context objective",
			tool:    tools.NameCompactContext,
			args:    `{"active_objective":"Audit the card styles","next_step":"Commit and report"}`,
			subject: "Audit the card styles",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			block := outcomeCardFixture(c.tool, c.args, "accepted", "")
			for _, collapsed := range []bool{true, false} {
				block.Collapsed = collapsed
				block.ToolCallDetailExpanded = !collapsed
				block.InvalidateCache()
				plain := stripANSI(strings.Join(block.Render(120, ""), "\n"))
				if strings.Contains(plain, c.tool+" "+c.subject) {
					t.Fatalf("expected %q off the header (collapsed=%v), got:\n%s", c.subject, collapsed, plain)
				}
				if !strings.Contains(plain, c.subject) {
					t.Fatalf("expected %q in the body (collapsed=%v), got:\n%s", c.subject, collapsed, plain)
				}
				if strings.Contains(plain, "▸") || strings.Contains(plain, "▾") {
					t.Fatalf("expected no disclosure glyph (collapsed=%v), got:\n%s", collapsed, plain)
				}
			}
		})
	}
}

// TestShellCardKeepsArgumentsOffTheBody pins the same rule for the shell card,
// whose expanded body used to repeat the description and the timeout that its
// header already carries.
func TestShellCardKeepsArgumentsOffTheBody(t *testing.T) {
	ApplyTheme(DefaultTheme())
	block := outcomeCardFixture(tools.NameShell,
		`{"command":"go test ./internal/tui/","description":"Run the TUI suite","timeout":"120","workdir":"/tmp/project"}`,
		"ok", "")
	block.ToolCallDetailExpanded = true

	plain := stripANSI(strings.Join(block.Render(120, ""), "\n"))

	if !strings.Contains(plain, "shell Run the TUI suite (timeout=120)") {
		t.Fatalf("expected the description and timeout on the header, got:\n%s", plain)
	}
	if strings.Count(plain, "Run the TUI suite") != 1 {
		t.Fatalf("expected the description only on the header, got:\n%s", plain)
	}
	if strings.Contains(plain, "timeout: 120s") {
		t.Fatalf("expected the timeout only on the header, got:\n%s", plain)
	}
	// The working directory qualifies the command block it sits under.
	if !strings.Contains(plain, "workdir: /tmp/project") {
		t.Fatalf("expected the workdir under the command block, got:\n%s", plain)
	}
}

// TestGenericToolCardUsesTheSharedShape pins the rendering half of the generic
// rule: a tool with no dedicated header entry (here an MCP tool) puts its
// subject and short options on the header, keeps long or structured arguments
// in labelled body sections, and labels its result so it does not read as a
// continuation of the last section.
func TestGenericToolCardUsesTheSharedShape(t *testing.T) {
	ApplyTheme(DefaultTheme())
	args := `{"repo":"keakon/chord","title":"unify the cards","body":"first line\nsecond line","labels":["ui","tui"]}`
	block := outcomeCardFixture("mcp__github__create_issue", args, "issue #42 created", "")
	block.ToolCallDetailExpanded = true

	plain := stripANSI(strings.Join(block.Render(96, ""), "\n"))

	if !strings.Contains(plain, "mcp__github__create_issue keakon/chord (title=unify the cards)") {
		t.Fatalf("expected subject and options on the header, got:\n%s", plain)
	}
	for _, want := range []string{"↳ Body:", "second line", "↳ Labels:", "• ui", "• tui", "↳ Result:"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("expected %q in the body, got:\n%s", want, plain)
		}
	}
	// The header line never carries a raw newline from a multi-line argument.
	if strings.Contains(strings.SplitN(plain, "\n", 5)[3], "first line") {
		t.Fatalf("expected the multi-line argument to stay off the header, got:\n%s", plain)
	}

	block.ToolCallDetailExpanded = false
	block.InvalidateCache()
	collapsed := stripANSI(strings.Join(block.Render(96, ""), "\n"))
	if strings.Contains(collapsed, "↳ Body:") {
		t.Fatalf("expected a collapsed generic card to hide its argument sections, got:\n%s", collapsed)
	}
}

// TestQuestionCardCarriesItsSectionsUnderABareHeader pins the question card on
// the shared shape: the header is the bare tool name because every question is
// rendered in full below it, each question block opens with a "↳ Header:"
// section rather than a "▸" separator (which now means "this card toggles"),
// and the selection stays visible after the answer arrives.
func TestQuestionCardCarriesItsSectionsUnderABareHeader(t *testing.T) {
	ApplyTheme(DefaultTheme())
	args := `{"questions":[{"question":"Which branch?","header":"Target branch","options":[{"label":"main","description":"trunk"},{"label":"develop","description":"integration"}]}]}`
	block := &Block{
		ID:            1,
		Type:          BlockToolCall,
		ToolName:      tools.NameQuestion,
		Content:       args,
		RawArgs:       args,
		ResultDone:    true,
		ResultPayload: `[{"header":"Target branch","selected":["main"]}]`,
		ResultContent: `[{"header":"Target branch","selected":["main"]}]`,
	}

	plain := stripANSI(strings.Join(block.Render(96, ""), "\n"))

	if !strings.Contains(plain, "✓ question") {
		t.Fatalf("expected the bare tool-name header, got:\n%s", plain)
	}
	if strings.Contains(plain, "question Which branch?") {
		t.Fatalf("expected the question off the header, got:\n%s", plain)
	}
	if !strings.Contains(plain, "Which branch?") {
		t.Fatalf("expected the question in the body, got:\n%s", plain)
	}
	if !strings.Contains(plain, "↳ Target branch:") {
		t.Fatalf("expected the question header as a section, got:\n%s", plain)
	}
	if strings.Contains(plain, "▸") {
		t.Fatalf("expected no disclosure glyph on a non-toggleable card, got:\n%s", plain)
	}
	for _, want := range []string{"✓ 1. main — trunk", "2. develop — integration"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("expected the answered options to stay visible (%q), got:\n%s", want, plain)
		}
	}
}
