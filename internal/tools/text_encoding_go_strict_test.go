package tools

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestGoStrictUTF8Path(t *testing.T) {
	if !goStrictUTF8Path("foo.go") {
		t.Fatal("expected .go suffix")
	}
	if !goStrictUTF8Path("FOO.GO") {
		t.Fatal("expected case-insensitive .go")
	}
	if !goStrictUTF8Path(filepath.Join("pkg", "go.mod")) {
		t.Fatal("expected go.mod")
	}
	if goStrictUTF8Path("readme.md") {
		t.Fatal("markdown should not be strict")
	}
}

func TestDecodeTextBytesGoSourceSkipsLegacy(t *testing.T) {
	// Invalid UTF-8 (lone continuation byte inside ASCII), not a Unicode BOM.
	raw := []byte("hel\x80lo")
	_, err := decodeTextBytes(raw, filepath.Join("tmp", "x.go"))
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "skipped legacy encoding detection") {
		t.Fatalf("unexpected error: %v", err)
	}
	// Same bytes without path still attempt legacy (may error with generic message).
	_, err2 := decodeTextBytes(raw, "")
	if err2 == nil {
		t.Fatal("expected error for invalid UTF-8 without legacy match")
	}
}
