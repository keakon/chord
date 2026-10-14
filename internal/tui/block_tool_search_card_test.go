package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/tools"
	"github.com/keakon/chord/internal/tui/terminaltext"
)

func TestGrepCollapsedSummaryCountsOnlyMatchesAndExpandedShowsAllDetails(t *testing.T) {
	block := &Block{
		ID:                     1,
		Type:                   BlockToolCall,
		ToolName:               tools.NameGrep,
		Content:                `{"pattern":"TODO"}`,
		ResultDone:             true,
		ToolCallDetailExpanded: false,
		ResultContent: strings.Join([]string{
			"a.go:1:TODO one",
			"b.go:2:TODO two",
			"Note: pattern was invalid regex; searched as literal text.",
			"grep: skipped path: vendor/blocked: no such file or directory",
			"(showing first 2 matches within 4096 KiB; narrow paths/includes/pattern for more precise results)",
		}, "\n"),
	}

	collapsed := terminaltext.StripANSI(strings.Join(block.Render(120, ""), "\n"))
	if !strings.Contains(collapsed, "✓ ▸ grep TODO") || !strings.Contains(collapsed, "2 matches · 2 files") {
		t.Fatalf("expected grep collapsed summary, got:\n%s", collapsed)
	}
	if strings.Contains(collapsed, "grep: skipped path: vendor/blocked") || strings.Contains(collapsed, "searched as literal text") {
		t.Fatalf("collapsed grep should hide full detail lines, got:\n%s", collapsed)
	}

	block.ToggleAtWidth(120)
	expanded := terminaltext.StripANSI(strings.Join(block.Render(120, ""), "\n"))
	for _, want := range []string{"✓ ▾ grep TODO", "a.go:1:TODO one", "Note: pattern was invalid regex; searched as literal text.", "grep: skipped path: vendor/blocked"} {
		if !strings.Contains(expanded, want) {
			t.Fatalf("expected expanded grep to contain %q, got:\n%s", want, expanded)
		}
	}
}

func TestGlobCollapsedSummaryShowsFilesAndArtifact(t *testing.T) {
	block := &Block{
		ID:                     1,
		Type:                   BlockToolCall,
		ToolName:               tools.NameGlob,
		Content:                `{"patterns":["**/*.go"]}`,
		ResultDone:             true,
		ToolCallDetailExpanded: false,
		ResultContent: strings.Join([]string{
			"a.go",
			"b.go",
			"",
			"(showing first 2 results within 4096 KiB; full results saved to /tmp/ws/artifacts/glob-results.log; refine pattern/path to narrow results)",
		}, "\n"),
	}

	collapsed := terminaltext.StripANSI(strings.Join(block.Render(120, ""), "\n"))
	if !strings.Contains(collapsed, "✓ ▸ glob **/*.go") || !strings.Contains(collapsed, "2 files") {
		t.Fatalf("expected glob collapsed summary, got:\n%s", collapsed)
	}

	block.ToggleAtWidth(120)
	expanded := terminaltext.StripANSI(strings.Join(block.Render(120, ""), "\n"))
	for _, want := range []string{"✓ ▾ glob **/*.go", "a.go", "(showing first 2 results"} {
		if !strings.Contains(expanded, want) {
			t.Fatalf("expected expanded glob to contain %q, got:\n%s", want, expanded)
		}
	}
}

func TestGrepHeaderShowsRelativeSearchPathInsideWorkingDir(t *testing.T) {
	wd := filepath.Join(string(os.PathSeparator), "tmp", "workspace")
	abs := filepath.Join(wd, "internal", "tui")
	block := &Block{
		ID:                1,
		Type:              BlockToolCall,
		ToolName:          "grep",
		Content:           fmt.Sprintf(`{"pattern":"TODO","paths":[%q],"includes":["*.go"]}`, abs),
		ResultDone:        true,
		displayWorkingDir: wd,
	}
	joined := terminaltext.StripANSI(strings.Join(block.Render(120, ""), "\n"))
	if !strings.Contains(joined, "grep TODO (paths=internal/tui, includes=*.go)") {
		t.Fatalf("expected grep header to show relative search path; got:\n%s", joined)
	}
	if strings.Contains(joined, abs) {
		t.Fatalf("did not expect grep header to show absolute path; got:\n%s", joined)
	}
}

