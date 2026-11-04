package tui

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/binary"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestDiagnosticHeapProfileReadableByPprof(t *testing.T) {
	held := make([]byte, 2<<20)
	runtime.GC()
	dir := t.TempDir()
	profile, metadata, err := collectDiagnosticHeap(dir)
	runtime.KeepAlive(held)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"file: heap.pprof", "forced_gc: false", "sample_rate_bytes:",
		"started_at:", "finished_at:", "[memory.before]", "[memory.after]", "go.last_gc_unix_ns:", "go.heap_alloc_bytes:"} {
		if !strings.Contains(metadata, field) {
			t.Errorf("metadata missing %q", field)
		}
	}
	path := filepath.Join(dir, diagnosticHeapFilename)
	if writeErr := os.WriteFile(path, profile, 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "go", "tool", "pprof", "-top", path).CombinedOutput()
	if err != nil {
		t.Fatalf("pprof could not parse heap: %v\n%s", err, out)
	}
	if !bytes.Contains(out, []byte("inuse_space")) {
		t.Fatalf("unexpected profile: %s", out)
	}
}

func TestDiagnosticHeapSanitizesStringTableWithoutChangingIndices(t *testing.T) {
	dir := t.TempDir()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	var wire []byte
	for _, value := range []string{"", dir + "/sample.go", home + "/other/file.go", "sample.Function"} {
		wire = binary.AppendUvarint(wire, 6<<3|2)
		wire = binary.AppendUvarint(wire, uint64(len(value)))
		wire = append(wire, value...)
	}
	// Non-string fields must pass through untouched, even if they contain bytes
	// that happen to look like paths.
	wire = append(wire, 9<<3, 123)
	var compressed bytes.Buffer
	zw := gzip.NewWriter(&compressed)
	zw.Write(wire)
	zw.Close()
	got, err := sanitizeDiagnosticHeap(compressed.Bytes(), dir)
	if err != nil {
		t.Fatal(err)
	}
	zr, err := gzip.NewReader(bytes.NewReader(got))
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := io.ReadAll(zr)
	zr.Close()
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(decoded, []byte(dir)) || bytes.Contains(decoded, []byte(home)) ||
		!bytes.Contains(decoded, []byte("<project-root>/sample.go")) || !bytes.Contains(decoded, []byte("<home>/other/file.go")) {
		t.Fatalf("profile paths not sanitized: %q", decoded)
	}
	var stringsSeen int
	for len(decoded) > 0 && decoded[0] == 6<<3|2 {
		size, n := binary.Uvarint(decoded[1:])
		decoded = decoded[1+n+int(size):]
		stringsSeen++
	}
	if stringsSeen != 4 || !bytes.Equal(decoded, []byte{9 << 3, 123}) {
		t.Fatal("profile field indices or non-string fields changed")
	}
}
