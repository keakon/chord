package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/mattn/go-runewidth"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
	"github.com/keakon/chord/internal/tui/terminaltext"
)

func TestParseToolArgsPreservesExactJSONNumbers(t *testing.T) {
	_, vals := parseToolArgs(`{"decimal":1.0000000000000001,"integer":10000000000000001,"exponent":1e-7}`)
	want := map[string]string{
		"decimal":  "1.0000000000000001",
		"integer":  "10000000000000001",
		"exponent": "1e-7",
	}
	for key, value := range want {
		if got := vals[key]; got != value {
			t.Errorf("vals[%q] = %q, want %q", key, got, value)
		}
	}
}

func TestParseToolArgsKeepsOnlyLastDuplicateOccurrence(t *testing.T) {
	keys, vals := parseToolArgs(`{"limit":75,"path":"first.go","limit":40,"path":"second.go"}`)
	if !slices.Equal(keys, []string{"limit", "path"}) {
		t.Fatalf("keys = %v", keys)
	}
	if vals["limit"] != "40" || vals["path"] != "second.go" {
		t.Fatalf("vals = %#v", vals)
	}
}

func TestParseToolArgsLastHiddenDuplicateRemovesEarlierValue(t *testing.T) {
	keys, vals := parseToolArgs(`{"enabled":true,"path":"sample.go","enabled":false}`)
	if !slices.Equal(keys, []string{"path"}) {
		t.Fatalf("keys = %v", keys)
	}
	if _, exists := vals["enabled"]; exists {
		t.Fatalf("vals = %#v, hidden final duplicate must replace earlier value", vals)
	}
}

func TestHeaderParamSummaryKeysSkipsDuplicatedDiagnosticSummary(t *testing.T) {
	block := &Block{
		Audit: &message.ToolArgsAudit{
			IgnoredArgs: []message.IgnoredToolArg{{
				Path:      "args.urls",
				ValueJSON: `["a","b"]`,
				Reason:    message.IgnoredToolArgReasonUnrecognized,
			}},
		},
	}
	keys := []string{"query", "urls"}
	vals := map[string]string{
		"query": `"search"`,
		"urls":  `["a","b"]`,
	}
	got := block.headerParamSummaryKeys(keys, vals)
	if !slices.Equal(got, []string{"query"}) {
		t.Fatalf("headerParamSummaryKeys = %v", got)
	}
}

func TestDiagnosticBaseTextMatchesHeaderRestyleSpelling(t *testing.T) {
	// The header restyle path locates a diagnostic's text by searching the
	// header for the spelling diagnosticBaseText produces, so that spelling is
	// shared rather than re-derived per call site: a second copy drifting (say,
	// rendering "=<missing>" as "(missing)") would silently stop matching.
	cases := []struct {
		name       string
		diagnostic toolArgDiagnostic
		wantBase   string
		wantPlain  string
	}{
		{
			name:       "flag",
			diagnostic: toolArgDiagnostic{path: "verbose"},
			wantBase:   "verbose",
			wantPlain:  "verbose",
		},
		{
			name:       "value",
			diagnostic: toolArgDiagnostic{path: "limit", value: "7"},
			wantBase:   "limit=7",
			wantPlain:  "limit=7",
		},
		{
			name:       "missing",
			diagnostic: toolArgDiagnostic{path: "patterns", missing: true},
			wantBase:   "patterns=<missing>",
			wantPlain:  "patterns=<missing>",
		},
		{
			name:       "ignored keeps the base text",
			diagnostic: toolArgDiagnostic{path: "verbose", value: "true", ignored: true},
			wantBase:   "verbose=true",
			wantPlain:  "ignored verbose=true",
		},
		{
			name:       "empty path renders nothing",
			diagnostic: toolArgDiagnostic{},
			wantBase:   "",
			wantPlain:  "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := diagnosticBaseText(tc.diagnostic); got != tc.wantBase {
				t.Fatalf("diagnosticBaseText = %q, want %q", got, tc.wantBase)
			}
			if got := diagnosticOptionPlain(tc.diagnostic); got != tc.wantPlain {
				t.Fatalf("diagnosticOptionPlain = %q, want %q", got, tc.wantPlain)
			}
		})
	}
}

func TestGenericCardRestylesDiagnosticInPlaceWithoutDuplicating(t *testing.T) {
	// The generic header already carries "limit=7" verbatim, so the diagnostic
	// must be restyled where it stands. If the base-text spelling used for the
	// search ever drifted from the one used for display, the search would miss,
	// the original value would stay unstyled, and the diagnostic would be
	// appended as a second copy on the same line.
	block := &Block{
		ID:       9,
		Type:     BlockToolCall,
		ToolName: "mcp__sample__search",
		Content:  `{"query":"chord","limit":7}`,
		Audit: &message.ToolArgsAudit{
			OriginalArgsJSON:  `{"query":"chord","limit":7}`,
			EffectiveArgsJSON: `{"query":"chord"}`,
			IgnoredArgs: []message.IgnoredToolArg{{
				Path:      "args.limit",
				ValueJSON: "7",
				Reason:    message.IgnoredToolArgReasonUnrecognized,
			}},
		},
	}
	rendered := strings.Join(block.Render(120, ""), "\n")
	plain := terminaltext.StripANSI(rendered)
	if got := strings.Count(plain, "limit=7"); got != 1 {
		t.Fatalf("header should carry limit=7 exactly once, got %d:\n%s", got, plain)
	}
	if !strings.Contains(plain, "ignored limit=7") {
		t.Fatalf("ignored diagnostic should be restyled in place:\n%s", plain)
	}
	if !strings.Contains(rendered, `;9m`) {
		t.Fatalf("restyled diagnostic is not struck through: %q", rendered)
	}
}

