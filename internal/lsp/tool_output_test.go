package lsp

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/keakon/x/powernap/pkg/lsp/protocol"

	"github.com/keakon/chord/internal/config"
)

func TestFormatDiagLine(t *testing.T) {
	s := formatDiagLine(Diagnostic{Line: 0, Col: 2, Message: "x", Source: "compiler", Severity: 1})
	if s != "[E] 1:3 x" {
		t.Fatalf("unexpected format: %q", s)
	}
}

func TestFormatDiagLineIncludesCode(t *testing.T) {
	s := formatDiagLine(Diagnostic{Line: 0, Col: 2, Code: "F821", Message: "Undefined name", Severity: 1})
	if s != "[E] 1:3 [F821] Undefined name" {
		t.Fatalf("unexpected format: %q", s)
	}
}

func TestParseToolOutputDiagnostics(t *testing.T) {
	got := ParseToolOutputDiagnostics("ok\n\nDiagnostics:\n[E] 3:4 [F821] Undefined name\n[W] 5:1 warning")
	if len(got) != 2 {
		t.Fatalf("parsed diagnostics = %#v, want two entries", got)
	}
	if got[0].Severity != 1 || got[0].Line != 2 || got[0].Col != 3 || got[0].Code != "F821" || got[0].Message != "Undefined name" {
		t.Fatalf("first diagnostic = %#v", got[0])
	}
	if got[1].Severity != 2 || got[1].Line != 4 || got[1].Col != 0 || got[1].Message != "warning" {
		t.Fatalf("second diagnostic = %#v", got[1])
	}
}

func TestAppendLSPDiagnosticsToToolOutputForPathsUsesSharedLimitAndFileLabels(t *testing.T) {
	mgr := &Manager{}
	pathA := "/tmp/a.go"
	pathB := "/tmp/b.go"
	mgr.diagByServer = map[string]map[string]diagCounts{}
	// Feed the batch formatter through its non-LSP extras input so this test
	// does not require starting a language server.
	extras := map[string][]Diagnostic{
		pathA: {{Severity: 1, Line: 0, Col: 0, Message: "a"}},
		pathB: {{Severity: 2, Line: 1, Col: 1, Message: "b"}},
	}
	out := mgr.AppendLSPDiagnosticsToToolOutputForPaths("Applied patch", []string{pathA, pathB}, false, nil, nil, extras)
	if !strings.Contains(out, pathA+":\n[E] 1:1 a") || !strings.Contains(out, pathB+":\n[W] 2:2 b") {
		t.Fatalf("batch output = %q", out)
	}
}

func TestAppendLSPDiagnosticsToToolOutputForPathsUsesBatchBudget(t *testing.T) {
	mgr := &Manager{}
	paths := []string{"/tmp/a.go", "/tmp/b.go"}
	extras := map[string][]Diagnostic{}
	for _, path := range paths {
		for i := range 6 {
			extras[path] = append(extras[path], Diagnostic{Severity: 1, Line: i, Message: path})
		}
	}
	out := mgr.AppendLSPDiagnosticsToToolOutputForPaths("Applied patch", paths, false, nil, nil, extras)
	if got := countFormattedDiagnostics(out); got != ToolOutputMaxDiagnosticsPerBatch {
		t.Fatalf("formatted diagnostics = %d, want batch budget %d\n%s", got, ToolOutputMaxDiagnosticsPerBatch, out)
	}
	if !strings.Contains(out, "diagnostics not shown due to output limits") {
		t.Fatalf("output = %q, want omitted diagnostics summary", out)
	}
}

