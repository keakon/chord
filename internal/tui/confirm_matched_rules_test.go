package tui

import (
	"strconv"
	"strings"
	"testing"

	tea "github.com/keakon/bubbletea/v2"
	"github.com/keakon/lipgloss/v2"

	"github.com/keakon/chord/internal/permission"
	"github.com/keakon/chord/internal/tools"
	"github.com/keakon/chord/internal/tui/terminaltext"
)

func matchedRulesConfirmModel(t *testing.T, width, height int, req *ConfirmRequest) *Model {
	t.Helper()
	model := NewModelWithSize(nil, width, height)
	model.confirmResultCh = make(chan ConfirmResult, 1)
	model.confirm.request = req
	return &model
}

func confirmMatchedRulesPlain(m *Model) string {
	return terminaltext.StripANSI(m.renderConfirmDialog())
}

// The section is a read-only rendering of the facts on the request: one ask
// rule keeps its permission, quoted pattern and action, and the decision keys
// stay outside the scrollable body.
func TestRenderConfirmMatchedRulesShowsRuleFactAndKeepsActions(t *testing.T) {
	m := matchedRulesConfirmModel(t, 100, 30, &ConfirmRequest{
		ToolName: "shell",
		ArgsJSON: `{"command":"rm -rf sample-dir"}`,
		NeedsApprovalRules: []permission.Rule{
			{Permission: "shell", Pattern: "rm *", Action: permission.ActionAsk},
		},
	})

	plain := confirmMatchedRulesPlain(m)
	for _, want := range []string{
		"Rules requiring approval (1)",
		`- shell: "rm *" → ask`,
		"[Enter/A] Allow",
		"[Esc/D] Deny",
	} {
		if !strings.Contains(plain, want) {
			t.Fatalf("missing %q in confirm dialog:\n%s", want, plain)
		}
	}
	if strings.Count(plain, "- ") != 1 {
		t.Fatalf("wrapped rule rows must not look like extra rules:\n%s", plain)
	}

	_ = m.handleConfirmKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	select {
	case result := <-m.confirmResultCh:
		if result.Action != ConfirmAllow || result.RuleIntent != nil {
			t.Fatalf("allow result = %#v, want plain allow", result)
		}
	default:
		t.Fatal("allow key no longer resolves the confirmation")
	}
}

// A compound command carries ask rules for the subcommands that need review;
// allow facts from the same decision must not be mixed into the section.
func TestRenderConfirmMatchedRulesShowsOnlyAskFacts(t *testing.T) {
	m := matchedRulesConfirmModel(t, 100, 30, &ConfirmRequest{
		ToolName: "shell",
		ArgsJSON: `{"command":"git status && git add sample.txt && git commit -m sample"}`,
		NeedsApprovalRules: []permission.Rule{
			{Permission: "shell", Pattern: "git add *", Action: permission.ActionAsk},
			{Permission: "shell", Pattern: "git commit *", Action: permission.ActionAsk},
		},
		AlreadyAllowedRules: []permission.Rule{
			{Permission: "shell", Pattern: "git status *", Action: permission.ActionAllow},
		},
	})

	plain := confirmMatchedRulesPlain(m)
	for _, want := range []string{
		"Rules requiring approval (2)",
		`- shell: "git add *" → ask`,
		`- shell: "git commit *" → ask`,
	} {
		if !strings.Contains(plain, want) {
			t.Fatalf("missing %q in confirm dialog:\n%s", want, plain)
		}
	}
	if strings.Contains(plain, "git status *") || strings.Contains(plain, "→ allow") {
		t.Fatalf("allow facts leaked into the ask section:\n%s", plain)
	}
}