func TestGenericToolParamSummarySanitizesParameterNames(t *testing.T) {
	keys, vals := parseToolArgs("{\"query\\n\":\"search\"}")
	got := formatToolHeaderParamsWithParsed("mcp_any_tool", keys, vals)
	if strings.ContainsAny(got, "\r\n") {
		t.Fatalf("summary contains raw control characters: %q", got)
	}
}

func TestJobCardMovesIgnoredArgumentIntoOptionGroup(t *testing.T) {
	block := &Block{
		ID:            1,
		Type:          BlockToolCall,
		ToolName:      tools.NameJobOutput,
		Content:       `{"job_id":"job-1"}`,
		ResultDone:    true,
		ResultContent: "ok",
		Audit: &message.ToolArgsAudit{
			OriginalArgsJSON:  `{"job_id":"job-1","path":"sub"}`,
			EffectiveArgsJSON: `{"job_id":"job-1"}`,
			IgnoredArgs: []message.IgnoredToolArg{{
				Path:      "args.path",
				ValueJSON: `"sub"`,
				Reason:    message.IgnoredToolArgReasonUnrecognized,
			}},
		},
	}
	rendered := strings.Join(block.Render(120, ""), "\n")
	plain := terminaltext.StripANSI(rendered)
	if !strings.Contains(plain, "(ignored path=sub)") {
		t.Fatalf("ignored argument should join the job option group:\n%s", plain)
	}
	if strings.Contains(plain, " · path=") {
		t.Fatalf("ignored argument must not be appended after the header:\n%s", plain)
	}
	if !strings.Contains(rendered, ";9m") {
		t.Fatalf("ignored argument is not struck through: %q", rendered)
	}
}

func TestWebFetchCardMovesIgnoredArgumentIntoOptionGroup(t *testing.T) {
	block := &Block{
		ID:            1,
		Type:          BlockToolCall,
		ToolName:      tools.NameWebFetch,
		Content:       `{"url":"https://example.invalid/notes"}`,
		ResultDone:    true,
		ResultContent: "ok",
		Audit: &message.ToolArgsAudit{
			OriginalArgsJSON:  `{"url":"https://example.invalid/notes","raw":true}`,
			EffectiveArgsJSON: `{"url":"https://example.invalid/notes"}`,
			IgnoredArgs: []message.IgnoredToolArg{{
				Path:      "args.raw",
				ValueJSON: `true`,
				Reason:    message.IgnoredToolArgReasonUnrecognized,
			}},
		},
	}
	rendered := strings.Join(block.Render(120, ""), "\n")
	plain := terminaltext.StripANSI(rendered)
	if !strings.Contains(plain, "https://example.invalid/notes (ignored raw=true)") {
		t.Fatalf("ignored argument should join the webfetch option group:\n%s", plain)
	}
	if !strings.Contains(rendered, ";9m") {
		t.Fatalf("ignored argument is not struck through: %q", rendered)
	}
}

func TestWriteCardJoinsIgnoredArgumentToExtras(t *testing.T) {
	block := &Block{
		ID:            1,
		Type:          BlockToolCall,
		ToolName:      tools.NameWrite,
		Content:       `{"path":"notes.md","content":"hello"}`,
		ResultDone:    true,
		ResultContent: "ok",
		Audit: &message.ToolArgsAudit{
			OriginalArgsJSON:  `{"path":"notes.md","content":"hello","append":true}`,
			EffectiveArgsJSON: `{"path":"notes.md","content":"hello"}`,
			IgnoredArgs: []message.IgnoredToolArg{{
				Path:      "args.append",
				ValueJSON: `true`,
				Reason:    message.IgnoredToolArgReasonUnrecognized,
			}},
		},
	}
	rendered := strings.Join(block.Render(120, ""), "\n")
	plain := terminaltext.StripANSI(rendered)
	if !strings.Contains(plain, "ignored append=true") {
		t.Fatalf("ignored argument should join the write extras chain:\n%s", plain)
	}
	if strings.Contains(plain, " · append=") {
		t.Fatalf("ignored argument must not be appended after the header:\n%s", plain)
	}
	if !strings.Contains(rendered, ";9m") {
		t.Fatalf("ignored argument is not struck through: %q", rendered)
	}
}

func TestEditCardJoinsIgnoredArgumentToHeaderOptions(t *testing.T) {
	block := &Block{
		ID:            1,
		Type:          BlockToolCall,
		ToolName:      tools.NameEdit,
		Content:       `{"path":"config.toml","old_string":"alpha","new_string":"beta"}`,
		ResultDone:    true,
		ResultContent: "ok",
		Audit: &message.ToolArgsAudit{
			OriginalArgsJSON:  `{"path":"config.toml","old_string":"alpha","new_string":"beta","create_dirs":true}`,
			EffectiveArgsJSON: `{"path":"config.toml","old_string":"alpha","new_string":"beta"}`,
			IgnoredArgs: []message.IgnoredToolArg{{
				Path:      "args.create_dirs",
				ValueJSON: `true`,
				Reason:    message.IgnoredToolArgReasonUnrecognized,
			}},
		},
	}
	rendered := strings.Join(block.Render(120, ""), "\n")
	plain := terminaltext.StripANSI(rendered)
	if !strings.Contains(plain, "(ignored create_dirs=true)") {
		t.Fatalf("ignored argument should join the edit option group:\n%s", plain)
	}
	if strings.Contains(plain, " · create_dirs=") {
		t.Fatalf("ignored argument must not be appended after the header:\n%s", plain)
	}
	if !strings.Contains(rendered, ";9m") {
		t.Fatalf("ignored argument is not struck through: %q", rendered)
	}
}

