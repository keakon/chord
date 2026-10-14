package recovery

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestListSessionActivityMatchesDisplayList(t *testing.T) {
	root := t.TempDir()
	for _, id := range []string{"one", "two"} {
		dir := filepath.Join(root, id)
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "main.jsonl"), []byte("{}\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	full, err := ListSessions(root, "")
	if err != nil {
		t.Fatal(err)
	}
	light, err := ListSessionActivity(root, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(full) != 2 || len(light) != len(full) {
		t.Fatalf("counts %d %d", len(full), len(light))
	}
	for i, item := range light {
		if item.ID != full[i].ID || item.Path != full[i].Path || item.Locked != full[i].Locked || !item.LastModTime.Equal(full[i].LastModTime) {
			t.Fatalf("entry differs: %+v %+v", item, full[i])
		}
	}
}

func TestListSessionActivityDoesNotLoadDisplayMetadata(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "sample")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.jsonl"), []byte("{}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, sessionMetaFile), []byte("invalid"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ListSessions(root, ""); err == nil {
		t.Fatal("display list accepted invalid metadata")
	}
	list, err := ListSessionActivity(root, "")
	if err != nil || len(list) != 1 {
		t.Fatalf("activity list: %+v %v", list, err)
	}
}

func BenchmarkListSessionActivity(b *testing.B) {
	root := b.TempDir()
	for i := range 100 {
		dir := filepath.Join(root, fmt.Sprintf("session-%03d", i))
		if err := os.Mkdir(dir, 0700); err != nil {
			b.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "main.jsonl"), []byte("{}\n"), 0600); err != nil {
			b.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, sessionMetaFile), []byte(`{"title":"Sample"}`), 0600); err != nil {
			b.Fatal(err)
		}
	}
	for _, test := range []struct {
		name string
		list func(string, string) ([]SessionInfo, error)
	}{{"display", ListSessions}, {"activity", ListSessionActivity}} {
		b.Run(test.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if _, err := test.list(root, ""); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func TestListSessionActivityReadOnly(t *testing.T) {
	for _, withGuard := range []bool{false, true} {
		name := "without-guard"
		if withGuard {
			name = "with-guard"
		}
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			dir := filepath.Join(root, "sample")
			if err := os.Mkdir(dir, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "main.jsonl"), []byte("{}\n"), 0400); err != nil {
				t.Fatal(err)
			}
			guard := filepath.Join(dir, sessionGuardFile)
			if withGuard {
				if err := os.WriteFile(guard, nil, 0400); err != nil {
					t.Fatal(err)
				}
			}
			before, err := os.ReadDir(dir)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(dir, 0500); err != nil {
				t.Fatal(err)
			}
			defer func() { _ = os.Chmod(dir, 0700) }()
			list, err := ListSessionActivity(root, "")
			if err != nil || len(list) != 1 || list[0].Locked {
				t.Fatalf("activity list: %+v, %v", list, err)
			}
			after, err := os.ReadDir(dir)
			if err != nil {
				t.Fatal(err)
			}
			if len(before) != len(after) {
				t.Fatalf("listing changed directory entries: %v -> %v", before, after)
			}
			for i, entry := range before {
				if entry.Name() != after[i].Name() {
					t.Fatal("listing changed files")
				}
			}
		})
	}
}

func TestListSessionActivityRetainsLiveGuardOwnership(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "sample")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.jsonl"), []byte("{}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	lock, err := AcquireSessionLock(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lock.Release() }()
	if err := os.Remove(filepath.Join(dir, sessionLockFile)); err != nil {
		t.Fatal(err)
	}
	list, err := ListSessionActivity(root, "")
	if err != nil || len(list) != 1 || !list[0].Locked {
		t.Fatalf("live guard not detected: %+v %v", list, err)
	}
}

func TestSessionActivityProbeDoesNotClaimOwnership(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, sessionGuardFile), nil, 0400); err != nil {
		t.Fatal(err)
	}
	probe, err := openSessionGuardForProbe(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer probe.Close()
	if err := lockSessionGuardForProbe(probe); err != nil {
		t.Fatal(err)
	}
	defer unlockSessionGuardFile(probe)
	locked, err := SessionLockActive(dir)
	if err != nil || locked {
		t.Fatalf("another read probe counted as owner: locked=%v err=%v", locked, err)
	}
}
