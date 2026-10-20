package agent

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/recovery"
)

// A queued input carrying an image must survive a restart. The attachment is
// moved into session-owned storage by the durable write, so restore validates
// it lazily and the TUI can still render the queued image.
func TestRestoredQueuedInputKeepsSessionOwnedImage(t *testing.T) {
	projectRoot := t.TempDir()
	sessionDir := testProjectSessionDir(t, projectRoot, "queued-image-restore")
	persistRestorableSession(t, sessionDir)

	imagesDir := filepath.Join(sessionDir, "images")
	if err := os.MkdirAll(imagesDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(images): %v", err)
	}
	payload := []byte("queued-image-payload")
	sourcePath := filepath.Join(imagesDir, "queued-image.png")
	if err := os.WriteFile(sourcePath, payload, 0o600); err != nil {
		t.Fatalf("WriteFile(image): %v", err)
	}

	a := newTestMainAgentForRestore(t, projectRoot, sessionDir)
	if err := a.RestoreSessionAtStartup(); err != nil {
		t.Fatalf("RestoreSessionAtStartup: %v", err)
	}
	a.newTurn(a.newDeliveryWork("user"))
	a.recordCommittedUserMessage(message.Message{Role: message.RoleUser, Content: "current request"})
	queued := submitDeliveryForTest(t, a, "queued image", recovery.InputDeliveryNextRequest)
	attach := &deliveryOperation{update: &UpdateInput{
		OperationID:      "attach-image",
		Target:           a.InputDeliveryTarget(),
		InputID:          queued,
		ExpectedRevision: 1,
		Content:          new("queued image"),
		Parts: []message.ContentPart{{
			Type:      message.ContentPartImage,
			MimeType:  "image/png",
			FileName:  "queued-image.png",
			ImagePath: sourcePath,
			DataBytes: int64(len(payload)),
		}},
	}}
	if result := runDeliveryOperation(t, a, attach); result.Result != InputQueueCommitted {
		t.Fatalf("attach image: %+v", result)
	}
	finishDeliveryWrites(t, a)

	// A hard restart: the second agent only reads what the first one persisted.
	b := newTestMainAgentForRestore(t, projectRoot, sessionDir)
	if err := b.RestoreSessionAtStartup(); err != nil {
		t.Fatalf("RestoreSessionAtStartup (restart): %v", err)
	}

	// The durable write runs through the same storage completion event the live
	// path uses, so the restored snapshot is produced without starting Run.
	b.initializeInputDelivery()
	finishDeliveryWrites(t, b)

	final := b.InputQueueSnapshot()
	if final == nil || final.State == nil || len(final.State.Inputs) != 1 {
		t.Fatalf("restored snapshot = %+v, want the queued image input", final)
	}
	input := final.State.Inputs[0]
	if input.InputID != queued {
		t.Fatalf("restored input id = %q, want %q", input.InputID, queued)
	}
	imageParts := 0
	for _, part := range input.Parts {
		if part.Type != message.ContentPartImage {
			continue
		}
		imageParts++
		if part.ImagePath == "" {
			t.Fatalf("restored image part lost its session-owned path: %+v", part)
		}
		if part.DataBytes != int64(len(payload)) {
			t.Fatalf("restored image part size = %d, want %d", part.DataBytes, len(payload))
		}
		if _, err := os.Stat(part.ImagePath); err != nil {
			t.Fatalf("restored image attachment missing: %v", err)
		}
	}
	if imageParts != 1 {
		t.Fatalf("restored image parts = %d, want 1", imageParts)
	}
}
