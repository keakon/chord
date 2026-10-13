package desktop

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestDocumentPolicy(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"report.pdf", "index.html", "中文 ' quoted.pdf"} {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, nil, 0600); err != nil {
			t.Fatal(err)
		}
		if err := validateDocument(path); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	for _, name := range []string{"run.exe", "shortcut.lnk", "app.desktop", "script.sh"} {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, nil, 0600); err != nil {
			t.Fatal(err)
		}
		if err := validateDocument(path); err == nil {
			t.Errorf("accepted %s", name)
		}
	}
	if err := validateDocument("https://example.org/x.pdf"); err == nil {
		t.Fatal("URL accepted")
	}
	if err := validateDocument(dir); err == nil {
		t.Fatal("directory accepted")
	}
}
func TestRemoteDesktopAndOutputBounds(t *testing.T) {
	t.Setenv("SSH_CONNECTION", "remote")
	service := New()
	caps, err := service.Capabilities(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if caps.DefaultOpen || caps.Enumerate || caps.NativeChooser {
		t.Fatal("remote desktop enabled")
	}
	var b boundedBuffer
	if n, err := b.Write(make([]byte, maxOutput+100)); err != nil || n != maxOutput+100 {
		t.Fatal("writer failed to drain helper")
	}
	if b.Len() != maxOutput || !b.exceeded {
		t.Fatal("helper output budget not enforced")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := runCommand(ctx, false, "does-not-exist"); !errors.Is(err, context.Canceled) {
		t.Fatalf("dispatch ignored cancellation: %v", err)
	}
}
