package lsp

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/keakon/x/powernap/pkg/lsp/protocol"

	"github.com/keakon/chord/internal/config"
)

func TestExternalWorkspaceDiscovery(t *testing.T) {
	base := resolvedTempDir(t)
	manager := NewManager(&config.Config{}, base, nil)
	for _, gitMarker := range []string{"directory", "file", "none"} {
		t.Run(gitMarker, func(t *testing.T) {
			root := resolvedTempDir(t)
			if gitMarker == "directory" {
				if err := os.Mkdir(filepath.Join(root, ".git"), 0755); err != nil {
					t.Fatal(err)
				}
			} else if gitMarker == "file" {
				if err := os.WriteFile(filepath.Join(root, ".git"), []byte("gitdir: /sample/git/worktrees/task\n"), 0644); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/sample\n"), 0644); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(root, "pkg", "main.go")
			got, matched, ok := manager.discoverWorkspaceRoot("gopls", config.LSPServerConfig{}, path)
			if !ok || !matched || got != root {
				t.Fatalf("root = (%q, %v, %v), want %q", got, matched, ok, root)
			}
			if manager.projectRootPath() != base {
				t.Fatal("external discovery changed the default directory")
			}
		})
	}
}

func TestExternalCheckoutBoundsMarkersAndPythonEnvironment(t *testing.T) {
	parent := resolvedTempDir(t)
	root := filepath.Join(parent, "checkout")
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(parent, "pyproject.toml"), nil, 0644); err != nil {
		t.Fatal(err)
	}
	manager := NewManager(&config.Config{}, resolvedTempDir(t), nil)
	got, matched, ok := manager.discoverWorkspaceRoot("pyright", config.LSPServerConfig{}, filepath.Join(root, "app.py"))
	if !ok || matched || got != root {
		t.Fatalf("discovery escaped checkout: (%q, %v, %v)", got, matched, ok)
	}
	if got := manager.settingsBoundary(root); got != root {
		t.Fatalf("environment boundary = %q", got)
	}
}

func TestExternalUnrootedFileIsSilentlySkipped(t *testing.T) {
	manager := NewManager(&config.Config{LSP: config.LSPConfig{"sample": {Command: "missing-server", FileTypes: []string{"sample"}}}}, resolvedTempDir(t), nil)
	path := filepath.Join(resolvedTempDir(t), "file.sample")
	out := manager.AfterFileWriteToolResult(context.Background(), path, "text", "written", false, WatchedFileCreated, "")
	if out != "written" {
		t.Fatalf("unexpected service note: %q", out)
	}
	if len(manager.starting) != 0 || len(manager.clients) != 0 {
		t.Fatal("started a server without a project root")
	}
}

func TestWorkspaceAliasesShareRoot(t *testing.T) {
	root := resolvedTempDir(t)
	if err := os.WriteFile(filepath.Join(root, "go.mod"), nil, 0644); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(resolvedTempDir(t), "alias")
	if err := os.Symlink(root, alias); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	manager := NewManager(&config.Config{}, resolvedTempDir(t), nil)
	for _, path := range []string{filepath.Join(root, "main.go"), filepath.Join(alias, "main.go")} {
		got, _, ok := manager.discoverWorkspaceRoot("gopls", config.LSPServerConfig{}, path)
		if !ok || got != root {
			t.Fatalf("root for %s = %q", path, got)
		}
	}
}

