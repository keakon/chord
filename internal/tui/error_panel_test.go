package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/keakon/bubbletea/v2"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/llm"
)

func TestRecordAgentErrorExtractsAPIErrorFields(t *testing.T) {
	m := NewModel(&sessionControlAgent{})
	apiErr := &llm.APIError{StatusCode: 429, Code: "rate_limit", Type: "rate_limit_error", Message: "slow down"}
	m.recordAgentError("", fmt.Errorf("request failed: %w", apiErr), "OpenAI", "gpt-4", "...abc1", false)

	records := m.snapshotAgentErrors()
	if len(records) != 1 {
		t.Fatalf("records = %d, want 1", len(records))
	}
	rec := records[0]
	if rec.StatusCode != 429 {
		t.Fatalf("StatusCode = %d, want 429", rec.StatusCode)
	}
	if rec.ErrorCode != "rate_limit" {
		t.Fatalf("ErrorCode = %q, want rate_limit", rec.ErrorCode)
	}
	if rec.ErrorType != "rate_limit_error" {
		t.Fatalf("ErrorType = %q, want rate_limit_error", rec.ErrorType)
	}
	if rec.Message != "slow down" {
		t.Fatalf("Message = %q, want \"slow down\"", rec.Message)
	}
}

func TestRecordAgentErrorPlainError(t *testing.T) {
	m := NewModel(&sessionControlAgent{})
	m.recordAgentError("sub-1", fmt.Errorf("connection timeout"), "", "", "", false)

	records := m.snapshotAgentErrors()
	if len(records) != 1 {
		t.Fatalf("records = %d, want 1", len(records))
	}
	rec := records[0]
	if rec.StatusCode != 0 {
		t.Fatalf("StatusCode = %d, want 0", rec.StatusCode)
	}
	if rec.AgentID != "sub-1" {
		t.Fatalf("AgentID = %q, want sub-1", rec.AgentID)
	}
	if rec.Message != "connection timeout" {
		t.Fatalf("Message = %q, want \"connection timeout\"", rec.Message)
	}
}

func TestSilentRetryErrorRecordsPanelWithoutConversationBlock(t *testing.T) {
	m := NewModelWithSize(&sessionControlAgent{}, 100, 30)
	apiErr := &llm.APIError{StatusCode: 503, Code: "overloaded", Type: "server_error", Message: "try later"}
	m.handleMiscAgentEvent(agent.ErrorEvent{
		Err:      apiErr,
		Silent:   true,
		Provider: "OpenAI",
		Model:    "gpt-4.1",
		Key:      "...abc1",
	})

	records := m.snapshotAgentErrors()
	if len(records) != 1 {
		t.Fatalf("records = %d, want 1", len(records))
	}
	rec := records[0]
	if rec.Provider != "OpenAI" || rec.Model != "gpt-4.1" || rec.KeySuffix != "...abc1" || rec.StatusCode != 503 {
		t.Fatalf("record = %+v, want retry error metadata", rec)
	}
	if blocks := m.viewport.visibleBlocks(); len(blocks) != 0 {
		t.Fatalf("visible blocks = %#v, want no conversation error block for silent retry error", blocks)
	}
}

func TestFinalErrorAfterMatchingRetryRecordedOnce(t *testing.T) {
	m := NewModelWithSize(&sessionControlAgent{}, 100, 30)
	apiErr := &llm.APIError{StatusCode: 503, Code: "overloaded", Message: "try later"}

	// A non-retriable, no-fallback error is emitted once as a silent retry
	// attempt (with metadata) and again as the final error.
	m.handleMiscAgentEvent(agent.ErrorEvent{
		Err:      apiErr,
		Silent:   true,
		Provider: "OpenAI",
		Model:    "gpt-4.1",
		Key:      "...abc1",
	})
	m.handleMiscAgentEvent(agent.ErrorEvent{Err: apiErr})

	records := m.snapshotAgentErrors()
	if len(records) != 1 {
		t.Fatalf("records = %d, want 1 (final error must not duplicate the retry record)", len(records))
	}
	// The richer retry record (with provider/model/key) is the one kept.
	if rec := records[0]; rec.Provider != "OpenAI" || rec.Model != "gpt-4.1" || rec.KeySuffix != "...abc1" {
		t.Fatalf("record = %+v, want retry metadata preserved", rec)
	}
	// The final error still renders as a conversation block.
	if blocks := m.viewport.visibleBlocks(); len(blocks) != 1 {
		t.Fatalf("visible blocks = %d, want 1 final error block", len(blocks))
	}
}

func TestDistinctFinalErrorAfterRetryRecordedSeparately(t *testing.T) {
	m := NewModelWithSize(&sessionControlAgent{}, 100, 30)
	m.handleMiscAgentEvent(agent.ErrorEvent{
		Err:      &llm.APIError{StatusCode: 503, Message: "try later"},
		Silent:   true,
		Provider: "OpenAI",
		Model:    "gpt-4.1",
	})
	// A different final error must not be collapsed into the retry record.
	m.handleMiscAgentEvent(agent.ErrorEvent{Err: &llm.APIError{StatusCode: 400, Message: "bad request"}})

	if records := m.snapshotAgentErrors(); len(records) != 2 {
		t.Fatalf("records = %d, want 2 distinct entries", len(records))
	}
}