func TestTodoCardShowsIgnoredArgumentInOptionGroup(t *testing.T) {
	block := &Block{
		ID:            1,
		Type:          BlockToolCall,
		ToolName:      tools.NameTodoWrite,
		Content:       `{"todos":[{"id":"1","content":"write docs","status":"pending"}]}`,
		ResultDone:    true,
		ResultContent: "ok",
		Audit: &message.ToolArgsAudit{
			OriginalArgsJSON:  `{"todos":[{"id":"1","content":"write docs","status":"pending"}],"priority":"high"}`,
			EffectiveArgsJSON: `{"todos":[{"id":"1","content":"write docs","status":"pending"}]}`,
			IgnoredArgs: []message.IgnoredToolArg{{
				Path:      "args.priority",
				ValueJSON: `"high"`,
				Reason:    message.IgnoredToolArgReasonUnrecognized,
			}},
		},
	}
	rendered := strings.Join(block.Render(120, ""), "\n")
	plain := terminaltext.StripANSI(rendered)
	if !strings.Contains(plain, "(ignored priority=high)") {
		t.Fatalf("ignored argument should form its own option group:\n%s", plain)
	}
	if strings.Contains(plain, " · priority=") {
		t.Fatalf("ignored argument must not be appended after the header:\n%s", plain)
	}
	if !strings.Contains(rendered, ";9m") {
		t.Fatalf("ignored argument is not struck through: %q", rendered)
	}
}

func shellIgnoredPathAudit() *message.ToolArgsAudit {
	return &message.ToolArgsAudit{
		OriginalArgsJSON:  `{"command":"git status --short --branch","description":"Check git status and unpushed commits","path":".chord/memory/records/x.md","timeout_ms":120000}`,
		EffectiveArgsJSON: `{"command":"git status --short --branch","description":"Check git status and unpushed commits","timeout_ms":120000}`,
		IgnoredArgs: []message.IgnoredToolArg{{
			Path:      "args.path",
			ValueJSON: `".chord/memory/records/x.md"`,
			Reason:    message.IgnoredToolArgReasonUnrecognized,
		}},
	}
}

func TestShellCardMovesIgnoredArgumentIntoOptionGroup(t *testing.T) {
	block := &Block{
		ID:            1,
		Type:          BlockToolCall,
		ToolName:      tools.NameShell,
		Content:       `{"command":"git status --short --branch","description":"Check git status and unpushed commits","timeout_ms":120000}`,
		ResultDone:    true,
		ResultContent: "ok",
		Audit:         shellIgnoredPathAudit(),
	}
	rendered := strings.Join(block.Render(160, ""), "\n")
	plain := terminaltext.StripANSI(rendered)
	want := "Check git status and unpushed commits (timeout=2m, ignored path=.chord/memory/records/x.md)"
	if !strings.Contains(plain, want) {
		t.Fatalf("ignored argument should join the timeout option group, want %q:\n%s", want, plain)
	}
	if strings.Contains(plain, " · path=") {
		t.Fatalf("ignored argument must not be appended after the header:\n%s", plain)
	}
	if !strings.Contains(rendered, ";9m") {
		t.Fatalf("ignored argument is not struck through: %q", rendered)
	}
	if strings.Contains(rendered, `\x1b`) {
		t.Fatalf("escape sequence was neutralized to a literal: %q", rendered)
	}
}

func TestShellCardShowsIgnoredArgumentWithoutTimeout(t *testing.T) {
	block := &Block{
		ID:            1,
		Type:          BlockToolCall,
		ToolName:      tools.NameShell,
		Content:       `{"command":"git status --short --branch","description":"Check git status and unpushed commits"}`,
		ResultDone:    true,
		ResultContent: "ok",
		Audit:         shellIgnoredPathAudit(),
	}
	rendered := strings.Join(block.Render(160, ""), "\n")
	plain := terminaltext.StripANSI(rendered)
	want := "Check git status and unpushed commits (ignored path=.chord/memory/records/x.md)"
	if !strings.Contains(plain, want) {
		t.Fatalf("ignored argument should form its own option group, want %q:\n%s", want, plain)
	}
	if strings.Contains(plain, "timeout=") {
		t.Fatalf("timeout should not be invented when the call has none:\n%s", plain)
	}
}

