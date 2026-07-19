package tui

import (
	"fmt"
	"slices"
	"strings"
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
		lines = []string{DimStyle.Render("No errors recorded in this session.")}
	} else {
		// Newest first so the most recent failure is visible without scrolling.
		for i, record := range slices.Backward(records) {
			lines = append(lines, formatErrorRecordLines(record, innerWidth)...)
			if i > 0 {
				lines = append(lines, "")
			}
		}
	}

	m.errorPanel.linesCacheLines = lines
	m.errorPanel.linesCacheWidth = innerWidth
	m.errorPanel.linesCacheVer = m.errorPanel.renderVersion
	return lines
}

// formatErrorRecordLines renders one error record into styled, width-wrapped
// display lines: a header (time, agent, HTTP status, provider code/type)
// followed by the wrapped error message.
func formatErrorRecordLines(rec agentErrorRecord, width int) []string {
	ts := rec.Timestamp.Format("15:04:05")

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

	out := []string{ErrorStyle.Render(header)}
	msg := strings.TrimSpace(rec.Message)
	if msg == "" {
		msg = "(no message)"
	}
	for _, line := range wrapText(msg, width) {
		out = append(out, "  "+line)
	}
	return out
}