func TestRetiredClientCannotPublishDiagnostics(t *testing.T) {
	root := resolvedTempDir(t)
	manager := NewManager(&config.Config{}, root, nil)
	key := clientKey{name: "sample", root: root}
	old, current := &Client{cwd: root}, &Client{cwd: root}
	manager.clients[key] = current
	path := filepath.Join(root, "main.go")
	if err := os.WriteFile(path, []byte("package sample"), 0600); err != nil {
		t.Fatal(err)
	}
	current.openFiles = map[string]int32{path: 1}
	uri := string(protocol.URIFromPath(path))
	publish := manager.onDiagnostics(key, old)
	publish(uri, path, nil, "", []protocol.Diagnostic{{Severity: 1, Message: "stale"}}, 1)
	if len(manager.diagByServer) != 0 {
		t.Fatal("retired client restored diagnostics")
	}
	manager.onDiagnostics(key, current)(uri, path, nil, "", []protocol.Diagnostic{{Severity: 1, Message: "current"}}, 1)
	if manager.diagByServer[key][uri].errors != 1 {
		t.Fatal("current client was not allowed to publish")
	}
}

func TestWorkspaceDiagnosticsAndFailuresStaySeparate(t *testing.T) {
	a, b := resolvedTempDir(t), resolvedTempDir(t)
	manager := NewManager(&config.Config{LSP: config.LSPConfig{"gopls": {FileTypes: []string{"go"}}}}, a, nil)
	for _, root := range []string{a, b} {
		if err := os.WriteFile(filepath.Join(root, "go.mod"), nil, 0644); err != nil {
			t.Fatal(err)
		}
		file := filepath.Join(root, "other.go")
		if err := os.WriteFile(file, []byte("package sample\n"), 0644); err != nil {
			t.Fatal(err)
		}
		key := clientKey{name: "gopls", root: root}
		client := &Client{cwd: root, diagnostics: map[protocol.DocumentURI][]protocol.Diagnostic{}}
		manager.clientsMu.Lock()
		manager.clients[key] = client
		manager.clientsMu.Unlock()
		manager.MarkTouched(file)
		publishTestDiagnostics(manager, key, protocol.URIFromPath(file), client, 1, testProtocolDiagnostic(root+" error", 0))
		manager.recordReviewSnapshot(file)
	}
	out := manager.AppendLSPDiagnosticsToToolOutput("written", filepath.Join(a, "main.go"), true, a)
	if !strings.Contains(out, a+" error") || strings.Contains(out, b+" error") {
		t.Fatalf("cross-workspace diagnostics: %s", out)
	}
	rows := manager.SidebarEntries()
	if len(rows) != 2 || rows[0].Errors != 1 || rows[1].Errors != 1 || rows[0].Root == rows[1].Root {
		t.Fatalf("rows = %+v", rows)
	}
	for _, root := range []string{a, b} {
		path := filepath.Join(root, "main.go")
		if manager.noteLSPDegradationForPath(path, lspDegradationStart, "gopls", "failed") == "" {
			t.Fatal("another workspace suppressed this failure")
		}
		if manager.noteLSPDegradationForPath(path, lspDegradationStart, "gopls", "failed") != "" {
			t.Fatal("repeated failure was not deduplicated")
		}
	}
}

