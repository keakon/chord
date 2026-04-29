package main

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

func TestResolveLogLevelProjectOverridesGlobal(t *testing.T) {
	global := &config.Config{LogLevel: "info"}
	project := &config.Config{LogLevel: "debug"}
	if got := resolveLogLevel(global, project); got != slog.LevelDebug {
		t.Fatalf("resolveLogLevel() = %v, want %v", got, slog.LevelDebug)
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

func TestLogEffectiveProxy(t *testing.T) {
	var buf bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(old) })

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
	dir := t.TempDir()
	logPath := filepath.Join(dir, "chord.log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	defer logFile.Close()

	logger := slog.New(slog.NewTextHandler(logFile, &slog.HandlerOptions{Level: slog.LevelDebug})).With(
		"project_root", dir,
		"pid", 123,
		"instance_id", "inst-1",
		"mode", "local",
	)
	r, err := redirectProcessStderr(logFile, logger)
	if err != nil {
		t.Fatalf("redirectProcessStderr: %v", err)
	}
	defer func() { _ = r.Restore() }()

	r.logLine(logger, "stderr line one\n")
	if err := logFile.Sync(); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if err := r.Restore(); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	text := string(data)
	if !strings.Contains(text, "instance_id=inst-1") {
		t.Fatalf("log = %q, want instance_id", text)
	}
	if !strings.Contains(text, "msg=stderr") {
		t.Fatalf("log = %q, want stderr message", text)
	}
	if !strings.Contains(text, "stderr_text=\"stderr line one\"") {
		t.Fatalf("log = %q, want stderr_text field", text)
	}
}
