package agent

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/keakon/chord/internal/command"
	"github.com/keakon/chord/internal/hook"
	"github.com/keakon/chord/internal/message"
)

type recordingActivityObserver struct {
	agentID  string
	activity ActivityType
	calls    int
}

type recordingBackgroundHookManager struct {
	envelopes []hook.Envelope
}

func (m *recordingBackgroundHookManager) Fire(context.Context, hook.Envelope) (*hook.Result, error) {
	return &hook.Result{Action: hook.ActionContinue}, nil
}

func (m *recordingBackgroundHookManager) FireBackground(_ context.Context, env hook.Envelope) {
	m.envelopes = append(m.envelopes, env)
}

func (m *recordingBackgroundHookManager) RunAutomation(context.Context, hook.Envelope) ([]hook.AutomationJobResult, error) {
	return nil, nil
}

func (r *recordingActivityObserver) OnAgentActivity(agentID string, activity ActivityType) {
	r.agentID = agentID
	r.activity = activity
	r.calls++
}

func TestSetActivityObserverAndEmitActivity(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	obs := &recordingActivityObserver{}
	a.SetActivityObserver(obs)

	a.emitActivity("main", ActivityStreaming, "planning")
	if obs.calls != 1 || obs.agentID != "main" || obs.activity != ActivityStreaming {
		t.Fatalf("observer = %+v, want one thinking notification for main", obs)
	}

	a.SetActivityObserver(nil)
	a.emitActivity("main", ActivityIdle, "done")
	if obs.calls != 1 {
		t.Fatalf("observer calls after unregister = %d, want 1", obs.calls)
	}
}

