package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

func TestEffectiveProgressReminderRequiresOptIn(t *testing.T) {
	for _, tc := range []struct {
		name    string
		global  *config.Config
		project *config.Config
		want    bool
	}{
		{"unset", nil, nil, false},
		{"empty configs", &config.Config{}, &config.Config{}, false},
		{"global enabled", &config.Config{ProgressReminder: new(true)}, nil, true},
		{"project enabled", &config.Config{}, &config.Config{ProgressReminder: new(true)}, true},
		{"project disables global", &config.Config{ProgressReminder: new(true)}, &config.Config{ProgressReminder: new(false)}, false},
		{"global disabled", &config.Config{ProgressReminder: new(false)}, nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := effectiveProgressReminder(tc.global, tc.project); got != tc.want {
				t.Fatalf("effectiveProgressReminder = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestProgressNoticeDisabledByDefault(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	if a.progressReminderEnabled {
		t.Fatal("an unconfigured agent must leave progress reminders off")
	}
	a.newTurn("")
	a.turn.progress.since = time.Now().Add(-progressNoticeInterval)
	a.turn.progress.rounds = progressNoticeRounds
	if _, ok := a.buildProgressNoticeOverlay(); ok {
		t.Fatal("a disabled agent staged a progress reminder")
	}
	if a.turn.progress.stagedForDispatch() {
		t.Fatal("a disabled agent left a dispatch claim staged")
	}
	before := a.ctxMgr.MessageCount()
	a.confirmProgressNotice(a.turn)
	a.flushPersist()
	if a.ctxMgr.MessageCount() != before {
		t.Fatal("a disabled agent persisted a reminder row")
	}
	for _, evt := range drainAgentEvents(a.outputCh) {
		if _, ok := evt.(ProgressNoticeEvent); ok {
			t.Fatal("a disabled agent emitted a progress card")
		}
	}
}

func TestProgressNoticeOptInEnablesGeneration(t *testing.T) {
	a := newTestMainAgentWithConfig(t, t.TempDir(), &config.Config{ProgressReminder: new(true)}, nil)
	if !a.progressReminderEnabled {
		t.Fatal("progress_reminder: true did not reach the agent")
	}
	a.newTurn("")
	a.turn.progress.since = time.Now().Add(-progressNoticeInterval)
	a.turn.progress.rounds = progressNoticeRounds
	if _, ok := a.buildProgressNoticeOverlay(); !ok {
		t.Fatal("an opted-in agent did not stage a progress reminder")
	}
}

func TestProgressNarrationThresholds(t *testing.T) {
	start := time.Unix(1000, 0)
	for _, tc := range []struct {
		name    string
		elapsed time.Duration
		rounds  int
		want    bool
	}{
		{"fast rounds", 5*time.Minute - time.Nanosecond, 20, false},
		{"long single round", 10 * time.Minute, 1, false},
		{"below round threshold", 5 * time.Minute, 9, false},
		{"both boundaries", 5 * time.Minute, 10, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := &progressNarration{since: start}
			for range tc.rounds {
				p.toolRoundStarted(false)
				p.toolRoundSettled()
			}
			if got := p.stage(start.Add(tc.elapsed)); got != tc.want {
				t.Fatalf("stage = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestProgressNarrationDispatchAndTextReset(t *testing.T) {
	start := time.Unix(1000, 0)
	p := &progressNarration{since: start, rounds: 10}
	now := start.Add(5 * time.Minute)
	if !p.stage(now) {
		t.Fatal("a qualifying chain must stage a reminder")
	}
	if !p.stage(now) {
		t.Fatal("an undispatched request must leave the reminder available")
	}
	if !p.confirm() || p.confirm() || p.stage(now.Add(time.Hour)) {
		t.Fatal("one text-free chain must deliver only once")
	}
	p.text(" \n\u200b", now)
	if p.stage(now.Add(time.Hour)) {
		t.Fatal("empty text must not rearm the reminder")
	}
	p.text("The cause is still unresolved; checking the next path.", now)
	if p.stage(now.Add(time.Hour)) {
		t.Fatal("text must also reset the round count")
	}
	p.toolRoundStarted(true)
	p.toolRoundSettled()
	if p.rounds != 0 {
		t.Fatal("a response with text must not count as a silent tool round")
	}
	for range 10 {
		p.toolRoundStarted(false)
		p.toolRoundSettled()
	}
	if p.stage(now.Add(5*time.Minute-time.Nanosecond)) || !p.stage(now.Add(5*time.Minute)) {
		t.Fatal("text must start a fresh timing interval")
	}
}

func TestProgressNarrationOverlappingWaits(t *testing.T) {
	start := time.Unix(1000, 0)
	p := &progressNarration{since: start, rounds: 10}
	p.beginWait(start.Add(30 * time.Second))
	p.beginWait(start.Add(40 * time.Second))
	p.endWait(start.Add(100 * time.Second))
	if p.stage(start.Add(120 * time.Second)) {
		t.Fatal("a pending user wait must suppress the reminder")
	}
	p.endWait(start.Add(130 * time.Second))
	if p.stage(start.Add(399*time.Second)) || !p.stage(start.Add(400*time.Second)) {
		t.Fatal("overlapping waits must be excluded once, leaving 5 minutes of active work")
	}
	p.beginWait(start.Add(200 * time.Second))
	p.text("Checking another possibility.", start.Add(210*time.Second))
	p.endWait(start.Add(310 * time.Second))
	for range 10 {
		p.toolRoundStarted(false)
		p.toolRoundSettled()
	}
	if p.stage(start.Add(609*time.Second)) || !p.stage(start.Add(610*time.Second)) {
		t.Fatal("text during a wait must reset the active-work baseline")
	}
}

// enableProgressReminder mirrors an explicit `progress_reminder: true` in
// config.yaml for tests that exercise reminder generation.
func enableProgressReminder(a *MainAgent) *MainAgent {
	a.progressReminderEnabled = true
	return a
}

func readyProgressAgent(t *testing.T) *MainAgent {
	t.Helper()
	a := newTestMainAgentWithConfig(t, t.TempDir(), &config.Config{ProgressReminder: new(true)}, nil)
	a.newTurn("")
	a.turn.progress.since = time.Now().Add(-progressNoticeInterval)
	a.turn.progress.rounds = progressNoticeRounds
	return a
}

func progressOverlay(t *testing.T, a *MainAgent) message.Message {
	t.Helper()
	for _, msg := range a.buildTurnOverlayMessages() {
		if strings.Contains(msg.Content, progressNoticeText) {
			return msg
		}
	}
	t.Fatal("missing progress reminder in the request")
	return message.Message{}
}

func waitProgressNotice(t *testing.T, a *MainAgent) ProgressNoticeEvent {
	t.Helper()
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	for {
		select {
		case evt := <-a.outputCh:
			if notice, ok := evt.(ProgressNoticeEvent); ok {
				return notice
			}
		case <-timer.C:
			t.Fatal("timed out waiting for progress notice")
		}
	}
}

func TestProgressNoticeDispatchPersistenceAndRecovery(t *testing.T) {
	for _, failPersistence := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "recovery"}[failPersistence], func(t *testing.T) {
			a := readyProgressAgent(t)
			if failPersistence {
				a.installRecoveryManager(newBrokenPathRecoveryManager(t))
			}
			before := a.ctxMgr.MessageCount()
			overlay := progressOverlay(t, a)
			if before != a.ctxMgr.MessageCount() || a.turn.progress.delivered {
				t.Fatal("request assembly consumed the reminder")
			}
			a.confirmProgressNotice(a.turn)
			a.confirmProgressNotice(a.turn)
			a.flushPersist()
			msgs := a.ctxMgr.Snapshot()
			if len(msgs) != before+1 {
				t.Fatalf("messages = %d, want one durable reminder", len(msgs))
			}
			row := msgs[before]
			if row.Kind != message.KindProgressNotice || row.Content != overlay.Content || message.IsUserAuthored(row) {
				t.Fatalf("invalid durable reminder: %+v", row)
			}
			if failPersistence {
				for {
					select {
					case evt := <-a.outputCh:
						if _, ok := evt.(ProgressNoticeEvent); ok {
							t.Fatal("failed persistence emitted a card")
						}
					default:
						goto drained
					}
				}
			drained:
				a.pendingOverlayAppendsMu.Lock()
				pending := len(a.pendingOverlayAppends)
				a.pendingOverlayAppendsMu.Unlock()
				if pending != 1 {
					t.Fatalf("pending overlay appends = %d, want 1", pending)
				}
				a.flushPendingOverlayAppends()
			}
			evt := waitProgressNotice(t, a)
			if evt.MessageIndex != before || evt.Message != progressNoticeText {
				t.Fatalf("notice = %+v", evt)
			}
			if _, ok := a.buildProgressNoticeOverlay(); ok {
				t.Fatal("a delivered reminder was staged again")
			}
		})
	}
}

func TestProgressNoticeCancelBeforeDispatchLeavesNoCard(t *testing.T) {
	a := readyProgressAgent(t)
	before := a.ctxMgr.MessageCount()
	progressOverlay(t, a)
	// A dispatch cancelled after request assembly never reaches the
	// request-started acknowledgement, so confirmProgressNotice is not called.
	a.turn.Cancel()
	a.flushPersist()
	if a.ctxMgr.MessageCount() != before {
		t.Fatal("cancelled dispatch persisted a reminder row")
	}
	for _, evt := range drainAgentEvents(a.outputCh) {
		if _, ok := evt.(ProgressNoticeEvent); ok {
			t.Fatal("cancelled dispatch emitted a progress card")
		}
	}
	if a.turn.progress.delivered {
		t.Fatal("cancelled dispatch consumed the reminder")
	}
}

func TestProgressNoticeCancelledPausedAndNewTurns(t *testing.T) {
	a := readyProgressAgent(t)
	a.turn.Cancel()
	if _, ok := a.buildProgressNoticeOverlay(); ok {
		t.Fatal("cancelled turn staged a reminder")
	}
	a.newTurn("")
	if a.turn.progress.rounds != 0 || a.turn.progress.delivered {
		t.Fatal("new turn inherited progress state")
	}
	a.turn.progress.since = time.Now().Add(-time.Hour)
	a.turn.progress.rounds = progressNoticeRounds
	a.sessionExecutionGate().Freeze()
	if _, ok := a.buildProgressNoticeOverlay(); ok {
		t.Fatal("paused session staged a reminder")
	}
}

func TestProgressNarrationCountsWholeResponseOnce(t *testing.T) {
	a := readyProgressAgent(t)
	a.sessionExecutionGate().Freeze()
	p := a.turn.progress
	p.rounds = 0
	p.toolRoundStarted(false)
	a.turn.PendingToolCalls.Store(3)
	// A later scheduler batch still belongs to the same assistant response.
	a.turn.toolExecutionBatches = []toolExecutionBatch{
		{},
		{Calls: []message.ToolCall{{ID: "last", Name: tools.NameRead, Args: json.RawMessage(`{"path":"sample.txt"}`)}}},
	}
	a.turn.nextToolBatch = 1
	for i, err := range []error{nil, errors.New("sample failure"), context.Canceled} {
		id := []string{"success", "error", "cancelled"}[i]
		payload := &ToolResultPayload{CallID: id, Name: tools.NameRead, Result: "result", Error: err}
		event := Event{Type: EventToolResult, TurnID: a.turn.ID, Payload: payload}
		a.handleToolResult(event)
		a.handleToolResult(event)
		if p.rounds != 0 {
			t.Fatal("a partial response or a duplicate terminal counted as a round")
		}
	}
	select {
	case event := <-a.eventCh:
		if event.Type != EventToolResult {
			t.Fatalf("event type = %v, want tool result", event.Type)
		}
		a.handleToolResult(event)
		a.handleToolResult(event)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the final scheduler batch")
	}
	if p.rounds != 1 {
		t.Fatalf("round count = %d, want one complete response", p.rounds)
	}
}

func TestProgressNarrationResponseTextExcludesToolRound(t *testing.T) {
	for _, tc := range []struct {
		name     string
		streamed string
		content  string
		want     int
	}{
		{"silent", "", "", 10},
		{"finalized text", "", "Checking another path.", 0},
		{"streamed text", "Checking another path.", "", 0},
		{"invisible text", "", " \n\u200b", 10},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := readyProgressAgent(t)
			a.sessionExecutionGate().Freeze()
			p := a.turn.progress
			p.rounds = 9
			if tc.streamed != "" {
				reducer := a.newMainLLMStreamReducer(nil, "", "", a.turn, false, nil, 1)
				reducer.Handle(message.StreamDelta{Type: message.StreamDeltaText, Text: tc.streamed})
			}
			calls := make([]message.ToolCall, 12)
			for i := range calls {
				calls[i] = message.ToolCall{ID: string(rune('a' + i)), Name: tools.NameRead, Args: json.RawMessage(`{"path":"sample.txt"}`)}
			}
			a.handleLLMResponse(Event{Type: EventLLMResponse, TurnID: a.turn.ID, Payload: &LLMResponsePayload{
				Content: tc.content, ToolCalls: calls, StopReason: "tool_calls",
			}})
			timer := time.NewTimer(2 * time.Second)
			defer timer.Stop()
			for range calls {
				select {
				case event := <-a.eventCh:
					if event.Type != EventToolResult {
						t.Fatalf("event type = %v, want tool result", event.Type)
					}
					a.handleToolResult(event)
				case <-timer.C:
					t.Fatal("timed out waiting for tool results")
				}
			}
			if p.rounds != tc.want {
				t.Fatalf("round count = %d, want %d", p.rounds, tc.want)
			}
		})
	}
}

func TestProgressNarrationStreamingAndWaitBinding(t *testing.T) {
	a := readyProgressAgent(t)
	p := a.turn.progress
	reducer := a.newMainLLMStreamReducer(nil, "", "", a.turn, false, nil, 1)
	reducer.Handle(message.StreamDelta{Type: message.StreamDeltaThinking, Text: "Considering the evidence"})
	if p.rounds != progressNoticeRounds {
		t.Fatal("thinking reset the round count")
	}
	reducer.Handle(message.StreamDelta{Type: message.StreamDeltaText, Text: "Checking the next path."})
	if p.rounds != 0 {
		t.Fatal("visible text did not reset the round count")
	}
	resume := pauseProgressNarration(a.turn.Ctx)
	if p.waiters != 1 {
		t.Fatal("tool context did not pause its own turn")
	}
	resume()
	if p.waiters != 0 {
		t.Fatal("settled user wait remained paused")
	}
}

func TestProgressNoticeTravelsWithDispatchedRequest(t *testing.T) {
	a := enableProgressReminder(newReadyTestMainAgent(t))
	a.newTurn("")
	a.turn.progress.since = time.Now().Add(-progressNoticeInterval)
	a.turn.progress.rounds = progressNoticeRounds
	sessionEpoch := a.sessionEpoch
	providerCfg := llm.NewProviderConfig("sample", config.ProviderConfig{
		Type: config.ProviderTypeChatCompletions,
		Models: map[string]config.ModelConfig{
			"test-model": {Limit: config.ModelLimit{Context: 128000, Output: 4096}},
		},
	}, []string{"test-key"})
	provider := &blockingStreamProvider{
		streamedCh: make(chan struct{}),
		releaseCh:  make(chan struct{}),
		calls: []scriptedStreamCall{{
			holdAfterStreams: true,
			resp:             &message.Response{Content: "Result", StopReason: "stop"},
		}},
	}
	released := false
	client := llm.NewClient(providerCfg, provider, "test-model", 4096, "sys")
	a.swapLLMClientWithRef(client, "test-model", 128000, "sample/test-model")

	runCtx, stopRun := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() { runDone <- a.Run(runCtx) }()
	requestCtx, cancelRequest := context.WithTimeout(context.Background(), 5*time.Second)
	waitForAgentState(t, "progress notice test agent started", func() bool { return a.started.Load() })
	before := a.ctxMgr.MessageCount()
	requestDone := make(chan error, 1)
	go func() {
		_, err := a.callLLMForRequest(requestCtx, llmRequestInput{
			Messages:        []message.Message{{Role: message.RoleUser, Content: "Inspect the behavior."}},
			sessionEpoch:    sessionEpoch,
			hasSessionEpoch: true,
		}, 0)
		requestDone <- err
	}()
	requestFinished := false
	defer func() {
		if !released {
			close(provider.releaseCh)
		}
		cancelRequest()
		stopRun()
		if err := a.Shutdown(5 * time.Second); err != nil {
			t.Errorf("shutdown: %v", err)
		}
		select {
		case <-runDone:
		case <-time.After(2 * time.Second):
			t.Error("agent did not stop")
		}
		if !requestFinished {
			select {
			case <-requestDone:
			case <-time.After(2 * time.Second):
				t.Error("LLM request did not stop")
			}
		}
	}()
	select {
	case <-provider.streamedCh:
	case <-requestCtx.Done():
		t.Fatal("timed out waiting for provider dispatch")
	}
	requests, _ := provider.snapshot()
	found := false
	for _, msg := range requests[0] {
		found = found || strings.Contains(msg.Content, progressNoticeText)
	}
	if !found {
		t.Fatal("provider did not receive the progress reminder")
	}
	notice := waitProgressNotice(t, a)
	if notice.MessageIndex != before || a.ctxMgr.MessageCount() != before+1 {
		t.Fatalf("dispatch notice = %+v, transcript messages = %d; expected one persisted reminder before provider response", notice, a.ctxMgr.MessageCount())
	}
	close(provider.releaseCh)
	released = true
	select {
	case err := <-requestDone:
		requestFinished = true
		if err != nil {
			t.Fatal(err)
		}
	case <-requestCtx.Done():
		t.Fatal("timed out waiting for request completion")
	}
	a.flushPersist()
	if count := a.ctxMgr.MessageCount(); count != before+1 {
		t.Fatalf("message count after completion = %d, want exactly one reminder", count)
	}
}

func TestProgressNoticeDispatchFromPreviousSessionIsIgnored(t *testing.T) {
	a := enableProgressReminder(newReadyTestMainAgent(t))
	a.sessionEpoch = 2
	a.newTurn("")
	a.turn.progress.since = time.Now().Add(-progressNoticeInterval)
	a.turn.progress.rounds = progressNoticeRounds
	progressOverlay(t, a)
	before := a.ctxMgr.MessageCount()

	a.dispatch(Event{
		Type: eventProgressNoticeDispatch,
		Payload: progressNoticeDispatchEvent{
			turn: a.turn, sessionEpoch: 1,
		},
	})

	if a.ctxMgr.MessageCount() != before || !a.turn.progress.stagedForDispatch() {
		t.Fatal("stale session dispatch consumed or persisted the progress notice")
	}
}