func TestShellCollapsedCardKeepsIgnoredArgumentInOptionGroup(t *testing.T) {
	block := &Block{
		ID:            1,
		Type:          BlockToolCall,
		ToolName:      tools.NameShell,
		Content:       `{"command":"git status --short --branch","description":"Check git status and unpushed commits","timeout_ms":120000}`,
		ResultDone:    true,
		ResultContent: "ok",
		Collapsed:     true,
		Audit:         shellIgnoredPathAudit(),
	}
	rendered := strings.Join(block.Render(160, ""), "\n")
	plain := terminaltext.StripANSI(rendered)
	want := "Check git status and unpushed commits (timeout=2m, ignored path=.chord/memory/records/x.md)"
	if !strings.Contains(plain, want) {
		t.Fatalf("collapsed header should keep the ignored option group, want %q:\n%s", want, plain)
	}
	if strings.Contains(plain, " · path=") {
		t.Fatalf("collapsed header must not append the ignored argument:\n%s", plain)
	}
}

func TestShellCardTruncatesStruckThroughOptionWithoutStyleLeak(t *testing.T) {
	block := &Block{
		ID:            1,
		Type:          BlockToolCall,
		ToolName:      tools.NameShell,
		Content:       `{"command":"git status --short --branch","description":"Check git status and unpushed commits","timeout_ms":120000}`,
		ResultDone:    true,
		ResultContent: "ok",
		Audit:         shellIgnoredPathAudit(),
	}
	lines := block.Render(80, "")
	var header string
	for _, line := range lines {
		if strings.Contains(terminaltext.StripANSI(line), "ignored") {
			header = line
			break
		}
	}
	if header == "" {
		t.Fatalf("header with ignored argument missing:\n%s", terminaltext.StripANSI(strings.Join(lines, "\n")))
	}
	if strings.Contains(header, `\x1b`) {
		t.Fatalf("escape sequence was neutralized to a literal: %q", header)
	}
	plainWidth := runewidth.StringWidth(terminaltext.StripANSI(header))
	// 83 matches the pre-existing width of a header whose mainPart alone fills
	// the appendToolHeaderSummary budget (long description, no diagnostics), so
	// the struck-through option group must not exceed that baseline.
	if plainWidth > 83 {
		t.Fatalf("header width = %d, want <= 83: %q", plainWidth, terminaltext.StripANSI(header))
	}
	if strings.Count(terminaltext.StripANSI(header), "…") == 0 {
		t.Fatalf("expected the option group to be truncated on a narrow card: %q", terminaltext.StripANSI(header))
	}
}

func TestReadCardStrikesThroughIgnoredUnrecognizedArgument(t *testing.T) {
	block := &Block{
		ID:            1,
		Type:          BlockToolCall,
		ToolName:      tools.NameRead,
		Content:       `{"limit":40,"path":"sample.go"}`,
		ResultDone:    true,
		ResultContent: "READ_RESULT lines=1-1 total=1\npackage sample",
		Audit: &message.ToolArgsAudit{
			OriginalArgsJSON:  `{"path":"sample.go","limit":40,"format":"json"}`,
			EffectiveArgsJSON: `{"limit":40,"path":"sample.go"}`,
			IgnoredArgs: []message.IgnoredToolArg{{
				Path:      "args.format",
				ValueJSON: `"json"`,
				Reason:    message.IgnoredToolArgReasonUnrecognized,
			}},
		},
	}
	rendered := strings.Join(block.Render(120, ""), "\n")
	plain := strings.ReplaceAll(terminaltext.StripANSI(rendered), "…", "")
	plain = strings.ReplaceAll(plain, "\x1b", "")
	if !strings.Contains(plain, "sample.go (limit=40, ignored format=json)") {
		t.Fatalf("ignored argument should join the read option group:\n%s", plain)
	}
	if !strings.Contains(rendered, ";9m") {
		t.Fatalf("ignored argument is not struck through: %q", rendered)
	}
}

func TestGlobCardShowsIgnoredAndMissingArgumentsInline(t *testing.T) {
	block := &Block{
		ID:                1,
		Type:              BlockToolCall,
		ToolName:          tools.NameGlob,
		Content:           `{"path":"/tmp/workspace",".patterns":["MEMORY.md",".chord/memory/records/*.md"]}`,
		ResultDone:        true,
		ResultStatus:      message.ToolStatusError,
		ResultContent:     "arguments do not match glob schema: args.patterns is required",
		displayWorkingDir: filepath.Join(string(os.PathSeparator), "tmp", "workspace"),
		Audit: &message.ToolArgsAudit{
			OriginalArgsJSON:  `{"path":"/tmp/workspace",".patterns":["MEMORY.md",".chord/memory/records/*.md"]}`,
			EffectiveArgsJSON: `{"path":"/tmp/workspace"}`,
			InvalidArgs: []message.InvalidToolArg{{
				Path:   "args.patterns",
				Reason: message.InvalidToolArgReasonMissing,
			}},
			IgnoredArgs: []message.IgnoredToolArg{{
				Path:      "args..patterns",
				ValueJSON: `["MEMORY.md",".chord/memory/records/*.md"]`,
				Reason:    message.IgnoredToolArgReasonUnrecognized,
			}},
		},
	}
	rendered := strings.Join(block.Render(120, ""), "\n")
	plain := strings.ReplaceAll(terminaltext.StripANSI(rendered), "…", "")
	plain = strings.ReplaceAll(plain, "\x1b", "")
	if !strings.Contains(plain, `glob <missing>`) {
		t.Fatalf("missing required argument should occupy the main parameter slot:\n%s", plain)
	}
	if strings.Contains(plain, "path=/tmp/workspace") || strings.Contains(plain, "(path=.)") {
		t.Fatalf("glob header should keep normal path elision rules:\n%s", plain)
	}
	if !strings.Contains(plain, `.p`) {
		t.Fatalf("unrecognized argument missing:\n%s", plain)
	}
	if strings.Contains(plain, "patterns=<missing>") {
		t.Fatalf("missing primary glob argument should render as value-only placeholder:\n%s", plain)
	}
	if !strings.Contains(plain, `(<missing>`) && strings.Contains(plain, `.patterns=MEMORY.md,.chord/memory/records/*.md ·`) {
		t.Fatalf("glob diagnostics should move ignored values into the option group:\n%s", plain)
	}
	if !strings.Contains(plain, `.p`) {
		t.Fatalf("glob option group should contain path and ignored patterns:\n%s", plain)
	}
	if strings.Contains(plain, `[1 items]`) {
		t.Fatalf("compacted generic summary should not duplicate diagnostic values:\n%s", plain)
	}
	if !strings.Contains(rendered, `;9m`) {
		t.Fatalf("unrecognized argument is not struck through: %q", rendered)
	}
	if !strings.Contains(rendered, "38;5;"+currentStyles.Palette.ErrorFg+"m") {
		t.Fatalf("missing required argument is not red: %q", rendered)
	}
}

