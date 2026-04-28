package tui

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/analytics"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/ratelimit"
	"github.com/keakon/chord/internal/skill"
	"github.com/keakon/chord/internal/tools"
)

type loopBusyAgentStub struct{}

func (loopBusyAgentStub) Events() <-chan agent.AgentEvent                           { return nil }
func (loopBusyAgentStub) SendUserMessage(string)                                    {}
func (loopBusyAgentStub) SendUserMessageWithParts([]message.ContentPart)            {}
func (loopBusyAgentStub) AppendContextMessage(message.Message)                      {}
func (loopBusyAgentStub) CancelCurrentTurn() bool                                   { return false }
func (loopBusyAgentStub) QueuePendingUserDraft(string, []message.ContentPart) bool  { return false }
func (loopBusyAgentStub) UpdatePendingUserDraft(string, []message.ContentPart) bool { return false }
func (loopBusyAgentStub) RemovePendingUserDraft(string) bool                        { return false }
func (loopBusyAgentStub) ResolveConfirm(string, string, string, string, string)     {}
func (loopBusyAgentStub) ResolveQuestion([]string, bool, string)                    {}
func (loopBusyAgentStub) SwitchModel(string) error                                  { return nil }
func (loopBusyAgentStub) AvailableModels() []agent.ModelOption                      { return nil }
func (loopBusyAgentStub) ProviderModelRef() string                                  { return "" }
func (loopBusyAgentStub) RunningModelRef() string                                   { return "" }
func (loopBusyAgentStub) RunningVariant() string                                    { return "" }
func (loopBusyAgentStub) GetSubAgents() []agent.SubAgentInfo                        { return nil }
func (loopBusyAgentStub) GetMessages() []message.Message                            { return nil }
func (loopBusyAgentStub) SwitchFocus(string)                                        {}
func (loopBusyAgentStub) FocusedAgentID() string                                    { return "" }
func (loopBusyAgentStub) StartupResumeStatus() (bool, string)                       { return false, "" }
func (loopBusyAgentStub) ContinueFromContext()                                      {}
func (loopBusyAgentStub) RemoveLastMessage()                                        {}
func (loopBusyAgentStub) GetTokenUsage() message.TokenUsage                         { return message.TokenUsage{} }
func (loopBusyAgentStub) GetUsageStats() analytics.SessionStats                     { return analytics.SessionStats{} }
func (loopBusyAgentStub) GetSidebarUsageStats() analytics.SessionStats {
	return analytics.SessionStats{}
}
func (loopBusyAgentStub) GetContextStats() (int, int)                               { return 0, 0 }
func (loopBusyAgentStub) GetContextMessageCount() int                               { return 0 }
func (loopBusyAgentStub) KeyStats() (int, int)                                      { return 0, 0 }
func (loopBusyAgentStub) CurrentRateLimitSnapshot() *ratelimit.KeyRateLimitSnapshot { return nil }
func (loopBusyAgentStub) ProxyInUseForRef(string) bool                              { return false }
func (loopBusyAgentStub) ProjectRoot() string                                       { return "" }
func (loopBusyAgentStub) CurrentRole() string                                       { return "builder" }
func (loopBusyAgentStub) LoopKeepsMainBusy() bool                                   { return true }
func (loopBusyAgentStub) CurrentLoopState() agent.LoopState                         { return agent.LoopStateExecuting }
func (loopBusyAgentStub) CurrentLoopTarget() string                                 { return "current task" }
func (loopBusyAgentStub) CurrentLoopIteration() int                                 { return 1 }
func (loopBusyAgentStub) CurrentLoopMaxIterations() int                             { return 10 }
func (loopBusyAgentStub) EnableLoopMode(string)                                     {}
func (loopBusyAgentStub) DisableLoopMode()                                          {}
func (loopBusyAgentStub) ListSessionSummaries() ([]agent.SessionSummary, error)     { return nil, nil }
func (loopBusyAgentStub) GetSessionSummary() *agent.SessionSummary                  { return nil }
func (loopBusyAgentStub) DeleteSession(string) error                                { return nil }
func (loopBusyAgentStub) ExportSession(string, string)                              {}
func (loopBusyAgentStub) ResumeSession()                                            {}
func (loopBusyAgentStub) ResumeSessionID(string)                                    {}
func (loopBusyAgentStub) NewSession()                                               {}
func (loopBusyAgentStub) ForkSession(int)                                           {}
func (loopBusyAgentStub) ExecutePlan(string, string)                                {}
func (loopBusyAgentStub) AvailableAgents() []string                                 { return nil }
func (loopBusyAgentStub) SwitchRole(string)                                         {}
func (loopBusyAgentStub) AvailableRoles() []string                                  { return nil }
func (loopBusyAgentStub) InvokedSkills() []*skill.Meta                              { return nil }
func (loopBusyAgentStub) GetTodos() []tools.TodoItem                                { return nil }
func (loopBusyAgentStub) IsCompactionRunning() bool                                 { return false }
func (loopBusyAgentStub) CancelCompaction() bool                                    { return false }