func TestExternalCheckoutsRealGopls(t *testing.T) {
	// Each test owns its workspace and server processes.
	t.Parallel()
	gopls := requireRealGopls(t)
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	base := resolvedTempDir(t)
	first := writeBrokenGoCheckout(t, base, "example.com/sample", "missingFirst")
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", base}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q")
	// The checkout is created here rather than by testgit, so it must opt out of
	// the developer's global core.hooksPath itself; a hook that needs a review
	// remote fails every commit made inside it.
	git("config", "core.hooksPath", "")
	git("add", "go.mod", "main.go")
	git("-c", "user.name=Test", "-c", "user.email=test@example.invalid", "-c", "commit.gpgsign=false", "commit", "-qm", "initial")
	linked := filepath.Join(resolvedTempDir(t), "linked")
	git("worktree", "add", "--detach", linked, "HEAD")
	second := writeBrokenGoCheckout(t, linked, "example.com/sample", "missingSecond")
	foreign := resolvedTempDir(t)
	third := writeBrokenGoCheckout(t, foreign, "example.com/other", "missingThird")
	manager := NewManager(&config.Config{LSP: config.LSPConfig{"gopls": {Command: gopls, FileTypes: []string{"go"}}}}, base, nil)
	defer stopManager(manager)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	for _, item := range []struct{ path, symbol string }{{first, "missingFirst"}, {second, "missingSecond"}, {third, "missingThird"}} {
		content, err := os.ReadFile(item.path)
		if err != nil {
			t.Fatal(err)
		}
		manager.MarkTouched(item.path)
		out := manager.AfterFileWriteToolResult(ctx, item.path, string(content), "written", true, WatchedFileChanged, base)
		if !strings.Contains(out, item.symbol) {
			t.Fatalf("write %s did not report its error: %s", item.path, out)
		}
		for _, other := range []string{"missingFirst", "missingSecond", "missingThird"} {
			if other != item.symbol && strings.Contains(out, other) {
				t.Fatalf("foreign diagnostic in %s: %s", item.path, out)
			}
		}
	}
	manager.clientsMu.RLock()
	count := len(manager.clients)
	manager.clientsMu.RUnlock()
	if count != 3 || manager.projectRootPath() != base {
		t.Fatalf("clients=%d default=%s", count, manager.projectRootPath())
	}
	if err := manager.RebindToProjectRoot(ctx, foreign); err != nil {
		t.Fatal(err)
	}
	if !hasDiagnosticMessage(manager.Diagnostics(first), "missingFirst") {
		t.Fatal("switch discarded another checkout's diagnostics")
	}
	git("worktree", "remove", "--force", linked)
	manager.pruneMissingRoots(ctx)
	manager.clientsMu.RLock()
	count = len(manager.clients)
	manager.clientsMu.RUnlock()
	if count != 2 || !hasDiagnosticMessage(manager.Diagnostics(third), "missingThird") {
		t.Fatalf("removal affected other instances: %d", count)
	}
}

func TestNestedCheckoutIsNotOwnedByParentClient(t *testing.T) {
	root := resolvedTempDir(t)
	nested := filepath.Join(root, "nested")
	if err := os.MkdirAll(filepath.Join(nested, ".git"), 0755); err != nil {
		t.Fatal(err)
	}
	manager := NewManager(&config.Config{}, root, nil)
	parentKey := clientKey{name: "sample", root: root}
	parent := &Client{cwd: root}
	manager.clients[parentKey] = parent
	path := filepath.Join(nested, "main.go")
	if _, ok := manager.clientForPathLocked(path); ok {
		t.Fatal("parent client accepted a separate checkout")
	}
	nestedKey := clientKey{name: "sample", root: nested}
	child := &Client{cwd: nested}
	manager.clients[nestedKey] = child
	if got, ok := manager.clientForPathLocked(path); !ok || got != child {
		t.Fatal("nested checkout was not routed to its own client")
	}
	manager.onDiagnostics(parentKey, parent)(string(protocol.URIFromPath(path)), path, nil, "", []protocol.Diagnostic{{Severity: 1, Message: "parent"}}, 1)
	if len(manager.diagByServer) != 0 {
		t.Fatal("parent server published a separate checkout diagnostic")
	}
}

func TestWorkspaceBoundaryCacheRefreshes(t *testing.T) {
	root := resolvedTempDir(t)
	manager := NewManager(&config.Config{}, root, nil)
	nested := filepath.Join(root, "nested")
	if err := os.Mkdir(nested, 0755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(nested, "main.go")
	if got := manager.workspaceBoundary(path); got != root {
		t.Fatalf("initial boundary = %q", got)
	}
	if err := os.Mkdir(filepath.Join(nested, ".git"), 0755); err != nil {
		t.Fatal(err)
	}
	manager.boundaryMu.Lock()
	for key, entry := range manager.boundaries {
		entry.expires = time.Time{}
		manager.boundaries[key] = entry
	}
	manager.boundaryMu.Unlock()
	if got := manager.workspaceBoundary(path); got != nested {
		t.Fatalf("refreshed boundary = %q", got)
	}
}
