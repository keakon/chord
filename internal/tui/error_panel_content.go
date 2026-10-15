package tui

import (
	"fmt"
	"slices"
	"strings"

	"github.com/keakon/chord/internal/tui/terminaltext"
)

// errorPanelLines builds the wrapped, styled content lines for the error panel,
// newest error first. Results are cached per (width, renderVersion).
func (m *Model) errorPanelLines(innerWidth int) []string {
	if innerWidth <= 0 {
		innerWidth = 60
	}
	if m.errorPanel.linesCacheLines != nil &&
		m.errorPanel.linesCacheWidth == innerWidth &&
		m.errorPanel.linesCacheVer == m.errorPanel.renderVersion {
		return m.errorPanel.linesCacheLines
	}

	records := m.snapshotAgentErrors()
	var lines []string
	if len(records) == 0 {
		lines = []string{currentStyles.DimStyle.Render("No errors recorded yet.")}
	} else {
		// Newest first so the most recent failure is visible without scrolling.
		for i, record := range slices.Backward(records) {
			lines = append(lines, formatErrorRecordLines(record, innerWidth)...)
			if i > 0 {
				lines = append(lines, "")
			}
		}
	}

	lines = wrapDialogLines(lines, innerWidth)
	m.errorPanel.linesCacheLines = lines
	m.errorPanel.linesCacheWidth = innerWidth
	m.errorPanel.linesCacheVer = m.errorPanel.renderVersion
	return lines
}

// errorRecordHeader builds the single-line header shared by the panel display
// and clipboard output: time, agent, provider/model, masked key, account, and
// the provider error code/type fields.
func errorRecordHeader(rec agentErrorRecord, ts string) string {
	header := ts
	// Only show agent ID when it's not the default main agent (useful for sub-agents)
	if rec.AgentID != "" && rec.AgentID != "main" && rec.AgentID != "main-1" {
		header += fmt.Sprintf("  %s", rec.AgentID)
	}
	if rec.Provider != "" {
		header += fmt.Sprintf("  %s", rec.Provider)
		if rec.Model != "" {
			header += fmt.Sprintf("/%s", rec.Model)
		}
	} else if rec.Model != "" {
		header += fmt.Sprintf("  %s", rec.Model)
	}
	if rec.MaskedKey != "" {
		header += fmt.Sprintf("  key=%s", rec.MaskedKey)
	}
	if rec.Email != "" {
		header += fmt.Sprintf("  email=%s", rec.Email)
	} else if rec.AccountID != "" {
		header += fmt.Sprintf("  account=%s", rec.AccountID)
	}
	if rec.StatusCode != 0 {
		header += fmt.Sprintf("  HTTP %d", rec.StatusCode)
	}
	if rec.ErrorCode != "" {
		header += fmt.Sprintf("  code=%s", rec.ErrorCode)
	}
	// Type is often generic like "invalid_request_error" or always "new_api_error", skip if not useful
	if rec.ErrorType != "" && rec.ErrorType != "new_api_error" {
		header += fmt.Sprintf("  type=%s", rec.ErrorType)
	}
	return header
}

// formatErrorRecordLines renders one error record into styled, width-wrapped
// display lines: the header followed by the wrapped error message.
func formatErrorRecordLines(rec agentErrorRecord, width int) []string {
	header := errorRecordHeader(rec, rec.Timestamp.Format("15:04:05"))
	out := []string{currentStyles.ErrorStyle.Render(terminaltext.Sanitize(header))}
	msg := strings.TrimSpace(rec.Message)
	if msg == "" {
		msg = "(no message)"
	}
	// Sanitize before wrapping: provider messages can carry raw control
	// characters (CRLF line endings in an HTML error page, stray ESC bytes).
	// A literal \r survives the ASCII wrap path and, when the dialog line is
	// rasterized, rewinds the cursor to column 0 so the trailing padding
	// repaints the row from the left, shredding the panel.
	// Include the two-column message indent in wrapping. Adding it after
	// wrapping makes the measured line width differ from the rendered width.
	return append(out, terminaltext.Wrap(terminaltext.Sanitize("  "+msg), width)...)
}

// plainErrorRecordText renders one record as unstyled, unwrapped text for the
// clipboard: the shared header fields with a full date, then the complete
// message on its own line.
func plainErrorRecordText(rec agentErrorRecord) string {
	msg := strings.TrimSpace(terminaltext.Sanitize(rec.Message))
	if msg == "" {
		msg = "(no message)"
	}
	header := terminaltext.Sanitize(errorRecordHeader(rec, rec.Timestamp.Format("2006-01-02 15:04:05")))
	// Display sanitization inserts cluster breaks that do not belong in copied text.
	return terminaltext.RestoreModifiers(header + "\n" + msg)
}
