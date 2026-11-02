package recovery

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/keakon/chord/internal/message"
)

// newAttachmentTestManager creates a locked session directory plus a manager,
// because attachment normalization writes under a session this process owns.
func newAttachmentTestManager(t *testing.T) (*RecoveryManager, string) {
	t.Helper()
	dir := t.TempDir()
	lock, err := AcquireSessionLock(dir)
	if err != nil {
		t.Fatalf("AcquireSessionLock: %v", err)
	}
	t.Cleanup(func() { _ = lock.Release() })
	return NewRecoveryManager(dir), dir
}

func TestNormalizeAttachmentPartsMovesInlineDataToSessionFile(t *testing.T) {
	mgr, dir := newAttachmentTestManager(t)
	payload := []byte("inline-png-bytes")
	parts := []message.ContentPart{{
		Type:     "image",
		MimeType: "image/png",
		Data:     payload,
		FileName: "shot.png",
	}}

	out, err := mgr.NormalizeAttachmentParts(parts)
	if err != nil {
		t.Fatalf("NormalizeAttachmentParts: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("parts = %d, want 1", len(out))
	}
	if len(out[0].Data) != 0 {
		t.Errorf("Data kept %d bytes, want cleared", len(out[0].Data))
	}
	if out[0].DataBytes != int64(len(payload)) {
		t.Errorf("DataBytes = %d, want %d", out[0].DataBytes, len(payload))
	}
	if !pathWithin(filepath.Join(dir, "images"), out[0].ImagePath) {
		t.Fatalf("ImagePath %q is not session-owned", out[0].ImagePath)
	}
	if filepath.Ext(out[0].ImagePath) != ".png" {
		t.Errorf("extension = %q, want .png", filepath.Ext(out[0].ImagePath))
	}
	got, err := os.ReadFile(out[0].ImagePath)
	if err != nil {
		t.Fatalf("read stored attachment: %v", err)
	}
	if string(got) != string(payload) {
		t.Errorf("stored bytes = %q, want %q", got, payload)
	}
}

func TestNormalizeAttachmentPartsClearsInlineDataForSessionPath(t *testing.T) {
	mgr, dir := newAttachmentTestManager(t)
	images := filepath.Join(dir, "images")
	if err := os.MkdirAll(images, 0o700); err != nil {
		t.Fatalf("mkdir images: %v", err)
	}
	path := filepath.Join(images, "existing.png")
	payload := []byte("on-disk")
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		t.Fatalf("write attachment: %v", err)
	}

	out, err := mgr.NormalizeAttachmentParts([]message.ContentPart{{
		Type:      message.ContentPartImage,
		MimeType:  "image/png",
		ImagePath: path,
		Data:      []byte("inline-copy"),
	}})
	if err != nil {
		t.Fatalf("NormalizeAttachmentParts: %v", err)
	}
	if len(out[0].Data) != 0 {
		t.Errorf("Data kept %d inline bytes, want cleared", len(out[0].Data))
	}
	if out[0].DataBytes != int64(len(payload)) {
		t.Errorf("DataBytes = %d, want %d", out[0].DataBytes, len(payload))
	}
	if out[0].ImagePath != path {
		t.Errorf("ImagePath = %q, want %q", out[0].ImagePath, path)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read stored attachment: %v", err)
	}
	if string(got) != string(payload) {
		t.Errorf("stored bytes = %q, want %q", got, payload)
	}
}

func TestNormalizeAttachmentPartsRehomesExternalPath(t *testing.T) {
	mgr, dir := newAttachmentTestManager(t)
	external := filepath.Join(t.TempDir(), "dropped.png")
	payload := []byte("external-bytes")
	if err := os.WriteFile(external, payload, 0o600); err != nil {
		t.Fatalf("write external attachment: %v", err)
	}
	parts := []message.ContentPart{{
		Type:      "image",
		MimeType:  "image/png",
		ImagePath: external,
	}}

	out, err := mgr.NormalizeAttachmentParts(parts)
	if err != nil {
		t.Fatalf("NormalizeAttachmentParts: %v", err)
	}
	if out[0].ImagePath == external {
		t.Fatal("external path was kept as durable; it must be re-homed")
	}
	if !pathWithin(filepath.Join(dir, "images"), out[0].ImagePath) {
		t.Fatalf("ImagePath %q is not session-owned", out[0].ImagePath)
	}
	if out[0].DataBytes != int64(len(payload)) {
		t.Errorf("DataBytes = %d, want %d", out[0].DataBytes, len(payload))
	}
	got, err := os.ReadFile(out[0].ImagePath)
	if err != nil {
		t.Fatalf("read stored attachment: %v", err)
	}
	if string(got) != string(payload) {
		t.Errorf("stored bytes = %q, want %q", got, payload)
	}
	// The user's original file is untouched: re-homing copies, never moves.
	if _, err := os.Stat(external); err != nil {
		t.Errorf("external file was disturbed: %v", err)
	}
}

func TestNormalizeAttachmentPartsIsIdempotent(t *testing.T) {
	mgr, _ := newAttachmentTestManager(t)
	parts := []message.ContentPart{{Type: "image", MimeType: "image/png", Data: []byte("once")}}

	first, err := mgr.NormalizeAttachmentParts(parts)
	if err != nil {
		t.Fatalf("first normalize: %v", err)
	}
	second, err := mgr.NormalizeAttachmentParts(first)
	if err != nil {
		t.Fatalf("second normalize: %v", err)
	}
	if second[0].ImagePath != first[0].ImagePath {
		t.Errorf("ImagePath changed on re-normalize: %q -> %q", first[0].ImagePath, second[0].ImagePath)
	}
	if second[0].DataBytes != first[0].DataBytes {
		t.Errorf("DataBytes changed on re-normalize: %d -> %d", first[0].DataBytes, second[0].DataBytes)
	}
}

// A failed write must hand back the original parts with their inline bytes: Data
// is the only copy at that point, and dropping it would lose the attachment.
func TestNormalizeAttachmentPartsKeepsDataWhenWriteFails(t *testing.T) {
	mgr, dir := newAttachmentTestManager(t)
	// A regular file where images/ must be makes directory creation fail.
	if err := os.WriteFile(filepath.Join(dir, "images"), []byte("not a dir"), 0o600); err != nil {
		t.Fatalf("block images dir: %v", err)
	}
	payload := []byte("must-survive")
	parts := []message.ContentPart{{Type: "image", MimeType: "image/png", Data: payload}}

	out, err := mgr.NormalizeAttachmentParts(parts)
	if err == nil {
		t.Fatal("expected an error when the images directory cannot be created")
	}
	if len(out) != 1 || len(out[0].Data) != len(payload) {
		t.Fatalf("inline data was dropped on failure: %+v", out)
	}
	if string(out[0].Data) != string(payload) {
		t.Errorf("inline data changed: %q", out[0].Data)
	}
}

func TestNormalizeAttachmentPartsLeavesTextPartsAlone(t *testing.T) {
	mgr, _ := newAttachmentTestManager(t)
	parts := []message.ContentPart{{Type: "text", Text: "hello"}}

	out, err := mgr.NormalizeAttachmentParts(parts)
	if err != nil {
		t.Fatalf("NormalizeAttachmentParts: %v", err)
	}
	if len(out) != 1 || out[0].Text != "hello" {
		t.Fatalf("text part was modified: %+v", out)
	}
}
