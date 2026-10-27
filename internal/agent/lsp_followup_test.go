package agent

import (
	"context"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
)

// fakeLSPFollowupSource serves scripted late diagnostics. Await blocks until
// release is closed (or ctx ends) when awaitable is set.
type fakeLSPFollowupSource struct {
	mu        sync.Mutex
	items     []LSPFollowupItem
	asked     []string
	awaitable bool
	release   chan struct{}
	awaited   chan context.Context
}

func (f *fakeLSPFollowupSource) Pending(paths []string, _ string) []LSPFollowupItem {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.asked = append([]string(nil), paths...)
	return append([]LSPFollowupItem(nil), f.items...)
}

func (f *fakeLSPFollowupSource) Awaitable([]string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.awaitable
}

func (f *fakeLSPFollowupSource) Await(ctx context.Context, _ []string) {
	if f.awaited != nil {
		f.awaited <- ctx
	}
	select {
	case <-f.release:
	case <-ctx.Done():
	}
}

func (f *fakeLSPFollowupSource) setItems(items ...LSPFollowupItem) {
	f.mu.Lock()
	f.items = items
	f.mu.Unlock()
}

func appendWriteResult(a *MainAgent, callID, path string) {
	a.ctxMgr.Append(message.Message{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{{ID: callID, Name: "edit"}}})
	a.ctxMgr.Append(message.Message{
		Role:       message.RoleTool,
		ToolCallID: callID,
		ToolStatus: message.ToolStatusSuccess,
		Content:    "Replaced 1 occurrence",
		FileState:  &message.ToolFileState{Writes: []message.TrackedFileState{{Path: path, Exists: true}}},
	})
}

func findLSPFollowupOverlay(overlays []message.Message) (message.Message, bool) {
	for _, overlay := range overlays {
		if strings.Contains(overlay.Content, lspFollowupLead) {
			return overlay, true
		}
	}
	return message.Message{}, false
}

func waitForLSPFollowupEvent(t *testing.T, a *MainAgent) LSPFollowupEvent {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case evt := <-a.outputCh:
			if followup, ok := evt.(LSPFollowupEvent); ok {
				return followup
			}
		case <-deadline:
			t.Fatal("timed out waiting for LSPFollowupEvent")
		}
	}
}

func waitForInternalEvent(t *testing.T, a *MainAgent, eventType string) Event {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case evt := <-a.eventCh:
			if evt.Type == eventType {
				return evt
			}
		case <-deadline:
			t.Fatalf("timed out waiting for internal event %s", eventType)
		}
	}
}

func TestLSPFollowupDispatchPersistsTheDeliveredTextAndAcks(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	a.ctxMgr.Append(message.Message{Role: message.RoleUser, Content: "fix the bug"})
	appendWriteResult(a, "call-1", "/repo/main.go")

	acked := 0
	reviews := []message.LSPReview{{Path: "/repo/main.go", ServerID: "gopls", Warnings: 1}}
	source := &fakeLSPFollowupSource{}
	source.setItems(LSPFollowupItem{DisplayPath: "main.go", Lines: "[W] 3:1 printf arg has wrong type", Reviews: reviews, Ack: func() { acked++ }})
	a.SetLSPFollowupSource(source)

	overlay, ok := findLSPFollowupOverlay(a.buildTurnOverlayMessages())
	if !ok {
		t.Fatal("expected the late diagnostics on the request tail")
	}
	if !reflect.DeepEqual(source.asked, []string{"/repo/main.go"}) {
		t.Fatalf("asked paths = %v, want the file this turn wrote", source.asked)
	}
	if overlay.Kind != message.KindTurnOverlay || !strings.Contains(overlay.Content, "main.go:\n[W] 3:1 printf arg has wrong type") {
		t.Fatalf("overlay = %+v, want a turn overlay naming the file and its diagnostics", overlay)
	}
	if acked != 0 {
		t.Fatal("late diagnostics were consumed before the request dispatched")
	}

	before := a.ctxMgr.MessageCount()
	a.emitStagedLSPFollowup()
	messages := a.ctxMgr.Snapshot()
	if len(messages) != before+1 {
		t.Fatalf("message count = %d, want %d", len(messages), before+1)
	}
	row := messages[len(messages)-1]
	if row.Role != message.RoleUser || row.Kind != message.KindLSPFollowup || row.Content != overlay.Content {
		t.Fatalf("durable row = %+v, want a KindLSPFollowup row carrying the delivered content", row)
	}
	if !reflect.DeepEqual(row.LSPReviews, reviews) {
		t.Fatalf("durable row reviews = %+v, want %+v", row.LSPReviews, reviews)
	}
	if message.IsUserAuthored(row) {
		t.Fatal("a late diagnostics follow-up must not count as user-authored")
	}
	if acked != 1 {
		t.Fatalf("acked = %d, want the delivered item consumed once", acked)
	}

	a.flushPersist()
	evt := waitForLSPFollowupEvent(t, a)
	if evt.MessageIndex != before || strings.Contains(evt.Message, "<system-reminder>") || !strings.Contains(evt.Message, lspFollowupLead) {
		t.Fatalf("event = %+v, want index %d and the bare follow-up text", evt, before)
	}

	a.emitStagedLSPFollowup()
	if got := a.ctxMgr.MessageCount(); got != before+1 || acked != 1 {
		t.Fatalf("a second dispatch confirmation wrote again: count=%d acked=%d", got, acked)
	}
}