func TestGrepHeaderHidesCurrentWorkingDirPath(t *testing.T) {
	wd := filepath.Join(string(os.PathSeparator), "tmp", "workspace")
	block := &Block{
		ID:                1,
		Type:              BlockToolCall,
		ToolName:          "grep",
		Content:           fmt.Sprintf(`{"pattern":"TODO","paths":[%q],"includes":["*.go"]}`, wd),
		ResultDone:        true,
		displayWorkingDir: wd,
	}
	joined := terminaltext.StripANSI(strings.Join(block.Render(120, ""), "\n"))
	if !strings.Contains(joined, "grep TODO (includes=*.go)") {
		t.Fatalf("expected grep header to hide current working directory path; got:\n%s", joined)
	}
	if strings.Contains(joined, "paths=") || strings.Contains(joined, wd) {
		t.Fatalf("did not expect grep header to show current working directory path; got:\n%s", joined)
	}
}

func TestGrepHeaderPrefersPatternAndElidesOptions(t *testing.T) {
	wd := filepath.Join(string(os.PathSeparator), "tmp", "workspace")
	pattern := `OverlayEvaluate|Evaluate\(|IsDisabled\(|MergedRuleset\(`
	block := &Block{
		ID:                1,
		Type:              BlockToolCall,
		ToolName:          "grep",
		Content:           fmt.Sprintf(`{"pattern":%q,"paths":[%q],"includes":["internal/permission/*.go","internal/agent/*.go"]}`, pattern, wd),
		ResultDone:        true,
		displayWorkingDir: wd,
	}
	joined := terminaltext.StripANSI(strings.Join(block.Render(120, ""), "\n"))
	if !strings.Contains(joined, pattern) {
		t.Fatalf("expected grep header to keep full pattern before options; got:\n%s", joined)
	}
	if strings.Contains(joined, "paths=") || strings.Contains(joined, wd) {
		t.Fatalf("did not expect grep header to show current working directory path; got:\n%s", joined)
	}
}

func TestGlobHeaderShowsRelativeSearchPathInsideWorkingDir(t *testing.T) {
	wd := filepath.Join(string(os.PathSeparator), "tmp", "workspace")
	abs := filepath.Join(wd, "internal")
	block := &Block{
		ID:                1,
		Type:              BlockToolCall,
		ToolName:          "glob",
		Content:           fmt.Sprintf(`{"patterns":["**/*.go"],"path":%q}`, abs),
		ResultDone:        true,
		displayWorkingDir: wd,
	}
	joined := terminaltext.StripANSI(strings.Join(block.Render(120, ""), "\n"))
	if !strings.Contains(joined, "glob **/*.go (path=internal)") {
		t.Fatalf("expected glob header to show relative search path; got:\n%s", joined)
	}
	if strings.Contains(joined, abs) {
		t.Fatalf("did not expect glob header to show absolute path; got:\n%s", joined)
	}
}

func TestSearchHeadersShowMultipleArrayParams(t *testing.T) {
	wd := filepath.Join(string(os.PathSeparator), "tmp", "workspace")
	cmd := filepath.Join(wd, "cmd")
	internal := filepath.Join(wd, "internal")
	grepBlock := &Block{
		ID:                1,
		Type:              BlockToolCall,
		ToolName:          "grep",
		Content:           fmt.Sprintf(`{"pattern":"TODO","paths":[%q,%q],"includes":["**/*.go","**/*.md"]}`, cmd, internal),
		ResultDone:        true,
		displayWorkingDir: wd,
	}
	grepJoined := terminaltext.StripANSI(strings.Join(grepBlock.Render(140, ""), "\n"))
	if !strings.Contains(grepJoined, "grep TODO (paths=cmd,internal, includes=**/*.go,**/*.md)") {
		t.Fatalf("expected grep header to show multiple paths/includes; got:\n%s", grepJoined)
	}
	if strings.Contains(grepJoined, cmd) || strings.Contains(grepJoined, internal) {
		t.Fatalf("did not expect grep header to show absolute paths; got:\n%s", grepJoined)
	}

	globBlock := &Block{
		ID:         2,
		Type:       BlockToolCall,
		ToolName:   "glob",
		Content:    `{"patterns":["**/*.go","**/*.md"],"path":"."}`,
		ResultDone: true,
	}
	globJoined := terminaltext.StripANSI(strings.Join(globBlock.Render(140, ""), "\n"))
	if !strings.Contains(globJoined, "glob **/*.go,**/*.md") {
		t.Fatalf("expected glob header to show multiple patterns; got:\n%s", globJoined)
	}
}

