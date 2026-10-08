package tui

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "github.com/keakon/bubbletea/v2"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/llm"
)

func TestRecordAgentErrorExtractsAPIErrorFields(t *testing.T) {
	m := NewModel(&sessionControlAgent{})
	apiErr := &llm.APIError{StatusCode: 429, Code: "rate_limit", Type: "rate_limit_error", Message: "slow down"}
	m.recordAgentError("", fmt.Errorf("request failed: %w", apiErr), "OpenAI", "gpt-4", "open...abc1", "", "", false)

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
	m.recordAgentError("sub-1", fmt.Errorf("connection timeout"), "", "", "", "", "", false)

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
		Err:       apiErr,
		Silent:    true,
		Provider:  "OpenAI",
		Model:     "gpt-4.1",
		Key:       "open...abc1",
		AccountID: "acc-1",
		Email:     "user@example.com",
	})

	records := m.snapshotAgentErrors()
	if len(records) != 1 {
		t.Fatalf("records = %d, want 1", len(records))
	}
	rec := records[0]
	if rec.Provider != "OpenAI" || rec.Model != "gpt-4.1" || rec.MaskedKey != "open...abc1" || rec.Email != "user@example.com" || rec.StatusCode != 503 {
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
		Key:      "open...abc1",
	})
	m.handleMiscAgentEvent(agent.ErrorEvent{Err: apiErr})

	records := m.snapshotAgentErrors()
	if len(records) != 1 {
		t.Fatalf("records = %d, want 1 (final error must not duplicate the retry record)", len(records))
	}
	// The richer retry record (with provider/model/key) is the one kept.
	if rec := records[0]; rec.Provider != "OpenAI" || rec.Model != "gpt-4.1" || rec.MaskedKey != "open...abc1" {
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
	for i := range maxAgentErrors + 5 {
		m.recordAgentError("", fmt.Errorf("err %d", i), "", "", "", "", "", false)
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
	m.recordAgentError("", fmt.Errorf("first failure"), "", "", "", "", "", false)

	m.beginSessionSwitch("fork", "")
	if records := m.snapshotAgentErrors(); len(records) != 1 {
		t.Fatalf("records after fork = %d, want 1", len(records))
	}

	m.beginSessionSwitch("resume", "session-1")
	if records := m.snapshotAgentErrors(); len(records) != 0 {
		t.Fatalf("records after resume = %d, want 0", len(records))
	}

	m.recordAgentError("", fmt.Errorf("second failure"), "", "", "", "", "", false)
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
	m.recordAgentError("", apiErr, "provider", "model-1", "gate...xyz9", "acc-1", "user@example.com", false)

	lines := m.errorPanelLines(m.errorPanelInnerWidth())
	joined := stripANSI(strings.Join(lines, "\n"))
	for _, want := range []string{"provider/model-1", "key=gate...xyz9", "email=user@example.com", "HTTP 500", "code=server_error", "upstream exploded"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("error panel lines missing %q\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "fp=") {
		t.Fatalf("error panel lines should not show fp\n%s", joined)
	}
}

func TestFormatErrorRecordLinesAccountsForMessageIndent(t *testing.T) {
	for _, message := range []string{
		"operation timed out while waiting for the request",
		strings.Repeat("x", 23),
	} {
		lines := formatErrorRecordLines(agentErrorRecord{Timestamp: time.Unix(0, 0), Message: message}, 24)
		for _, line := range lines[1:] {
			if got := tuiStringWidth(line); got > 24 {
				t.Fatalf("message line width = %d, want <= 24: %q", got, line)
			}
		}
		if got := stripANSI(strings.Join(lines, "\n")); !strings.Contains(got, "  ") {
			t.Fatalf("message lost its indent: %q", got)
		}
	}
}

// errorPanelClipboardResult runs the writeClipboardCmd sequence returned by a
// panel copy action and returns its result message.
func errorPanelClipboardResult(t *testing.T, cmd tea.Cmd) clipboardWriteResultMsg {
	t.Helper()
	msg := cmd()
	v := reflect.ValueOf(msg)
	if v.Kind() != reflect.Slice || v.Len() != 2 {
		t.Fatalf("clipboard command msg = %T, want 2-command sequence", msg)
	}
	return v.Index(1).Call(nil)[0].Interface().(clipboardWriteResultMsg)
}

func TestErrorPanelCopyAllWritesPlainText(t *testing.T) {
	origWrite := clipboardWriteAll
	var copied string
	clipboardWriteAll = func(text string) error {
		copied = text
		return nil
	}
	defer func() { clipboardWriteAll = origWrite }()

	m := NewModelWithSize(&sessionControlAgent{}, 100, 40)
	m.openErrorPanel()
	m.recordAgentError("", &llm.APIError{StatusCode: 429, Code: "rate_limit", Type: "rate_limit_error", Message: "slow down"}, "provider", "model-1", "gate...xyz9", "acc-1", "user@example.com", false)
	longMessage := "connection timeout " + strings.Repeat("x", 200)
	m.recordAgentError("", fmt.Errorf("%s", longMessage), "", "", "", "", "", false)

	cmd := m.handleErrorPanelKey(tea.KeyPressMsg(tea.Key{Text: "y", Code: 'y'}))
	if cmd == nil {
		t.Fatal("y should return a clipboard command")
	}
	result := errorPanelClipboardResult(t, cmd)
	if result.err != nil {
		t.Fatalf("clipboard write err = %v", result.err)
	}
	if result.success != "Error log copied to clipboard" {
		t.Fatalf("clipboard success = %q, want %q", result.success, "Error log copied to clipboard")
	}
	for _, want := range []string{
		"provider/model-1", "key=gate...xyz9", "email=user@example.com",
		"HTTP 429", "code=rate_limit", "type=rate_limit_error", "slow down", longMessage,
	} {
		if !strings.Contains(copied, want) {
			t.Fatalf("copied text missing %q\n%s", want, copied)
		}
	}
	if strings.Contains(copied, "\x1b[") {
		t.Fatalf("copied text contains ANSI escapes: %q", copied)
	}
	// Newest error first, matching the panel display order.
	newestAt := strings.Index(copied, "connection timeout")
	oldestAt := strings.Index(copied, "slow down")
	if newestAt < 0 || oldestAt < 0 || newestAt > oldestAt {
		t.Fatalf("copied text order = %d/%d, want the newest error first\n%s", newestAt, oldestAt, copied)
	}
	// The copy carries a full date rather than the panel's HH:MM:SS.
	if ts := m.snapshotAgentErrors()[0].Timestamp.Format("2006-01-02 15:04:05"); !strings.Contains(copied, ts) {
		t.Fatalf("copied text missing full timestamp %q\n%s", ts, copied)
	}
	if hint := m.errorPanelHint(); !strings.Contains(hint, "y copy") {
		t.Fatalf("error panel hint = %q, want y copy", hint)
	}
	if cmd := m.handleErrorPanelKey(tea.KeyPressMsg(tea.Key{Text: "Y", Code: 'Y'})); cmd == nil {
		t.Fatal("Y should also copy the error log")
	}
}

func TestErrorPanelCopyAllEmpty(t *testing.T) {
	origWrite := clipboardWriteAll
	writeCalled := false
	clipboardWriteAll = func(text string) error {
		writeCalled = true
		return nil
	}
	defer func() { clipboardWriteAll = origWrite }()

	m := NewModelWithSize(&sessionControlAgent{}, 100, 40)
	m.openErrorPanel()
	if cmd := m.handleErrorPanelKey(tea.KeyPressMsg(tea.Key{Text: "y", Code: 'y'})); cmd == nil {
		t.Fatal("y on an empty panel should enqueue an info toast")
	}
	if writeCalled {
		t.Fatal("empty panel must not write to the clipboard")
	}
}

func TestErrorPanelSuperCopyCopiesAll(t *testing.T) {
	origWrite := clipboardWriteAll
	var copied string
	clipboardWriteAll = func(text string) error {
		copied = text
		return nil
	}
	defer func() { clipboardWriteAll = origWrite }()

	m := NewModelWithSize(&sessionControlAgent{}, 100, 40)
	m.openErrorPanel()
	m.recordAgentError("", fmt.Errorf("panel failure"), "", "", "", "", "", false)

	cmd := m.handleSuperCopy()
	if cmd == nil {
		t.Fatal("Cmd+C in the error panel should copy the error log")
	}
	if result := errorPanelClipboardResult(t, cmd); result.err != nil {
		t.Fatalf("clipboard write err = %v", result.err)
	}
	if !strings.Contains(copied, "panel failure") {
		t.Fatalf("copied text = %q, want panel failure", copied)
	}
}