func TestAppendLSPDiagnosticsToToolOutputForPathsReportsChangesPerFile(t *testing.T) {
	mgr := &Manager{}
	pathA, pathB := "/tmp/a.go", "/tmp/b.go"
	key := func(message string) []Diagnostic { return []Diagnostic{{Severity: 1, Line: 0, Message: message}} }
	baselines := map[string][]Diagnostic{pathA: key("old"), pathB: key("gone")}
	extras := map[string][]Diagnostic{pathA: key("new"), pathB: nil}
	out := mgr.AppendLSPDiagnosticsToToolOutputForPaths("Applied patch", []string{pathA, pathB}, false, baselines, nil, extras)
	if !strings.Contains(out, "Diagnostics changed for "+pathA+": Diagnostics changed: 1 new, 1 resolved.") {
		t.Fatalf("output = %q, want new/resolved summary for %s", out, pathA)
	}
	if !strings.Contains(out, "Diagnostics changed for "+pathB+": Diagnostics changed: 0 new, 1 resolved.") {
		t.Fatalf("output = %q, want resolved summary for %s", out, pathB)
	}
}

func TestFormatDiagnosticsBlockWithRangesPrioritizesNearDiagnostics(t *testing.T) {
	diags := []Diagnostic{
		{Severity: 1, Line: 300, Col: 0, Message: "far"},
		{Severity: 1, Line: 12, Col: 0, Message: "near"},
		{Severity: 2, Line: 11, Col: 0, Message: "near warning"},
	}
	out := formatDiagnosticsBlockWithRanges("", diags, config.DiagnosticOutputConfig{
		MaxNearDiagnostics:    1,
		MaxOutsideDiagnostics: 1,
		MaxTotalDiagnostics:   2,
		NearRangeBeforeLines:  2,
		NearRangeAfterLines:   2,
	}, []EditRange{{StartLine: 10, EndLine: 10}}, true)
	if !strings.Contains(out, "near") || !strings.Contains(out, "far") {
		t.Fatalf("expected near and outside diagnostics, got %q", out)
	}
	if strings.Contains(out, "near warning") {
		t.Fatalf("expected warning omitted by near limit, got %q", out)
	}
	if !strings.Contains(out, "1 diagnostics not shown due to output limits; they may still need fixing.") {
		t.Fatalf("expected omitted count warning, got %q", out)
	}
}

func TestFormatDiagnosticsBlockFillsLimitWithInfoAndHints(t *testing.T) {
	diags := []Diagnostic{
		{Severity: 4, Line: 4, Message: "hint"},
		{Severity: 3, Line: 3, Message: "info"},
		{Severity: 2, Line: 2, Message: "warning"},
	}
	out := formatDiagnosticsBlockWithRanges("", diags, config.DiagnosticOutputConfig{MaxTotalDiagnostics: 10}, nil, true)
	for _, want := range []string{"warning", "info", "hint"} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected %q included when E/W do not fill the limit, got %q", want, out)
		}
	}
}

func TestFormatDiagnosticsBlockPrioritizesErrorsWarningsOverInfoHints(t *testing.T) {
	diags := []Diagnostic{
		{Severity: 3, Line: 0, Message: "info"},
		{Severity: 4, Line: 1, Message: "hint"},
		{Severity: 2, Line: 2, Message: "warning one"},
		{Severity: 1, Line: 3, Message: "error"},
		{Severity: 2, Line: 4, Message: "warning two"},
	}
	out := formatDiagnosticsBlockWithRanges("", diags, config.DiagnosticOutputConfig{MaxTotalDiagnostics: 3}, nil, true)
	for _, want := range []string{"error", "warning one", "warning two"} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected priority diagnostic %q, got %q", want, out)
		}
	}
	if strings.Contains(out, "info") || strings.Contains(out, "hint") {
		t.Fatalf("expected info/hint omitted when E/W fill limit, got %q", out)
	}
	if !strings.Contains(out, "2 diagnostics not shown due to output limits; they may still need fixing.") {
		t.Fatalf("expected omitted count warning, got %q", out)
	}
}

