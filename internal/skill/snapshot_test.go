package skill

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestSnapshotLoadUsesCapturedDeclarations(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "SKILL.md")
	for name, data := range map[string]string{
		"SKILL.md":   "---\nname: sample\ndescription: original helper\n---\noriginal body\n",
		"chord.yaml": "context: inline\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	snapshot, err := ReadSnapshot(path, os.ReadFile)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, []byte("---\nname: sample\ndescription: changed helper\n---\nchanged body\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, "chord.yaml"), []byte("context: fork\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := snapshot.Load()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Description != "original helper" || loaded.Context != "inline" || loaded.Content != "original body\n" {
		t.Fatalf("snapshot changed: %+v", loaded)
	}
}

func TestSnapshotStopsAtDeclarationReadError(t *testing.T) {
	dir := t.TempDir()
	names := []string{"SKILL.md", "chord.yaml", "agent.yaml"}
	for _, name := range names {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("sample"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	readErr := errors.New("declaration read failed")
	for index, name := range names {
		t.Run(name, func(t *testing.T) {
			var readNames []string
			snapshot, err := ReadSnapshot(filepath.Join(dir, "SKILL.md"), func(path string) ([]byte, error) {
				readNames = append(readNames, filepath.Base(path))
				if filepath.Base(path) == name {
					return nil, readErr
				}
				return os.ReadFile(path)
			})
			if !errors.Is(err, readErr) || snapshot != nil {
				t.Fatalf("snapshot=%+v error=%v, want no snapshot and the reader error", snapshot, err)
			}
			if !slices.Equal(readNames, names[:index+1]) {
				t.Fatalf("read declarations=%v, want %v", readNames, names[:index+1])
			}
		})
	}
}
