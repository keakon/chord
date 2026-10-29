package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/recovery"
)

// restoredQueueStub reports a pending startup resume and serves the durable
// queue state, so the model follows the same init path as a real
// --continue / --resume startup.
type restoredQueueStub struct {
	*sessionControlAgent
	resumePending    bool
	snapshot         *agent.InputQueueSnapshot
	pending          []recovery.PendingInput
	status           recovery.SessionPauseStatus
	waitingMailboxes []agent.SubAgentMailboxMessage
}

func (s *restoredQueueStub) StartupResumeStatus() (bool, string) {
	return s.resumePending, s.sessionControlAgent.startupResumeID
}
func (s *restoredQueueStub) InputQueueSnapshot() *agent.InputQueueSnapshot { return s.snapshot }
func (s *restoredQueueStub) PendingSessionInputs() []recovery.PendingInput { return s.pending }
func (s *restoredQueueStub) CurrentSessionPauseStatus() recovery.SessionPauseStatus {
	return s.status
}
func (s *restoredQueueStub) WaitingSubAgentMailboxMessages() []agent.SubAgentMailboxMessage {
	return append([]agent.SubAgentMailboxMessage(nil), s.waitingMailboxes...)
}

func restoredQueueSnapshot(target agent.InputTarget, inputs []recovery.PendingInput) *agent.InputQueueSnapshot {
	return &agent.InputQueueSnapshot{
		Target:      target,
		ViewVersion: 1,
		State: &recovery.InputDeliveryState{
			SessionID: target.SessionID,
			Revision:  1,
			Inputs:    inputs,
		},
	}
}

// A restored session must show its durable queue. The agent publishes the
// snapshot while the restore rebuild is still pending, and that rebuild clears
// the drafts it already projected; re-projection must not be discarded just
// because the snapshot carries the same ViewVersion it had before.
func TestRestoredSessionShowsDurableQueuedInputs(t *testing.T) {
	for _, tc := range []struct {
		name   string
		policy recovery.InputDeliveryPolicy
	}{
		{name: "next request", policy: recovery.InputDeliveryNextRequest},
		{name: "after completion", policy: recovery.InputDeliveryAfterCompletion},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base := &sessionControlAgent{messages: []message.Message{{Role: "user", Content: "earlier request"}}}
			backend := &restoredQueueStub{
				sessionControlAgent: base,
				resumePending:       true,
				pending: []recovery.PendingInput{{
					InputID:        "input-1",
					OwnerTaskID:    "main",
					DraftID:        "draft-1",
					Revision:       1,
					Sequence:       1,
					Stage:          recovery.PendingInputStageQueued,
					DeliveryPolicy: tc.policy,
					Content:        "queued follow-up",
					WorkDir:        t.TempDir(),
				}},
			}
			backend.snapshot = restoredQueueSnapshot(base.InputDeliveryTarget(), backend.pending)

			// NewModelWithSize runs the same init path as cmd/chord: it sees the
			// pending resume, so it defers queue restore to the rebuild.
			m := NewModelWithSize(backend, 120, 30)
			if !m.startupRestorePending {
				t.Fatal("model did not observe the pending startup resume")
			}

			// The startup order the agent actually produces: the snapshot the
			// restore publishes, then the switch and restored events, then the
			// loadOnly commit's second publish. The first waitForAgentEvent
			// drains all of them into one batch.
			m.Update(agentEventBatchMsg{
				{event: agent.ToastEvent{Message: "session restored"}},
				{event: agent.InputQueueChangedEvent{Snapshot: backend.snapshot}},
				{event: agent.SessionSwitchStartedEvent{Kind: "resume", SessionID: "123"}},
				{event: agent.SessionRestoredEvent{}},
				{event: agent.InputQueueChangedEvent{Snapshot: backend.snapshot}},
			})
			if len(m.queuedDrafts) != 1 {
				t.Fatalf("queued drafts after the batch = %d, want 1", len(m.queuedDrafts))
			}

			m.Update(sessionRestoredRebuildMsg{reason: transcriptRestoreReasonStartup})

			visible := m.visibleQueuedDrafts()
			if len(visible) != 1 {
				t.Fatalf("visible queued drafts after restore = %d, want 1", len(visible))
			}
			draft := visible[0]
			if draft.Content != "queued follow-up" {
				t.Fatalf("queued draft content = %q", draft.Content)
			}
			if !draft.DeliveryManaged || !draft.Mirrored {
				t.Fatalf("queued draft = %+v, want the durable managed input", draft)
			}
			if draft.DeliveryPolicy != tc.policy {
				t.Fatalf("queued draft policy = %q, want %q", draft.DeliveryPolicy, tc.policy)
			}
		})
	}
}

