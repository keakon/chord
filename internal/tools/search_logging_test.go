package tools

import (
	"bytes"

	"github.com/keakon/golog"
	"github.com/keakon/golog/log"

	"strings"
	"testing"
	"time"

	"github.com/keakon/chord/internal/logtest"
)

func TestLogSlowSearchBelowThresholdSkipsLog(t *testing.T) {
	var buf bytes.Buffer
	logger := logtest.NewLogger(&buf, golog.DebugLevel)
	log.SetDefaultLogger(logger)
	defer log.SetDefaultLogger(logtest.NewLogger(nil, golog.InfoLevel))

	logSlowSearch("Grep", "internal/tui", "needle", "*.go", time.Now(), "scanned_files", 12, 1, false)
	if buf.Len() != 0 {
		t.Fatalf("expected no log below threshold, got %q", buf.String())
	}
}

func TestLogSlowSearchWarnsWithSearchFacts(t *testing.T) {
	var buf bytes.Buffer
	logger := logtest.NewLogger(&buf, golog.DebugLevel)
	log.SetDefaultLogger(logger)
	defer log.SetDefaultLogger(logtest.NewLogger(nil, golog.InfoLevel))

	logSlowSearch("Grep", "internal/tui", "needle", "*.go", time.Now().Add(-slowSearchWarnThreshold-time.Millisecond), "scanned_files", 42, 3, true)
	got := buf.String()
	for _, want := range []string{
		"[W ",
		"slow search tool",
		"tool=Grep",
		"search_path=internal/tui",
		"pattern=needle",
		"filter=*.go",
		"scanned_files=42",
		"match_count=3",
		"truncated=true",
		"duration_ms=",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected log to contain %q, got %q", want, got)
		}
	}
}

func TestLogSlowSearchSupportsToolSpecificCountField(t *testing.T) {
	var buf bytes.Buffer
	logger := logtest.NewLogger(&buf, golog.DebugLevel)
	log.SetDefaultLogger(logger)
	defer log.SetDefaultLogger(logtest.NewLogger(nil, golog.InfoLevel))

	logSlowSearch("Glob", ".", "**/*.go", "", time.Now().Add(-slowSearchWarnThreshold-time.Millisecond), "candidate_count", 17, 4, false)
	got := buf.String()
	for _, want := range []string{
		"tool=Glob",
		"candidate_count=17",
		"match_count=4",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected log to contain %q, got %q", want, got)
		}
	}
	if strings.Contains(got, "scanned_files=") {
		t.Fatalf("did not expect scanned_files in glob log, got %q", got)
	}
}