func TestSanitizeOSC9Payload(t *testing.T) {
	if g := sanitizeOSC9Payload("hello"); g != "hello" {
		t.Fatalf("got %q", g)
	}
	if g := sanitizeOSC9Payload("a\x07b\x1b[c"); g != "a b [c" {
		t.Fatalf("got %q", g)
	}
	if g := sanitizeOSC9Payload("line1\nline2"); g != "line1 line2" {
		t.Fatalf("got %q", g)
	}
	long := strings.Repeat("x", osc9MaxRunes+50)
	if g := sanitizeOSC9Payload(long); len([]rune(g)) != osc9MaxRunes {
		t.Fatalf("len = %d, want %d", len([]rune(g)), osc9MaxRunes)
	}
	if g := sanitizeOSC9Payload("   \x01\x02   "); g != "Chord" {
		t.Fatalf("empty/control got %q, want Chord", g)
	}
}

func TestMaybeOSC9NotifyCmd(t *testing.T) {
	var buf bytes.Buffer
	m := Model{
		desktopOSC9Enabled: true,
		terminalAppFocused: false,
		oscNotifyOut:       &buf,
	}
	cmd := m.maybeOSC9NotifyCmd("Ready")
	if cmd == nil {
		t.Fatal("expected notify cmd")
	}
	if msg := cmd(); msg != nil {
		t.Fatalf("cmd msg = %#v, want nil", msg)
	}
	if got := buf.String(); got != "\x1b]9;Ready\x07" {
		t.Fatalf("osc sequence = %q", got)
	}
}

func TestMaybeOSC9NotifyCmdSuppressed(t *testing.T) {
	var buf bytes.Buffer
	m := Model{
		desktopOSC9Enabled: false,
		terminalAppFocused: false,
		oscNotifyOut:       &buf,
	}
	if cmd := m.maybeOSC9NotifyCmd("Ready"); cmd != nil {
		t.Fatal("expected nil cmd when disabled")
	}
	m.desktopOSC9Enabled = true
	m.terminalAppFocused = true
	if cmd := m.maybeOSC9NotifyCmd("Ready"); cmd != nil {
		t.Fatal("expected nil cmd when focused")
	}
}

func TestOSC9IdleNotificationUsesLastAssistantMessage(t *testing.T) {
	var buf bytes.Buffer
	m := NewModelWithSize(nil, 80, 24)
	m.desktopOSC9Enabled = true
	m.terminalAppFocused = false
	m.oscNotifyOut = &buf
	m.viewport.AppendBlock(&Block{ID: 1, Type: BlockAssistant, Content: "模型回复内容"})

	cmd := m.handleAgentEvent(agentEventMsg{event: agent.IdleEvent{}})
	if cmd == nil {
		t.Fatal("expected idle followup command")
	}
	_ = cmd()
	if got := buf.String(); got != "\x1b]9;模型回复内容\x07" {
		t.Fatalf("osc sequence = %q, want assistant content", got)
	}
}

func TestOSC9IdleNotificationUsesLastErrorMessage(t *testing.T) {
	var buf bytes.Buffer
	m := NewModelWithSize(nil, 80, 24)
	m.desktopOSC9Enabled = true
	m.terminalAppFocused = false
	m.oscNotifyOut = &buf

	_ = m.handleAgentEvent(agentEventMsg{event: agent.ErrorEvent{Err: errors.New("请求中断：网络错误")}})
	cmd := m.handleAgentEvent(agentEventMsg{event: agent.IdleEvent{}})
	if cmd == nil {
		t.Fatal("expected idle followup command")
	}
	_ = cmd()
	if got := buf.String(); got != "\x1b]9;请求中断：网络错误\x07" {
		t.Fatalf("osc sequence = %q, want error content", got)
	}
}

