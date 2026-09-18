package tui

import (
	"testing"
	"time"

	"github.com/keakon/chord/internal/message"
)

func pendingLocalShellBlock(id int, cmd string) *Block {
	return &Block{
		ID:                    id,
		Type:                  BlockUser,
		Content:               "!" + cmd,
		UserLocalShell:        true,
		UserLocalShellCmd:     cmd,
		UserLocalShellPending: true,
		StartedAt:             time.Now(),
		MsgIndex:              -1,
	}
}

// A running !command has no durable message yet, so a full rebuild from
// messages must carry the pending card over; otherwise the later result has no
// card to update and only reaches the model context.
func TestTranscriptRebuildPreservesPendingLocalShellCard(t *testing.T) {
	backend := &sessionControlAgent{messages: []message.Message{{Role: "user", Content: "hello"}}}
	m := NewModelWithSize(backend, 120, 24)
	m.viewport.AppendBlock(pendingLocalShellBlock(7, "sleep 30"))
	m.nextBlockID = 8

	m.rebuildViewportFromMessagesWithReason("session_restored")

	found := false
	for _, b := range m.viewport.visibleBlocks() {
		if b != nil && b.UserLocalShellCmd == "sleep 30" {
			found = true
			if !b.UserLocalShellPending {
				t.Fatal("preserved local shell card lost its pending state")
			}
			if b.ID != 7 {
				t.Fatalf("preserved local shell card ID = %d, want 7", b.ID)
			}
		}
	}
	if !found {
		t.Fatal("transcript rebuild dropped the pending local shell card")
	}

	m.handleShellBangResult(shellBangResultMsg{userLine: "!sleep 30", cmd: "sleep 30", output: "done", blockID: 7, transcriptEpoch: m.sessionTranscriptEpoch})

	if len(backend.contextMessages) != 1 {
		t.Fatalf("context messages = %d, want 1", len(backend.contextMessages))
	}
	settled := false
	for _, b := range m.viewport.visibleBlocks() {
		if b != nil && b.ID == 7 {
			settled = true
			if b.UserLocalShellPending {
				t.Fatal("local shell card still pending after its result arrived")
			}
			if b.UserLocalShellResult != "done" {
				t.Fatalf("local shell result = %q, want done", b.UserLocalShellResult)
			}
		}
	}
	if !settled {
		t.Fatal("local shell result found no card to settle")
	}
}

// A session switch replaces the transcript the command was launched in, so the
// pending card is not carried over and the late result must not be injected
// into the new session's context.
func TestShellBangResultDroppedAfterSessionSwitch(t *testing.T) {
	backend := &sessionControlAgent{messages: []message.Message{{Role: "user", Content: "hello"}}}
	m := NewModelWithSize(backend, 120, 24)
	m.viewport.AppendBlock(pendingLocalShellBlock(3, "sleep 30"))
	m.nextBlockID = 4
	launchEpoch := m.sessionTranscriptEpoch

	m.sessionTranscriptEpoch++
	m.rebuildViewportFromMessagesWithReason("session_restored")
	for _, b := range m.viewport.visibleBlocks() {
		if b != nil && b.ID == 3 {
			t.Fatal("pending local shell card survived a session switch")
		}
	}

	m.handleShellBangResult(shellBangResultMsg{userLine: "!sleep 30", cmd: "sleep 30", output: "done", blockID: 3, transcriptEpoch: launchEpoch})

	if len(backend.contextMessages) != 0 {
		t.Fatalf("context messages = %d, want 0", len(backend.contextMessages))
	}
	for _, b := range m.viewport.visibleBlocks() {
		if b != nil && b.ID == 3 {
			t.Fatal("stale local shell result recreated a card in the new session")
		}
	}
}
