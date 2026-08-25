package lsp

import (
	"path/filepath"
	"strings"

	"github.com/keakon/chord/internal/message"
)

// RebuildReportedDiagnosticsFromMessages recovers the other-file diagnostics a
// restored transcript already showed, keyed by absolute path. Resume otherwise
// starts with an empty suppression window and re-announces diagnostics that are
// already visible earlier in the same conversation.
//
// The rendered form is the source of truth here: diagnosticIdentityKey excludes
// exactly the fields that a round-trip through rendered text cannot carry, so
// ParseToolOutputDiagnostics reconstructs identities that match the ones
// recorded when the lines were first emitted.
func RebuildReportedDiagnosticsFromMessages(msgs []message.Message, projectRoot string) map[string][]Diagnostic {
	var out map[string][]Diagnostic
	for _, msg := range msgs {
		if msg.Role != "tool" || !strings.Contains(msg.Content, otherFilesDiagnosticsHeader) {
			continue
		}
		for path, diags := range parseOtherFileDiagnosticsSection(msg.Content, projectRoot) {
			if out == nil {
				out = make(map[string][]Diagnostic)
			}
			out[path] = append(out[path], diags...)
		}
	}
	return out
}

// parseOtherFileDiagnosticsSection reads the "LSP diagnostics in other files:"
// block of one tool result. Paths are rendered relative to the tool working
// directory when they sit inside it, so relative entries resolve against the
// project root.
func parseOtherFileDiagnosticsSection(content, projectRoot string) map[string][]Diagnostic {
	_, section, found := strings.Cut(content, otherFilesDiagnosticsHeader)
	if !found {
		return nil
	}
	var (
		out         map[string][]Diagnostic
		currentPath string
	)
	for raw := range strings.SplitSeq(section, "\n") {
		line := strings.TrimRight(raw, " \t")
		if strings.TrimSpace(line) == "" {
			continue
		}
		// A path label is the only non-diagnostic line inside the section.
		if !strings.HasPrefix(strings.TrimSpace(line), "[") && strings.HasSuffix(line, ":") {
			currentPath = resolveReportedDiagnosticPath(strings.TrimSuffix(strings.TrimSpace(line), ":"), projectRoot)
			continue
		}
		if currentPath == "" {
			continue
		}
		diags := ParseToolOutputDiagnostics(line)
		if len(diags) == 0 {
			// The omitted-count footer and any trailing prose end the block for
			// this file rather than attaching to the next one.
			continue
		}
		if out == nil {
			out = make(map[string][]Diagnostic)
		}
		out[currentPath] = append(out[currentPath], diags...)
	}
	return out
}

func resolveReportedDiagnosticPath(path, projectRoot string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if !filepath.IsAbs(path) && projectRoot != "" {
		path = filepath.Join(projectRoot, path)
	}
	return normalizeWaiterPath(path)
}