// Upstream deduplicates by the full Rule. The TUI keeps that order without
// sorting or merging: same pattern under different permissions and a wildcard
// permission stay as separate rows.
func TestRenderConfirmMatchedRulesKeepsOrderAndDistinctPermissions(t *testing.T) {
	m := matchedRulesConfirmModel(t, 100, 30, &ConfirmRequest{
		ToolName: "edit",
		ArgsJSON: `{"path":"sample.txt"}`,
		NeedsApprovalRules: []permission.Rule{
			{Permission: "delete", Pattern: "sample*", Action: permission.ActionAsk},
			{Permission: "write", Pattern: "sample*", Action: permission.ActionAsk},
			{Permission: "*", Pattern: "sample*", Action: permission.ActionAsk},
		},
	})

	plain := confirmMatchedRulesPlain(m)
	wants := []string{
		"Rules requiring approval (3)",
		`- delete: "sample*" → ask`,
		`- write: "sample*" → ask`,
		`- *: "sample*" → ask`,
	}
	previous := -1
	for _, want := range wants {
		at := strings.Index(plain, want)
		if at < 0 {
			t.Fatalf("missing %q in confirm dialog:\n%s", want, plain)
		}
		if at <= previous {
			t.Fatalf("rule %q out of upstream order:\n%s", want, plain)
		}
		previous = at
	}
}

// A patch blocked by a delete rule shows the delete fact, not a fabricated
// apply_patch attribution.
func TestRenderConfirmMatchedRulesShowsDeleteFactForPatch(t *testing.T) {
	m := matchedRulesConfirmModel(t, 100, 30, &ConfirmRequest{
		ToolName: tools.NameApplyPatch,
		ArgsJSON: `{"patch":"*** Begin Patch\n*** Delete File: sample.txt\n*** End Patch"}`,
		NeedsApprovalRules: []permission.Rule{
			{Permission: tools.NameDelete, Pattern: "sample.txt", Action: permission.ActionAsk},
		},
	})

	plain := confirmMatchedRulesPlain(m)
	if !strings.Contains(plain, `- delete: "sample.txt" → ask`) {
		t.Fatalf("missing delete rule fact in confirm dialog:\n%s", plain)
	}
	if strings.Contains(plain, "- apply_patch:") {
		t.Fatalf("patch confirmation fabricated an apply_patch rule:\n%s", plain)
	}
}

// Requests without rule facts, and Done (never a permission confirmation),
// keep the original body. An empty list is not a claim that no rule matched.
func TestRenderConfirmMatchedRulesOmitsSectionWithoutFacts(t *testing.T) {
	cases := []struct {
		name string
		req  *ConfirmRequest
	}{
		{
			name: "ordinary tool without rule facts",
			req:  &ConfirmRequest{ToolName: "read", ArgsJSON: `{"path":"sample.txt"}`},
		},
		{
			name: "done carrying stale rule fields",
			req: &ConfirmRequest{
				ToolName:   tools.NameDone,
				ArgsJSON:   `{"report":"All requested work is finished"}`,
				DoneReport: "All requested work is finished",
				NeedsApprovalRules: []permission.Rule{
					{Permission: "shell", Pattern: "rm *", Action: permission.ActionAsk},
				},
			},
		},
		{
			name: "deny with reason confirmation",
			req: &ConfirmRequest{
				ToolName:        "shell",
				ArgsJSON:        `{"command":"echo ok"}`,
				ForceDenyReason: true,
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := matchedRulesConfirmModel(t, 100, 30, tc.req)
			plain := confirmMatchedRulesPlain(m)
			if strings.Contains(plain, "Rules requiring approval") || strings.Contains(plain, "rm *") {
				t.Fatalf("unexpected rule section in confirm dialog:\n%s", plain)
			}
		})
	}
}

