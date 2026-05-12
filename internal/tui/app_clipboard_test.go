package tui

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"

	tea "charm.land/bubbletea/v2"
)

const tinyPNGBase64 = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR4nGP4z8DwHwAFAAH/iZk9HQAAAABJRU5ErkJggg=="

func writeTinyPNG(t *testing.T) string {
	t.Helper()
	data, err := base64.StdEncoding.DecodeString(tinyPNGBase64)
	if err != nil {
		t.Fatalf("decode tiny png: %v", err)
	}
	path := filepath.Join(t.TempDir(), "image.png")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write tiny png: %v", err)
	}
	return path
}

func TestHandleNonKeyInputMsgKeepsImagePathPasteAsText(t *testing.T) {
	path := writeTinyPNG(t)
	m := NewModelWithSize(nil, 80, 24)
	m.mode = ModeInsert

	cmd := m.handleNonKeyInputMsg(tea.PasteMsg{Content: path})
	if cmd != nil {
		t.Fatalf("expected no attachment command, got %T", cmd)
	}
	if got := m.input.Value(); got != path {
		t.Fatalf("input value = %q, want pasted path text %q", got, path)
	}
	if len(m.attachments) != 0 {
		t.Fatalf("attachments = %d, want 0", len(m.attachments))
	}
}

func TestPickImageFileAttachesTypedPath(t *testing.T) {
	path := writeTinyPNG(t)
	m := NewModelWithSize(nil, 80, 24)
	m.mode = ModeInsert
	m.input.SetValue(path)

	cmd := m.pickImageFile()
	if cmd == nil {
		t.Fatal("expected attach command for typed image path")
	}
	msg := cmd()
	attach, ok := msg.(attachmentReadyMsg)
	if !ok {
		t.Fatalf("cmd() = %T, want attachmentReadyMsg", msg)
	}
	if attach.err != nil {
		t.Fatalf("attachment error = %v", attach.err)
	}
	if attach.attachment.ImagePath != path {
		t.Fatalf("attachment image path = %q, want %q", attach.attachment.ImagePath, path)
	}
	if attach.attachment.MimeType != "image/png" {
		t.Fatalf("attachment mime = %q, want image/png", attach.attachment.MimeType)
	}
}
