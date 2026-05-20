package lsp

import (
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
	if !strings.Contains(out, "1 more diagnostics omitted") {
		t.Fatalf("expected omitted count, got %q", out)
	}
}

func TestFormatDiagnosticsBlockWithRangesHidesInfoAndHints(t *testing.T) {
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
	if strings.Contains(out, "info") || strings.Contains(out, "hint") {
		t.Fatalf("expected info/hint hidden, got %q", out)
	}
	if !strings.Contains(out, "warning") {
		t.Fatalf("expected warning, got %q", out)
	}
}

func TestFormatDiagnosticsBlockWithoutRangesHidesInfoAndHints(t *testing.T) {
	diags := []Diagnostic{
		{Severity: 3, Line: 1, Message: "info"},
		{Severity: 4, Line: 2, Message: "hint"},
		{Severity: 2, Line: 3, Message: "warning"},
	}
	out := formatDiagnosticsBlockWithRanges("", diags, config.DiagnosticOutputConfig{MaxTotalDiagnostics: 10}, nil, true)
	if strings.Contains(out, "info") || strings.Contains(out, "hint") {
		t.Fatalf("expected info/hint hidden without ranges, got %q", out)
	}
	if !strings.Contains(out, "warning") {
		t.Fatalf("expected warning, got %q", out)
	}
}

func TestAppendLSPDiagnosticsToToolOutput_OtherFilesApplyOutputFilter(t *testing.T) {
	mgr := NewManager(&config.Config{}, t.TempDir(), nil)
	edited := filepath.Join(t.TempDir(), "edited.py")
	other := filepath.Join(t.TempDir(), "other.py")
	out := mgr.appendLSPDiagnosticsToToolOutput("ok", edited, true, false, nil, config.DiagnosticOutputConfig{MaxTotalDiagnostics: 10})
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

	out = mgr.appendLSPDiagnosticsToToolOutput("ok", edited, true, false, nil, config.DiagnosticOutputConfig{MaxTotalDiagnostics: 10})
	if !strings.Contains(out, "edited warning") {
		t.Fatalf("expected edited file diagnostics kept, got %q", out)
	}
	if !strings.Contains(out, "other error") {
		t.Fatalf("expected other-file error kept, got %q", out)
	}
	if strings.Contains(out, "other info") || strings.Contains(out, "other hint") {
		t.Fatalf("expected other-file info/hint filtered, got %q", out)
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