func TestEmitGlobalIdleWaitsForRunningSubAgent(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	sub := newControllableTestSubAgent(t, a, "task-1")
	sub.setState(SubAgentStateRunning, "working")

	if a.emitGlobalIdleIfReady() {
		t.Fatal("global idle emitted while SubAgent was running")
	}
	select {
	case evt := <-a.Events():
		t.Fatalf("unexpected event while SubAgent active: %T", evt)
	default:
	}

	sub.setState(SubAgentStateCompleted, "done")
	if !a.emitGlobalIdleIfReady() {
		t.Fatal("expected global idle after the final running SubAgent completed")
	}
	select {
	case evt := <-a.Events():
		if _, ok := evt.(GlobalIdleEvent); !ok {
			t.Fatalf("event = %T, want GlobalIdleEvent", evt)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for GlobalIdleEvent")
	}
	if a.emitGlobalIdleIfReady() {
		t.Fatal("duplicate global idle emitted without intervening work")
	}
}

func TestDispatchDoesNotRepeatGlobalIdleWithoutInterveningWork(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	if !a.emitGlobalIdleIfReady() {
		t.Fatal("expected initial global idle")
	}
	<-a.Events()

	a.dispatch(Event{Type: EventAgentLog, SourceID: "agent-1", Payload: "late informational event"})

	for len(a.outputCh) > 0 {
		if _, ok := (<-a.outputCh).(GlobalIdleEvent); ok {
			t.Fatal("unexpected repeated GlobalIdleEvent without intervening work")
		}
	}
}

func TestNonIdleActivityRearmsGlobalIdle(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	if !a.emitGlobalIdleIfReady() {
		t.Fatal("expected initial global idle")
	}
	<-a.Events()

	a.emitActivity("agent-1", ActivityStreaming, "working")
	a.emitActivity("agent-1", ActivityIdle, "")
	if !a.emitGlobalIdleIfReady() {
		t.Fatal("expected a new GlobalIdleEvent after intervening SubAgent activity")
	}
}

func TestGlobalIdleHookKeepsLastMainTurnID(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	hooks := &recordingBackgroundHookManager{}
	a.hookEngine = hooks
	a.newTurn()
	turnID := a.currentTurnID()

	a.setIdleAndDrainPending()
	if !a.emitGlobalIdleIfReady() {
		t.Fatal("expected global idle after main turn completed")
	}

	if len(hooks.envelopes) != 1 {
		t.Fatalf("background hook count = %d, want 1", len(hooks.envelopes))
	}
	env := hooks.envelopes[0]
	if env.Point != hook.OnIdle || env.TurnID != turnID {
		t.Fatalf("idle hook envelope = %#v, want point=%q turn_id=%d", env, hook.OnIdle, turnID)
	}
}

func TestEmitGlobalIdleWaitsForQueuedAutomaticWork(t *testing.T) {
	tests := []struct {
		name  string
		queue func(*MainAgent)
	}{
		{
			name: "pending user input",
			queue: func(a *MainAgent) {
				a.pendingUserMessages = []pendingUserMessage{{Content: "queued follow-up", FromUser: true}}
			},
		},
		{
			name: "automatic continuation prompt",
			queue: func(a *MainAgent) {
				a.pendingAutoContinuePrompt = "continue"
			},
		},
		{
			name: "internal event",
			queue: func(a *MainAgent) {
				a.sendEvent(Event{Type: EventResetNudge})
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := newTestMainAgent(t, t.TempDir())
			tt.queue(a)
			if a.emitGlobalIdleIfReady() {
				t.Fatal("global idle emitted while automatic work was queued")
			}
		})
	}
}

func TestGlobalIdleWaitsForQueuedSubAgentInput(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	sub := newControllableTestSubAgent(t, a, "task-queued-input")
	sub.setState(SubAgentStateIdle, "finishing previous request")
	if !sub.InjectUserMessage("mailbox follow-up") {
		t.Fatal("InjectUserMessage() = false")
	}

	if a.emitGlobalIdleIfReady() {
		t.Fatal("global idle emitted while SubAgent input was queued")
	}
	select {
	case evt := <-a.Events():
		if _, ok := evt.(GlobalIdleEvent); ok {
			t.Fatal("unexpected GlobalIdleEvent while SubAgent input was queued")
		}
	default:
	}
	<-sub.inputCh
	sub.setState(SubAgentStateCompleted, "done")
	if !a.emitGlobalIdleIfReady() {
		t.Fatal("expected global idle after queued SubAgent input was drained")
	}
}

func TestGlobalIdleDrainsActionableOwnerMailboxBeforeNotifying(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	owner := newControllableTestSubAgent(t, a, "task-owner")
	owner.setState(SubAgentStateIdle, "finishing previous request")
	a.enqueueOwnedSubAgentMailbox(SubAgentMailboxMessage{
		MessageID:    "child-complete-1",
		AgentID:      "child-1",
		TaskID:       "task-child",
		OwnerAgentID: owner.instanceID,
		OwnerTaskID:  owner.taskID,
		Kind:         SubAgentMailboxKindCompleted,
		Priority:     SubAgentMailboxPriorityUrgent,
		Summary:      "child completed",
	})

	if a.emitGlobalIdleIfReady() {
		t.Fatal("global idle emitted instead of resuming mailbox owner")
	}
	if owner.State() != SubAgentStateRunning {
		t.Fatalf("owner.State() = %q, want running", owner.State())
	}
	select {
	case pending := <-owner.inputCh:
		if text := pendingUserMessageText(pending); !strings.Contains(text, "child completed") {
			t.Fatalf("queued owner message = %q, want child completion", text)
		}
	default:
		t.Fatal("expected actionable mailbox to queue owner follow-up")
	}
	if queued := a.ownedSubAgentMailboxes[owner.instanceID]; len(queued) != 0 {
		t.Fatalf("owned mailbox queue = %#v, want drained", queued)
	}
}

func TestProgressOnlyMailboxDoesNotPreventGlobalIdle(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.subAgentInbox.progress["worker-1"] = SubAgentMailboxMessage{
		AgentID: "worker-1",
		Kind:    SubAgentMailboxKindProgress,
		Summary: "still working",
	}

	if !a.emitGlobalIdleIfReady() {
		t.Fatal("progress-only mailbox should not require an LLM continuation")
	}
}

func TestCompletedSubAgentMailboxStartsMainTurnBeforeGlobalIdle(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	sub := newControllableTestSubAgent(t, a, "task-1")
	sub.setState(SubAgentStateCompleted, "done")

	a.handleSubAgentMailboxEvent(Event{
		SourceID: sub.instanceID,
		Payload: &SubAgentMailboxMessage{
			MessageID: "worker-1-1",
			AgentID:   sub.instanceID,
			TaskID:    sub.taskID,
			Kind:      SubAgentMailboxKindCompleted,
			Summary:   "done",
		},
	})

	if a.currentTurn() == nil {
		t.Fatal("completed SubAgent mailbox did not start the main summary turn")
	}
	if got := len(a.pendingSubAgentMailboxes); got != 1 {
		t.Fatalf("pending completion mailbox count = %d, want 1", got)
	}
	if a.pendingSubAgentMailboxes[0] == nil || a.pendingSubAgentMailboxes[0].MessageID != "worker-1-1" {
		t.Fatalf("pending completion mailbox = %#v, want worker-1-1", a.pendingSubAgentMailboxes[0])
	}
	if a.emitGlobalIdleIfReady() {
		t.Fatal("global idle emitted while the main summary turn was active")
	}
}

func TestMainMailboxOverlayPersistsExactModelMessage(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	mailbox := &SubAgentMailboxMessage{
		MessageID: "worker-1-1",
		AgentID:   "worker-1",
		TaskID:    "task-1",
		Kind:      SubAgentMailboxKindCompleted,
		Summary:   "finished review",
	}
	a.pendingSubAgentMailboxes = []*SubAgentMailboxMessage{mailbox}

	overlays := a.buildTurnOverlayMessages()
	if len(overlays) != 1 {
		t.Fatalf("overlay count = %d, want 1", len(overlays))
	}
	wantContent := "<system-reminder>\n" + formatSubAgentMailboxInjectionText(mailbox) + "\n</system-reminder>"
	got := overlays[0]
	if got.Content != wantContent || got.Kind != message.KindSubAgentMailbox || got.Mailbox == nil {
		t.Fatalf("overlay = %#v, want durable mailbox message", got)
	}
	if got.Mailbox.MessageID != "worker-1-1" || got.Mailbox.Kind != "completed" {
		t.Fatalf("mailbox metadata = %#v", got.Mailbox)
	}
	ctx := a.ctxMgr.Snapshot()
	if len(ctx) != 1 || ctx[0].Content != got.Content || ctx[0].Mailbox == nil {
		t.Fatalf("context messages = %#v, want exact overlay persisted in context", ctx)
	}
	a.flushPersist()
	persisted, err := a.recovery.LoadMessages("main")
	if err != nil {
		t.Fatalf("LoadMessages(main): %v", err)
	}
	if len(persisted) != 1 || persisted[0].Content != got.Content || persisted[0].Mailbox == nil || persisted[0].Mailbox.MessageID != "worker-1-1" {
		t.Fatalf("persisted messages = %#v, want exact durable mailbox message", persisted)
	}
}

func TestMainMailboxRetryDoesNotDuplicateDurableMessage(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	mailbox := &SubAgentMailboxMessage{
		MessageID: "worker-1-1",
		AgentID:   "worker-1",
		TaskID:    "task-1",
		Kind:      SubAgentMailboxKindCompleted,
		Summary:   "finished review",
	}
	a.pendingSubAgentMailboxes = []*SubAgentMailboxMessage{mailbox}
	a.activeSubAgentMailboxes = []*SubAgentMailboxMessage{mailbox}
	a.activeSubAgentMailbox = mailbox
	a.activeSubAgentMailboxAck = true
	first := a.buildTurnOverlayMessages()

	a.markActiveSubAgentMailboxAck(false)
	a.requeueActiveSubAgentMailbox()
	a.activeSubAgentMailboxes = nil
	a.activeSubAgentMailbox = nil
	a.pendingSubAgentMailboxes = nil
	if !a.stageNextSubAgentMailboxBatch() {
		t.Fatal("retry mailbox was not restaged")
	}
	second := a.buildTurnOverlayMessages()
	if len(first) != 1 || len(second) != 1 {
		t.Fatalf("overlay counts = (%d, %d), want one delivery per request", len(first), len(second))
	}
	prepared, _ := applyTurnOverlayMessages(a.ctxMgr.Snapshot(), second)
	if got := countSubAgentMailboxMessages(prepared, mailbox.MessageID); got != 1 {
		t.Fatalf("request mailbox copies = %d, want 1", got)
	}
	a.flushPersist()
	if got := countSubAgentMailboxMessages(a.ctxMgr.Snapshot(), mailbox.MessageID); got != 1 {
		t.Fatalf("context mailbox copies = %d, want 1", got)
	}
	persisted, err := a.recovery.LoadMessages("main")
	if err != nil {
		t.Fatalf("LoadMessages(main): %v", err)
	}
	if got := countSubAgentMailboxMessages(persisted, mailbox.MessageID); got != 1 {
		t.Fatalf("persisted mailbox copies = %d, want 1", got)
	}
}

func countSubAgentMailboxMessages(messages []message.Message, messageID string) int {
	count := 0
	for _, msg := range messages {
		if msg.Kind == message.KindSubAgentMailbox && msg.Mailbox != nil && msg.Mailbox.MessageID == messageID {
			count++
		}
	}
	return count
}

func TestApplyTurnOverlayMessagesKeepsDurableMailboxAtConversationTail(t *testing.T) {
	base := []message.Message{{Role: "user", Content: "request"}, {Role: "assistant", Content: "working"}}
	mailbox := message.Message{Role: "user", Content: "mailbox", Kind: message.KindSubAgentMailbox}
	transient := message.Message{Role: "user", Content: "runtime hint"}

	got, prefixCount := applyTurnOverlayMessages(base, []message.Message{mailbox, transient})
	if prefixCount != 1 {
		t.Fatalf("prefixCount = %d, want 1", prefixCount)
	}
	if len(got) != 4 {
		t.Fatalf("message count = %d, want 4", len(got))
	}
	if got[0].Content != "runtime hint" || got[1].Content != "request" || got[2].Content != "working" || got[3].Content != "mailbox" {
		t.Fatalf("message order = %#v", got)
	}
}

func TestApplyTurnOverlayMessagesDoesNotDuplicateExistingMailbox(t *testing.T) {
	mailbox := message.Message{
		Role:    message.RoleUser,
		Content: "mailbox",
		Kind:    message.KindSubAgentMailbox,
		Mailbox: &message.MailboxMetadata{MessageID: "worker-1-1"},
	}
	got, prefixCount := applyTurnOverlayMessages([]message.Message{{Role: message.RoleUser, Content: "request"}, mailbox}, []message.Message{mailbox})
	if prefixCount != 0 {
		t.Fatalf("prefixCount = %d, want 0", prefixCount)
	}
	if count := countSubAgentMailboxMessages(got, "worker-1-1"); count != 1 {
		t.Fatalf("mailbox copies = %d, want 1", count)
	}
}

func TestCustomSlashExpansionAndModelExpansion(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.SetCustomCommands([]*command.Definition{
		{Name: "Review", Template: "Review this change:\n$ARGUMENTS"},
		{Name: "Explain", Template: "Explain the code"},
	})

	expanded, ok := a.customSlashExpansion("/review file.go")
	if !ok || expanded != "Review this change:\nfile.go" {
		t.Fatalf("customSlashExpansion review = (%q, %v)", expanded, ok)
	}
	expanded, ok = a.customSlashExpansion("/EXPLAIN topic")
	if !ok || expanded != "Explain the code\n\ntopic" {
		t.Fatalf("customSlashExpansion explain = (%q, %v)", expanded, ok)
	}
	if _, ok := a.customSlashExpansion("review file.go"); ok {
		t.Fatal("customSlashExpansion without slash should not match")
	}
	if _, ok := a.customSlashExpansion("/missing args"); ok {
		t.Fatal("customSlashExpansion unknown command should not match")
	}

	content, parts := a.expandSlashCommandForModel("  /review main.go  ", nil)
	if content != "Review this change:\nmain.go" || parts != nil {
		t.Fatalf("expandSlashCommandForModel content = %q parts=%#v", content, parts)
	}
	content, parts = a.expandSlashCommandForModel("ignored", []message.ContentPart{{Type: "text", Text: " /review part.go "}})
	if content != "Review this change:\npart.go" || parts != nil {
		t.Fatalf("expandSlashCommandForModel text part = %q parts=%#v", content, parts)
	}
	origParts := []message.ContentPart{{Type: "image"}, {Type: "text", Text: "/review image"}}
	content, parts = a.expandSlashCommandForModel("original", origParts)
	if content != "original" || len(parts) != len(origParts) {
		t.Fatalf("image parts should bypass expansion, got content=%q parts=%#v", content, parts)
	}
}

func TestExpandCommandTemplate(t *testing.T) {
	if got := expandCommandTemplate("Run: $ARGUMENTS", "go test"); got != "Run: go test" {
		t.Fatalf("placeholder expansion = %q", got)
	}
	if got := expandCommandTemplate("Run", "go test"); got != "Run\n\ngo test" {
		t.Fatalf("argument append expansion = %q", got)
	}
	if got := expandCommandTemplate("Run", ""); got != "Run" {
		t.Fatalf("empty argument expansion = %q", got)
	}
}

func TestAutomationFeedbackFormattingAndPolicies(t *testing.T) {
	result := hook.AutomationResult{
		Status:  hook.AutomationStatusFailed,
		Summary: "lint failed",
		Body:    "line1\nline2\nline3",
	}
	formatted := formatAutomationFeedback(hook.HookDef{Name: "lint", ResultFormat: hook.ResultFormatTail, MaxResultLines: 2}, result)
	for _, want := range []string{"[hook:lint]", "status: failed", "summary: lint failed", "line2\nline3"} {
		if !strings.Contains(formatted, want) {
			t.Fatalf("formatted feedback = %q, want substring %q", formatted, want)
		}
	}
	if strings.Contains(formatted, "line1") {
		t.Fatalf("formatted feedback = %q, tail format should omit first line", formatted)
	}

	if got := selectAutomationBody(hook.HookDef{ResultFormat: hook.ResultFormatFull}, result); got != result.Body {
		t.Fatalf("full body = %q", got)
	}
	if got := selectAutomationBody(hook.HookDef{}, result); got != result.Summary {
		t.Fatalf("default summary body = %q", got)
	}
	if got := trimAutomationBody("a\nb\nc", 2, 100); got != "a\nb" {
		t.Fatalf("trim lines = %q", got)
	}
	if got := trimAutomationBody("abcdef", 50, 3); got != "abc\n... (truncated)" {
		t.Fatalf("trim bytes = %q", got)
	}

	defaultTailLines := make([]string, hook.DefaultMaxResultLines+2)
	for i := range defaultTailLines {
		defaultTailLines[i] = fmt.Sprintf("line-%d", i)
	}
	defaultTail := selectAutomationBody(hook.HookDef{ResultFormat: hook.ResultFormatTail}, hook.AutomationResult{
		Body: strings.Join(defaultTailLines, "\n"),
	})
	gotLines := strings.Split(defaultTail, "\n")
	if len(gotLines) != hook.DefaultMaxResultLines {
		t.Fatalf("default tail lines = %d, want %d", len(gotLines), hook.DefaultMaxResultLines)
	}
	if gotLines[0] != defaultTailLines[2] || gotLines[len(gotLines)-1] != defaultTailLines[len(defaultTailLines)-1] {
		t.Fatalf("default tail = %q, want last %d lines", defaultTail, hook.DefaultMaxResultLines)
	}
	defaultTrim := trimAutomationBody(strings.Repeat("x", hook.DefaultMaxResultBytes+1), 0, 0)
	if !strings.HasSuffix(defaultTrim, "... (truncated)") {
		t.Fatalf("default trim = %q, want truncation suffix", defaultTrim)
	}
	if firstLine, _, _ := strings.Cut(defaultTrim, "\n"); len(firstLine) != hook.DefaultMaxResultBytes {
		t.Fatalf("default trim first line bytes = %d, want %d", len(firstLine), hook.DefaultMaxResultBytes)
	}

	if !shouldAppendAutomationResult(hook.HookDef{}, hook.AutomationResult{AppendContext: true}) {
		t.Fatal("AppendContext should force append")
	}
	if !shouldAppendAutomationResult(hook.HookDef{Result: hook.ResultAlwaysAppend}, hook.AutomationResult{}) {
		t.Fatal("always_append should append")
	}
	if !shouldAppendAutomationResult(hook.HookDef{Result: hook.ResultAppendOnFailure}, hook.AutomationResult{Status: hook.AutomationStatusFailed}) {
		t.Fatal("append_on_failure should append failed result")
	}
	if shouldAppendAutomationResult(hook.HookDef{Result: hook.ResultAppendOnFailure}, hook.AutomationResult{Status: hook.AutomationStatusSuccess}) {
		t.Fatal("append_on_failure should not append successful result")
	}

	for _, tc := range []struct {
		severity string
		want     string
	}{
		{severity: "warning", want: "warn"},
		{severity: "warn", want: "warn"},
		{severity: "error", want: "error"},
		{severity: "", want: "info"},
	} {
		if got := hookToastLevel(hook.AutomationResult{Severity: tc.severity}); got != tc.want {
			t.Fatalf("hookToastLevel(%q) = %q, want %q", tc.severity, got, tc.want)
		}
	}
}
