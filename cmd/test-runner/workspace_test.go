//go:build unix

package main

import (
	"encoding/json"
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