func TestLSPFollowupUndispatchedRequestIsNeitherPersistedNorAcked(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	a.ctxMgr.Append(message.Message{Role: message.RoleUser, Content: "fix the bug"})
	appendWriteResult(a, "call-1", "/repo/main.go")
	acked := 0
	source := &fakeLSPFollowupSource{}
	source.setItems(LSPFollowupItem{DisplayPath: "main.go", Lines: "[W] 3:1 unused", Ack: func() { acked++ }})
	a.SetLSPFollowupSource(source)

	if _, ok := findLSPFollowupOverlay(a.buildTurnOverlayMessages()); !ok {
		t.Fatal("expected the late diagnostics on the first request")
	}
	// The first request never dispatched; the next assembly restages.
	source.setItems()
	if _, ok := findLSPFollowupOverlay(a.buildTurnOverlayMessages()); ok {
		t.Fatal("a stale staged follow-up leaked into the next request")
	}
	before := a.ctxMgr.MessageCount()
	a.emitStagedLSPFollowup()
	if a.ctxMgr.MessageCount() != before || acked != 0 {
		t.Fatalf("undispatched follow-up was persisted or acked: count=%d acked=%d", a.ctxMgr.MessageCount(), acked)
	}
}

func TestLSPFollowupClaimsOnlyTheCurrentTurnsWrites(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	a.ctxMgr.Append(message.Message{Role: message.RoleUser, Content: "first request"})
	appendWriteResult(a, "call-1", "/repo/old.go")
	a.ctxMgr.Append(message.Message{Role: message.RoleUser, Content: "second request"})
	source := &fakeLSPFollowupSource{}
	a.SetLSPFollowupSource(source)
	a.buildTurnOverlayMessages()
	if source.asked != nil {
		t.Fatal("a turn without writes asked for late diagnostics")
	}

	appendWriteResult(a, "call-2", "/repo/new.go")
	appendWriteResult(a, "call-3", "/repo/new.go")
	a.ctxMgr.Append(message.Message{Role: message.RoleUser, Kind: message.KindLSPFollowup, Content: "earlier follow-up"})
	got := currentTurnWrittenPaths(a.ctxMgr.ScanBackward)
	if !reflect.DeepEqual(got, []string{"/repo/new.go"}) {
		t.Fatalf("current turn paths = %v, want only this turn's written file once", got)
	}
}

func newLateTurnEndAgent(t *testing.T, provider *blockingStreamProvider) (*MainAgent, *fakeLSPFollowupSource) {
	t.Helper()
	a := newReadyTestMainAgent(t)
	providerCfg := llm.NewProviderConfig("provider", config.ProviderConfig{
		Type:   config.ProviderTypeChatCompletions,
		Models: map[string]config.ModelConfig{"test-model": {Limit: config.ModelLimit{Context: 100000, Input: 100000, Output: 4096}}},
	}, []string{"test-key"})
	a.llmClient = llm.NewClient(providerCfg, provider, "test-model", 4096, "sys")
	a.ctxMgr.Append(message.Message{Role: message.RoleUser, Content: "fix the bug"})
	appendWriteResult(a, "call-1", "/repo/main.go")
	a.newTurn("")
	a.started.Store(true)
	source := &fakeLSPFollowupSource{awaitable: true, release: make(chan struct{}), awaited: make(chan context.Context, 1)}
	a.SetLSPFollowupSource(source)
	return a, source
}

