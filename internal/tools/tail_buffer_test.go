package tools

import (
	"strings"
	"testing"
)

func TestTailBufferKeepsTheNewestBytes(t *testing.T) {
	b := NewTailBuffer(8)
	for _, chunk := range []string{"aaaa", "bbbb", "cccc"} {
		if _, err := b.Write([]byte(chunk)); err != nil {
			t.Fatalf("Write(%q): %v", chunk, err)
		}
	}

	got := b.String()
	if !strings.HasSuffix(got, "bbbbcccc") {
		t.Fatalf("retained = %q, want the newest 8 bytes", got)
	}
	if !strings.HasPrefix(got, "...(output truncated:") {
		t.Fatalf("retained = %q, want a truncation notice", got)
	}
	if strings.Contains(got, "aaaa") {
		t.Fatalf("retained = %q, want the stale head dropped", got)
	}
	if raw := b.raw(); raw != "bbbbcccc" {
		t.Fatalf("raw = %q, want the retained window without a notice", raw)
	}

	// A single write larger than the cap keeps only its own tail, and must not
	// leave the window above the cap.
	big := NewTailBuffer(4)
	if _, err := big.Write([]byte("abcdefgh")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if got := big.String(); !strings.HasSuffix(got, "efgh") {
		t.Fatalf("retained = %q, want the newest 4 bytes", got)
	}
	if _, err := big.Write([]byte("ijklmn")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if got := big.String(); !strings.HasSuffix(got, "klmn") {
		t.Fatalf("retained = %q, want the newest 4 bytes after the second oversized write", got)
	}
}

func TestTailBufferCursorsReportDroppedBytes(t *testing.T) {
	b := NewTailBuffer(4)
	if _, err := b.Write([]byte("abcdefgh")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if b.hasDataAfter(8) {
		t.Fatal("hasDataAfter(8) must be false when nothing was written after the cursor")
	}
	if !b.hasDataAfter(7) {
		t.Fatal("hasDataAfter(7) must be true for retained output past the cursor")
	}

	// A reader whose cursor predates the retained window is told how much it
	// missed and resumes from the window start.
	chunk, next, dropped := b.readFrom(0)
	if chunk != "efgh" || next != 8 || dropped != 4 {
		t.Fatalf("readFrom(0) = (%q, %d, %d), want (efgh, 8, 4)", chunk, next, dropped)
	}
	// A cursor past the end is clamped instead of panicking.
	chunk, next, dropped = b.readFrom(99)
	if chunk != "" || next != 8 || dropped != 0 {
		t.Fatalf("readFrom(99) = (%q, %d, %d), want (\"\", 8, 0)", chunk, next, dropped)
	}

	// tail reports the dropped prefix separately from its own cut.
	snippet, base, truncated := b.tail(2)
	if snippet != "gh" || base != 4 || !truncated {
		t.Fatalf("tail(2) = (%q, %d, %t), want (gh, 4, true)", snippet, base, truncated)
	}
	if _, _, truncated := b.tail(99); truncated {
		t.Fatal("tail(99) must not report a cut when the window fits")
	}
}

func BenchmarkTailBufferStringTruncated(b *testing.B) {
	buffer := NewTailBuffer(1 << 20)
	chunk := []byte(strings.Repeat("x", 64<<10))
	for i := 0; i < 32; i++ {
		_, _ = buffer.Write(chunk)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = buffer.String()
	}
}
