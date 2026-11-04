package tui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/keakon/golog"
)

func TestDiagnosticLogPIDFieldEncoding(t *testing.T) {
	for _, tt := range []struct {
		name string
		line string
		pid  int
	}{
		{name: "default header", line: "[I 2026-01-01 10:00:00 sample:1] message"},
		{name: "process", line: "[I 2026-01-01 10:00:00 sample:1 pid=12] message", pid: 12},
		{name: "brackets", line: "[I 2026-01-01 10:00:00 sample:1 pwd=/tmp/sample[one] pid=12] message", pid: 12},
		{name: "spaces and brackets", line: `[I 2026-01-01 10:00:00 sample:1 pwd="/tmp/sample [one]" pid=12] message`, pid: 12},
		{name: "PID in quoted path", line: `[I 2026-01-01 10:00:00 sample:1 pwd="/tmp/sample pid=34" pid=12] message`, pid: 12},
		{name: "escaped quote", line: `[I 2026-01-01 10:00:00 sample:1 pwd="/tmp/sample\" pid=34" pid=12] message`, pid: 12},
		{name: "quoted session and agent", line: `[I 2026-01-01 10:00:00 sample:1 pwd=/tmp/sample pid=12 sid="session [one]" agent="sample\"agent"] message`, pid: 12},
		{name: "PID only in body", line: "[I 2026-01-01 10:00:00 sample:1] quoted pid=12]"},
		{name: "other process", line: "[I 2026-01-01 10:00:00 sample:1 pid=34] quoted pid=12]", pid: 34},
		{name: "PID in path without process", line: `[I 2026-01-01 10:00:00 sample:1 pwd="/tmp/sample pid=12"] quoted pid=12]`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if pid, header := diagnosticLogPID(tt.line); !header || pid != tt.pid {
				t.Fatalf("PID/header = %d/%t, want %d/true", pid, header, tt.pid)
			}
		})
	}
	if pid, header := diagnosticLogPID("continuation mentions pid=12"); header || pid != 0 {
		t.Fatalf("continuation treated as header: %d/%t", pid, header)
	}
}

func TestDiagnosticLogSelectionWithEncodedPaths(t *testing.T) {
	for _, pwd := range []string{
		"/tmp/sample",
		"/tmp/sample[one]",
		"/tmp/sample [one]",
		"/tmp/sample pid=34",
		"/tmp/sample pid=34 child",
		"/tmp/sample\" pid=34",
		"/tmp/sample\\path",
		"/tmp/sample\tpath\nchild",
	} {
		t.Run(pwd, func(t *testing.T) {
			dir := t.TempDir()
			f, err := os.Create(filepath.Join(dir, "chord.log"))
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			logger := golog.NewLogger(golog.InfoLevel)
			fields := "pwd=" + pwd
			if strings.ContainsAny(pwd, " \t\r\n=\"") {
				fields = "pwd=" + strconv.Quote(pwd)
			}
			handler := golog.NewHandler(golog.InfoLevel, golog.ParseFormat("[%l %D %T %s "+fields+" pid=12] %m"))
			handler.AddWriter(f)
			logger.AddHandler(handler)
			logger.Info("target event")
			if _, err := f.WriteString("target continuation\n[I 2026-01-01 10:00:00 sample:1 pid=34] quoted pid=12]\n"); err != nil {
				t.Fatal(err)
			}
			if err := f.Close(); err != nil {
				t.Fatal(err)
			}
			result := collectDiagnosticLogTail(context.Background(), dir, dir, 12)
			if !strings.Contains(result.tail, "target event\ntarget continuation\n") ||
				strings.Contains(result.tail, "quoted pid=") || len(result.errors) != 0 ||
				len(result.sources) != 1 || result.sources[0] != "chord.log" ||
				!strings.Contains(result.metadata(dir), "runtime_log_status: available") {
				t.Fatalf("unexpected log selection: %+v", result)
			}
		})
	}
}