// Real newlines, tabs, quotes, backslashes, terminal escapes and CJK text are
// escaped into one visible row before styling and wrapping. A literal \n stays
// distinguishable from a real newline, and no raw control byte reaches the
// overlay.
func TestRenderConfirmMatchedRulesEscapesRawFieldsAsVisibleText(t *testing.T) {
	pattern := "a\nb\tc\\n\"\x1b[31m中文"
	rules := []permission.Rule{
		{Permission: "shell\x1b[2m", Pattern: pattern, Action: permission.ActionAsk},
		{Permission: "shell", Pattern: "**sample**", Action: permission.ActionAsk},
		{Permission: "shell", Pattern: "  leading and trailing  ", Action: permission.ActionAsk},
	}
	lines := renderConfirmMatchedRules(rules, confirmDialogInnerWidth(100))
	plain := terminaltext.StripANSI(strings.Join(lines, "\n"))

	if len(lines) != 4 {
		t.Fatalf("rule rows = %d, want title + 3 rules:\n%s", len(lines), plain)
	}
	if !strings.Contains(plain, strconv.Quote(pattern)) {
		t.Fatalf("escaped pattern missing from rule rows:\n%s", plain)
	}
	if !strings.Contains(plain, `\x1b[2m`) {
		t.Fatalf("permission escape missing from rule rows:\n%s", plain)
	}
	if !strings.Contains(plain, `- shell: "**sample**" → ask`) {
		t.Fatalf("pattern must stay plain text, not Markdown:\n%s", plain)
	}
	if !strings.Contains(plain, `- shell: "  leading and trailing  " → ask`) {
		t.Fatalf("pattern whitespace was trimmed or rewritten:\n%s", plain)
	}
	if strings.Contains(plain, "\x1b") || strings.Contains(plain, "\n\n") {
		t.Fatalf("raw control byte reached the rule rows:\n%s", plain)
	}

	m := matchedRulesConfirmModel(t, 100, 30, &ConfirmRequest{
		ToolName:           "shell",
		ArgsJSON:           `{"command":"echo ok"}`,
		NeedsApprovalRules: rules,
	})
	rendered := confirmMatchedRulesPlain(m)
	if !strings.Contains(rendered, strconv.Quote(pattern)) || !strings.Contains(rendered, "中文") {
		t.Fatalf("escaped pattern not visible in rendered dialog:\n%s", rendered)
	}
	if !strings.Contains(rendered, `"  leading and trailing  " → ask`) {
		t.Fatalf("pattern whitespace not visible in rendered dialog:\n%s", rendered)
	}
	if strings.Contains(rendered, "\x1b[31m") {
		t.Fatalf("terminal escape from the rule reached the overlay:\n%s", rendered)
	}
}

func TestRenderConfirmMatchedRulesDistinguishesWrappedWhitespace(t *testing.T) {
	patterns := []string{
		"echo sample beta gamma delta*",
		"echo sample  beta gamma delta*",
	}
	for _, width := range []int{30, 40, 100} {
		t.Run(strconv.Itoa(width), func(t *testing.T) {
			displays := make([]string, len(patterns))
			for i, pattern := range patterns {
				m := matchedRulesConfirmModel(t, width, 40, &ConfirmRequest{
					ToolName: tools.NameShell,
					ArgsJSON: `{"command":"echo sample beta gamma delta-run && echo sample  beta gamma delta-run"}`,
					NeedsApprovalRules: []permission.Rule{
						{Permission: tools.NameShell, Pattern: pattern, Action: permission.ActionAsk},
					},
				})
				displays[i] = confirmMatchedRulesPlain(m)
				if m.confirm.scroll.total > m.confirm.scroll.visible {
					t.Fatalf("rule is outside the rendered window: total=%d visible=%d", m.confirm.scroll.total, m.confirm.scroll.visible)
				}
			}
			if displays[0] == displays[1] {
				t.Fatalf("different whitespace patterns produced identical confirmation dialogs:\n%s", displays[0])
			}
		})
	}
}

func TestRenderConfirmMatchedRulesPreservesCharactersAfterWrapping(t *testing.T) {
	cases := []struct {
		name    string
		pattern string
	}{
		{name: "spaced pattern", pattern: strings.Repeat("sample ", 24) + "TAIL"},
		{name: "leading and trailing spaces", pattern: "  " + strings.Repeat("sample ", 24) + "  "},
		{name: "escaped Unicode pattern", pattern: strings.Repeat("sample 中文 \t\\\"\n", 8) + "TAIL"},
	}
	for _, width := range []int{23, 34, 84} {
		t.Run(strconv.Itoa(width), func(t *testing.T) {
			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					lines := renderConfirmMatchedRules([]permission.Rule{
						{Permission: tools.NameShell, Pattern: tc.pattern, Action: permission.ActionAsk},
					}, width)
					rows := wrapDialogLines(lines[1:], width)
					got := terminaltext.StripANSI(strings.Join(rows, ""))
					want := "- shell: " + strconv.Quote(tc.pattern) + " → ask"
					if got != want {
						t.Fatalf("wrapped rule changed characters:\ngot:  %q\nwant: %q", got, want)
					}
					for _, row := range rows {
						if got := terminaltext.Width(row); got > width {
							t.Fatalf("wrapped row width = %d, want <= %d: %q", got, width, row)
						}
					}
				})
			}
		})
	}
}