func TestTurnEndWaitsForLateDiagnosticsAndContinuesWithThem(t *testing.T) {
	t.Parallel()
	provider := &blockingStreamProvider{calls: []scriptedStreamCall{{resp: &message.Response{Content: "fixed it", StopReason: "stop"}}}}
	a, source := newLateTurnEndAgent(t, provider)
	turn := a.turn

	if !a.deferTurnEndForLateDiagnostics(lateTurnEnd{turn: turn, payload: &LLMResponsePayload{StopReason: "stop"}}) {
		t.Fatal("a turn whose edit still owes its analysis pass ended without waiting")
	}
	select {
	case ctx := <-source.awaited:
		if ctx != turn.Ctx {
			t.Fatal("the wait does not follow the turn's cancellation")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the turn end never started waiting")
	}
	source.setItems(LSPFollowupItem{DisplayPath: "main.go", Lines: "[W] 3:1 printf arg has wrong type"})
	close(source.release)

	a.handleLateDiagnosticsSettled(waitForInternalEvent(t, a, eventLateDiagnosticsSettled))
	waitForBlockingStreamProviderCalls(t, provider, 1)
	seen, _ := provider.snapshot()
	last := seen[0][len(seen[0])-1]
	if !strings.Contains(last.Content, lspFollowupLead) || !strings.Contains(last.Content, "printf arg has wrong type") {
		t.Fatalf("continued request tail = %q, want the late diagnostics", last.Content)
	}
}

func TestTurnEndWithoutLateFindingsFinishesNormally(t *testing.T) {
	t.Parallel()
	provider := &blockingStreamProvider{}
	a, source := newLateTurnEndAgent(t, provider)
	turn := a.turn
	end := lateTurnEnd{turn: turn, assistant: message.Message{Role: message.RoleAssistant, Content: "done"}, payload: &LLMResponsePayload{StopReason: "stop", Content: "done"}}
	if !a.deferTurnEndForLateDiagnostics(end) {
		t.Fatal("expected the turn end to wait")
	}
	close(source.release)
	a.handleLateDiagnosticsSettled(waitForInternalEvent(t, a, eventLateDiagnosticsSettled))
	if a.turn != nil {
		t.Fatal("a turn without late findings did not end")
	}
	if seen, _ := provider.snapshot(); len(seen) != 0 {
		t.Fatalf("a turn without late findings sent %d more requests", len(seen))
	}
}

func TestTurnEndSkipsTheWaitWhenNothingIsOwed(t *testing.T) {
	t.Parallel()
	a, source := newLateTurnEndAgent(t, &blockingStreamProvider{})
	source.mu.Lock()
	source.awaitable = false
	source.mu.Unlock()
	if a.deferTurnEndForLateDiagnostics(lateTurnEnd{turn: a.turn, payload: &LLMResponsePayload{StopReason: "stop"}}) {
		t.Fatal("a turn end waited although no analysis pass is owed and nothing is pending")
	}
	if a.deferTurnEndForLateDiagnostics(lateTurnEnd{turn: a.turn, payload: &LLMResponsePayload{StopReason: "length"}, isTruncated: true}) {
		t.Fatal("a truncated reply waited for late diagnostics")
	}
}

func TestTurnEndWaitIsDroppedWhenTheTurnIsCancelled(t *testing.T) {
	t.Parallel()
	provider := &blockingStreamProvider{}
	a, source := newLateTurnEndAgent(t, provider)
	turn := a.turn
	source.setItems(LSPFollowupItem{DisplayPath: "main.go", Lines: "[W] 3:1 printf arg has wrong type"})
	if !a.deferTurnEndForLateDiagnostics(lateTurnEnd{turn: turn, payload: &LLMResponsePayload{StopReason: "stop"}}) {
		t.Fatal("expected the turn end to wait")
	}
	turn.Cancel()

	a.handleLateDiagnosticsSettled(waitForInternalEvent(t, a, eventLateDiagnosticsSettled))
	if seen, _ := provider.snapshot(); len(seen) != 0 {
		t.Fatalf("a cancelled turn was continued with %d requests", len(seen))
	}
	if a.turn != turn {
		t.Fatal("the stale resume changed the turn the cancellation owns")
	}
}
