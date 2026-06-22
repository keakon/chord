package main

import (
	"bytes"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/keakon/golog"

	"github.com/keakon/chord/internal/config"
)

func TestRotatingLogFileRotatesAtSoftLimit(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "chord.log")
	w, err := newRotatingLogFileWithOptions(path, rotatingLogOptions{
		MaxSize:             64,
		MaxFiles:            3,
		CheckEveryBytes:     16,
		MaintenanceInterval: 10 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("newRotatingLogFileWithOptions: %v", err)
	}
	defer w.Close()

	payload := bytes.Repeat([]byte("x"), 40)
	if _, err := w.Write(payload); err != nil {
		t.Fatalf("first write: %v", err)
	}
	if _, err := w.Write(payload); err != nil {
		t.Fatalf("second write: %v", err)
	}
	if err := w.maybeMaintain(); err != nil {
		t.Fatalf("maybeMaintain: %v", err)
	}

	if _, err := os.Stat(path + ".1"); err != nil {
		t.Fatalf("expected rotated file .1: %v", err)
	}
	if _, err := os.Stat(path + ".2"); err == nil {
		t.Fatal("did not expect .2 after a single rotation")
	}
}

func TestRotatingLogFileCloseIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "chord.log")
	w, err := newRotatingLogFileWithOptions(path, rotatingLogOptions{MaintenanceInterval: 10 * time.Millisecond})
	if err != nil {
		t.Fatalf("newRotatingLogFileWithOptions: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

func TestRotatingLogFileWriteAfterCloseReturnsErrClosed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "chord.log")
	w, err := newRotatingLogFileWithOptions(path, rotatingLogOptions{MaintenanceInterval: time.Hour})
	if err != nil {
		t.Fatalf("newRotatingLogFileWithOptions: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := w.Write([]byte("after close")); err != os.ErrClosed {
		t.Fatalf("Write after Close err = %v, want %v", err, os.ErrClosed)
	}
}

func TestRotatingLogFileKeepsOnlyConfiguredRotations(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "chord.log")
	w, err := newRotatingLogFileWithOptions(path, rotatingLogOptions{
		MaxSize:             32,
		MaxFiles:            2,
		CheckEveryBytes:     1,
		MaintenanceInterval: time.Hour,
	})
	if err != nil {
		t.Fatalf("newRotatingLogFileWithOptions: %v", err)
	}
	defer w.Close()

	payload := bytes.Repeat([]byte("x"), 40)
	for i := 0; i < 4; i++ {
		if _, err := w.Write(payload); err != nil {
			t.Fatalf("Write %d: %v", i, err)
		}
		if err := w.maybeMaintain(); err != nil {
			t.Fatalf("maybeMaintain %d: %v", i, err)
		}
	}

	if _, err := os.Stat(path); err != nil {
		t.Fatalf("active log missing: %v", err)
	}
	if _, err := os.Stat(path + ".1"); err != nil {
		t.Fatalf("latest rotated log missing: %v", err)
	}
	if _, err := os.Stat(path + ".2"); !os.IsNotExist(err) {
		t.Fatalf("unexpected extra rotated log .2 err=%v", err)
	}
}

func TestRotatingLogFileReopensWhenActivePathIsRemoved(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "chord.log")
	w, err := newRotatingLogFileWithOptions(path, rotatingLogOptions{MaintenanceInterval: time.Hour})
	if err != nil {
		t.Fatalf("newRotatingLogFileWithOptions: %v", err)
	}
	defer w.Close()

	if _, err := w.Write([]byte("before\n")); err != nil {
		t.Fatalf("Write before remove: %v", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatalf("Remove active log: %v", err)
	}
	if err := w.maybeMaintain(); err != nil {
		t.Fatalf("maybeMaintain after remove: %v", err)
	}
	if _, err := w.Write([]byte("after\n")); err != nil {
		t.Fatalf("Write after reopen: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile reopened log: %v", err)
	}
	if got := string(data); got != "after\n" {
		t.Fatalf("reopened log content = %q, want only new content", got)
	}
}

