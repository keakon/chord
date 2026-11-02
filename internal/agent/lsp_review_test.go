package agent

import (
	"reflect"
	"sync/atomic"
	"testing"

	"github.com/keakon/chord/internal/message"
)

func TestCleanLateReviewPersistsWithoutRequestTextOrCard(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	appendWriteResult(a, "write-1", "/repo/main.go")
	var acked atomic.Int32
	reviews := []message.LSPReview{{Path: "/repo/main.go", ServerID: "gopls"}}
	source := &fakeLSPFollowupSource{}
	source.setItems(LSPFollowupItem{DisplayPath: "main.go", Reviews: reviews, Ack: func() { acked.Add(1) }})
	a.SetLSPFollowupSource(source)
	for _, overlay := range a.directoryCandidateInputs(a.ctxMgr.Snapshot()).overlays {
		if overlay.Content == message.SystemReminderOverlayText("") {
			t.Fatal("clean review added empty reminder to budget estimation")
		}
	}
	if _, ok := a.buildLSPFollowupOverlay(); ok {
		t.Fatal("clean review produced request text")
	}
	if acked.Load() != 0 {
		t.Fatal("staging consumed clean review")
	}
	a.emitStagedLSPFollowup()
	a.flushPersist()
	rows := a.ctxMgr.Snapshot()
	row := rows[len(rows)-1]
	if row.Kind != message.KindLSPReview || row.Content != "" || !reflect.DeepEqual(row.LSPReviews, reviews) || acked.Load() != 1 {
		t.Fatalf("clean durable row = %+v, acked = %d", row, acked.Load())
	}
	if visible := filterQuestionStateMessages(rows); len(visible) != len(rows)-1 {
		t.Fatalf("local review was not filtered from model input: %+v", visible)
	}
	for {
		select {
		case event := <-a.outputCh:
			if _, ok := event.(LSPFollowupEvent); ok {
				t.Fatalf("clean review produced a notification card: %+v", event)
			}
		default:
			return
		}
	}
}

func TestSubAgentCleanLateReviewPersistsWithoutCard(t *testing.T) {
	t.Parallel()
	parent, sub := newMixedBatchTestSubAgent(t)
	sub.ctxMgr.Append(message.Message{Role: message.RoleTool, FileState: &message.ToolFileState{Writes: []message.TrackedFileState{{Path: "/repo/main.go", Exists: true}}}})
	var acked atomic.Int32
	source := &fakeLSPFollowupSource{}
	source.setItems(LSPFollowupItem{Reviews: []message.LSPReview{{Path: "/repo/main.go", ServerID: "gopls"}}, Ack: func() { acked.Add(1) }})
	sub.SetLSPFollowupSource(source)
	for _, overlay := range sub.directoryCandidateInputs(sub.ctxMgr.Snapshot()).overlays {
		if overlay.Content == message.SystemReminderOverlayText("") {
			t.Fatal("clean subagent review added empty reminder to budget estimation")
		}
	}
	if _, ok := sub.buildLSPFollowupOverlay(); ok {
		t.Fatal("clean review produced a subagent overlay")
	}
	sub.emitStagedLSPFollowup()
	parent.flushPersist()
	rows := sub.ctxMgr.Snapshot()
	if row := rows[len(rows)-1]; row.Kind != message.KindLSPReview || len(row.LSPReviews) != 1 || acked.Load() != 1 {
		t.Fatalf("clean subagent review = %+v, acked = %d", row, acked.Load())
	}
	if sub.deferredLSPFollowup {
		t.Fatal("clean review queued a deferred card")
	}
}