func TestFormatDiagnosticsBlockWithRangesPrioritizesErrorsWarningsAcrossRanges(t *testing.T) {
	diags := []Diagnostic{
		{Severity: 3, Line: 10, Message: "near info"},
		{Severity: 4, Line: 11, Message: "near hint"},
		{Severity: 2, Line: 300, Message: "far warning"},
	}
	out := formatDiagnosticsBlockWithRanges("", diags, config.DiagnosticOutputConfig{
		MaxNearDiagnostics:    2,
		MaxOutsideDiagnostics: 1,
		MaxTotalDiagnostics:   2,
		NearRangeAfterLines:   1,
		NearRangeBeforeLines:  1,
	}, []EditRange{{StartLine: 10, EndLine: 10}}, true)
	for _, want := range []string{"far warning", "near info"} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected %q included, got %q", want, out)
		}
	}
	if strings.Contains(out, "near hint") {
		t.Fatalf("expected one info/hint omitted after outside warning gets priority, got %q", out)
	}
}

func TestFormatDiagnosticsBlockWithRangesIncludesInfoAndHintsWhenSlotsAvailable(t *testing.T) {
	diags := []Diagnostic{
		{Severity: 3, Line: 10, Message: "info"},
		{Severity: 4, Line: 10, Message: "hint"},
		{Severity: 2, Line: 10, Message: "warning"},
	}
	out := formatDiagnosticsBlockWithRanges("", diags, config.DiagnosticOutputConfig{
		MaxNearDiagnostics:   10,
		MaxTotalDiagnostics:  10,
		NearRangeAfterLines:  1,
		NearRangeBeforeLines: 1,
	}, []EditRange{{StartLine: 10, EndLine: 10}}, true)
	if !strings.Contains(out, "warning") || !strings.Contains(out, "info") || !strings.Contains(out, "hint") {
		t.Fatalf("expected warning/info/hint included when slots are available, got %q", out)
	}
}

func TestFormatDiagnosticsBlockWithoutRangesIncludesInfoAndHintsWhenSlotsAvailable(t *testing.T) {
	diags := []Diagnostic{
		{Severity: 3, Line: 1, Message: "info"},
		{Severity: 4, Line: 2, Message: "hint"},
		{Severity: 2, Line: 3, Message: "warning"},
	}
	out := formatDiagnosticsBlockWithRanges("", diags, config.DiagnosticOutputConfig{MaxTotalDiagnostics: 10}, nil, true)
	if !strings.Contains(out, "warning") || !strings.Contains(out, "info") || !strings.Contains(out, "hint") {
		t.Fatalf("expected warning/info/hint included when slots are available, got %q", out)
	}
}

func TestAppendLSPDiagnosticsToToolOutput_OtherFilesWithoutPrimaryHaveNoBlankLineAfterHeader(t *testing.T) {
	tmp := t.TempDir()
	mgr := NewManager(&config.Config{}, tmp, nil)
	edited := filepath.Join(tmp, "edited.py")
	other := filepath.Join(tmp, "other.py")
	mgr.clientsMu.Lock()
	mgr.clients["test"] = &Client{diagnostics: map[protocol.DocumentURI][]protocol.Diagnostic{
		protocol.DocumentURI("file://" + filepath.ToSlash(other)): {
			{Severity: protocol.SeverityInformation, Range: protocol.Range{Start: protocol.Position{Line: 1, Character: 0}}, Message: "other info"},
		},
	}}
	mgr.clientsMu.Unlock()

	out := mgr.appendLSPDiagnosticsToToolOutput("ok", edited, true, nil, config.DiagnosticOutputConfig{MaxTotalDiagnostics: 10})
	if strings.Contains(out, "Diagnostics:\n\n") {
		t.Fatalf("expected first diagnostics block to follow header without blank line, got %q", out)
	}
	if !strings.Contains(out, "Diagnostics:\nLSP diagnostics in other files:") {
		t.Fatalf("expected other-file diagnostics directly after header, got %q", out)
	}
}