// Both policies are queued at once and each keeps its own presentation: the
// next-request input is runnable on the next send, the follow-up waits for the
// gate. The restored durable input must be projected once, not twice.
func TestRestoredSessionProjectsEachQueuedInputOnce(t *testing.T) {
	base := &sessionControlAgent{messages: []message.Message{{Role: "user", Content: "earlier request"}}}
	inputs := []recovery.PendingInput{
		{InputID: "input-next", OwnerTaskID: "main", DraftID: "draft-next", Revision: 1, Sequence: 1, Stage: recovery.PendingInputStageQueued, DeliveryPolicy: recovery.InputDeliveryNextRequest, Content: "next request", WorkDir: t.TempDir()},
		{InputID: "input-after", OwnerTaskID: "main", DraftID: "draft-after", Revision: 1, Sequence: 2, Stage: recovery.PendingInputStageQueued, DeliveryPolicy: recovery.InputDeliveryAfterCompletion, Content: "after completion", WorkDir: t.TempDir()},
	}
	backend := &restoredQueueStub{
		sessionControlAgent: base,
		resumePending:       true,
		pending:             inputs,
	}
	backend.snapshot = restoredQueueSnapshot(base.InputDeliveryTarget(), inputs)
	m := NewModelWithSize(backend, 120, 30)
	m.Update(agentEventBatchMsg{
		{event: agent.SessionSwitchStartedEvent{Kind: "resume", SessionID: "123"}},
		{event: agent.SessionRestoredEvent{}},
		{event: agent.InputQueueChangedEvent{Snapshot: backend.snapshot}},
	})
	m.Update(sessionRestoredRebuildMsg{reason: transcriptRestoreReasonStartup})

	visible := m.visibleQueuedDrafts()
	if len(visible) != 2 {
		t.Fatalf("visible queued drafts = %d, want 2", len(visible))
	}
	byPolicy := make(map[recovery.InputDeliveryPolicy]queuedDraft, len(visible))
	for _, draft := range visible {
		if draft.InputID == "" || !draft.DeliveryManaged {
			t.Fatalf("queued draft = %+v, want the managed durable input", draft)
		}
		if _, dup := byPolicy[draft.DeliveryPolicy]; dup {
			t.Fatalf("policy %q projected twice: %+v", draft.DeliveryPolicy, visible)
		}
		byPolicy[draft.DeliveryPolicy] = draft
	}
	if got := byPolicy[recovery.InputDeliveryNextRequest].DeliveryStatus; got != "Next request" {
		t.Fatalf("next request status = %q", got)
	}
	if got := byPolicy[recovery.InputDeliveryAfterCompletion].DeliveryStatus; !strings.HasPrefix(got, "After completion") {
		t.Fatalf("after completion status = %q", got)
	}
}

