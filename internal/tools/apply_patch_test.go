package tools

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"
)

func applyPatchArgs(t *testing.T, patch string) []byte {
	t.Helper()
	args, err := json.Marshal(ApplyPatchArgs{Patch: patch})
	if err != nil {
		t.Fatal(err)
	}
	return args
}

func assertApplyPatchFile(t *testing.T, path, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Fatalf("%s = %q, want %q", path, got, want)
	}
}

func assertApplyPatchFileBytes(t *testing.T, path string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("%s = %X, want %X", path, got, want)
	}
}

// updateHunksFor parses a single-file update patch and returns its hunks, so
// matching behaviour can be exercised without touching the filesystem.
func updateHunksFor(t *testing.T, patch string) []applyPatchHunk {
	t.Helper()
	doc, err := ParseApplyPatch(patch)
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Operations) != 1 || len(doc.Operations[0].Hunks) != 1 {
		t.Fatalf("doc = %#v, want a single update hunk", doc)
	}
	return doc.Operations[0].Hunks
}
