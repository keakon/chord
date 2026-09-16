//go:build darwin

package clipboardread

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// stubReaderCommand replaces the osascript process with a shell script for one
// test, so the decode, normalization, and error mapping run end to end without
// touching the real pasteboard.
func stubReaderCommand(t *testing.T, body string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "reader.sh")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatalf("write reader stub: %v", err)
	}
	orig := newReaderCommand
	newReaderCommand = func(ctx context.Context) *exec.Cmd {
		return exec.CommandContext(ctx, path)
	}
	t.Cleanup(func() { newReaderCommand = orig })
}

func stubReaderOutput(t *testing.T, mimeType string, data []byte) {
	t.Helper()
	fixture := filepath.Join(t.TempDir(), "payload.txt")
	if err := os.WriteFile(fixture, []byte(readerOutput(mimeType, data)), 0o644); err != nil {
		t.Fatalf("write reader payload: %v", err)
	}
	stubReaderCommand(t, "cat "+fixture)
}

func TestReadNormalizesPNGFromReader(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, img); err != nil {
		t.Fatal(err)
	}
	stubReaderOutput(t, "image/png", encoded.Bytes())

	data, mimeType, err := Read()
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if mimeType != "image/png" && mimeType != "image/jpeg" {
		t.Fatalf("mime type = %q, want normalized PNG/JPEG", mimeType)
	}
	if _, _, err := image.Decode(bytes.NewReader(data)); err != nil {
		t.Fatalf("normalized clipboard image is not decodable: %v", err)
	}
}

func TestReadKeepsPDFFromReader(t *testing.T) {
	pdf := []byte("%PDF-1.4 sample")
	stubReaderOutput(t, "application/pdf", pdf)

	data, mimeType, err := Read()
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if mimeType != "application/pdf" || !bytes.Equal(data, pdf) {
		t.Fatalf("Read = %d bytes, %q; want the original PDF", len(data), mimeType)
	}
}

func TestReadReportsNoAttachment(t *testing.T) {
	stubReaderOutput(t, "NONE", nil)

	if _, _, err := Read(); !errors.Is(err, ErrNoAttachment) {
		t.Fatalf("Read() error = %v, want %v", err, ErrNoAttachment)
	}
}

func TestReadReportsReaderFailure(t *testing.T) {
	stubReaderCommand(t, "echo 'reader exploded' >&2; exit 3")

	_, _, err := Read()
	if err == nil {
		t.Fatal("Read() succeeded with a failing reader")
	}
	if !strings.Contains(err.Error(), "clipboard attachment unavailable") {
		t.Fatalf("Read() error = %v, want a product-level message", err)
	}
	if strings.Contains(err.Error(), "reader exploded") {
		t.Fatalf("Read() error leaks the reader's stderr: %v", err)
	}
}

func TestReadTimesOutHungReader(t *testing.T) {
	stubReaderCommand(t, "sleep 5")
	origTimeout, origWaitDelay := readTimeout, waitDelay
	readTimeout, waitDelay = 100*time.Millisecond, 50*time.Millisecond
	t.Cleanup(func() { readTimeout, waitDelay = origTimeout, origWaitDelay })

	start := time.Now()
	_, _, err := Read()
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("Read() error = %v, want a timeout", err)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("Read took %v to give up on a hung reader", elapsed)
	}
}

func TestReaderScriptReportsOversizedPayload(t *testing.T) {
	// The script compares against the limit before encoding, so an oversized
	// pasteboard item is reported by length alone.
	fixture := filepath.Join(t.TempDir(), "payload.txt")
	header := fmt.Sprintf("%s\napplication/pdf\n%d\n", readerProtocolMagic, maxAttachmentBytes+1)
	if err := os.WriteFile(fixture, []byte(header), 0o644); err != nil {
		t.Fatalf("write reader payload: %v", err)
	}
	stubReaderCommand(t, "cat "+fixture)

	if _, _, err := Read(); err == nil {
		t.Fatal("Read() accepted an oversized payload")
	}
}