// A queued image must survive the restore projection: the attachment stays
// session-owned after the durable write, so the restored draft still carries it.
func TestRestoredSessionShowsQueuedImageAttachment(t *testing.T) {
	imagesDir := filepath.Join(t.TempDir(), "images")
	if err := os.MkdirAll(imagesDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(images): %v", err)
	}
	imagePath := filepath.Join(imagesDir, "queued-image.png")
	if err := os.WriteFile(imagePath, []byte("image-payload"), 0o600); err != nil {
		t.Fatalf("WriteFile(image): %v", err)
	}
	base := &sessionControlAgent{messages: []message.Message{{Role: "user", Content: "earlier request"}}}
	inputs := []recovery.PendingInput{{
		InputID:        "input-image",
		OwnerTaskID:    "main",
		DraftID:        "draft-image",
		Revision:       1,
		Sequence:       1,
		Stage:          recovery.PendingInputStageQueued,
		DeliveryPolicy: recovery.InputDeliveryNextRequest,
		Content:        "queued image",
		WorkDir:        t.TempDir(),
		Parts: []message.ContentPart{{
			Type:      message.ContentPartImage,
			MimeType:  "image/png",
			FileName:  "queued-image.png",
			ImagePath: imagePath,
		}},
	}}
	backend := &restoredQueueStub{
		sessionControlAgent: base,
		resumePending:       true,
		pending:             inputs,
	}
	backend.snapshot = restoredQueueSnapshot(base.InputDeliveryTarget(), inputs)
	m := NewModelWithSize(backend, 120, 30)
	m.Update(agentEventBatchMsg{
		{event: agent.SessionSwitchStartedEvent{Kind: "resume", SessionID: "123"}},
		{event: agent.SessionRestoredEvent{}},
		{event: agent.InputQueueChangedEvent{Snapshot: backend.snapshot}},
	})
	m.Update(sessionRestoredRebuildMsg{reason: transcriptRestoreReasonStartup})

	visible := m.visibleQueuedDrafts()
	if len(visible) != 1 {
		t.Fatalf("visible queued drafts = %d, want 1", len(visible))
	}
	_, images := queuedDraftTextAndImageCount(visible[0])
	if images != 1 {
		t.Fatalf("restored queued image count = %d, want 1", images)
	}
}

// Stopping a background job leaves its result waiting for the next request.
// Restore replays it through MailboxQueuedEvent before the switch clears the
// waiting rows, so the restore projection has to rebuild them.
func TestRestoredSessionShowsWaitingBackgroundResult(t *testing.T) {
	base := &sessionControlAgent{messages: []message.Message{{Role: "user", Content: "earlier request"}}}
	msg := agent.SubAgentMailboxMessage{
		MessageID:    "background-job-1",
		Kind:         agent.SubAgentMailboxKindBackgroundResult,
		AgentID:      "job-1",
		OwnerAgentID: "main",
		Summary:      "Run production build",
	}
	backend := &restoredQueueStub{
		sessionControlAgent: base,
		resumePending:       true,
		waitingMailboxes:    []agent.SubAgentMailboxMessage{msg},
	}
	backend.snapshot = restoredQueueSnapshot(base.InputDeliveryTarget(), nil)
	m := NewModelWithSize(backend, 120, 30)

	// The startup order the agent produces: the restore replay lands first,
	// then the switch clears the queue, then the rebuild runs.
	m.Update(agentEventBatchMsg{
		{event: agent.MailboxQueuedEvent{Message: msg}},
		{event: agent.SessionSwitchStartedEvent{Kind: "resume", SessionID: "123"}},
		{event: agent.SessionRestoredEvent{}},
	})
	m.Update(sessionRestoredRebuildMsg{reason: transcriptRestoreReasonStartup})

	visible := m.visibleQueuedMailboxes()
	if len(visible) != 1 {
		t.Fatalf("visible queued mailboxes = %d, want the stopped job's result", len(visible))
	}
	if visible[0].MessageID != "background-job-1" || visible[0].Summary != "Run production build" {
		t.Fatalf("visible queued mailbox = %+v", visible[0])
	}
}

// The rebuild's drop flag is consumed by the re-projection that follows it. A
// backend that publishes no queue leaves nothing to project, so the flag must
// not stay set and keep the ViewVersion guard disabled for the rest of the run.
func TestRestoreClearsProjectionStaleWithoutQueue(t *testing.T) {
	base := &sessionControlAgent{messages: []message.Message{{Role: "user", Content: "earlier request"}}}
	backend := &restoredQueueStub{sessionControlAgent: base, resumePending: true}
	m := NewModelWithSize(backend, 120, 30)
	m.Update(agentEventBatchMsg{
		{event: agent.SessionSwitchStartedEvent{Kind: "resume", SessionID: "123"}},
		{event: agent.SessionRestoredEvent{}},
	})
	m.Update(sessionRestoredRebuildMsg{reason: transcriptRestoreReasonStartup})

	if m.inputQueue.projectionStale {
		t.Fatal("projectionStale outlived the restore re-projection point")
	}
}
