package tui

import (
	"archive/zip"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func diagnosticExportModel(t *testing.T) (*Model, string) {
	t.Helper()
	root := t.TempDir()
	for _, name := range []string{"CHORD_CONFIG_HOME", "CHORD_STATE_DIR", "CHORD_CACHE_DIR", "CHORD_SESSIONS_DIR", "CHORD_LOGS_DIR"} {
		t.Setenv(name, filepath.Join(root, name))
	}
	m := NewModelWithSize(nil, 80, 24)
	m.workingDir = root
	return &m, root
}

func TestDiagnosticsExportDefersIOAndSuppressesDuplicate(t *testing.T) {
	m, root := diagnosticExportModel(t)
	cmd := m.exportDiagnosticsBundleNow("test")
	if cmd == nil || !m.diagnosticsPending {
		t.Fatal("export not scheduled")
	}
	if _, err := os.Stat(filepath.Join(root, "CHORD_STATE_DIR")); !os.IsNotExist(err) {
		t.Fatalf("snapshot created state directory before command ran: %v", err)
	}
	duplicate := m.exportDiagnosticsBundleNow("test")
	if duplicate != nil {
		if _, ok := duplicate().(diagnosticsBundleMsg); ok {
			t.Fatal("duplicate export was started")
		}
	}
	// Changes made after the UI snapshot belong to a later export.
	m.input.SetValue("changed after snapshot")
	result := cmd().(diagnosticsBundleMsg)
	if result.err != nil {
		t.Fatal(result.err)
	}
	bundle, err := zip.OpenReader(result.path)
	if err != nil {
		t.Fatal(err)
	}
	defer bundle.Close()
	found := make(map[string]bool)
	for _, entry := range bundle.File {
		found[entry.Name] = true
		if entry.Name == "tui-dump.txt" {
			reader, err := entry.Open()
			if err != nil {
				t.Fatal(err)
			}
			content, err := io.ReadAll(reader)
			reader.Close()
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(content), "changed after snapshot") {
				t.Fatal("background export read mutable UI state")
			}
		}
	}
	for _, name := range []string{"context.json", "metadata.txt", "tui-dump.txt", diagnosticHeapFilename} {
		if !found[name] {
			t.Errorf("missing %s", name)
		}
	}
	m.Update(result)
	if m.diagnosticsPending {
		t.Fatal("completed export still marked pending")
	}
}

func TestDiagnosticsExportCancellationAndFailureCleanUp(t *testing.T) {
	m, root := diagnosticExportModel(t)
	data, err := m.buildDiagnosticsBundle(time.Now(), "test")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, cancelErr := writeDiagnosticsBundle(ctx, data); !errors.Is(cancelErr, context.Canceled) {
		t.Fatalf("cancelled export = %v", cancelErr)
	}
	// Force the publish step to fail after ZIP creation.
	data.filename = filepath.Join("missing-directory", "bundle.zip")
	if _, publishErr := writeDiagnosticsBundle(context.Background(), data); publishErr == nil {
		t.Fatal("publish failure not reported")
	}
	err = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if strings.HasSuffix(path, ".zip") || strings.HasSuffix(path, ".tmp") {
			t.Errorf("failed export left artifact: %s", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	m.diagnosticsPending = true
	m.Update(diagnosticsBundleMsg{err: errors.New("sample failure")})
	if m.diagnosticsPending {
		t.Fatal("failed export still marked pending")
	}
}