func TestCollapsedGrepShowsMatchCountSummary(t *testing.T) {
	block := &Block{
		ID:                     1,
		Type:                   BlockToolCall,
		ToolName:               "grep",
		Content:                `{"pattern":"TODO"}`,
		ResultContent:          "a.go:1:TODO\nb.go:2:TODO",
		ResultDone:             true,
		ToolCallDetailExpanded: false,
	}

	joined := terminaltext.StripANSI(strings.Join(block.Render(90, ""), "\n"))
	if !strings.Contains(joined, "2 matches") {
		t.Fatalf("expected Grep collapsed summary to show match count; got:\n%s", joined)
	}
}

func TestCollapsedGrepUsesKeySummaryForLowCounts(t *testing.T) {
	tests := []struct {
		name          string
		resultContent string
		wantPresent   string
		wantAbsent    string
	}{
		{
			name:          "zero matches",
			resultContent: "No matches found.",
			wantPresent:   "No matches",
			wantAbsent:    "0 matches",
		},
		{
			name:          "one match",
			resultContent: "a.go:1:TODO",
			wantPresent:   "1 match",
			wantAbsent:    "a.go:1:TODO",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			block := &Block{
				ID:                     1,
				Type:                   BlockToolCall,
				ToolName:               "grep",
				Content:                `{"pattern":"TODO"}`,
				ResultContent:          tt.resultContent,
				ResultDone:             true,
				ToolCallDetailExpanded: false,
			}

			joined := terminaltext.StripANSI(strings.Join(block.Render(90, ""), "\n"))
			if tt.wantPresent != "" && !strings.Contains(joined, tt.wantPresent) {
				t.Fatalf("expected output to contain %q; got:\n%s", tt.wantPresent, joined)
			}
			if tt.wantAbsent != "" && strings.Contains(joined, tt.wantAbsent) {
				t.Fatalf("expected output to omit %q; got:\n%s", tt.wantAbsent, joined)
			}
		})
	}
}

func TestExpandedShortGrepAndGlobResultsDoNotDuplicateContent(t *testing.T) {
	tests := []struct {
		name          string
		toolName      string
		resultContent string
		wantCounted   string
	}{
		{
			name:          "grep single match",
			toolName:      "grep",
			resultContent: "a.go:1:TODO",
			wantCounted:   "a.go:1:TODO",
		},
		{
			name:          "grep no matches",
			toolName:      "grep",
			resultContent: "No matches found.",
			wantCounted:   "No matches found.",
		},
		{
			name:          "glob single file",
			toolName:      "glob",
			resultContent: "a.go",
			wantCounted:   "a.go",
		},
		{
			name:          "glob no files",
			toolName:      "glob",
			resultContent: "No files matched the pattern.",
			wantCounted:   "No files matched the pattern.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			block := &Block{
				ID:                     1,
				Type:                   BlockToolCall,
				ToolName:               tt.toolName,
				Content:                `{"pattern":"TODO"}`,
				ResultContent:          tt.resultContent,
				ResultDone:             true,
				ToolCallDetailExpanded: true,
			}

			joined := terminaltext.StripANSI(strings.Join(block.Render(90, ""), "\n"))
			if got := strings.Count(joined, tt.wantCounted); got != 1 {
				t.Fatalf("expected %q exactly once in expanded card, got %d occurrences:\n%s", tt.wantCounted, got, joined)
			}
			if strings.Contains(joined, "[space]") {
				t.Fatalf("short search result should not show a disclosure footer, got:\n%s", joined)
			}
		})
	}
}

func TestExpandedGrepCountSummaryStillShowsAllMatches(t *testing.T) {
	block := &Block{
		ID:                     1,
		Type:                   BlockToolCall,
		ToolName:               "grep",
		Content:                `{"pattern":"TODO"}`,
		ResultContent:          "a.go:1:TODO\nb.go:2:TODO",
		ResultDone:             true,
		ToolCallDetailExpanded: true,
	}

	joined := terminaltext.StripANSI(strings.Join(block.Render(90, ""), "\n"))
	if !strings.Contains(joined, "2 matches") {
		t.Fatalf("expected expanded Grep card to keep the count summary, got:\n%s", joined)
	}
	for _, want := range []string{"a.go:1:TODO", "b.go:2:TODO"} {
		if strings.Count(joined, want) != 1 {
			t.Fatalf("expected %q exactly once in expanded card, got:\n%s", want, joined)
		}
	}
	if !strings.Contains(joined, "✓ ▾ grep TODO") {
		t.Fatalf("expected expanded card to show the expanded disclosure glyph, got:\n%s", joined)
	}
}

