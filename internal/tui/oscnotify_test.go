package tui

import (
	"errors"
	"strings"
	"testing"

	tea "github.com/keakon/bubbletea/v2"

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
func (loopBusyAgentStub) ResolveHandoff(string, string, string, string)             {}
func (loopBusyAgentStub) ProviderModelRef() string                                  { return "" }
func (loopBusyAgentStub) RunningModelRef() string                                   { return "" }
func (loopBusyAgentStub) RunningVariant() string                                    { return "" }
func (loopBusyAgentStub) CurrentPoolName() string                                   { return "" }
func (loopBusyAgentStub) PoolNames() []string                                       { return nil }
func (loopBusyAgentStub) MainModelPoolName() string                                 { return "" }
func (loopBusyAgentStub) MainModelPoolNames() []string                              { return nil }
func (loopBusyAgentStub) AgentOverridePoolName(string) (string, bool)               { return "", false }
func (loopBusyAgentStub) SetCurrentModelPool(string) error                          { return nil }
func (loopBusyAgentStub) SetAgentModelPool(string, string) error                    { return nil }
func (loopBusyAgentStub) GetSubAgents() []agent.SubAgentInfo                        { return nil }
func (loopBusyAgentStub) GetMessages() []message.Message                            { return nil }
func (loopBusyAgentStub) SwitchFocus(string)                                        {}
func (loopBusyAgentStub) FocusedAgentID() string                                    { return "" }
func (loopBusyAgentStub) FocusedAgentName() string                                  { return "" }
func (loopBusyAgentStub) StartupResumeStatus() (bool, string)                       { return false, "" }
func (loopBusyAgentStub) ContinueFromContext()                                      {}
func (loopBusyAgentStub) RemoveLastMessage()                                        {}
func (loopBusyAgentStub) GetTokenUsage() message.TokenUsage                         { return message.TokenUsage{} }
func (loopBusyAgentStub) GetUsageStats() analytics.SessionStats                     { return analytics.SessionStats{} }
func (loopBusyAgentStub) GetSidebarUsageStats() analytics.SessionStats {
	return analytics.SessionStats{}
}
func (loopBusyAgentStub) GetSidebarWalltimeStats() analytics.WalltimeStats {
	return analytics.WalltimeStats{}
}
func (loopBusyAgentStub) GetContextStats() (int, int) { return 0, 0 }
func (loopBusyAgentStub) GetContextMessageCount() int { return 0 }
func (loopBusyAgentStub) GetContextBytes() int        { return 0 }
func (loopBusyAgentStub) GetContextReductionStats() agent.ContextReductionStats {
	return agent.ContextReductionStats{}
}
func (loopBusyAgentStub) KeyStats() (int, int)                                      { return 0, 0 }
func (loopBusyAgentStub) CurrentRateLimitSnapshot() *ratelimit.KeyRateLimitSnapshot { return nil }
func (loopBusyAgentStub) ProxyInUseForRef(string) bool                              { return false }
func (loopBusyAgentStub) ProjectRoot() string                                       { return "" }
func (loopBusyAgentStub) CurrentRole() string                                       { return "builder" }
func (loopBusyAgentStub) LoopKeepsMainBusy() bool                                   { return true }
func (loopBusyAgentStub) CurrentLoopState() agent.LoopState                         { return agent.LoopStateExecuting }
func (loopBusyAgentStub) MemoryEnabled() bool                                       { return false }
func (loopBusyAgentStub) CurrentLoopTarget() string                                 { return "current task" }
func (loopBusyAgentStub) CurrentLoopIteration() int                                 { return 1 }
func (loopBusyAgentStub) CurrentLoopMaxIterations() int                             { return 10 }
func (loopBusyAgentStub) CanUseLoopMode() bool                                      { return true }
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

func rawTerminalOutput(t *testing.T, cmd tea.Cmd) string {
	t.Helper()
	if cmd == nil {
		t.Fatal("expected terminal output command")
	}
	msg := cmd()
	raw, ok := msg.(tea.RawMsg)
	if !ok {
		t.Fatalf("command message = %T, want tea.RawMsg", msg)
	}
	out, ok := raw.Msg.(string)
	if !ok {
		t.Fatalf("raw terminal output = %T, want string", raw.Msg)
	}
	return out
}

func rawTerminalOutputFromBatch(t *testing.T, cmd tea.Cmd) string {
	t.Helper()
	if cmd == nil {
		t.Fatal("expected batch command")
	}
	msg := cmd()
	batch, ok := msg.(tea.BatchMsg)
	if !ok {
		t.Fatalf("command message = %T, want tea.BatchMsg", msg)
	}
	var output strings.Builder
	for _, sub := range batch {
		if sub == nil {
			continue
		}
		raw, ok := sub().(tea.RawMsg)
		if !ok {
			continue
		}
		text, ok := raw.Msg.(string)
		if !ok {
			t.Fatalf("raw terminal output = %T, want string", raw.Msg)
		}
		output.WriteString(text)
	}
	if output.Len() == 0 {
		t.Fatal("batch did not contain raw terminal output")
	}
	return output.String()
}

func TestSanitizeNotificationPayload(t *testing.T) {
	if g := sanitizeNotificationPayload("hello"); g != "hello" {
		t.Fatalf("got %q", g)
	}
	if g := sanitizeNotificationPayload("a\x07b\x1b[c"); g != "a b [c" {
		t.Fatalf("got %q", g)
	}
	if g := sanitizeNotificationPayload("line1\nline2"); g != "line1 line2" {
		t.Fatalf("got %q", g)
	}
	long := strings.Repeat("x", maxNotificationRunes+50)
	if g := sanitizeNotificationPayload(long); len([]rune(g)) != maxNotificationRunes {
		t.Fatalf("len = %d, want %d", len([]rune(g)), maxNotificationRunes)
	}
	if g := sanitizeNotificationPayload("   \x01\x02   "); g != "Chord" {
		t.Fatalf("empty/control got %q, want Chord", g)
	}
}

func TestTerminalNotificationOSC777Sequence(t *testing.T) {
	if got := terminalNotificationOSC777Sequence("Chord", "Ready"); got != "\x1b]777;notify;Chord;Ready\x07\a" {
		t.Fatalf("osc sequence = %q", got)
	}
}

func TestMaybeTerminalNotifyCmd(t *testing.T) {
	m := Model{
		desktopNotificationsEnabled:  true,
		terminalAppFocused:           false,
		terminalNotificationProtocol: terminalNotificationOSC9,
	}
	if got := rawTerminalOutput(t, m.maybeTerminalNotifyCmd("Ready")); got != "\x1b]9;Ready\x07\a" {
		t.Fatalf("osc sequence = %q", got)
	}
}

func TestMaybeOSC777NotifyCmd(t *testing.T) {
	m := Model{
		desktopNotificationsEnabled:  true,
		terminalAppFocused:           false,
		terminalNotificationProtocol: terminalNotificationOSC777,
	}
	if got := rawTerminalOutput(t, m.maybeTerminalNotifyCmd("Ready")); got != "\x1b]777;notify;Chord;Ready\x07\a" {
		t.Fatalf("osc sequence = %q", got)
	}
}

func TestMaybeTerminalNotifyCmdSuppressedWhenDisabled(t *testing.T) {
	m := Model{
		desktopNotificationsEnabled: false,
		terminalAppFocused:          false,
	}
	if cmd := m.maybeTerminalNotifyCmd("Ready"); cmd != nil {
		t.Fatal("expected nil cmd when disabled")
	}

	// Notifications (OSC sequence plus BEL) are emitted even while focused:
	// focused terminals usually hide the OSC banner, and the bell is the
	// reliable audible cue in that situation.
	m.desktopNotificationsEnabled = true
	m.desktopNotificationsForeground = true
	m.terminalAppFocused = true
	if cmd := m.maybeTerminalNotifyCmd("Ready"); cmd == nil {
		t.Fatal("expected notify cmd when focused")
	}
}

func TestMaybeTerminalNotifyCmdEmitsWhenFocused(t *testing.T) {
	m := Model{
		desktopNotificationsEnabled:    true,
		terminalAppFocused:             true,
		desktopNotificationsForeground: true,
		terminalNotificationProtocol:   terminalNotificationOSC9,
	}
	if got := rawTerminalOutput(t, m.maybeTerminalNotifyCmd("Ready")); got != "\x1b]9;Ready\x07\a" {
		t.Fatalf("osc sequence = %q", got)
	}
}

func TestMaybeTerminalNotifyCmdCanSuppressFocusedNotifications(t *testing.T) {
	m := Model{
		desktopNotificationsEnabled:    true,
		desktopNotificationsForeground: false,
		terminalAppFocused:             true,
	}
	if cmd := m.maybeTerminalNotifyCmd("Ready"); cmd != nil {
		t.Fatal("expected nil cmd when foreground notifications are disabled")
	}
}

func TestOSC9IdleNotificationUsesLastAssistantMessage(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	m.desktopNotificationsEnabled = true
	m.terminalAppFocused = false
	m.terminalNotificationProtocol = terminalNotificationOSC9
	m.viewport.AppendBlock(&Block{ID: 1, Type: BlockAssistant, Content: "model reply content"})

	if got := rawTerminalOutput(t, m.handleAgentEvent(agentEventMsg{event: agent.GlobalIdleEvent{}})); got != "\x1b]9;model reply content\x07\a" {
		t.Fatalf("osc sequence = %q, want assistant content", got)
	}
}

func TestOSC9IdleNotificationUsesLastErrorMessage(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	m.desktopNotificationsEnabled = true
	m.terminalAppFocused = false
	m.terminalNotificationProtocol = terminalNotificationOSC9

	_ = m.handleAgentEvent(agentEventMsg{event: agent.ErrorEvent{Err: errors.New("request interrupted: network error")}})
	if got := rawTerminalOutput(t, m.handleAgentEvent(agentEventMsg{event: agent.GlobalIdleEvent{}})); got != "\x1b]9;request interrupted: network error\x07\a" {
		t.Fatalf("osc sequence = %q, want error content", got)
	}
}

func TestLoopTerminalInfoUsesToastWithoutTranscriptBlock(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)

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
}

