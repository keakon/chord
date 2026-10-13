package atomicfile

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestReplaceDurableBeforeReplacementFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "record.json")
	if _, err := ReplaceDurable(path, []byte("before"), 0o600); err != nil {
		t.Fatal(err)
	}
	failure := errors.New("injected pre-replacement failure")
	outcome, err := replaceDurable(path, []byte("after"), 0o600, func(string, string) (ReplaceOutcome, error) {
		return NotReplaced, failure
	})
	if outcome != NotReplaced || !errors.Is(err, failure) {
		t.Fatalf("got %s, %v", outcome, err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "before" {
		t.Fatalf("original record: %q, %v", data, err)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil || len(entries) != 1 {
		t.Fatalf("temporary files remain: %v, %v", entries, err)
	}
}

func TestReplaceDurable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private", "nested", "record.json")
	for _, data := range []string{"first", "second"} {
		outcome, err := ReplaceDurable(path, []byte(data), 0o600)
		if err != nil || outcome != Durable {
			t.Fatalf("replace: %s, %v", outcome, err)
		}
		got, err := os.ReadFile(path)
		if err != nil || string(got) != data {
			t.Fatalf("read: %q, %v", got, err)
		}
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil || len(entries) != 1 {
		t.Fatalf("temporary files remain: %v, %v", entries, err)
	}
}

func TestReplaceDurableInvalidPath(t *testing.T) {
	outcome, err := ReplaceDurable("", []byte("value"), 0o600)
	if outcome != NotReplaced || err == nil {
		t.Fatalf("got %s, %v", outcome, err)
	}
}