func TestAppendLSPDiagnosticsToToolOutput_LimitsDiagnosticsGlobally(t *testing.T) {
	tmp := t.TempDir()
	mgr := NewManager(&config.Config{}, tmp, nil)
	edited := filepath.Join(tmp, "edited.py")
	diagnostics := map[protocol.DocumentURI][]protocol.Diagnostic{
		protocol.DocumentURI("file://" + filepath.ToSlash(edited)): {},
	}
	for i := range 6 {
		diagnostics[protocol.DocumentURI("file://"+filepath.ToSlash(edited))] = append(diagnostics[protocol.DocumentURI("file://"+filepath.ToSlash(edited))], protocol.Diagnostic{
			Severity: protocol.SeverityWarning,
			Range:    protocol.Range{Start: protocol.Position{Line: uint32(i), Character: 0}},
			Message:  fmt.Sprintf("edited warning %d", i),
		})
	}
	for i := range 6 {
		path := filepath.Join(tmp, fmt.Sprintf("other%d.py", i))
		diagnostics[protocol.DocumentURI("file://"+filepath.ToSlash(path))] = []protocol.Diagnostic{
			{Severity: protocol.SeverityWarning, Range: protocol.Range{Start: protocol.Position{Line: 0, Character: 0}}, Message: fmt.Sprintf("other warning %d", i)},
		}
	}
	mgr.clientsMu.Lock()
	mgr.clients["test"] = &Client{diagnostics: diagnostics}
	mgr.clientsMu.Unlock()

	out := mgr.appendLSPDiagnosticsToToolOutput("ok", edited, true, nil, config.DiagnosticOutputConfig{MaxTotalDiagnostics: 10})
	if got := countFormattedDiagnostics(out); got != 10 {
		t.Fatalf("formatted diagnostics = %d, want 10\n%s", got, out)
	}
	if !strings.Contains(out, "edited warning 5") {
		t.Fatalf("expected current file diagnostics to consume budget first, got %q", out)
	}
	if strings.Contains(out, "other warning 4") || strings.Contains(out, "other warning 5") {
		t.Fatalf("expected global budget to omit diagnostics after 10 total, got %q", out)
	}
}

func countFormattedDiagnostics(s string) int {
	count := 0
	for line := range strings.SplitSeq(s, "\n") {
		if strings.HasPrefix(line, "[E] ") || strings.HasPrefix(line, "[W] ") || strings.HasPrefix(line, "[I] ") || strings.HasPrefix(line, "[H] ") || strings.HasPrefix(line, "[?] ") {
			count++
		}
	}
	return count
}

func TestAppendLSPDiagnosticsToToolOutput_OtherFilesIncludeInfoHintsWhenSlotsAvailable(t *testing.T) {
	mgr := NewManager(&config.Config{}, t.TempDir(), nil)
	edited := filepath.Join(t.TempDir(), "edited.py")
	other := filepath.Join(t.TempDir(), "other.py")
	out := mgr.appendLSPDiagnosticsToToolOutput("ok", edited, true, nil, config.DiagnosticOutputConfig{MaxTotalDiagnostics: 10})
	if out != "ok" {
		t.Fatalf("expected unchanged output when manager empty, got %q", out)
	}

	mgr.clientsMu.Lock()
	mgr.clients["test"] = &Client{diagnostics: map[protocol.DocumentURI][]protocol.Diagnostic{
		protocol.DocumentURI("file://" + filepath.ToSlash(edited)): {
			{Severity: protocol.SeverityWarning, Range: protocol.Range{Start: protocol.Position{Line: 0, Character: 0}}, Message: "edited warning"},
		},
		protocol.DocumentURI("file://" + filepath.ToSlash(other)): {
			{Severity: protocol.SeverityInformation, Range: protocol.Range{Start: protocol.Position{Line: 1, Character: 0}}, Message: "other info"},
			{Severity: protocol.SeverityHint, Range: protocol.Range{Start: protocol.Position{Line: 2, Character: 0}}, Message: "other hint"},
			{Severity: protocol.SeverityError, Range: protocol.Range{Start: protocol.Position{Line: 3, Character: 0}}, Message: "other error"},
		},
	}}
	mgr.clientsMu.Unlock()

	out = mgr.appendLSPDiagnosticsToToolOutput("ok", edited, true, nil, config.DiagnosticOutputConfig{MaxTotalDiagnostics: 10})
	if !strings.Contains(out, "edited warning") {
		t.Fatalf("expected edited file diagnostics kept, got %q", out)
	}
	if strings.Contains(out, "Diagnostics:\n\n[W]") {
		t.Fatalf("expected no blank line after Diagnostics header, got %q", out)
	}
	if !strings.Contains(out, "other error") {
		t.Fatalf("expected other-file error kept, got %q", out)
	}
	if !strings.Contains(out, "other info") || !strings.Contains(out, "other hint") {
		t.Fatalf("expected other-file info/hint included when slots are available, got %q", out)
	}
}

