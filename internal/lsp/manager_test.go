package lsp

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/message"
)

func TestRelPathEscapesDir(t *testing.T) {
	if !relPathEscapesDir("..") {
		t.Fatal("relPathEscapesDir should reject parent directory")
	}
	if !relPathEscapesDir(filepath.Join("..", "outside.go")) {
		t.Fatal("relPathEscapesDir should reject paths outside directory")
	}
	if relPathEscapesDir("..foo") {
		t.Fatal("relPathEscapesDir should allow sibling-like names inside directory")
	}
	if relPathEscapesDir(filepath.Join("sub", "..foo")) {
		t.Fatal("relPathEscapesDir should allow nested names starting with dots")
	}
}

func TestClientHandlesFileAllowsDotDotPrefixWithinRoot(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "..foo.go")
	client := &Client{cwd: root, cfg: config.LSPServerConfig{FileTypes: []string{".go"}}}
	if !client.HandlesFile(path) {
		t.Fatal("HandlesFile should allow files inside root whose name starts with '..'")
	}
}

func TestClientHandlesFileRejectsOutsideRoot(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(filepath.Dir(root), "outside.go")
	client := &Client{cwd: root, cfg: config.LSPServerConfig{FileTypes: []string{".go"}}}
	if client.HandlesFile(path) {
		t.Fatal("HandlesFile should reject files outside root")
	}
}

func TestWaitForClientForPathWaitsForAsyncStartup(t *testing.T) {
	root := t.TempDir()
	mgr := NewManager(&config.Config{
		LSP: config.LSPConfig{
			"gopls": {
				Command:   "gopls",
				FileTypes: []string{".go"},
			},
		},
	}, root, nil)

	path := filepath.Join(root, "main.go")

	mgr.clientsMu.Lock()
	mgr.starting["gopls"] = true
	mgr.clientsMu.Unlock()

	go func() {
		time.Sleep(50 * time.Millisecond)
		mgr.clientsMu.Lock()
		mgr.clients["gopls"] = &Client{
			cwd: root,
			cfg: config.LSPServerConfig{FileTypes: []string{".go"}},
		}
		delete(mgr.starting, "gopls")
		mgr.clientsMu.Unlock()
	}()

	client, ok := mgr.waitForClientForPath(context.Background(), path, 300*time.Millisecond)
	if !ok {
		t.Fatal("waitForClientForPath did not wait for the async client startup")
	}
	if client == nil {
		t.Fatal("waitForClientForPath returned a nil client")
	}
}

func TestWaitForClientForPathReturnsWhenStartupSettles(t *testing.T) {
	root := t.TempDir()
	mgr := NewManager(&config.Config{
		LSP: config.LSPConfig{
			"gopls": {
				Command:   "gopls",
				FileTypes: []string{".go"},
			},
		},
	}, root, nil)

	path := filepath.Join(root, "main.go")

	mgr.clientsMu.Lock()
	mgr.starting["gopls"] = true
	mgr.clientsMu.Unlock()

	go func() {
		time.Sleep(40 * time.Millisecond)
		mgr.clientsMu.Lock()
		delete(mgr.starting, "gopls")
		mgr.clientsMu.Unlock()
	}()

	start := time.Now()
	if _, ok := mgr.waitForClientForPath(context.Background(), path, time.Second); ok {
		t.Fatal("waitForClientForPath unexpectedly found a client")
	}
	if elapsed := time.Since(start); elapsed >= 500*time.Millisecond {
		t.Fatalf("waitForClientForPath should stop once startup settles, took %v", elapsed)
	}
}

func TestSidebarEntriesIncludePerServerReviewedSnapshotsForTouchedFiles(t *testing.T) {
	mgr := NewManager(&config.Config{
		LSP: config.LSPConfig{
			"gopls": {
				Command:   "gopls",
				FileTypes: []string{".go"},
			},
			"pyright": {
				Command:   "pyright-langserver",
				FileTypes: []string{".py"},
			},
		},
	}, t.TempDir(), nil)
	mgr.clients["gopls"] = &Client{}
	mgr.reviewByServer = map[string]map[string]reviewCounts{
		"gopls": {
			normalizeWaiterPath("/a.go"):         {errors: 1, warnings: 2},
			normalizeWaiterPath("/untouched.go"): {errors: 99, warnings: 99},
		},
	}
	mgr.touchedPaths = map[string]struct{}{
		normalizeWaiterPath("/a.go"): {},
	}

	rows := mgr.SidebarEntries()
	if len(rows) != 2 {
		t.Fatalf("SidebarEntries() len = %d, want 2", len(rows))
	}
	if rows[0].Name != "gopls" || !rows[0].OK || rows[0].Errors != 1 || rows[0].Warnings != 2 {
		t.Fatalf("gopls row = %+v, want OK with 1E/2W", rows[0])
	}
	if rows[1].Name != "pyright" || !rows[1].Pending || rows[1].Errors != 0 || rows[1].Warnings != 0 {
		t.Fatalf("pyright row = %+v, want pending with zero diagnostics", rows[1])
	}
}