func TestRotatingLogFileRebindsStderrRedirectOnReopen(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "chord.log")
	w, err := newRotatingLogFileWithOptions(path, rotatingLogOptions{MaintenanceInterval: time.Hour})
	if err != nil {
		t.Fatalf("newRotatingLogFileWithOptions: %v", err)
	}
	defer w.Close()

	redirect := &stderrRedirect{active: true, writeFile: w.CurrentFile()}
	w.SetStderrRedirect(redirect)
	if err := os.Remove(path); err != nil {
		t.Fatalf("Remove active log: %v", err)
	}
	if err := w.maybeMaintain(); err != nil {
		t.Fatalf("maybeMaintain after remove: %v", err)
	}

	redirect.mu.Lock()
	got := redirect.writeFile
	redirect.mu.Unlock()
	if got == nil || got != w.CurrentFile() {
		t.Fatalf("stderr redirect writeFile not rebound to current log")
	}
}

func TestResolveLogLevelProjectOverridesGlobal(t *testing.T) {
	global := &config.Config{LogLevel: "info"}
	project := &config.Config{LogLevel: "debug"}
	if got := resolveLogLevel(global, project); got != golog.DebugLevel {
		t.Fatalf("resolveLogLevel() = %v, want %v", got, golog.DebugLevel)
	}
}

func TestDebugLoggingEnabled(t *testing.T) {
	if !debugLoggingEnabled(&config.Config{LogLevel: "debug"}, nil) {
		t.Fatal("expected debug logging enabled for global debug")
	}
	if debugLoggingEnabled(&config.Config{LogLevel: "info"}, nil) {
		t.Fatal("did not expect debug logging for info")
	}
}

func TestProxyScheme(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "http", in: "http://proxy.example:8080", want: "http"},
		{name: "https", in: "https://user:pass@proxy.example", want: "https"},
		{name: "socks", in: "socks5://127.0.0.1:1080", want: "socks5"},
		{name: "missing scheme", in: "proxy.example:8080", want: "unknown"},
		{name: "empty", in: "", want: "unknown"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := proxyScheme(tt.in); got != tt.want {
				t.Fatalf("proxyScheme(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestGologLoggerWithContextAddsInstanceFields(t *testing.T) {
	var buf bytes.Buffer
	logger := newGologLoggerWithContext(&buf, golog.InfoLevel, logContext{
		PWD:     "/tmp/workspace",
		PID:     1234,
		SID:     "20260502015258426",
		AgentID: "sub-agent1",
	})
	logger.Info("hello")

	text := buf.String()
	for _, want := range []string{
		"pwd=/tmp/workspace",
		"pid=1234",
		"sid=20260502015258426",
		"agent=sub-agent1",
		"] hello",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("log output = %q, want %q", text, want)
		}
	}
}

func TestGologLoggerWithContextOmitsMainAgent(t *testing.T) {
	var buf bytes.Buffer
	logger := newGologLoggerWithContext(&buf, golog.InfoLevel, logContext{
		PWD:     "/tmp/workspace",
		PID:     1234,
		SID:     "20260502015258426",
		AgentID: "main",
	})
	logger.Info("hello")

	if text := buf.String(); strings.Contains(text, "agent=") {
		t.Fatalf("log output = %q, want no agent field", text)
	}
}

func TestLogEffectiveProxy(t *testing.T) {
	var buf bytes.Buffer
	old := getDefaultLogger()
	setDefaultLogger(newGologLogger(&buf, golog.DebugLevel))
	t.Cleanup(func() { setDefaultLogger(old) })

	logEffectiveProxy("")
	logEffectiveProxy("direct")
	logEffectiveProxy("https://user:pass@proxy.example")

	text := buf.String()
	for _, want := range []string{
		"proxy: using environment",
		"proxy: disabled",
		"proxy: configured",
		"scheme=https",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("log output = %q, want %q", text, want)
		}
	}
	if strings.Contains(text, "user:pass") {
		t.Fatalf("log output leaked proxy credentials: %q", text)
	}
}