func TestFormatDiagnosticsBlockWithRangesIncludesOnlyInfoHints(t *testing.T) {
	diags := []Diagnostic{
		{Severity: 3, Line: 10, Message: "info"},
		{Severity: 4, Line: 11, Message: "hint"},
	}
	out := formatDiagnosticsBlockWithRanges("", diags, config.DiagnosticOutputConfig{
		MaxNearDiagnostics:   10,
		MaxTotalDiagnostics:  10,
		NearRangeAfterLines:  1,
		NearRangeBeforeLines: 1,
	}, []EditRange{{StartLine: 10, EndLine: 10}}, true)
	if !strings.Contains(out, "info") || !strings.Contains(out, "hint") {
		t.Fatalf("expected info/hint diagnostics included when slots are available, got %q", out)
	}
}

func TestFormatDiagnosticsBlockWithoutRangesIncludesOnlyInfoHints(t *testing.T) {
	diags := []Diagnostic{
		{Severity: 3, Line: 1, Message: "info"},
		{Severity: 4, Line: 2, Message: "hint"},
	}
	out := formatDiagnosticsBlockWithRanges("", diags, config.DiagnosticOutputConfig{MaxTotalDiagnostics: 10}, nil, true)
	if !strings.Contains(out, "info") || !strings.Contains(out, "hint") {
		t.Fatalf("expected info/hint diagnostics included when slots are available, got %q", out)
	}
}

func TestAppendLSPDiagnosticsToToolOutput_OmitsCleanDiagnosticsStatus(t *testing.T) {
	mgr := NewManager(&config.Config{}, t.TempDir(), nil)
	edited := filepath.Join(t.TempDir(), "edited.go")
	out := mgr.appendLSPDiagnosticsToToolOutput("ok", edited, false, nil, config.DiagnosticOutputConfig{})
	if out != "ok" {
		t.Fatalf("expected unchanged output when diagnostics are clean, got %q", out)
	}
}

func TestAppendLSPDiagnosticsToToolOutput_IncludesOnlyInfoHintsWhenSlotsAvailable(t *testing.T) {
	tmp := t.TempDir()
	mgr := NewManager(&config.Config{}, tmp, nil)
	edited := filepath.Join(tmp, "edited.py")
	mgr.clientsMu.Lock()
	mgr.clients["test"] = &Client{diagnostics: map[protocol.DocumentURI][]protocol.Diagnostic{
		protocol.DocumentURI("file://" + filepath.ToSlash(edited)): {
			{Severity: protocol.SeverityInformation, Range: protocol.Range{Start: protocol.Position{Line: 0, Character: 0}}, Message: "info"},
			{Severity: protocol.SeverityHint, Range: protocol.Range{Start: protocol.Position{Line: 1, Character: 0}}, Message: "hint"},
		},
	}}
	mgr.clientsMu.Unlock()

	out := mgr.appendLSPDiagnosticsToToolOutput("ok", edited, false, []EditRange{{StartLine: 0, EndLine: 0}}, config.DiagnosticOutputConfig{MaxTotalDiagnostics: 10})
	if !strings.Contains(out, "info") || !strings.Contains(out, "hint") {
		t.Fatalf("expected only info/hint diagnostics included when slots are available, got %q", out)
	}
}