func TestGrepCardShowsDiagnosticListsLikeNormalHeader(t *testing.T) {
	block := &Block{
		ID:                1,
		Type:              BlockToolCall,
		ToolName:          tools.NameGrep,
		Content:           `{"pattern":"TODO",".paths":["/tmp/workspace/internal","/tmp/workspace/cmd"],".includes":["**/*.go","**/*.md"]}`,
		ResultDone:        true,
		ResultStatus:      message.ToolStatusError,
		ResultContent:     "arguments do not match grep schema: args.paths is invalid",
		displayWorkingDir: filepath.Join(string(os.PathSeparator), "tmp", "workspace"),
		Audit: &message.ToolArgsAudit{
			OriginalArgsJSON: `{"pattern":"TODO",".paths":["/tmp/workspace/internal","/tmp/workspace/cmd"],".includes":["**/*.go","**/*.md"]}`,
			IgnoredArgs: []message.IgnoredToolArg{
				{
					Path:      "args..paths",
					ValueJSON: `["/tmp/workspace/internal","/tmp/workspace/cmd"]`,
					Reason:    message.IgnoredToolArgReasonUnrecognized,
				},
				{
					Path:      "args..includes",
					ValueJSON: `["**/*.go","**/*.md"]`,
					Reason:    message.IgnoredToolArgReasonUnrecognized,
				},
			},
		},
	}
	rendered := strings.Join(block.Render(120, ""), "\n")
	plain := strings.ReplaceAll(terminaltext.StripANSI(rendered), "…", "")
	plain = strings.ReplaceAll(plain, "\x1b", "")
	for _, want := range []string{
		"grep TODO (ignored .paths=internal,cmd, ignored .includes=**/*.go,**/*.md)",
		"ignored .paths=internal,cmd",
		"ignored .includes=**/*.go,**/*.md",
	} {
		if !strings.Contains(plain, want) {
			t.Fatalf("grep diagnostic list formatting mismatch, want %q:\n%s", want, plain)
		}
	}
	if strings.Contains(plain, `["/tmp/workspace/internal","/tmp/workspace/cmd"]`) || strings.Contains(plain, `["**/*.go","**/*.md"]`) {
		t.Fatalf("grep diagnostic lists should follow normal header formatting:\n%s", plain)
	}
	if !strings.Contains(rendered, ";9m") {
		t.Fatalf("ignored options are not struck through: %q", rendered)
	}
	if strings.Contains(rendered, `\x1b`) {
		t.Fatalf("escape sequences were neutralized to literals: %q", rendered)
	}
}

func TestGrepCardShowsMissingPatternInMainSlot(t *testing.T) {
	block := &Block{
		ID:                1,
		Type:              BlockToolCall,
		ToolName:          tools.NameGrep,
		Content:           `{"path":"/tmp/workspace","includes":["**/*.go"],".pattern":"TODO"}`,
		ResultDone:        true,
		ResultStatus:      message.ToolStatusError,
		ResultContent:     "arguments do not match grep schema: args.pattern is required",
		displayWorkingDir: filepath.Join(string(os.PathSeparator), "tmp", "workspace"),
		Audit: &message.ToolArgsAudit{
			OriginalArgsJSON:  `{"path":"/tmp/workspace","includes":["**/*.go"],".pattern":"TODO"}`,
			EffectiveArgsJSON: `{"path":"/tmp/workspace","includes":["**/*.go"]}`,
			InvalidArgs: []message.InvalidToolArg{{
				Path:   "args.pattern",
				Reason: message.InvalidToolArgReasonMissing,
			}},
			IgnoredArgs: []message.IgnoredToolArg{{
				Path:      "args..pattern",
				ValueJSON: `"TODO"`,
				Reason:    message.IgnoredToolArgReasonUnrecognized,
			}},
		},
	}
	rendered := strings.Join(block.Render(120, ""), "\n")
	plain := terminaltext.StripANSI(rendered)
	if !strings.Contains(plain, `grep <missing>`) {
		t.Fatalf("missing grep pattern should occupy the main parameter slot:\n%s", plain)
	}
	if strings.Contains(plain, "pattern=<missing>") {
		t.Fatalf("missing grep pattern should render as value-only placeholder:\n%s", plain)
	}
	if !strings.Contains(plain, `.p`) {
		t.Fatalf("ignored grep pattern should stay in the option list:\n%s", plain)
	}
	if !strings.Contains(plain, `includes=**/*.go`) {
		t.Fatalf("grep should keep valid options in the option list:\n%s", plain)
	}
	if strings.Contains(plain, "path=/tmp/workspace") {
		t.Fatalf("grep should keep normal path elision rules:\n%s", plain)
	}
	if !strings.Contains(rendered, `;9m`) {
		t.Fatalf("ignored grep pattern is not struck through: %q", rendered)
	}
	if !strings.Contains(rendered, "38;5;"+currentStyles.Palette.ErrorFg+"m") {
		t.Fatalf("missing grep pattern is not red: %q", rendered)
	}
}