func TestRecordReviewSnapshotDoesNotOverwriteOtherTouchedFiles(t *testing.T) {
	mgr := &Manager{
		diagByServer: map[string]map[string]diagCounts{
			"gopls": {
				"file:///a.go": {errors: 1, warnings: 0},
				"file:///b.go": {errors: 0, warnings: 3},
			},
		},
		reviewByServer: map[string]map[string]reviewCounts{
			"gopls": {
				normalizeWaiterPath("/a.go"): {errors: 2, warnings: 0},
			},
		},
		touchedPaths: map[string]struct{}{
			normalizeWaiterPath("/a.go"): {},
			normalizeWaiterPath("/b.go"): {},
		},
	}
	mgr.recordReviewSnapshot("/b.go")
	gotA := mgr.reviewByServer["gopls"][normalizeWaiterPath("/a.go")]
	gotB := mgr.reviewByServer["gopls"][normalizeWaiterPath("/b.go")]
	if gotA.errors != 2 || gotA.warnings != 0 {
		t.Fatalf("review snapshot for a.go = %+v, want preserved 2E/0W", gotA)
	}
	if gotB.errors != 0 || gotB.warnings != 3 {
		t.Fatalf("review snapshot for b.go = %+v, want 0E/3W", gotB)
	}
}

func TestRecordReviewSnapshotClearsStaleDiagnosticsForCleanTouchedFile(t *testing.T) {
	mgr := NewManager(&config.Config{
		LSP: config.LSPConfig{
			"gopls": {
				Command:   "gopls",
				FileTypes: []string{".go"},
			},
		},
	}, t.TempDir(), nil)
	path := normalizeWaiterPath(filepath.Join(mgr.projectRoot, "a.go"))
	mgr.clients["gopls"] = &Client{}
	mgr.reviewByServer = map[string]map[string]reviewCounts{
		"gopls": {
			path: {errors: 1, warnings: 0},
		},
	}
	mgr.touchedPaths = map[string]struct{}{
		path: {},
	}

	mgr.recordReviewSnapshot(path)

	got := mgr.reviewByServer["gopls"][path]
	if got.errors != 0 || got.warnings != 0 {
		t.Fatalf("review snapshot after clean edit = %+v, want 0E/0W", got)
	}
	rows := mgr.SidebarEntries()
	if len(rows) != 1 {
		t.Fatalf("SidebarEntries() len = %d, want 1", len(rows))
	}
	if rows[0].Name != "gopls" || rows[0].Errors != 0 || rows[0].Warnings != 0 {
		t.Fatalf("gopls row = %+v, want clean diagnostics", rows[0])
	}
}

func TestCurrentReviewSnapshotsIncludesCleanConnectedServer(t *testing.T) {
	mgr := NewManager(&config.Config{
		LSP: config.LSPConfig{
			"gopls": {
				Command:   "gopls",
				FileTypes: []string{".go"},
			},
		},
	}, t.TempDir(), nil)
	path := filepath.Join(mgr.projectRoot, "a.go")
	mgr.clients["gopls"] = &Client{}

	got := mgr.CurrentReviewSnapshots(path)
	want := []message.LSPReview{{ServerID: "gopls", Errors: 0, Warnings: 0}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("CurrentReviewSnapshots() = %#v, want %#v", got, want)
	}
}

func TestRebuildTouchedPathsNormalizesAndSorts(t *testing.T) {
	root := t.TempDir()
	mgr := NewManager(&config.Config{}, root, nil)
	mgr.RebuildTouchedPaths([]string{"bar.go", filepath.Join(root, "foo.go")})
	got := mgr.TouchedPaths()
	want := []string{
		normalizeWaiterPath(filepath.Join(root, "bar.go")),
		normalizeWaiterPath(filepath.Join(root, "foo.go")),
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("TouchedPaths() = %#v, want %#v", got, want)
	}
}

func TestConfiguredServersSortsNamesAndFileTypesAndReturnsCopies(t *testing.T) {
	mgr := NewManager(&config.Config{
		LSP: config.LSPConfig{
			"zed": {
				Command:   "zed-lsp",
				FileTypes: []string{"tsx", ".ts", "Go", ""},
			},
			"alpha": {
				Command:   "alpha-lsp",
				FileTypes: []string{".py", "pyi"},
			},
			"disabled": {
				Command:   "off",
				Disabled:  true,
				FileTypes: []string{".txt"},
			},
		},
	}, t.TempDir(), nil)

	got := mgr.ConfiguredServers()
	want := []ConfiguredServerInfo{
		{Name: "alpha", FileTypes: []string{"*.py", "*.pyi"}},
		{Name: "zed", FileTypes: []string{"*.go", "*.ts", "*.tsx"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ConfiguredServers() = %#v, want %#v", got, want)
	}

	got[0].FileTypes[0] = "*.mutated"
	if again := mgr.ConfiguredServers(); !reflect.DeepEqual(again, want) {
		t.Fatalf("ConfiguredServers() should return copies, got %#v after mutation, want %#v", again, want)
	}
}
