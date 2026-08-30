package agent

import (
	"strings"
	"testing"

	"github.com/keakon/chord/internal/ctxmgr"
	"github.com/keakon/chord/internal/message"
)

// TestEstimateMessagesTokensRouting verifies the agent-side helper routes
// through the context manager's usage-calibrated estimator when available and
// falls back to the plain bytes/3 heuristic otherwise.
func TestEstimateMessagesTokensRouting(t *testing.T) {
	msg := message.Message{Content: strings.Repeat("x", 3000)}
	// No manager: plain fallback (3000 bytes / 3 = 1000).
	if got := estimateMessagesTokens(nil, []message.Message{msg}); got != 1000 {
		t.Fatalf("estimateMessagesTokens(nil) = %d, want 1000", got)
	}
	// Calibrated manager: 600 tokens over 3000 bytes => ratio 0.2 => 600.
	m := ctxmgr.NewManager(8192, 0)
	m.Append(msg)
	m.UpdateFromUsage(message.TokenUsage{InputTokens: 600})
	if got := estimateMessagesTokens(m, []message.Message{msg}); got != 600 {
		t.Fatalf("estimateMessagesTokens(calibrated) = %d, want 600", got)
	}
	if got := estimateMessageTokens(m, msg); got != 600 {
		t.Fatalf("estimateMessageTokens(calibrated) = %d, want 600", got)
	}
	if got := estimateBytesForTokens(m, 600); got != 3000 {
		t.Fatalf("estimateBytesForTokens(calibrated) = %d, want 3000", got)
	}
	if got := estimateBytesForTokens(nil, 600); got != 1800 {
		t.Fatalf("estimateBytesForTokens(nil) = %d, want 1800", got)
	}
}