// A model that reaches for the glob spelling of the argument sends the plural
// "patterns" instead of the singular "pattern" the grep schema requires. The
// discarded plural belongs in the parenthesized option group, struck through
// the way glob renders its own discarded patterns — not trailing the header
// behind a " · " separator, which no longer looks like a valid grep call.
func TestGrepCardFoldsPluralPatternsIntoOptionGroup(t *testing.T) {
	block := &Block{
		ID:                1,
		Type:              BlockToolCall,
		ToolName:          tools.NameGrep,
		Content:           `{"includes":["**/*.go"],"patterns":["ExportToMarkdown\\(","ExportMarkdownToFile\\(","Export\\(messages"]}`,
		ResultDone:        true,
		ResultStatus:      message.ToolStatusError,
		ResultContent:     "arguments do not match grep schema: args.pattern is required",
		displayWorkingDir: filepath.Join(string(os.PathSeparator), "tmp", "workspace"),
		Audit: &message.ToolArgsAudit{
			OriginalArgsJSON:  `{"includes":["**/*.go"],"patterns":["ExportToMarkdown\\(","ExportMarkdownToFile\\(","Export\\(messages"]}`,
			EffectiveArgsJSON: `{"includes":["**/*.go"]}`,
			InvalidArgs: []message.InvalidToolArg{{
				Path:   "args.pattern",
				Reason: message.InvalidToolArgReasonMissing,
			}},
			IgnoredArgs: []message.IgnoredToolArg{{
				Path:      "args.patterns",
				ValueJSON: `["ExportToMarkdown\\(","ExportMarkdownToFile\\(","Export\\(messages"]`,
				Reason:    message.IgnoredToolArgReasonUnrecognized,
			}},
		},
	}
	rendered := strings.Join(block.Render(160, ""), "\n")
	plain := strings.ReplaceAll(terminaltext.StripANSI(rendered), "…", "")
	plain = strings.ReplaceAll(plain, "\x1b", "")
	if !strings.Contains(plain, `grep <missing>`) {
		t.Fatalf("missing grep pattern should occupy the main parameter slot:\n%s", plain)
	}
	if strings.Contains(plain, "pattern=<missing>") {
		t.Fatalf("missing grep pattern should render as value-only placeholder:\n%s", plain)
	}
	// The plural must sit inside the option group beside the valid includes,
	// not after it behind the " · " separator.
	if !strings.Contains(plain, `(includes=**/*.go, ignored patterns=`) {
		t.Fatalf("discarded plural patterns should join the option group:\n%s", plain)
	}
	if strings.Contains(plain, `· patterns=`) {
		t.Fatalf("discarded plural patterns should not trail the header:\n%s", plain)
	}
	// List formatting follows paths=/includes= rather than raw JSON.
	if strings.Contains(plain, `patterns=[`) {
		t.Fatalf("plural patterns should render like other grep list arguments:\n%s", plain)
	}
	if !strings.Contains(plain, `ExportToMarkdown`) {
		t.Fatalf("discarded pattern values missing:\n%s", plain)
	}
	if !strings.Contains(rendered, `;9m`) {
		t.Fatalf("discarded plural patterns are not struck through: %q", rendered)
	}
	if !strings.Contains(rendered, "38;5;"+currentStyles.Palette.ErrorFg+"m") {
		t.Fatalf("missing grep pattern is not red: %q", rendered)
	}
}

// A model that writes the singular "include" instead of the schema's
// "includes" must not push the dropped value behind the result summary: the
// diagnostic shares the option group with the effective arguments, before the
// match counts that belong to the executed call.
func TestGrepCardFoldsSingularIncludeIntoOptionGroup(t *testing.T) {
	block := &Block{
		ID:                1,
		Type:              BlockToolCall,
		ToolName:          tools.NameGrep,
		Content:           `{"paths":["internal/tui"],"pattern":"TODO"}`,
		ResultDone:        true,
		ResultContent:     "internal/tui/app.go:1:TODO one\nNote: ignored unrecognized parameter(s): args.include",
		displayWorkingDir: filepath.Join(string(os.PathSeparator), "tmp", "workspace"),
		Audit: &message.ToolArgsAudit{
			OriginalArgsJSON:  `{"paths":["internal/tui"],"pattern":"TODO","include":"*.go"}`,
			EffectiveArgsJSON: `{"paths":["internal/tui"],"pattern":"TODO"}`,
			IgnoredArgs: []message.IgnoredToolArg{{
				Path:      "args.include",
				ValueJSON: `"*.go"`,
				Reason:    message.IgnoredToolArgReasonUnrecognized,
			}},
		},
	}
	plain := strings.ReplaceAll(terminaltext.StripANSI(strings.Join(block.Render(160, ""), "\n")), "…", "")
	if !strings.Contains(plain, "grep TODO (paths=internal/tui, ignored include=*.go) · 1 match · 1 file") {
		t.Fatalf("dropped singular include should join the option group before the result summary:\n%s", plain)
	}
	if strings.Contains(plain, "· ignored include") {
		t.Fatalf("dropped singular include should not trail the result summary:\n%s", plain)
	}
}