// Long rules stay complete in the scrollable body: the tail of the pattern
// and the action are reachable, and wrapped continuation rows do not become
// extra rule entries.
func TestRenderConfirmMatchedRulesLongPatternScrollsToAction(t *testing.T) {
	pattern := strings.Repeat("sample-pattern-", 20) + "TAIL"
	rules := []permission.Rule{
		{Permission: "shell", Pattern: pattern, Action: permission.ActionAsk},
	}
	m := matchedRulesConfirmModel(t, 100, 20, &ConfirmRequest{
		ToolName:           "shell",
		ArgsJSON:           `{"command":"echo ok"}`,
		NeedsApprovalRules: rules,
	})
	if got := len(renderConfirmMatchedRules(rules, confirmDialogInnerWidth(m.width))); got <= 2 {
		t.Fatalf("long rule did not wrap: section rows = %d", got)
	}

	first := confirmMatchedRulesPlain(m)
	if !strings.Contains(first, "Tool: shell") || !strings.Contains(first, "[Enter/A] Allow") {
		t.Fatalf("first page lost the summary or the decisions:\n%s", first)
	}
	if m.confirm.scroll.total <= m.confirm.scroll.visible {
		t.Fatalf("long rule did not enter the scrollable body: total=%d visible=%d", m.confirm.scroll.total, m.confirm.scroll.visible)
	}
	if got := lipgloss.Height(m.renderConfirmDialog()); got > confirmDialogMaxHeight(m.height) {
		t.Fatalf("confirm dialog exceeds terminal height: %d", got)
	}

	// Walk non-overlapping pages to prove every wrapped row is reachable and
	// that continuation rows never read as another rule entry.
	var pages []string
	for offset := 0; offset < m.confirm.scroll.total; offset += max(m.confirm.scroll.visible, 1) {
		m.confirm.scroll.offset = offset
		pages = append(pages, confirmMatchedRulesPlain(m))
	}
	joined := strings.Join(pages, "\n")
	for _, want := range []string{"Rules requiring approval (1)", "TAIL", "ask", "[Enter/A] Allow"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("scrolled pages lost %q:\n%s", want, joined)
		}
	}
	if got := strings.Count(joined, "- shell:"); got != 1 {
		t.Fatalf("wrapped rows produced %d rule entries, want 1:\n%s", got, joined)
	}

	_ = m.handleConfirmKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnd}))
	last := confirmMatchedRulesPlain(m)
	if !strings.Contains(last, "TAIL") || !strings.Contains(last, "ask") || !strings.Contains(last, "[Enter/A] Allow") {
		t.Fatalf("last page lost the rule tail, action or decisions:\n%s", last)
	}
}