func TestSilentRetryErrorDoesNotFinalizeStreamingBlock(t *testing.T) {
	m := NewModelWithSize(&sessionControlAgent{}, 100, 30)
	m.handleStreamingAgentEvent(agent.StreamTextEvent{Text: "partial answer before the stream was interrupted"})
	if m.currentAssistantBlock == nil || !m.currentAssistantBlock.Streaming {
		t.Fatal("expected an active streaming assistant block before the silent error")
	}

	m.handleMiscAgentEvent(agent.ErrorEvent{
		Err:    &llm.APIError{StatusCode: 503, Message: "stream interrupted"},
		Silent: true,
	})

	if m.currentAssistantBlock == nil {
		t.Fatal("currentAssistantBlock = nil, want streaming block kept across silent retry error")
	}
	if !m.currentAssistantBlock.Streaming {
		t.Fatal("streaming assistant block was finalized by a silent retry error")
	}
}

func TestFormatErrorRecordHeaderModelWithoutProvider(t *testing.T) {
	rec := agentErrorRecord{Model: "gpt-4.1", Message: "boom"}
	lines := formatErrorRecordLines(rec, 80)
	if len(lines) == 0 {
		t.Fatal("no lines rendered")
	}
	header := stripANSI(lines[0])
	if strings.Contains(header, "/gpt-4.1") {
		t.Fatalf("header = %q, want model without dangling slash when provider is empty", header)
	}
	if !strings.Contains(header, "gpt-4.1") {
		t.Fatalf("header = %q, want model name present", header)
	}
}

func TestVisibleErrorRecordsPanelAndConversationBlock(t *testing.T) {
	m := NewModelWithSize(&sessionControlAgent{}, 100, 30)
	m.handleMiscAgentEvent(agent.ErrorEvent{Err: fmt.Errorf("final failure")})

	if records := m.snapshotAgentErrors(); len(records) != 1 {
		t.Fatalf("records = %d, want 1", len(records))
	}
	blocks := m.viewport.visibleBlocks()
	if len(blocks) != 1 || blocks[0].Type != BlockError || blocks[0].Content != "final failure" {
		t.Fatalf("visible blocks = %#v, want one final error block", blocks)
	}
}

func TestRecordAgentErrorRingBufferEvictsOldest(t *testing.T) {
	m := NewModel(&sessionControlAgent{})
	for i := 0; i < maxAgentErrors+5; i++ {
		m.recordAgentError("", fmt.Errorf("err %d", i), "", "", "", false)
	}

	records := m.snapshotAgentErrors()
	if len(records) != maxAgentErrors {
		t.Fatalf("records = %d, want %d", len(records), maxAgentErrors)
	}
	// Oldest retained should be err 5 (0..4 evicted), newest err maxAgentErrors+4.
	if want := fmt.Sprintf("err %d", 5); records[0].Message != want {
		t.Fatalf("records[0].Message = %q, want %q", records[0].Message, want)
	}
	if want := fmt.Sprintf("err %d", maxAgentErrors+4); records[len(records)-1].Message != want {
		t.Fatalf("records[last].Message = %q, want %q", records[len(records)-1].Message, want)
	}
}

func TestSessionSwitchClearsAgentErrorsForNewAndResumeButForkKeepsThem(t *testing.T) {
	m := NewModel(&sessionControlAgent{})
	m.recordAgentError("", fmt.Errorf("first failure"), "", "", "", false)

	m.beginSessionSwitch("fork", "")
	if records := m.snapshotAgentErrors(); len(records) != 1 {
		t.Fatalf("records after fork = %d, want 1", len(records))
	}

	m.beginSessionSwitch("resume", "session-1")
	if records := m.snapshotAgentErrors(); len(records) != 0 {
		t.Fatalf("records after resume = %d, want 0", len(records))
	}

	m.recordAgentError("", fmt.Errorf("second failure"), "", "", "", false)
	m.beginSessionSwitch("new", "")
	if records := m.snapshotAgentErrors(); len(records) != 0 {
		t.Fatalf("records after new session = %d, want 0", len(records))
	}
}

func TestErrorPanelOpenClose(t *testing.T) {
	m := NewModel(&sessionControlAgent{})
	m.openErrorPanel()
	if m.mode != ModeErrorPanel {
		t.Fatalf("mode = %v, want ModeErrorPanel", m.mode)
	}

	_ = m.handleErrorPanelKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))
	if m.mode == ModeErrorPanel {
		t.Fatal("mode still ModeErrorPanel after esc, want closed")
	}

	m.openErrorPanel()
	_ = m.handleErrorPanelKey(tea.KeyPressMsg(tea.Key{Text: "q", Code: 'q'}))
	if m.mode == ModeErrorPanel {
		t.Fatal("mode still ModeErrorPanel after q, want closed")
	}
}

func TestErrorPanelLinesShowStructuredFields(t *testing.T) {
	m := NewModel(&sessionControlAgent{})
	m.width = 100
	m.height = 40
	apiErr := &llm.APIError{StatusCode: 500, Code: "server_error", Type: "api_error", Message: "upstream exploded"}
	m.recordAgentError("", apiErr, "provider", "model-1", "...xyz9", false)

	lines := m.errorPanelLines(m.errorPanelInnerWidth())
	joined := stripANSI(strings.Join(lines, "\n"))
	for _, want := range []string{"provider/model-1", "key=...xyz9", "HTTP 500", "code=server_error", "upstream exploded"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("error panel lines missing %q\n%s", want, joined)
		}
	}
}