func TestAppendLSPDiagnosticsToToolOutput_CurrentFileHintsPrecedeOtherErrors(t *testing.T) {
	tmp := t.TempDir()
	mgr := NewManager(&config.Config{}, tmp, nil)
	edited := filepath.Join(tmp, "edited.py")
	other := filepath.Join(tmp, "other.py")
	mgr.clientsMu.Lock()
	mgr.clients["test"] = &Client{diagnostics: map[protocol.DocumentURI][]protocol.Diagnostic{
		protocol.DocumentURI("file://" + filepath.ToSlash(edited)): {
			{Severity: protocol.SeverityHint, Range: protocol.Range{Start: protocol.Position{Line: 0, Character: 0}}, Message: "edited hint"},
		},
		protocol.DocumentURI("file://" + filepath.ToSlash(other)): {
			{Severity: protocol.SeverityError, Range: protocol.Range{Start: protocol.Position{Line: 0, Character: 0}}, Message: "other error"},
		},
	}}
	mgr.clientsMu.Unlock()

	out := mgr.appendLSPDiagnosticsToToolOutput("ok", edited, true, nil, config.DiagnosticOutputConfig{MaxTotalDiagnostics: 2})
	if !strings.Contains(out, "edited hint") || !strings.Contains(out, "other error") {
		t.Fatalf("expected current-file hint and other-file error included, got %q", out)
	}
	if strings.Index(out, "edited hint") > strings.Index(out, "other error") {
		t.Fatalf("expected current-file hint to be shown before other-file error, got %q", out)
	}
}

func TestAppendLSPDiagnosticsToToolOutput_LimitsOtherFilesAfterSelection(t *testing.T) {
	tmp := t.TempDir()
	mgr := NewManager(&config.Config{}, tmp, nil)
	edited := filepath.Join(tmp, "edited.py")
	diagnostics := map[protocol.DocumentURI][]protocol.Diagnostic{}
	for i := range ToolOutputMaxOtherErrorFiles + 1 {
		path := filepath.Join(tmp, fmt.Sprintf("other%d.py", i))
		diagnostics[protocol.DocumentURI("file://"+filepath.ToSlash(path))] = []protocol.Diagnostic{
			{Severity: protocol.SeverityInformation, Range: protocol.Range{Start: protocol.Position{Line: 0, Character: 0}}, Message: fmt.Sprintf("info %d", i)},
		}
	}
	mgr.clientsMu.Lock()
	mgr.clients["test"] = &Client{diagnostics: diagnostics}
	mgr.clientsMu.Unlock()

	out := mgr.appendLSPDiagnosticsToToolOutput("ok", edited, true, nil, config.DiagnosticOutputConfig{MaxTotalDiagnostics: 10})
	if got := strings.Count(out, "LSP diagnostics in other files:"); got != ToolOutputMaxOtherErrorFiles {
		t.Fatalf("other file blocks = %d, want %d\n%s", got, ToolOutputMaxOtherErrorFiles, out)
	}
	if !strings.Contains(out, "info 0") {
		t.Fatalf("expected info/hint-only other files to be eligible when slots are available, got %q", out)
	}
}

func TestEditRangesForReplacement(t *testing.T) {
	ranges := EditRangesForReplacement("a\nb\nc\nb\n", "b", "bb\ncc", true)
	if len(ranges) != 2 {
		t.Fatalf("len(ranges) = %d, want 2", len(ranges))
	}
	if ranges[0] != (EditRange{StartLine: 1, EndLine: 2}) || ranges[1] != (EditRange{StartLine: 3, EndLine: 4}) {
		t.Fatalf("ranges = %+v", ranges)
	}
}