func TestDiagnosticLogSelectionAcrossNoiseAndRotation(t *testing.T) {
	dir := t.TempDir()
	target := "[I 2026-01-01 10:00:00 sample:1 pid=12] target\n"
	other := "[I 2026-01-01 10:00:01 sample:1 pid=34] quote pid=12 in body\n"
	for name, content := range map[string]string{
		"chord.log":   strings.Repeat(other, 7000),
		"chord.log.1": target + "continuation mentions pid=34\n" + strings.Repeat(other, 300),
		"chord.log.2": strings.ReplaceAll(target, "target", "earlier"),
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	result := collectDiagnosticLogTail(context.Background(), dir, dir, 12)
	if result.scanLimited || len(result.errors) > 0 || len(result.sources) != 2 {
		t.Fatalf("unexpected scan: %+v", result)
	}
	want := strings.ReplaceAll(target, "target", "earlier") + target + "continuation mentions pid=34\n"
	if result.tail != want {
		t.Fatalf("tail = %q, want %q", result.tail, want)
	}
}

func TestDiagnosticLogOutputLimitsAfterFiltering(t *testing.T) {
	dir := t.TempDir()
	var content strings.Builder
	for i := range 300 {
		fmt.Fprintf(&content, "[I 2026-01-01 10:00:00 sample:1 pid=12] item %03d\n", i)
		content.WriteString("[I 2026-01-01 10:00:00 sample:1 pid=34] other\n")
	}
	if err := os.WriteFile(filepath.Join(dir, "chord.log"), []byte(content.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	result := collectDiagnosticLogTail(context.Background(), dir, dir, 12)
	if !result.outputLimited || strings.Count(result.tail, "\n") != diagnosticLogTailLines ||
		strings.Contains(result.tail, "item 059") || !strings.Contains(result.tail, "item 060") ||
		!strings.Contains(result.tail, "item 299") || strings.Contains(result.tail, "other") {
		t.Fatalf("unexpected tail: %+v", result)
	}
}

func TestDiagnosticLogScanLimitAndCancellation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "chord.log")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(diagnosticLogScanBytes + 1); err != nil {
		t.Fatal(err)
	}
	f.Close()
	result := collectDiagnosticLogTail(context.Background(), dir, dir, 12)
	if !result.scanLimited || result.scanned != diagnosticLogScanBytes || result.tail != "" ||
		!strings.Contains(result.metadata(dir), "runtime_log_status: scan_limit") {
		t.Fatalf("unexpected bounded scan: %+v", result)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result = collectDiagnosticLogTail(ctx, dir, dir, 12)
	if len(result.errors) == 0 || result.scanned != 0 {
		t.Fatalf("cancelled scan: %+v", result)
	}
}

func TestDiagnosticLogChunkBoundaryAndOversizedLine(t *testing.T) {
	for _, size := range []int{diagnosticLogChunkBytes - 1, diagnosticLogChunkBytes, diagnosticLogTailBytes * 2} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			dir := t.TempDir()
			content := "[I 2026-01-01 10:00:00 sample:1 pid=12] " + strings.Repeat("x", size) + "\r\n"
			if err := os.WriteFile(filepath.Join(dir, "chord.log"), []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			result := collectDiagnosticLogTail(context.Background(), dir, dir, 12)
			if len(result.errors) > 0 || len(result.tail) > diagnosticLogTailBytes || !strings.Contains(result.tail, "pid=12]") {
				t.Fatalf("unexpected tail length=%d errors=%v", len(result.tail), result.errors)
			}
			if size < diagnosticLogTailBytes && result.tail != strings.ReplaceAll(content, "\r\n", "\n") {
				t.Fatal("chunk boundary lost log content")
			}
			if size > diagnosticLogTailBytes && (!result.outputLimited || !strings.Contains(result.tail, "[line truncated]")) {
				t.Fatal("oversized line was not marked")
			}
		})
	}
}

func TestDiagnosticLogMissingAndReadErrorMetadata(t *testing.T) {
	dir := t.TempDir()
	missing := collectDiagnosticLogTail(context.Background(), filepath.Join(dir, "missing"), dir, 12)
	if !strings.Contains(missing.metadata(dir), "runtime_log_status: not_found") {
		t.Fatal(missing.metadata(dir))
	}
	path := filepath.Join(dir, "not-directory")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	result := collectDiagnosticLogTail(context.Background(), path, dir, 12)
	if len(result.errors) == 0 || !strings.Contains(result.metadata(dir), "runtime_log_status: error") {
		t.Fatalf("read error missing: %+v", result)
	}
}

func TestDiagnosticLogContinuationAcrossRotation(t *testing.T) {
	dir := t.TempDir()
	header := "[I 2026-01-01 10:00:00 sample:1 pid=12] target\n"
	for name, content := range map[string]string{
		"chord.log.1": header,
		"chord.log":   "continuation\n[I 2026-01-01 10:00:01 sample:1 pid=34] other\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	result := collectDiagnosticLogTail(context.Background(), dir, dir, 12)
	if result.tail != header+"continuation\n" {
		t.Fatalf("rotation lost continuation: %q", result.tail)
	}
}

func TestDiagnosticLogTruncationDuringScan(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "chord.log")
	if err := os.WriteFile(path, []byte(strings.Repeat("line\n", diagnosticLogChunkBytes)), 0o600); err != nil {
		t.Fatal(err)
	}
	truncated := false
	var result diagnosticLogTail
	err := scanDiagnosticLogReverse(context.Background(), path, &result, func(string, bool) bool {
		if !truncated {
			truncated = true
			if err := os.Truncate(path, 0); err != nil {
				t.Fatal(err)
			}
		}
		return true
	})
	if err == nil || !strings.Contains(err.Error(), "read runtime log") {
		t.Fatalf("concurrent truncation not reported: %v", err)
	}
}
