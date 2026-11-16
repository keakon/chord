package skill

import (
	"os"
	"path/filepath"
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
	snapshot, err := ReadSnapshot(path)
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
