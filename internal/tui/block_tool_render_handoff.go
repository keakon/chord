package tui

import (
	"encoding/json"
	"strings"

	"github.com/mattn/go-runewidth"
)

// renderHandoffCall renders the Handoff tool card. The plan path is shown in
// the header like Read, the raw success JSON is never echoed, and the terminal
// state distinguishes the user's decision: confirmed (✓ path), rejected
// (✗ path + rejected reason), or cancelled (◌ path + Cancelled). While the
// handoff selector is open the card stays non-terminal (no ✓) because the
// runtime defers the handoff tool result until the user decides.
func (b *Block) renderHandoffCall(width int, spinnerFrame string) []string {
	metrics := newWideHeaderToolCardMetrics(width)
	blockStyle := metrics.blockStyle
	toolCardBg := metrics.toolCardBg
	cardWidth := metrics.cardWidth
	contentWidth := metrics.contentWidth

	planPath := handoffPlanPathFromArgs(b.Content)
	if planPath != "" {
		planPath = b.displayToolPath(planPath)
	}

	prefix := b.renderToolPrefix(spinnerFrame)
	headerLine := renderToolHeaderLine(prefix, b.ToolName)
	if planPath != "" {
		baseWidth := runewidth.StringWidth(stripANSI(headerLine))
		budget := cardWidth - 4 - baseWidth - 1
		if budget > 0 {
			headerLine = headerLine + " " + truncateToolHeaderMiddle(planPath, budget)
		}
	}
	headerLine = buildToolHeaderLine(headerLine, b.ToolProgress, cardWidth, b.toolExecutionIsQueued() && b.ToolQueuedByExecutionEvent, b.toolExecutionIsRunning())

	var result []string
	result = append(result, headerLine)

	if b.toolResultIsError() {
		if strings.TrimSpace(b.ResultContent) != "" {
			result = appendErrorResultLines(result, b.ResultContent, contentWidth)
		}
	} else if b.toolResultIsCancelled() {
		result = appendCancelledResultLines(result, b.ResultContent, contentWidth)
	} else if b.ResultDone {
		// Success terminal state: the raw result is the structured JSON used by
		// the runtime; the plan path is already in the header, so show only the
		// rejected-reason form when the user rejected the handoff.
		if statusText := handoffRejectedReason(b.ResultContent); statusText != "" {
			result = append(result, "")
			for i, line := range wrapText(sanitizeToolDisplayText(statusText), contentWidth-len("  ↳ rejected reason: ")) {
				if i == 0 {
					result = append(result, ErrorStyle.Render("  ↳ rejected reason: "+line))
				} else {
					result = append(result, ErrorStyle.Render("    "+line))
				}
			}
		}
	}
	result = appendToolElapsedFooter(result, b)
	return renderPrewrappedToolCard(blockStyle, cardWidth, toolCardTitle("TOOL CALL", b.displayLabelID()), result, toolCardBg, railANSISeq("tool", b.Focused))
}

// handoffPlanPathFromArgs extracts the plan_path argument from the handoff
// tool call's arguments JSON, falling back to tolerant parsing when the args
// object does not decode cleanly.
func handoffPlanPathFromArgs(argsJSON string) string {
	var args struct {
		PlanPath string `json:"plan_path"`
	}
	if json.Unmarshal([]byte(argsJSON), &args) != nil {
		args.PlanPath = tolerantToolArgValue(argsJSON, "plan_path")
	}
	return strings.TrimSpace(args.PlanPath)
}

// handoffRejectedReason returns the user's rejection reason when the handoff
// result carries one, mirroring the "Done rejected: ..." convention used by
// the Done tool card.
func handoffRejectedReason(result string) string {
	trimmed := strings.TrimSpace(result)
	const prefix = "Handoff rejected:"
	if !strings.HasPrefix(trimmed, prefix) {
		return ""
	}
	reason := strings.TrimSpace(strings.TrimPrefix(trimmed, prefix))
	if reason == "" {
		return ""
	}
	return reason
}