func TestLoopBlockedInfoWithCategoryDoesNotCreateUnnamedStatusCard(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	message := "Loop blocked (required_input_missing): missing candidate evidence"

	cmd := m.handleAgentEvent(agentEventMsg{event: agent.InfoEvent{Message: message}})
	if cmd == nil {
		t.Fatal("expected info followup command")
	}
	if len(m.viewport.visibleBlocks()) != 0 {
		t.Fatalf("visible blocks = %#v, want no unnamed transcript status block", m.viewport.visibleBlocks())
	}
	if m.activeToast == nil || m.activeToast.Message != message {
		t.Fatalf("activeToast = %+v, want loop blocked toast", m.activeToast)
	}
}

func TestIdleEventDoesNotNotifyWhileLoopStillBusy(t *testing.T) {
	m := NewModelWithSize(loopBusyAgentStub{}, 80, 24)
	m.desktopNotificationsEnabled = true
	m.terminalAppFocused = false

	if cmd := m.handleAgentEvent(agentEventMsg{event: agent.IdleEvent{}}); cmd != nil {
		t.Fatalf("idle command = %#v, want nil while loop is still busy", cmd)
	}
	if got := m.activities["main"].Type; got != agent.ActivityExecuting {
		t.Fatalf("main activity = %q, want %q while loop still busy", got, agent.ActivityExecuting)
	}
}

