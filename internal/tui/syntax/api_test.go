package syntax

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
)

type budgetWriter struct {
	remaining, calls int
	err              error
}

func (w *budgetWriter) Write(p []byte) (int, error) {
	w.calls++
	if len(p) > w.remaining {
		n := w.remaining
		w.remaining = 0
		return n, w.err
	}
	w.remaining -= len(p)
	return len(p), nil
}

func TestWritePreservesBoundedWriterFailure(t *testing.T) {
	limitErr := errors.New("output limit")
	writer := &budgetWriter{remaining: 32, err: limitErr}
	h := New("sample.go", "", "")
	err := h.Write(writer, strings.Repeat("var value = 123\n", 1000))
	if !errors.Is(err, limitErr) {
		t.Fatalf("error = %v, want writer limit", err)
	}
	if writer.calls > 20 {
		t.Fatalf("formatter continued after limit: %d writes", writer.calls)
	}
	if len(h.renderCache) != 0 {
		t.Fatal("writer path retained full rendered output")
	}
}

func TestWriteMatchesSnippetAndKeepsPlainFallback(t *testing.T) {
	for _, path := range []string{"sample.go", "sample.unknown"} {
		source := "package main\nvar value = 123\n"
		h := New(path, "", "")
		var out bytes.Buffer
		if err := h.Write(&out, source); err != nil {
			t.Fatal(err)
		}
		if got, want := out.String(), h.Snippet(source, ""); got != want {
			t.Fatalf("path %s write %q != snippet %q", path, got, want)
		}
	}
}

func TestEnsurePreservesAnchoredStreamingCache(t *testing.T) {
	h := New("sample.go", "package main", "")
	h.Line("package main", "")
	if !h.ReadyForPath("sample.go") {
		t.Fatal("lexer not ready for rendered path")
	}
	before := h.renderCache
	Ensure(&h, "sample.go", "package main\nfunc main() {}", "")
	before[12345] = "sentinel"
	if h.renderCache[12345] != "sentinel" {
		t.Fatal("growing anchored sample replaced the render cache")
	}
	if err := h.Write(io.Discard, "package main"); err != nil {
		t.Fatal(err)
	}
	priorKey := h.ContextKey()
	Ensure(&h, "sample.py", "def sample(): pass", "")
	if h.ContextKey() == priorKey || h.ReadyForPath("sample.go") || len(h.renderCache) != 0 {
		t.Fatal("path change retained old lexer or rendered prefix")
	}
}
