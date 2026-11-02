package agent

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/keakon/chord/internal/message"
)

func TestSubAgentAttachmentsAreNormalizedBeforeContextAppendAndPersistence(t *testing.T) {
	for _, mode := range []string{"user input", "context append"} {
		t.Run(mode, func(t *testing.T) {
			a := newTestMainAgent(t, t.TempDir())
			sub := newControllableTestSubAgent(t, a, "attachment-test")
			sub.llmMu.Lock()
			sub.llmClient = nil
			sub.llmMu.Unlock()
			payload := []byte("\x89PNG\r\n\x1a\nsubagent-attachment")
			part := message.ContentPart{
				Type:     message.ContentPartImage,
				MimeType: "image/png",
				FileName: "shot.png",
				Data:     payload,
			}
			switch mode {
			case "user input":
				sub.appendPendingUserMessage(pendingUserMessage{
					Content: "inspect this image",
					Parts:   []message.ContentPart{part},
				})
			case "context append":
				sub.appendContextOnly(message.Message{
					Role:    message.RoleUser,
					Content: "inspect this image",
					Parts:   []message.ContentPart{part},
				})
			}

			inMemory := sub.ctxMgr.Snapshot()
			if len(inMemory) != 1 || len(inMemory[0].Parts) != 1 {
				t.Fatalf("in-memory messages = %#v, want one message with one attachment", inMemory)
			}
			assertSubAgentAttachmentIsPathBacked(t, a, inMemory[0].Parts[0], payload)
			if got, want := sub.ctxMgr.BytesBreakdown().Image, len(payload); got != want {
				t.Fatalf("context image bytes = %d, want %d", got, want)
			}

			a.flushPersist()
			persisted, err := a.recoveryManager().LoadMessages(sub.instanceID)
			if err != nil {
				t.Fatalf("LoadMessages(%q): %v", sub.instanceID, err)
			}
			if len(persisted) != 1 || len(persisted[0].Parts) != 1 {
				t.Fatalf("persisted messages = %#v, want one message with one attachment", persisted)
			}
			assertSubAgentAttachmentIsPathBacked(t, a, persisted[0].Parts[0], payload)
		})
	}
}

func assertSubAgentAttachmentIsPathBacked(t *testing.T, a *MainAgent, part message.ContentPart, want []byte) {
	t.Helper()
	if len(part.Data) != 0 {
		t.Fatalf("attachment retained %d inline bytes", len(part.Data))
	}
	if part.ImagePath == "" {
		t.Fatal("attachment has no session-owned image path")
	}
	rel, err := filepath.Rel(filepath.Join(a.SessionDir(), "images"), part.ImagePath)
	if err != nil || rel == ".." || len(rel) >= 3 && rel[:3] == ".."+string(filepath.Separator) {
		t.Fatalf("attachment path %q is outside session images directory", part.ImagePath)
	}
	if part.DataBytes != int64(len(want)) {
		t.Fatalf("attachment DataBytes = %d, want %d", part.DataBytes, len(want))
	}
	stored, err := os.ReadFile(part.ImagePath)
	if err != nil {
		t.Fatalf("ReadFile(%q): %v", part.ImagePath, err)
	}
	if !bytes.Equal(stored, want) {
		t.Fatalf("stored attachment = %q, want %q", stored, want)
	}
}

