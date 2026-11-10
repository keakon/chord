//go:build unix

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/externalsupervisor"
)

func TestReadyReportsUnprovenCleanupBeforeMissingMarker(t *testing.T) {
	run := testRun{dir: t.TempDir(), id: strings.Repeat("a", 32)}
	receipt := externalsupervisor.Receipt{
		Version: 2,
		RunID:   run.id,
		Cleanup: externalsupervisor.Cleanup{Note: "sample identity lookup failed"},
	}
	data, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(run.dir, run.id+".json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := run.ready(); err == nil || !strings.Contains(err.Error(), receipt.Cleanup.Note) {
		t.Fatalf("ready() = %v, want the cleanup failure cause", err)
	}
	receipt.Cleanup.Proven = true
	data, err = json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(run.dir, run.id+".json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := run.ready(); err == nil || !strings.Contains(err.Error(), "cleanup evidence unavailable") {
		t.Fatalf("ready() = %v, process cleanup alone must not prove output EOF", err)
	}
}

func TestWorkRootStableAcrossRunsAndShared(t *testing.T) {
	base := t.TempDir()
	var diagnostics bytes.Buffer
	first, err := newRun(base, &diagnostics)
	if err != nil {
		t.Fatal(err)
	}
	second, err := newRun(base, &diagnostics)
	if err != nil {
		t.Fatal(err)
	}
	defer second.lock.Close()
	if first.work() != second.work() {
		t.Fatalf("work root changed between concurrent runs: %s vs %s", first.work(), second.work())
	}
	// Cleanup removes only the run directory: concurrent runs write into the
	// shared work root, so its contents must survive another run's cleanup.
	payload := filepath.Join(first.work(), "payload")
	if err := os.WriteFile(payload, []byte("sample"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := first.remove(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(payload); err != nil {
		t.Fatalf("shared work root removed by run cleanup: %v", err)
	}
	if _, err := os.Stat(first.dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("run directory not removed: %v", err)
	}
}

func TestSweepRemovesWorkRootWhenNoRunsRemain(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, runsDirectory)
	work := filepath.Join(root, workDirectory)
	if err := os.MkdirAll(filepath.Join(work, "go-build123"), 0o700); err != nil {
		t.Fatal(err)
	}
	var diagnostics bytes.Buffer
	run, err := newRun(base, &diagnostics)
	if err != nil {
		t.Fatal(err)
	}
	defer run.lock.Close()
	if _, err := os.Stat(filepath.Join(work, "go-build123")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale work contents survived the sweep: %v", err)
	}
	if _, err := os.Stat(work); err != nil {
		t.Fatalf("work root missing after sweep: %v", err)
	}
}

func TestSweepSkippedWhileUnprovenRunRemains(t *testing.T) {
	base := t.TempDir()
	var diagnostics bytes.Buffer
	stale, err := newRun(base, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if err := stale.lock.Close(); err != nil {
		t.Fatal(err)
	}
	leftover := filepath.Join(stale.work(), "leftover")
	if err := os.MkdirAll(leftover, 0o700); err != nil {
		t.Fatal(err)
	}
	next, err := newRun(base, &diagnostics)
	if err != nil {
		t.Fatal(err)
	}
	defer next.lock.Close()
	if next.work() != stale.work() {
		t.Fatalf("work root changed while a run remained: %s vs %s", stale.work(), next.work())
	}
	if _, err := os.Stat(leftover); err != nil {
		t.Fatalf("sweep ran while an unproven run remained: %v", err)
	}
}