func TestOSC9LoopTerminalInfoNotification(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	m.desktopOSC9Enabled = true
	m.terminalAppFocused = false
	var buf bytes.Buffer
	m.oscNotifyOut = &buf

	cmd := m.handleAgentEvent(agentEventMsg{event: agent.InfoEvent{Message: "Loop completed: all steps finished."}})
	if cmd == nil {
		t.Fatal("expected info followup command")
	}
	if len(m.viewport.visibleBlocks()) != 0 {
		t.Fatalf("visible blocks = %#v, want no transcript block for loop terminal info", m.viewport.visibleBlocks())
	}
	if m.activeToast == nil || m.activeToast.Message != "Loop completed: all steps finished." {
		t.Fatalf("activeToast = %+v, want loop completion toast", m.activeToast)
	}
	// Loop terminal InfoEvent no longer emits OSC9 directly;
	// the IdleEvent that follows will emit OSC9 instead.
	if got := buf.String(); got != "" {
		t.Fatalf("osc sequence = %q, want empty (notification deferred to IdleEvent)", got)
	}
}

func TestIdleEventDoesNotNotifyWhileLoopStillBusy(t *testing.T) {
	var buf bytes.Buffer
	m := NewModelWithSize(loopBusyAgentStub{}, 80, 24)
	m.desktopOSC9Enabled = true
	m.terminalAppFocused = false
	m.oscNotifyOut = &buf

	cmd := m.handleAgentEvent(agentEventMsg{event: agent.IdleEvent{}})
	if cmd != nil {
		_ = cmd()
	}
	if got := buf.String(); got != "" {
		t.Fatalf("osc sequence = %q, want empty while loop still busy", got)
	}
	if got := m.activities["main"].Type; got != agent.ActivityExecuting {
		t.Fatalf("main activity = %q, want %q while loop still busy", got, agent.ActivityExecuting)
	}
}

func TestConfirmRequestNotifiesWhileLoopStillBusy(t *testing.T) {
	var buf bytes.Buffer
	m := NewModelWithSize(loopBusyAgentStub{}, 80, 24)
	m.desktopOSC9Enabled = true
	m.terminalAppFocused = false
	m.oscNotifyOut = &buf

	cmd := m.handleAgentEvent(agentEventMsg{event: agent.ConfirmRequestEvent{
		ToolName:  "Edit",
		ArgsJSON:  `{"path":"internal/tui/app.go"}`,
		RequestID: "req-1",
	}})
	if cmd == nil {
		t.Fatal("expected confirm followup command")
	}
	msg := cmd()
	batch, ok := msg.(tea.BatchMsg)
	if !ok {
		t.Fatalf("cmd() = %T, want tea.BatchMsg", msg)
	}
	for _, sub := range batch {
		if sub == nil {
			continue
		}
		if subMsg := sub(); subMsg != nil {
			updated, next := m.Update(subMsg)
			model, ok := updated.(*Model)
			if !ok {
				t.Fatalf("Update returned %T, want *Model", updated)
			}
			m = *model
			if next != nil {
				_ = next()
			}
		}
	}
	if got := buf.String(); got != "\x1b]9;Chord: Permission confirmation required\x07" {
		t.Fatalf("osc sequence = %q, want confirm notification while loop is busy", got)
	}
}

func TestQuestionRequestNotifiesWhileLoopStillBusy(t *testing.T) {
	var buf bytes.Buffer
	m := NewModelWithSize(loopBusyAgentStub{}, 80, 24)
	m.desktopOSC9Enabled = true
	m.terminalAppFocused = false
	m.oscNotifyOut = &buf

	cmd := m.handleAgentEvent(agentEventMsg{event: agent.QuestionRequestEvent{
		ToolName:  "Question",
		Header:    "name",
		Question:  "who?",
		Options:   []string{"alice", "bob"},
		RequestID: "req-2",
	}})
	if cmd == nil {
		t.Fatal("expected question followup command")
	}
	msg := cmd()
	batch, ok := msg.(tea.BatchMsg)
	if !ok {
		t.Fatalf("cmd() = %T, want tea.BatchMsg", msg)
	}
	for _, sub := range batch {
		if sub == nil {
			continue
		}
		if subMsg := sub(); subMsg != nil {
			updated, next := m.Update(subMsg)
			model, ok := updated.(*Model)
			if !ok {
				t.Fatalf("Update returned %T, want *Model", updated)
			}
			m = *model
			if next != nil {
				_ = next()
			}
		}
	}
	if got := buf.String(); got != "\x1b]9;Chord: Question requires your input\x07" {
		t.Fatalf("osc sequence = %q, want question notification while loop is busy", got)
	}
}