func TestReadCardShowsInvalidArgumentInRed(t *testing.T) {
	block := &Block{
		ID:            1,
		Type:          BlockToolCall,
		ToolName:      tools.NameRead,
		Content:       `{"path":"sample.go","limit":"forty"}`,
		ResultDone:    true,
		ResultStatus:  message.ToolStatusError,
		ResultContent: "arguments do not match Read schema: args.limit must be an integer",
		Audit: &message.ToolArgsAudit{
			InvalidArgs: []message.InvalidToolArg{{
				Path:      "args.limit",
				ValueJSON: `"forty"`,
				Reason:    message.InvalidToolArgReasonInvalid,
			}},
		},
	}
	rendered := strings.Join(block.Render(120, ""), "\n")
	plain := terminaltext.StripANSI(rendered)
	if !strings.Contains(plain, "limit=forty") {
		t.Fatalf("invalid argument missing:\n%s", plain)
	}
	if !strings.Contains(rendered, "38;5;"+currentStyles.Palette.ErrorFg+"m") {
		t.Fatalf("invalid argument is not red: %q", rendered)
	}
}

func TestExecutionFailureKeepsValidArgumentsNormal(t *testing.T) {
	block := &Block{
		ID:            1,
		Type:          BlockToolCall,
		ToolName:      tools.NameGlob,
		Content:       `{"patterns":["**/*.go"],"path":"restricted"}`,
		ResultDone:    true,
		ResultStatus:  message.ToolStatusError,
		ResultContent: "permission denied",
	}
	lines := block.Render(120, "")
	header := renderedLineContaining(t, lines, "**/*.go")
	for _, cell := range renderedCellsForText(t, header, "**/*.go") {
		if colorsEqual(cell.Style.Fg, colorOfTheme(currentStyles.Palette.ErrorFg)) {
			t.Fatalf("valid argument cell %q unexpectedly uses error color", cell.Content)
		}
	}
}

func TestReadCardStrikesThroughShadowedDuplicateValues(t *testing.T) {
	block := &Block{
		ID:            1,
		Type:          BlockToolCall,
		ToolName:      tools.NameRead,
		Content:       `{"limit":40,"offset":300,"path":"second.go"}`,
		ResultDone:    true,
		ResultContent: "READ_RESULT lines=301-301 total=301\npackage sample",
		Audit: &message.ToolArgsAudit{
			EffectiveArgsJSON: `{"limit":40,"offset":300,"path":"second.go"}`,
			IgnoredArgs: []message.IgnoredToolArg{
				{Path: "args.limit", ValueJSON: "75", Reason: message.IgnoredToolArgReasonShadowed},
				{Path: "args.offset", ValueJSON: "664", Reason: message.IgnoredToolArgReasonShadowed},
				{Path: "args.path", ValueJSON: `"first.go"`, Reason: message.IgnoredToolArgReasonShadowed},
			},
		},
	}
	rendered := strings.Join(block.Render(160, ""), "\n")
	plain := terminaltext.StripANSI(rendered)
	for _, want := range []string{"second.go (offset=300, limit=40, ", "limit=75", "offset=664", "path=first.go"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("rendered card missing %q:\n%s", want, plain)
		}
	}
	if strings.Count(plain, "limit=40") != 1 || strings.Count(plain, "offset=300") != 1 {
		t.Fatalf("effective duplicate values rendered more than once:\n%s", plain)
	}
	if strings.Count(rendered, ";9m") < 3 {
		t.Fatalf("shadowed values are not all struck through: %q", rendered)
	}
}

func TestReadCallErrorKeepsInvalidArgsVisible(t *testing.T) {
	block := &Block{
		ID:            1,
		Type:          BlockToolCall,
		ToolName:      "read",
		Content:       `{"path":"internal/config/config.go","offset":"1.0"}`,
		ResultDone:    true,
		ResultStatus:  message.ToolStatusError,
		ResultContent: "arguments do not match read schema: args.offset must be an integer, got string \"1.0\"",
	}

	plain := terminaltext.StripANSI(strings.Join(block.renderReadCall(120, ""), "\n"))
	if !strings.Contains(plain, "internal/config/config.go") {
		t.Fatalf("expected invalid-args Read header to keep the path, got:\n%s", plain)
	}
	if !strings.Contains(plain, "offset=1.0") {
		t.Fatalf("expected Read header to show the offending offset value, got:\n%s", plain)
	}
}

