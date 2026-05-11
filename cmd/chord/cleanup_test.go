package main

import (
	"bytes"
	"testing"

	"github.com/keakon/chord/internal/bytefmt"
	"github.com/keakon/chord/internal/maintenance"
)

func TestCleanupByteFormatter(t *testing.T) {
	tests := []struct {
		name  string
		bytes int64
		want  string
	}{
		{name: "zero", bytes: 0, want: "0 B"},
		{name: "bytes", bytes: 847, want: "847 B"},
		{name: "kb", bytes: 1536, want: "1.5 KB"},
		{name: "mb", bytes: 276285348, want: "263.5 MB"},
		{name: "gb", bytes: 31775732436, want: "29.6 GB"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := bytefmt.Short(tt.bytes); got != tt.want {
				t.Fatalf("bytefmt.Short(%d) = %q, want %q", tt.bytes, got, tt.want)
			}
		})
	}
}

func TestWriteCleanupStatusUsesHumanReadableSizes(t *testing.T) {
	st := &maintenance.Status{
		StateDir:     "/state",
		CacheDir:     "/cache",
		LogsDir:      "/state/logs",
		StateBytes:   31775732436,
		CacheBytes:   847,
		LogsBytes:    276285348,
		SessionCount: 8260,
		ProjectCount: 8373,
		Warnings:     []string{"scan /locked: permission denied"},
	}

	var buf bytes.Buffer
	writeCleanupStatus(&buf, st)

	want := "state_dir: /state (29.6 GB)\n" +
		"cache_dir: /cache (847 B)\n" +
		"logs_dir: /state/logs (263.5 MB)\n" +
		"sessions: 8260 across 8373 projects\n" +
		"warning: scan /locked: permission denied\n"
	if got := buf.String(); got != want {
		t.Fatalf("cleanup status output = %q, want %q", got, want)
	}
}

func TestWriteCleanupCandidateUsesHumanReadableSizes(t *testing.T) {
	candidate := maintenance.CleanupCandidate{
		Path:  "/state/sessions/proj/session-a",
		Bytes: 276285348,
	}

	var buf bytes.Buffer
	writeCleanupCandidate(&buf, "would remove", candidate)

	want := "would remove /state/sessions/proj/session-a (263.5 MB)\n"
	if got := buf.String(); got != want {
		t.Fatalf("cleanup candidate output = %q, want %q", got, want)
	}
}

func TestWriteCleanupCandidateSkip(t *testing.T) {
	candidate := maintenance.CleanupCandidate{
		Path: "/state/logs/chord.log.2",
		Skip: "permission denied",
	}

	var buf bytes.Buffer
	writeCleanupCandidate(&buf, "would remove", candidate)

	want := "skip /state/logs/chord.log.2: permission denied\n"
	if got := buf.String(); got != want {
		t.Fatalf("cleanup candidate skip output = %q, want %q", got, want)
	}
}