func TestIdleEventDoesNotNotifyWhileSubAgentStillActive(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	m.desktopNotificationsEnabled = true
	m.terminalAppFocused = false
	m.activities["agent-1"] = agent.AgentActivityEvent{AgentID: "agent-1", Type: agent.ActivityStreaming}

	if cmd := m.handleAgentEvent(agentEventMsg{event: agent.IdleEvent{}}); cmd != nil {
		t.Fatalf("idle command = %#v, want nil before global idle", cmd)
	}

	if got := rawTerminalOutput(t, m.handleAgentEvent(agentEventMsg{event: agent.GlobalIdleEvent{}})); got == "" {
		t.Fatal("expected OSC notification after global idle")
	}
}

func TestGlobalIdleEventDoesNotNotifyWithQueuedDraft(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	m.desktopNotificationsEnabled = true
	m.terminalAppFocused = false
	m.queuedDrafts = []queuedDraft{{Content: "queued follow-up"}}

	if cmd := m.handleAgentEvent(agentEventMsg{event: agent.GlobalIdleEvent{}}); cmd != nil {
		t.Fatalf("global idle command = %#v, want nil while queued draft can auto-continue", cmd)
	}
}

func TestGlobalIdleEventSuppressedDoesNotNotify(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	m.desktopNotificationsEnabled = true
	m.terminalAppFocused = false
	m.activities["main"] = agent.AgentActivityEvent{AgentID: "main", Type: agent.ActivityStreaming}
	m.viewport.AppendBlock(&Block{ID: 1, Type: BlockAssistant, Content: "model reply content"})

	if cmd := m.handleAgentEvent(agentEventMsg{event: agent.GlobalIdleEvent{SuppressUserNotification: true}}); cmd != nil {
		t.Fatalf("global idle command = %#v, want nil when user notification is suppressed", cmd)
	}
	if got := m.activities["main"].Type; got != agent.ActivityIdle {
		t.Fatalf("main activity after suppressed global idle = %q, want idle", got)
	}
}