func TestGrepGlobForceExpandedHeuristicDoesNotBlockCollapse(t *testing.T) {
	// 11 result lines is exactly the case where the generic "one hidden line"
	// heuristic used to force the card expanded, which also made Space unable
	// to collapse it again (the toggle guard treats force-expanded as pinned).
	tests := []struct {
		name     string
		toolName string
		matches  []string
	}{
		{"grep", "grep", []string{"a.go:1:l0", "a.go:2:l1", "a.go:3:l2", "a.go:4:l3", "a.go:5:l4", "a.go:6:l5", "a.go:7:l6", "a.go:8:l7", "a.go:9:l8", "a.go:10:l9", "a.go:11:l10"}},
		{"glob", "glob", []string{"a0.go", "a1.go", "a2.go", "a3.go", "a4.go", "a5.go", "a6.go", "a7.go", "a8.go", "a9.go", "a10.go"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			block := &Block{
				ID:                     1,
				Type:                   BlockToolCall,
				ToolName:               tt.toolName,
				Content:                `{"pattern":"TODO"}`,
				ResultContent:          strings.Join(tt.matches, "\n"),
				ResultDone:             true,
				ToolCallDetailExpanded: false,
			}

			// Collapsed: the count summary must win over any force-expand.
			collapsed := terminaltext.StripANSI(strings.Join(block.Render(120, ""), "\n"))
			if !strings.Contains(collapsed, "✓ ▸ "+tt.toolName) {
				t.Fatalf("expected collapsed card to show the collapsed disclosure glyph, got:\n%s", collapsed)
			}
			for _, m := range tt.matches {
				if strings.Contains(collapsed, m) {
					t.Fatalf("expected collapsed card to hide match line %q, got:\n%s", m, collapsed)
				}
			}

			// Space expands...
			block.ToggleAtWidth(120)
			expanded := terminaltext.StripANSI(strings.Join(block.Render(120, ""), "\n"))
			if !strings.Contains(expanded, "✓ ▾ "+tt.toolName) {
				t.Fatalf("expected expanded card to show the expanded disclosure glyph, got:\n%s", expanded)
			}
			if !strings.Contains(expanded, tt.matches[0]) || !strings.Contains(expanded, tt.matches[len(tt.matches)-1]) {
				t.Fatalf("expected expanded card to show all matches, got:\n%s", expanded)
			}

			// ...and a second Space collapses again.
			block.ToggleAtWidth(120)
			recollapsed := terminaltext.StripANSI(strings.Join(block.Render(120, ""), "\n"))
			if !strings.Contains(recollapsed, "✓ ▸ "+tt.toolName) {
				t.Fatalf("expected second toggle to restore collapsed glyph, got:\n%s", recollapsed)
			}
		})
	}
}

func TestCollapsedGlobShowsFileCountSummary(t *testing.T) {
	block := &Block{
		ID:                     1,
		Type:                   BlockToolCall,
		ToolName:               "glob",
		Content:                `{"patterns":["**/*.go"]}`,
		ResultContent:          "a.go\nb.go\nc.go",
		ResultDone:             true,
		ToolCallDetailExpanded: false,
	}

	joined := terminaltext.StripANSI(strings.Join(block.Render(90, ""), "\n"))
	if !strings.Contains(joined, "3 files") {
		t.Fatalf("expected Glob collapsed summary to show file count; got:\n%s", joined)
	}
}

func TestCollapsedGlobUsesKeySummaryForLowCounts(t *testing.T) {
	tests := []struct {
		name          string
		resultContent string
		wantPresent   string
		wantAbsent    string
	}{
		{
			name:          "zero files",
			resultContent: "No files matched the pattern.",
			wantPresent:   "No files",
			wantAbsent:    "No files matched the pattern.",
		},
		{
			name:          "one file",
			resultContent: "a.go",
			wantPresent:   "1 file",
			wantAbsent:    "a.go",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			block := &Block{
				ID:                     1,
				Type:                   BlockToolCall,
				ToolName:               "glob",
				Content:                `{"patterns":["**/*.go"]}`,
				ResultContent:          tt.resultContent,
				ResultDone:             true,
				ToolCallDetailExpanded: false,
			}

			joined := terminaltext.StripANSI(strings.Join(block.Render(90, ""), "\n"))
			if tt.wantPresent != "" && !strings.Contains(joined, tt.wantPresent) {
				t.Fatalf("expected output to contain %q; got:\n%s", tt.wantPresent, joined)
			}
			if tt.wantAbsent != "" && strings.Contains(joined, tt.wantAbsent) {
				t.Fatalf("expected output to omit %q; got:\n%s", tt.wantAbsent, joined)
			}
		})
	}
}