func TestReadCallErrorShowsUnexpectedArgKeys(t *testing.T) {
	block := &Block{
		ID:            1,
		Type:          BlockToolCall,
		ToolName:      "read",
		Content:       `{"pat":"sample.go","limit":5}`,
		ResultDone:    true,
		ResultStatus:  message.ToolStatusError,
		ResultContent: "arguments do not match read schema: args.path is required",
	}

	plain := terminaltext.StripANSI(strings.Join(block.renderReadCall(120, ""), "\n"))
	if !strings.Contains(plain, "pat=sample.go") {
		t.Fatalf("expected Read header to surface the unexpected key, got:\n%s", plain)
	}
	if !strings.Contains(plain, "limit=5") {
		t.Fatalf("expected Read header to keep valid params alongside the bad one, got:\n%s", plain)
	}
}

func TestReadCallErrorTruncatesLongArgValues(t *testing.T) {
	block := &Block{
		ID:            1,
		Type:          BlockToolCall,
		ToolName:      "read",
		Content:       fmt.Sprintf(`{"path":"a.go","offset":%q}`, strings.Repeat("x", 200)),
		ResultDone:    true,
		ResultStatus:  message.ToolStatusError,
		ResultContent: "arguments do not match read schema: args.offset must be an integer",
	}

	plain := terminaltext.StripANSI(strings.Join(block.renderReadCall(160, ""), "\n"))
	if strings.Contains(plain, strings.Repeat("x", 150)) {
		t.Fatalf("expected long arg value to be truncated, got:\n%s", plain)
	}
	if !strings.Contains(plain, "offset="+strings.Repeat("x", 50)) {
		t.Fatalf("expected truncated offset prefix to stay visible, got:\n%s", plain)
	}
}

func TestWriteCallErrorKeepsInvalidArgsVisible(t *testing.T) {
	block := &Block{
		ID:            2,
		Type:          BlockToolCall,
		ToolName:      "write",
		Content:       `{"path":"docs/notes.md","content":123}`,
		ResultDone:    true,
		ResultStatus:  message.ToolStatusError,
		ResultContent: "arguments do not match write schema: args.content must be a string, got number 123",
	}

	plain := terminaltext.StripANSI(strings.Join(block.renderWriteCall(120, ""), "\n"))
	if !strings.Contains(plain, "docs/notes.md") {
		t.Fatalf("expected invalid-args Write header to keep the path, got:\n%s", plain)
	}
	if !strings.Contains(plain, "content=123") {
		t.Fatalf("expected Write header to show the offending content value, got:\n%s", plain)
	}
}

func TestEscalateCallKeepsInvalidArgsVisible(t *testing.T) {
	block := &Block{
		ID:            1,
		Type:          BlockToolCall,
		ToolName:      tools.NameEscalate,
		Content:       `{"reason":"Need approval to rotate credentials","files_changed":"src/auth.go"}`,
		ResultDone:    true,
		ResultStatus:  message.ToolStatusError,
		ResultContent: "arguments do not match escalate schema: args.files_changed must be an array, got string \"src/auth.go\"",
	}

	plain := terminaltext.StripANSI(strings.Join(block.renderProseControlCall(140, ""), "\n"))
	if !strings.Contains(plain, "Need approval to rotate credentials") {
		t.Fatalf("expected escalate reason to survive a wrongly typed sibling field, got:\n%s", plain)
	}
}

func TestTaskCallKeepsInvalidArgsVisible(t *testing.T) {
	block := &Block{
		ID:            1,
		Type:          BlockToolCall,
		ToolName:      tools.NameDelegate,
		Content:       `{"description":"Explore the repo layout","agent_type":42}`,
		ResultDone:    true,
		ResultStatus:  message.ToolStatusError,
		ResultContent: "arguments do not match task schema: args.agent_type must be a string, got number 42",
	}

	plain := terminaltext.StripANSI(strings.Join(block.renderTaskCall(140, ""), "\n"))
	if !strings.Contains(plain, "Explore the repo layout") {
		t.Fatalf("expected delegate description to survive a wrongly typed sibling field, got:\n%s", plain)
	}
}

func TestCancelCallKeepsInvalidArgsVisible(t *testing.T) {
	block := &Block{
		ID:            1,
		Type:          BlockToolCall,
		ToolName:      tools.NameCancel,
		Content:       `{"target_task_id":"task-42","reason":{"because":"stale"}}`,
		ResultDone:    true,
		ResultStatus:  message.ToolStatusError,
		ResultContent: "arguments do not match cancel schema: args.reason must be a string, got object",
	}

	plain := terminaltext.StripANSI(strings.Join(block.renderCancelCall(140, ""), "\n"))
	if !strings.Contains(plain, "task-42") {
		t.Fatalf("expected cancel target to survive a wrongly typed sibling field, got:\n%s", plain)
	}
	if !strings.Contains(plain, "stale") {
		t.Fatalf("expected cancel reason value to stay visible, got:\n%s", plain)
	}
}

func TestNotifyCallKeepsInvalidArgsVisible(t *testing.T) {
	block := &Block{
		ID:            1,
		Type:          BlockToolCall,
		ToolName:      tools.NameNotify,
		Content:       `{"message":"ping the worker","kind":9}`,
		ResultDone:    true,
		ResultStatus:  message.ToolStatusError,
		ResultContent: "arguments do not match notify schema: args.kind must be a string, got number 9",
	}

	plain := terminaltext.StripANSI(strings.Join(block.renderNotifyCall(140, ""), "\n"))
	if !strings.Contains(plain, "ping the worker") {
		t.Fatalf("expected notify message to survive a wrongly typed sibling field, got:\n%s", plain)
	}
}