func TestConfirmRequestNotifiesWhileLoopStillBusy(t *testing.T) {
	m := NewModelWithSize(loopBusyAgentStub{}, 80, 24)
	m.desktopNotificationsEnabled = true
	m.terminalAppFocused = false

	cmd := m.handleAgentEvent(agentEventMsg{event: agent.ConfirmRequestEvent{
		ToolName:  tools.NameEdit,
		ArgsJSON:  `{"patch":"*** Begin Patch\n*** Update File: internal/tui/app.go\n@@\n-old\n+new\n*** End Patch\n"}`,
		RequestID: "req-1",
	}})
	if got := rawTerminalOutputFromBatch(t, cmd); got == "" {
		t.Fatalf("osc sequence = %q, want confirm notification while loop is busy", got)
	}
}

func TestQuestionRequestNotifiesWhileLoopStillBusy(t *testing.T) {
	m := NewModelWithSize(loopBusyAgentStub{}, 80, 24)
	m.desktopNotificationsEnabled = true
	m.terminalAppFocused = false

	cmd := m.handleAgentEvent(agentEventMsg{event: agent.QuestionRequestEvent{
		RequestID: "q-1",
		Question:  "Continue?",
		Options:   []string{"Yes", "No"},
	}})
	if got := rawTerminalOutputFromBatch(t, cmd); got == "" {
		t.Fatalf("osc sequence = %q, want question notification while loop is busy", got)
	}
}

func TestIdleNotificationIgnoresMidTurnNarrationBeforeToolActivity(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	// A turn whose reply never materialized: narration, then tool activity, and
	// no assistant block for the final response (for example an output-limit
	// truncation that produced only thinking).
	m.viewport.AppendBlock(&Block{ID: 1, Type: BlockUser, Content: "commit it"})
	m.viewport.AppendBlock(&Block{ID: 2, Type: BlockAssistant, Content: "I am on main, checking hunks:"})
	m.viewport.AppendBlock(&Block{ID: 3, Type: BlockToolCall, ToolName: "shell"})
	m.viewport.AppendBlock(&Block{ID: 4, Type: BlockToolResult, Content: "diff output"})

	if got, ok := m.lastAssistantOrErrorTextForNotification(); ok {
		t.Fatalf("notification text = %q, want none: mid-turn narration must not be announced as the turn result", got)
	}
	if got := m.idleNotificationText(); got != "Chord: Ready for input" {
		t.Fatalf("idleNotificationText = %q, want the neutral fallback", got)
	}
}

func TestIdleNotificationUsesFinalReplyAfterToolActivity(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	m.viewport.AppendBlock(&Block{ID: 1, Type: BlockUser, Content: "commit it"})
	m.viewport.AppendBlock(&Block{ID: 2, Type: BlockAssistant, Content: "checking:"})
	m.viewport.AppendBlock(&Block{ID: 3, Type: BlockToolCall, ToolName: "shell"})
	m.viewport.AppendBlock(&Block{ID: 4, Type: BlockToolResult, Content: "diff output"})
	m.viewport.AppendBlock(&Block{ID: 5, Type: BlockAssistant, Content: "committed as 4f2e59fc"})

	got, ok := m.lastAssistantOrErrorTextForNotification()
	if !ok {
		t.Fatal("expected the final reply to be used for the notification")
	}
	if got != "committed as 4f2e59fc" {
		t.Fatalf("notification text = %q, want the final reply", got)
	}
}

func TestMaybeTerminalNotifyCmdBellBehavior(t *testing.T) {
	// Notifications always pair the OSC sequence with a standalone BEL: focused
	// terminals usually swallow the OSC notification itself, so the bell is the
	// only channel the user reliably hears.
	cases := []struct {
		name    string
		focused bool
		wantOut string
	}{
		{"focused", true, "\x1b]9;Ready\x07\a"},
		{"blurred", false, "\x1b]9;Ready\x07\a"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := NewModelWithSize(nil, 80, 24)
			m.desktopNotificationsEnabled = true
			m.desktopNotificationsForeground = true
			m.terminalAppFocused = tc.focused
			m.terminalNotificationProtocol = terminalNotificationOSC9
			if got := rawTerminalOutput(t, m.maybeTerminalNotifyCmd("Ready")); got != tc.wantOut {
				t.Errorf("output = %q, want %q", got, tc.wantOut)
			}
		})
	}
}
