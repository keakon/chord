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

	out := mgr.appendLSPDiagnosticsToToolOutput("ok", edited, true, nil, config.DiagnosticOutputConfig{MaxTotalDiagnostics: 1})
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
	for i := 0; i < ToolOutputMaxOtherErrorFiles+1; i++ {
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
