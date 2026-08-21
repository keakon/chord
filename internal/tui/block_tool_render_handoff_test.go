package tui

import (
	"strings"
	"testing"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/tools"
)

// handoffTestBlock builds a handoff tool card with the given args and result.
func handoffTestBlock(argsJSON, result string, status agent.ToolResultStatus, done bool) *Block {
	return &Block{
		ID:                1,
		Type:              BlockToolCall,
		ToolName:          tools.NameHandoff,
		Content:           argsJSON,
		ResultContent:     result,
		ResultDone:        done,
		ResultStatus:      status,
		displayWorkingDir: "/work",
	}
}

func TestHandoffCardShowsPlanPathInHeader(t *testing.T) {
	block := handoffTestBlock(`{"plan_path":"docs/plans/plan-004.md"}`, "", "", false)
	joined := stripANSI(strings.Join(block.Render(120, ""), "\n"))
	if !strings.Contains(joined, "handoff") {
		t.Fatalf("expected handoff tool name in header; got:\n%s", joined)
	}
	if !strings.Contains(joined, "docs/plans/plan-004.md") {
		t.Fatalf("expected plan path in header; got:\n%s", joined)
	}
}

func TestHandoffCardSuccessHidesRawJSON(t *testing.T) {
	block := handoffTestBlock(
		`{"plan_path":"docs/plans/plan-004.md"}`,
		`{"plan_path":"/abs/work/docs/plans/plan-004.md"}`,
		agent.ToolResultStatusSuccess,
		true,
	)
	joined := stripANSI(strings.Join(block.Render(120, ""), "\n"))
	if strings.Contains(joined, "plan_path") || strings.Contains(joined, "/abs/work") {
		t.Fatalf("handoff success card must not echo the raw JSON result; got:\n%s", joined)
	}
	if !strings.Contains(joined, "docs/plans/plan-004.md") {
		t.Fatalf("expected relative plan path in success header; got:\n%s", joined)
	}
	if !strings.Contains(joined, "✓") {
		t.Fatalf("expected success marker on confirmed handoff; got:\n%s", joined)
	}
}

func TestHandoffCardRejectedShowsReason(t *testing.T) {
	block := handoffTestBlock(
		`{"plan_path":"docs/plans/plan-004.md"}`,
		"Handoff rejected: use reviewer first",
		agent.ToolResultStatusSuccess,
		true,
	)
	joined := stripANSI(strings.Join(block.Render(120, ""), "\n"))
	if !strings.Contains(joined, "✗") {
		t.Fatalf("expected rejection marker on rejected handoff; got:\n%s", joined)
	}
	if !strings.Contains(joined, "rejected reason: use reviewer first") {
		t.Fatalf("expected rejected reason text; got:\n%s", joined)
	}
	if strings.Contains(joined, `{"plan_path"`) {
		t.Fatalf("rejected handoff must not echo raw JSON; got:\n%s", joined)
	}
}

func TestHandoffCardCancelledShowsCancelled(t *testing.T) {
	block := handoffTestBlock(
		`{"plan_path":"docs/plans/plan-004.md"}`,
		"Cancelled",
		agent.ToolResultStatusCancelled,
		true,
	)
	joined := stripANSI(strings.Join(block.Render(120, ""), "\n"))
	if !strings.Contains(joined, "◌") && !strings.Contains(joined, "Cancelled") {
		t.Fatalf("expected cancelled state; got:\n%s", joined)
	}
}

func TestHandoffCardPendingDoesNotShowSuccessMarker(t *testing.T) {
	// While the handoff selector is open the card is not yet terminal.
	block := handoffTestBlock(`{"plan_path":"docs/plans/plan-004.md"}`, "", "", false)
	joined := stripANSI(strings.Join(block.Render(120, ""), "\n"))
	if strings.Contains(joined, "✓") {
		t.Fatalf("pending handoff must not show the success marker; got:\n%s", joined)
	}
}
