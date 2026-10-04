package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func generatorFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, name := range []string{"catalog.yaml", "endpoints.yaml", "models.yaml", "bindings.yaml"} {
		data, err := os.ReadFile(filepath.Join("..", "..", "internal", "modelcatalog", "data", name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestValidateDoesNotWriteAndRejectsInvalidCandidate(t *testing.T) {
	dir := generatorFixture(t)
	out := filepath.Join(dir, "artifact.json")
	if err := os.WriteFile(out, []byte("sentinel"), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout bytes.Buffer
	if err := run([]string{"-dir", dir, "-validate", "-out", out}, &stdout); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(out)
	if err != nil || string(data) != "sentinel" {
		t.Fatalf("validation modified artifact: %q, %v", data, err)
	}
	if err := os.Mkdir(filepath.Join(dir, "candidates"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "candidates", "invalid.yaml"), []byte("wire_model_id: sample\nunknown_field: true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"-dir", dir, "-validate"}, &stdout); err == nil || !strings.Contains(err.Error(), "unknown_field") {
		t.Fatalf("invalid candidate accepted: %v", err)
	}
}

func TestRevisionMismatchDoesNotWrite(t *testing.T) {
	dir := generatorFixture(t)
	out := filepath.Join(dir, "artifact.json")
	if err := run([]string{"-dir", dir, "-out", out, "-revision", "v1900-01-01.1"}, &bytes.Buffer{}); err == nil {
		t.Fatal("mismatched release accepted")
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Fatalf("failed generation left an artifact: %v", err)
	}
}