func TestTargetedSubAgentAttachmentCommitKeepsReceipt(t *testing.T) {
	for _, mode := range []string{"direct", "reopened admission", "queued"} {
		t.Run(mode, func(t *testing.T) {
			a := newTestMainAgent(t, t.TempDir())
			sub := newControllableTestSubAgent(t, a, "attachment-task")
			sub.setState(SubAgentStateRunning, "")
			sub.llmMu.Lock()
			sub.llmClient = nil
			sub.llmMu.Unlock()
			parts := []message.ContentPart{
				{Type: message.ContentPartText, Text: "inspect"},
				{Type: message.ContentPartImage, MimeType: "image/png", Data: []byte("image")},
				{Type: message.ContentPartPDF, MimeType: "application/pdf", FileName: "sample.pdf", Data: []byte("pdf")},
			}
			// Main remains focused: routing must use the captured task, not focus.
			switch mode {
			case "direct":
				a.SendUserMessageWithPartsToTarget(ConversationTarget{TaskID: sub.taskID}, parts, "request-1")
			case "reopened admission":
				a.handleSubAgentSendMessageEvent(Event{Payload: &SubAgentSendMessagePayload{TaskID: sub.taskID, Parts: parts, RequestID: "request-1", Message: "inspect", Kind: "follow_up"}})
			case "queued":
				if !sub.QueuePendingUserDraft("draft-1", parts) {
					t.Fatal("queued draft rejected")
				}
			}
			pending := sub.takePendingUserMessagesForContinuation()
			if len(pending) != 1 {
				t.Fatalf("pending = %d, want 1", len(pending))
			}
			sub.appendPendingUserMessage(pending[0])
			msgs := sub.ctxMgr.Snapshot()
			if len(msgs) != 1 || len(msgs[0].Parts) != len(parts) {
				t.Fatalf("messages = %#v", msgs)
			}
			assertSubAgentAttachmentIsPathBacked(t, a, msgs[0].Parts[1], parts[1].Data)
			assertSubAgentAttachmentIsPathBacked(t, a, msgs[0].Parts[2], parts[2].Data)
			var committedParts []message.ContentPart
			timer := time.NewTimer(2 * time.Second)
			defer timer.Stop()
		waitForCommit:
			for {
				select {
				case raw := <-a.Events():
					switch evt := raw.(type) {
					case UserMessageCommittedEvent:
						if mode == "queued" || evt.AgentID != sub.instanceID || evt.RequestID != "request-1" || evt.MessageIndex != 0 {
							t.Fatalf("unexpected commit: %#v", evt)
						}
						committedParts = evt.Parts
						break waitForCommit
					case PendingDraftConsumedEvent:
						if mode != "queued" || evt.AgentID != sub.instanceID || evt.DraftID != "draft-1" {
							t.Fatalf("unexpected draft consumption: %#v", evt)
						}
						committedParts = evt.Parts
						break waitForCommit
					}
				case <-timer.C:
					t.Fatal("timed out waiting for attachment commit")
				}
			}
			if len(committedParts) != len(parts) {
				t.Fatalf("committed parts = %d, want %d", len(committedParts), len(parts))
			}
			for i := 1; i < len(parts); i++ {
				assertSubAgentAttachmentIsPathBacked(t, a, committedParts[i], parts[i].Data)
			}
			if mode != "queued" && msgs[0].InputRequestID != "request-1" {
				t.Fatalf("InputRequestID = %q, want request-1", msgs[0].InputRequestID)
			}
			if len(a.ctxMgr.Snapshot()) != 0 {
				t.Fatal("targeted input leaked to main")
			}
			a.flushPersist()
			persisted, err := a.recoveryManager().LoadMessages(sub.instanceID)
			if err != nil || len(persisted) != 1 || persisted[0].InputRequestID != msgs[0].InputRequestID {
				t.Fatalf("persisted receipt lost: messages=%#v err=%v", persisted, err)
			}
		})
	}
}

func TestSubAgentCommitPreservesInlineDataWhenNormalizationUnavailable(t *testing.T) {
	for _, mode := range []string{"no recovery", "write failure"} {
		t.Run(mode, func(t *testing.T) {
			a := newTestMainAgent(t, t.TempDir())
			sub := newControllableTestSubAgent(t, a, "attachment-task")
			sub.llmMu.Lock()
			sub.llmClient = nil
			sub.llmMu.Unlock()
			if mode == "no recovery" {
				sub.sessionEpoch = recoveryEpochNone
			} else {
				// An ordinary file where the images directory belongs forces IO failure.
				if err := os.WriteFile(filepath.Join(a.SessionDir(), "images"), []byte("blocked"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			parts := []message.ContentPart{
				{Type: message.ContentPartImage, MimeType: "image/png", Data: []byte("image")},
				{Type: message.ContentPartPDF, MimeType: "application/pdf", Data: []byte("pdf")},
			}
			sub.appendPendingUserMessage(pendingUserMessage{RequestID: "request-1", FromUser: true, Parts: parts})
			timer := time.NewTimer(2 * time.Second)
			defer timer.Stop()
			for {
				select {
				case raw := <-a.Events():
					evt, ok := raw.(UserMessageCommittedEvent)
					if !ok {
						continue
					}
					if evt.RequestID != "request-1" || len(evt.Parts) != len(parts) {
						t.Fatalf("commit = %#v", evt)
					}
					for i, part := range evt.Parts {
						if !bytes.Equal(part.Data, parts[i].Data) || part.ImagePath != "" {
							t.Fatalf("commit lost the only payload copy: %#v", part)
						}
					}
					return
				case <-timer.C:
					t.Fatal("timed out waiting for inline commit")
				}
			}
		})
	}
}

func TestSubAgentCommitReportsDroppedAttachments(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	sub := newControllableTestSubAgent(t, a, "attachment-task")
	// The test model only accepts text. A receipt must still retire the payload
	// even when filtering removes every binary part.
	sub.appendPendingUserMessage(pendingUserMessage{InputID: "input-1", RequestID: "request-1", FromUser: true, Parts: []message.ContentPart{
		{Type: message.ContentPartImage, MimeType: "image/png", Data: []byte("image")},
		{Type: message.ContentPartPDF, MimeType: "application/pdf", Data: []byte("pdf")},
	}})
	if entries, err := os.ReadDir(filepath.Join(a.SessionDir(), "images")); err != nil && !os.IsNotExist(err) || len(entries) != 0 {
		t.Fatalf("dropped attachments were written: entries=%v err=%v", entries, err)
	}
	if len(sub.stagedPauseInputs) != 1 || len(sub.stagedPauseInputs[0].Parts) != 0 {
		t.Fatalf("staged inputs kept dropped payloads: %#v", sub.stagedPauseInputs)
	}
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	for {
		select {
		case raw := <-a.Events():
			evt, ok := raw.(UserMessageCommittedEvent)
			if !ok {
				continue
			}
			if evt.RequestID != "request-1" || len(evt.Parts) != 0 {
				t.Fatalf("dropped attachment commit = %#v", evt)
			}
			return
		case <-timer.C:
			t.Fatal("timed out waiting for dropped attachment commit")
		}
	}
}
