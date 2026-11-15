package maintenance

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/keakon/chord/internal/recovery"
)

type dumpCleanupContext struct {
	context.Context
	check func() error
}

func (c dumpCleanupContext) Err() error { return c.check() }

func TestCleanupLLMDumpsHoldsGuardThroughDeletion(t *testing.T) {
	for _, cancel := range []bool{false, true} {
		name := "complete"
		if cancel {
			name = "cancelled"
		}
		t.Run(name, func(t *testing.T) {
			projectDir := t.TempDir()
			sessionDir := filepath.Join(projectDir, "session")
			dumpsDir := filepath.Join(sessionDir, "dumps", "llm")
			if err := os.MkdirAll(dumpsDir, 0o700); err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"first.json", "second.json"} {
				if err := os.WriteFile(filepath.Join(dumpsDir, name), []byte("{}"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			root, err := os.OpenRoot(projectDir)
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			result := &CleanupResult{}
			checks := 0
			ctx := dumpCleanupContext{Context: t.Context(), check: func() error {
				checks++
				lock, lockErr := recovery.AcquireExistingSessionLock(sessionDir)
				if lock != nil {
					_ = lock.Release()
				}
				if _, ok := errors.AsType[*recovery.SessionLockedError](lockErr); !ok {
					t.Fatalf("resume during dump cleanup = %v, want locked", lockErr)
				}
				if checks == 2 {
					if len(result.Deleted) != 1 {
						t.Fatalf("deleted before second file = %d, want 1", len(result.Deleted))
					}
					if cancel {
						return context.Canceled
					}
				}
				return nil
			}}
			err = cleanupSessionLLMDumps(ctx, root, "session", sessionDir, nil, result)
			wantDeleted := 2
			if cancel {
				wantDeleted = 1
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("cleanup error = %v, want cancellation", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if checks != 2 || len(result.Deleted) != wantDeleted {
				t.Fatalf("checks = %d, deleted = %d; want 2, %d", checks, len(result.Deleted), wantDeleted)
			}
			lock, err := recovery.AcquireExistingSessionLock(sessionDir)
			if err != nil {
				t.Fatalf("resume after cleanup: %v", err)
			}
			defer lock.Release()
		})
	}
}

func TestCleanupLLMDumpsDryRunDoesNotCreateGuard(t *testing.T) {
	loc := testLocator(t)
	sessionDir := filepath.Join(loc.SessionsRoot, "project", "session")
	dumpsDir := filepath.Join(sessionDir, "dumps", "llm")
	if err := os.MkdirAll(dumpsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dumpsDir, "sample.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := CleanupLLMDumps(loc, CleanupOptions{Now: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Candidates) != 1 || len(result.Deleted) != 0 {
		t.Fatalf("dry run result = %+v", result)
	}
	entries, err := os.ReadDir(sessionDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "dumps" {
		t.Fatalf("dry run changed session directory: %v", entries)
	}
}
