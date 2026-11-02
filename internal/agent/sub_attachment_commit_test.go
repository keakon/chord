package agent

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

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
