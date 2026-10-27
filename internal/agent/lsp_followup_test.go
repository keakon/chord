package agent

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/recovery"
)

// fakeLSPFollowupSource serves scripted late diagnostics.
type fakeLSPFollowupSource struct {
	mu    sync.Mutex
	items []LSPFollowupItem
	asked []string
}

func (f *fakeLSPFollowupSource) Pending(paths []string, _ string) []LSPFollowupItem {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.asked = append([]string(nil), paths...)
	return append([]LSPFollowupItem(nil), f.items...)
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

func TestLSPFollowupDispatchPersistsTheDeliveredTextAndAcks(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	a.ctxMgr.Append(message.Message{Role: message.RoleUser, Content: "fix the bug"})
	appendWriteResult(a, "call-1", "/repo/main.go")

	var acked atomic.Int32
	reviews := []message.LSPReview{{Path: "/repo/main.go", ServerID: "gopls", Warnings: 1}}
	source := &fakeLSPFollowupSource{}
	source.setItems(LSPFollowupItem{DisplayPath: "main.go", Lines: "[W] 3:1 printf arg has wrong type", Reviews: reviews, Ack: func() { acked.Add(1) }})
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
	if acked.Load() != 0 {
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
	if acked.Load() != 0 {
		t.Fatal("late diagnostics were consumed before the follow-up became durable")
	}

	a.flushPersist()
	evt := waitForLSPFollowupEvent(t, a)
	if evt.MessageIndex != before || strings.Contains(evt.Message, "<system-reminder>") || !strings.Contains(evt.Message, lspFollowupLead) {
		t.Fatalf("event = %+v, want index %d and the bare follow-up text", evt, before)
	}
	if acked.Load() != 1 {
		t.Fatalf("acked = %d, want the delivered item consumed once after persistence", acked.Load())
	}

	a.emitStagedLSPFollowup()
	if got := a.ctxMgr.MessageCount(); got != before+1 || acked.Load() != 1 {
		t.Fatalf("a second dispatch confirmation wrote again: count=%d acked=%d", got, acked.Load())
	}
}

func TestLSPFollowupPersistenceFailureKeepsTheItemPending(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("not a directory"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Writes through this manager fail: its session directory is below a
	// regular file, so the follow-up never becomes durable.
	a.installRecoveryManager(recovery.NewRecoveryManager(filepath.Join(blocker, "session")))
	a.ctxMgr.Append(message.Message{Role: message.RoleUser, Content: "fix the bug"})
	appendWriteResult(a, "call-1", "/repo/main.go")

	var acked atomic.Int32
	source := &fakeLSPFollowupSource{}
	source.setItems(LSPFollowupItem{DisplayPath: "main.go", Lines: "[W] 3:1 unused", Ack: func() { acked.Add(1) }})
	a.SetLSPFollowupSource(source)
	if _, ok := findLSPFollowupOverlay(a.buildTurnOverlayMessages()); !ok {
		t.Fatal("expected the late diagnostics on the request tail")
	}

	before := a.ctxMgr.MessageCount()
	a.emitStagedLSPFollowup()
	a.flushPersist()
	if got := a.ctxMgr.MessageCount(); got != before+1 {
		t.Fatalf("message count = %d, want the in-memory row appended once", got)
	}
	if acked.Load() != 0 {
		t.Fatal("a follow-up whose persistence failed was consumed anyway")
	}
}

func TestLSPFollowupUndispatchedRequestIsNeitherPersistedNorAcked(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	a.ctxMgr.Append(message.Message{Role: message.RoleUser, Content: "fix the bug"})
	appendWriteResult(a, "call-1", "/repo/main.go")
	var acked atomic.Int32
	source := &fakeLSPFollowupSource{}
	source.setItems(LSPFollowupItem{DisplayPath: "main.go", Lines: "[W] 3:1 unused", Ack: func() { acked.Add(1) }})
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
	if a.ctxMgr.MessageCount() != before || acked.Load() != 0 {
		t.Fatalf("undispatched follow-up was persisted or acked: count=%d acked=%d", a.ctxMgr.MessageCount(), acked.Load())
	}
}

func TestLSPFollowupClaimsRecentWritesAcrossTurns(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	a.ctxMgr.Append(message.Message{Role: message.RoleUser, Content: "first request"})
	appendWriteResult(a, "call-1", "/repo/old.go")
	a.ctxMgr.Append(message.Message{Role: message.RoleUser, Content: "second request"})
	source := &fakeLSPFollowupSource{}
	a.SetLSPFollowupSource(source)
	a.buildTurnOverlayMessages()
	if !reflect.DeepEqual(source.asked, []string{"/repo/old.go"}) {
		t.Fatalf("asked paths = %v, want the previous turn's write while its watch is live", source.asked)
	}

	appendWriteResult(a, "call-2", "/repo/new.go")
	appendWriteResult(a, "call-3", "/repo/new.go")
	a.ctxMgr.Append(message.Message{Role: message.RoleUser, Kind: message.KindLSPFollowup, Content: "earlier follow-up"})
	got := recentWrittenPaths(a.ctxMgr.ScanBackward)
	if !reflect.DeepEqual(got, []string{"/repo/new.go", "/repo/old.go"}) {
		t.Fatalf("recent paths = %v, want newest first and deduplicated", got)
	}

	a.ctxMgr.Append(message.Message{Role: message.RoleUser, IsCompactionSummary: true, Content: "summary"})
	appendWriteResult(a, "call-4", "/repo/after-compaction.go")
	got = recentWrittenPaths(a.ctxMgr.ScanBackward)
	if !reflect.DeepEqual(got, []string{"/repo/after-compaction.go"}) {
		t.Fatalf("recent paths after compaction = %v, want only writes after the summary", got)
	}
}

func TestRecentWrittenPathsStopsAtBackscanLimit(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	appendWriteResult(a, "call-old", "/repo/old.go")
	for range lspFollowupBackscanMessages {
		a.ctxMgr.Append(message.Message{Role: message.RoleAssistant, Content: "filler"})
	}
	appendWriteResult(a, "call-new", "/repo/new.go")
	got := recentWrittenPaths(a.ctxMgr.ScanBackward)
	if !reflect.DeepEqual(got, []string{"/repo/new.go"}) {
		t.Fatalf("recent paths = %v, want the write inside the backscan window only", got)
	}
}