func TestWriteStartupStderrNoticeUsesProvidedLogPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "startup.log")
	writeStartupStderrNotice(path, os.ErrPermission)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !strings.Contains(string(data), "startup stderr redirect unavailable") {
		t.Fatalf("unexpected notice: %q", string(data))
	}
}

func TestRedirectProcessStderrWritesStructuredInstanceTaggedLines(t *testing.T) {
	var buf bytes.Buffer
	logger := newGologLogger(&buf, golog.DebugLevel)
	r := &stderrRedirect{logger: logger}

	r.logLine("stderr line one\n")

	text := buf.String()
	for _, want := range []string{
		"stderr",
		"stderr_text=stderr line one",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("log = %q, want %q", text, want)
		}
	}
}

func TestChordCodeUsesGologForLogging(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	for _, dir := range []string{"cmd", "internal"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || filepath.Ext(path) != ".go" {
				return nil
			}
			fileSet := token.NewFileSet()
			parsed, err := parser.ParseFile(fileSet, path, nil, parser.ImportsOnly)
			if err != nil {
				return err
			}
			for _, imported := range parsed.Imports {
				for _, disallowed := range []string{`"log/slog"`, `"log"`} {
					if imported.Path.Value == disallowed {
						rel, _ := filepath.Rel(root, path)
						t.Fatalf("%s imports disallowed logging package %s", rel, disallowed)
					}
				}
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", dir, err)
		}
	}
}

func TestRedirectProcessStderrUsesUpdatedLogger(t *testing.T) {
	var buf bytes.Buffer
	r := &stderrRedirect{logger: newGologLoggerWithContext(&buf, golog.DebugLevel, logContext{PID: 1})}

	r.logLine("before session\n")
	r.SetLogger(newGologLoggerWithContext(&buf, golog.DebugLevel, logContext{PID: 1, SID: "20260502015258426"}))
	r.logLine("after session\n")

	text := buf.String()
	if !strings.Contains(text, "before session") || !strings.Contains(text, "after session") {
		t.Fatalf("log output = %q, want both stderr lines", text)
	}
	if strings.Contains(text, "sid=20260502015258426] stderr stderr_text=before session") {
		t.Fatalf("pre-session stderr unexpectedly had sid: %q", text)
	}
	if !strings.Contains(text, "sid=20260502015258426] stderr stderr_text=after session") {
		t.Fatalf("updated stderr logger did not include sid: %q", text)
	}
}

func TestStderrRedirectRebindNoopsWhenInactive(t *testing.T) {
	dir := t.TempDir()
	oldFile, err := os.Create(filepath.Join(dir, "old.log"))
	if err != nil {
		t.Fatalf("Create old log: %v", err)
	}
	defer oldFile.Close()
	newFile, err := os.Create(filepath.Join(dir, "new.log"))
	if err != nil {
		t.Fatalf("Create new log: %v", err)
	}
	defer newFile.Close()

	r := &stderrRedirect{active: false, writeFile: oldFile}
	if err := r.Rebind(newFile); err != nil {
		t.Fatalf("Rebind inactive redirect: %v", err)
	}
	if r.writeFile != oldFile {
		t.Fatal("inactive redirect should keep existing write file")
	}
}

func TestStderrRedirectRestoreNilAndInactiveAreNoops(t *testing.T) {
	var nilRedirect *stderrRedirect
	if err := nilRedirect.Restore(); err != nil {
		t.Fatalf("nil Restore: %v", err)
	}
	if err := (&stderrRedirect{}).Restore(); err != nil {
		t.Fatalf("inactive Restore: %v", err)
	}
}