// Editing arguments and editing rule-picker candidates are separate flows:
// returning to the body still shows the original matched facts.
func TestRenderConfirmMatchedRulesSurviveEditAndRulePicker(t *testing.T) {
	req := &ConfirmRequest{
		ToolName: "shell",
		ArgsJSON: `{"command":"rm sample.txt"}`,
		NeedsApprovalRules: []permission.Rule{
			{Permission: "shell", Pattern: "rm *", Action: permission.ActionAsk},
		},
	}
	m := matchedRulesConfirmModel(t, 100, 30, req)

	_ = m.handleConfirmKey(tea.KeyPressMsg(tea.Key{Text: "e", Code: 'e'}))
	if !m.confirm.editing {
		t.Fatal("expected argument editor")
	}
	m.confirm.editInput.SetValue(`{"command":"echo edited"}`)
	_ = m.handleConfirmKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))
	plain := confirmMatchedRulesPlain(m)
	for _, want := range []string{"rm sample.txt", `- shell: "rm *" → ask`} {
		if !strings.Contains(plain, want) {
			t.Fatalf("edit escape lost %q:\n%s", want, plain)
		}
	}
	if strings.Contains(plain, "echo edited") {
		t.Fatalf("discarded edit leaked into the body:\n%s", plain)
	}

	_ = m.handleConfirmKey(tea.KeyPressMsg(tea.Key{Text: "e", Code: 'e'}))
	m.confirm.editInput.SetValue(`{"command":"echo submitted"}`)
	_ = m.handleConfirmKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	select {
	case result := <-m.confirmResultCh:
		if result.Action != ConfirmAllow || result.FinalArgsJSON != `{"command":"echo submitted"}` {
			t.Fatalf("edit submit result = %#v, want allow with edited args", result)
		}
	default:
		t.Fatal("edit submit no longer allows the call")
	}

	// A fresh request for the picker round trip: Enter above resolves it.
	m.confirm = confirmState{request: req}
	m.confirmResultCh = make(chan ConfirmResult, 1)
	_ = m.handleConfirmKey(tea.KeyPressMsg(tea.Key{Text: "m", Code: 'm'}))
	if !m.confirm.pickingRule {
		t.Fatal("expected rule picker")
	}
	_ = m.handleConfirmKey(tea.KeyPressMsg(tea.Key{Text: "e", Code: 'e'}))
	m.confirm.rulePatternInput.SetValue("edited *")
	_ = m.handleConfirmKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	_ = m.handleConfirmKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))

	plain = confirmMatchedRulesPlain(m)
	if !strings.Contains(plain, `- shell: "rm *" → ask`) {
		t.Fatalf("picker round trip lost the matched fact:\n%s", plain)
	}
	if strings.Contains(plain, "edited *") {
		t.Fatalf("picker candidate replaced the matched fact:\n%s", plain)
	}
}

// The render cache is keyed by the request, size, scroll offset and theme;
// a new request or a resize must not keep the previous request's rules.
func TestRenderConfirmMatchedRulesCacheFollowsRequestAndResize(t *testing.T) {
	m := matchedRulesConfirmModel(t, 100, 30, &ConfirmRequest{
		ToolName: "shell",
		ArgsJSON: `{"command":"rm sample.txt"}`,
		NeedsApprovalRules: []permission.Rule{
			{Permission: "shell", Pattern: "rm *", Action: permission.ActionAsk},
		},
	})
	if !strings.Contains(confirmMatchedRulesPlain(m), "Rules requiring approval (1)") {
		t.Fatal("expected rule section for the first request")
	}

	m.confirm.request = &ConfirmRequest{ToolName: "shell", ArgsJSON: `{"command":"echo ok"}`}
	plain := confirmMatchedRulesPlain(m)
	if strings.Contains(plain, "Rules requiring approval") || strings.Contains(plain, "rm *") {
		t.Fatalf("next request shows stale rules:\n%s", plain)
	}

	m.confirm.request = &ConfirmRequest{
		ToolName: "shell",
		ArgsJSON: `{"command":"rm next.txt"}`,
		NeedsApprovalRules: []permission.Rule{
			{Permission: "shell", Pattern: "rmdir *", Action: permission.ActionAsk},
		},
	}
	for _, size := range [][2]int{{40, 12}, {30, 6}, {120, 40}} {
		m.width, m.height = size[0], size[1]
		dialog := m.renderConfirmDialog()
		plain := terminaltext.StripANSI(dialog)
		if lipgloss.Width(dialog) > size[0]-1 || lipgloss.Height(dialog) > size[1] {
			t.Fatalf("confirm dialog exceeds %dx%d:\n%s", size[0], size[1], plain)
		}
		if !strings.Contains(plain, `[Enter/A] Allow`) {
			t.Fatalf("decisions lost at %dx%d:\n%s", size[0], size[1], plain)
		}
	}
	if !strings.Contains(confirmMatchedRulesPlain(m), "Rules requiring approval (1)") {
		t.Fatal("rule section lost after resize")
	}
}
