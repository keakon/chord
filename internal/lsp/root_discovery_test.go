package lsp

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	pnprotocol "github.com/keakon/x/powernap/pkg/lsp/protocol"

	"github.com/keakon/chord/internal/config"
)

// TestDiscoverWorkspaceRootNestedPackage verifies that a file inside a nested
// frontend package resolves to the nearest ancestor holding a root marker, not
// to the git/project root. This is the fix for monorepos where TypeScript
// (or other server) dependencies live in a subdirectory such as frontend/.
func TestDiscoverWorkspaceRootNestedPackage(t *testing.T) {
	root := t.TempDir()
	frontend := filepath.Join(root, "frontend")
	src := filepath.Join(frontend, "src", "composables")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(frontend, "tsconfig.json"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(src, "apiBase.ts")

	m := &Manager{projectRoot: root}
	cfg := config.LSPServerConfig{
		RootMarkers: []string{"tsconfig.json", "jsconfig.json", "package.json", ".git"},
	}
	got, markerMatched, ok := m.discoverWorkspaceRoot("sample", cfg, target)
	if !ok {
		t.Fatal("expected a workspace root to be resolvable")
	}
	if !markerMatched {
		t.Fatal("expected the nested frontend marker to match")
	}
	if got != frontend {
		t.Fatalf("discoverWorkspaceRoot = %q, want nested frontend root %q", got, frontend)
	}
}

func TestDiscoverWorkspaceRootRootedProject(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "tsconfig.json"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "src", "app.ts")

	m := &Manager{projectRoot: root}
	cfg := config.LSPServerConfig{RootMarkers: []string{"tsconfig.json"}}
	got, markerMatched, ok := m.discoverWorkspaceRoot("sample", cfg, target)
	if !ok {
		t.Fatal("expected a workspace root to be resolvable")
	}
	if !markerMatched {
		t.Fatal("expected the project-root marker to match")
	}
	if got != root {
		t.Fatalf("discoverWorkspaceRoot = %q, want project root %q", got, root)
	}
}

func TestDiscoverWorkspaceRootNoMarkerFallsBackToProjectRoot(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(src, "app.go")

	m := &Manager{projectRoot: root}
	cfg := config.LSPServerConfig{RootMarkers: []string{"tsconfig.json", ".git"}}
	got, markerMatched, ok := m.discoverWorkspaceRoot("sample", cfg, target)
	if !ok {
		t.Fatal("expected a workspace root to be resolvable")
	}
	if markerMatched {
		t.Fatal("no configured marker exists, so markerMatched must be false")
	}
	if got != root {
		t.Fatalf("discoverWorkspaceRoot = %q, want project root fallback %q", got, root)
	}
}

// TestDiscoverWorkspaceRootStopsAtProjectRoot verifies discovery never walks
// above the Chord project root even when a marker exists higher up.
func TestDiscoverWorkspaceRootStopsAtProjectRoot(t *testing.T) {
	parent := t.TempDir()
	if err := os.WriteFile(filepath.Join(parent, ".git"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(parent, "workspace")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "file.ts")

	m := &Manager{projectRoot: root}
	cfg := config.LSPServerConfig{RootMarkers: []string{".git"}}
	got, markerMatched, ok := m.discoverWorkspaceRoot("sample", cfg, target)
	if !ok {
		t.Fatal("expected a workspace root to be resolvable")
	}
	if markerMatched {
		t.Fatal("marker above the project root must not match")
	}
	if got != root {
		t.Fatalf("discoverWorkspaceRoot walked above project root: got %q, want %q", got, root)
	}
}

// TestDiscoverWorkspaceRootStopsAtProjectRootForPathItself verifies discovery
// never walks above the project root when the path itself is the project
// root: the root directory must be the first candidate, even when it holds no
// marker and one exists higher up. Regression: the walk used to start at the
// root's parent for this input, so the root was never checked and a marker
// above the project root was picked as the workspace root.
func TestDiscoverWorkspaceRootStopsAtProjectRootForPathItself(t *testing.T) {
	parent := t.TempDir()
	if err := os.WriteFile(filepath.Join(parent, ".git"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(parent, "workspace")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}

	m := &Manager{projectRoot: root}
	cfg := config.LSPServerConfig{RootMarkers: []string{".git"}}
	got, markerMatched, ok := m.discoverWorkspaceRoot("sample", cfg, root)
	if !ok {
		t.Fatal("expected a workspace root to be resolvable")
	}
	if markerMatched {
		t.Fatal("marker above the project root must not match")
	}
	if got != root {
		t.Fatalf("discoverWorkspaceRoot = %q, want project root %q", got, root)
	}
}

// TestNestedRootsRouteToTheirOwnClient verifies that with one client per
// (server, root), a file inside a nested package resolves to that package's
// client instead of an arbitrary client for the same server.
func TestNestedRootsRouteToTheirOwnClient(t *testing.T) {
	root := t.TempDir()
	frontend := filepath.Join(root, "frontend")
	admin := filepath.Join(root, "admin-ui")
	for _, dir := range []string{frontend, admin} {
		if err := os.MkdirAll(filepath.Join(dir, "src"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "tsconfig.json"), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	m := NewManager(&config.Config{
		LSP: config.LSPConfig{
			"typescript": {
				Command:     "ts",
				FileTypes:   []string{".ts", ".tsx", ".js"},
				RootMarkers: []string{"tsconfig.json"},
			},
		},
	}, root, nil)
	feClient := existingTrivialClient(frontend, ".ts")
	admClient := existingTrivialClient(admin, ".ts")
	m.clientsMu.Lock()
	m.clients = map[clientKey]*Client{
		{name: "typescript", root: frontend}: feClient,
		{name: "typescript", root: admin}:    admClient,
	}
	m.clientsMu.Unlock()

	m.clientsMu.RLock()
	c1, ok1 := m.clientForPathLocked(filepath.Join(frontend, "src", "a.ts"))
	c2, ok2 := m.clientForPathLocked(filepath.Join(admin, "src", "b.ts"))
	m.clientsMu.RUnlock()

	if !ok1 || c1 != feClient {
		t.Fatalf("frontend file routed to wrong client: ok=%v client=%p want %p", ok1, c1, feClient)
	}
	if !ok2 || c2 != admClient {
		t.Fatalf("admin file routed to wrong client: ok=%v client=%p want %p", ok2, c2, admClient)
	}
}

// existingTrivialClient builds a Client that handles .ts paths under root
// without launching a real server process.
func existingTrivialClient(root string, fileTypes ...string) *Client {
	return &Client{
		cwd:       root,
		cfg:       config.LSPServerConfig{FileTypes: fileTypes},
		openFiles: map[string]int32{},
	}
}

// TestNotificationsGoOnlyToNearestRootClient is the regression test for two
// instances of one server both being notified about the same file. The
// repository-root instance also accepts files inside the nested package
// (HandlesFile only rejects paths outside a client's own root), so before
// ownership was resolved by nearest root, both received didChange and both
// published diagnostics for the same URI under the server name "typescript" —
// the second publish overwrote or cleared the first in diagByServer.
func TestNotificationsGoOnlyToNearestRootClient(t *testing.T) {
	root := t.TempDir()
	frontend := filepath.Join(root, "frontend")
	if err := os.MkdirAll(filepath.Join(frontend, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{root, frontend} {
		if err := os.WriteFile(filepath.Join(dir, "tsconfig.json"), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	m := NewManager(&config.Config{
		LSP: config.LSPConfig{
			"typescript": {
				Command:     "ts",
				FileTypes:   []string{".ts"},
				RootMarkers: []string{"tsconfig.json"},
			},
		},
	}, root, nil)
	rootFake, nestedFake := &fakePowernapClient{}, &fakePowernapClient{}
	rootClient := existingTrivialClient(root, ".ts")
	rootClient.client = rootFake
	nestedClient := existingTrivialClient(frontend, ".ts")
	nestedClient.client = nestedFake
	m.clientsMu.Lock()
	m.clients = map[clientKey]*Client{
		{name: "typescript", root: root}:     rootClient,
		{name: "typescript", root: frontend}: nestedClient,
	}
	m.clientsMu.Unlock()

	nestedPath := filepath.Join(frontend, "src", "a.ts")
	versions, err := m.DidChangeVersions(context.Background(), nestedPath, "export {}")
	if err != nil {
		t.Fatalf("DidChangeVersions() error = %v", err)
	}
	if len(versions) != 1 {
		t.Fatalf("versions = %v, want exactly one entry for the owning instance", versions)
	}
	if got := rootFake.syncedURIs(); len(got) != 0 {
		t.Fatalf("repository-root instance was told about a nested-package file: %v", got)
	}
	if got := nestedFake.syncedURIs(); len(got) != 1 {
		t.Fatalf("nested instance sync count = %d, want 1", len(got))
	}

	// A file the nested instance cannot serve still reaches the root instance.
	rootPath := filepath.Join(root, "b.ts")
	if _, err := m.DidChangeVersions(context.Background(), rootPath, "export {}"); err != nil {
		t.Fatalf("DidChangeVersions(root file) error = %v", err)
	}
	if got := rootFake.syncedURIs(); len(got) != 1 {
		t.Fatalf("repository-root instance sync count = %d, want 1", len(got))
	}
	if got := nestedFake.syncedURIs(); len(got) != 1 {
		t.Fatalf("nested instance was told about a file outside its root: %v", got)
	}
}

// TestStartDoesNotLaunchForUnmatchedFileType verifies Start does not start a
// server for a path whose file type the server does not cover. Regression:
// Start previously dropped the file-type filter, so reading any file (e.g. a
// .go file) in a repo containing a typescript entry started typescript and
// failed to find its TypeScript installation at the repo root.
func TestStartDoesNotLaunchForUnmatchedFileType(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".git"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	m := NewManager(&config.Config{
		LSP: config.LSPConfig{
			"typescript": {
				Command:     "chord-no-such-typescript-server",
				FileTypes:   []string{".ts", ".tsx", ".js", ".jsx"},
				RootMarkers: []string{".git"},
			},
		},
	}, root, nil)

	// A .go file must not start typescript at all: no attempt, no failure.
	m.Start(context.Background(), filepath.Join(root, "internal", "x.go"))
	m.startFailMu.Lock()
	_, goFailed := m.startFail[clientKey{name: "typescript", root: root}]
	m.startFailMu.Unlock()
	if goFailed {
		t.Fatal("typescript was started for a .go file; Start must filter by file_types")
	}

	// A .ts file legitimately starts typescript; with a nonexistent command
	// that surfaces as a recorded start failure, proving Start did try.
	tsKey := clientKey{name: "typescript", root: root}
	m.Start(context.Background(), filepath.Join(root, "internal", "x.ts"))
	waitForLSPStartDone(t, m, tsKey)
	m.startFailMu.Lock()
	_, failed := m.startFail[tsKey]
	m.startFailMu.Unlock()
	if !failed {
		t.Fatal("typescript was not started for a .ts file (no start attempt recorded)")
	}
}

// waitForLSPStartDone waits until the async Start goroutine for key has
// finished (the fast-fail path of NewClient clears m.starting).
func waitForLSPStartDone(t *testing.T, m *Manager, key clientKey) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		m.clientsMu.RLock()
		_, starting := m.starting[key]
		m.clientsMu.RUnlock()
		if !starting {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("LSP start attempt for %+v did not finish", key)
}

// TestHasPendingStartForPathLockedFiltersFileTypes verifies the pending-start
// check only considers servers whose file types cover the path. Regression
// guard for hasPendingStartForPathLocked losing its file-type filter.
func TestHasPendingStartForPathLockedFiltersFileTypes(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".git"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	m := NewManager(&config.Config{
		LSP: config.LSPConfig{
			"typescript": {
				Command:     "tsserver",
				FileTypes:   []string{".ts"},
				RootMarkers: []string{".git"},
			},
		},
	}, root, nil)
	m.starting[clientKey{name: "typescript", root: root}] = true

	m.clientsMu.RLock()
	defer m.clientsMu.RUnlock()
	if m.hasPendingStartForPathLocked(filepath.Join(root, "main.go")) {
		t.Fatal("pending-start reported for a .go path that typescript does not cover")
	}
	if !m.hasPendingStartForPathLocked(filepath.Join(root, "main.ts")) {
		t.Fatal("pending-start not reported for a .ts path typescript covers")
	}
}

// TestEvictExcessClientsReclaimsLeastRecentlyUsed verifies the per-server
// instance cap: when a server holds more than maxClientsPerServer live roots,
// the least-recently-used instance is evicted and its workspace root is no
// longer reported as handled, so a monorepo sweep cannot leak an unbounded
// number of language-server processes.
func TestEvictExcessClientsReclaimsLeastRecentlyUsed(t *testing.T) {
	root := t.TempDir()
	m := NewManager(&config.Config{
		LSP: config.LSPConfig{
			"typescript": {
				Command:   "tsserver",
				FileTypes: []string{".ts"},
			},
		},
	}, root, nil)

	m.clientsMu.Lock()
	for i := 0; i <= maxClientsPerServer; i++ { // one over the cap
		child := filepath.Join(root, fmt.Sprintf("pkg%d", i))
		if err := os.MkdirAll(child, 0o755); err != nil {
			t.Fatal(err)
		}
		c := existingTrivialClient(child, ".ts")
		c.touch(int64(i)) // ascending so pkg0 is the LRU
		m.clients[clientKey{name: "typescript", root: child}] = c
	}
	evicted, _ := m.evictExcessClientsLocked()
	m.clientsMu.Unlock()

	if len(evicted) != 1 {
		t.Fatalf("evicted %d clients, want 1", len(evicted))
	}
	// pkg0 has the smallest lastUsed, so it must be the evicted root.
	if evicted[0].cwd != filepath.Join(root, "pkg0") {
		t.Fatalf("evicted root = %q, want %q", evicted[0].cwd, filepath.Join(root, "pkg0"))
	}
	m.clientsMu.RLock()
	defer m.clientsMu.RUnlock()
	if _, ok := m.clients[clientKey{name: "typescript", root: filepath.Join(root, "pkg0")}]; ok {
		t.Fatal("least-recently-used instance still present after eviction")
	}
	if len(m.clients) != maxClientsPerServer {
		t.Fatalf("live clients = %d, want %d", len(m.clients), maxClientsPerServer)
	}
}

// TestEvictExcessClientsSkipsWithinLimit verifies the cap only triggers when a
// server actually exceeds maxClientsPerServer; a server at the limit keeps all
// instances.
func TestEvictExcessClientsSkipsWithinLimit(t *testing.T) {
	root := t.TempDir()
	m := NewManager(&config.Config{}, root, nil)

	m.clientsMu.Lock()
	for i := range maxClientsPerServer {
		child := filepath.Join(root, fmt.Sprintf("pkg%d", i))
		c := existingTrivialClient(child, ".ts")
		c.touch(int64(i))
		m.clients[clientKey{name: "typescript", root: child}] = c
	}
	evicted, _ := m.evictExcessClientsLocked()
	m.clientsMu.Unlock()
	if len(evicted) != 0 {
		t.Fatalf("evicted %d clients within limit, want 0", len(evicted))
	}
}

// TestEvictExcessClientsClearsOrphanedDiagnostics verifies that evicting an
// instance also drops the diagnostics it published. diagByServer is keyed by
// server name, so an entry left behind by a closed instance would linger in the
// sidebar and in tool output with no client remaining that could ever clear it.
// Entries for roots a surviving instance still handles must stay.
func TestEvictExcessClientsClearsOrphanedDiagnostics(t *testing.T) {
	root := t.TempDir()
	m := NewManager(&config.Config{}, root, nil)

	m.clientsMu.Lock()
	for i := 0; i <= maxClientsPerServer; i++ { // one over the cap
		child := filepath.Join(root, fmt.Sprintf("pkg%d", i))
		if err := os.MkdirAll(child, 0o755); err != nil {
			t.Fatal(err)
		}
		c := existingTrivialClient(child, ".ts")
		c.touch(int64(i + 1)) // ascending so pkg0 is the LRU
		m.clients[clientKey{name: "typescript", root: child}] = c
	}
	orphanPath := filepath.Join(root, "pkg0", "a.ts")
	keptPath := filepath.Join(root, "pkg1", "b.ts")
	orphanURI := string(pnprotocol.URIFromPath(orphanPath))
	keptURI := string(pnprotocol.URIFromPath(keptPath))
	m.diagMu.Lock()
	m.diagByServer[clientKey{name: "typescript", root: filepath.Join(root, "pkg0")}] = map[string]diagCounts{
		orphanURI: {errors: 1},
	}
	m.diagByServer[clientKey{name: "typescript", root: filepath.Join(root, "pkg1")}] = map[string]diagCounts{
		keptURI: {warnings: 2},
	}
	m.reviewByServer["typescript"] = map[string]reviewCounts{
		normalizeWaiterPath(orphanPath): {errors: 1},
	}
	m.diagMu.Unlock()

	_, survivors := m.evictExcessClientsLocked()
	m.clientsMu.Unlock()
	cleared := m.dropOrphanedDiagnostics(survivors)

	if len(cleared) != 1 || cleared[0].uri != orphanURI || cleared[0].server != "typescript" {
		t.Fatalf("cleared = %+v, want one entry for %q", cleared, orphanURI)
	}
	m.diagMu.Lock()
	defer m.diagMu.Unlock()
	if _, ok := m.diagByServer[clientKey{name: "typescript", root: filepath.Join(root, "pkg0")}][orphanURI]; ok {
		t.Fatal("diagnostics of the evicted instance survived eviction")
	}
	if _, ok := m.diagByServer[clientKey{name: "typescript", root: filepath.Join(root, "pkg1")}][keptURI]; !ok {
		t.Fatal("diagnostics of a surviving instance were dropped")
	}
	if _, ok := m.reviewByServer["typescript"][normalizeWaiterPath(orphanPath)]; ok {
		t.Fatal("review counts of the evicted instance survived eviction")
	}
	// The server still has diagnostics for the surviving instance, so the review
	// map empties out while diagByServer does not. ResetReviews reads
	// len(reviewByServer) to decide whether the sidebar needs a refresh, so an
	// emptied entry must be removed rather than left behind as an empty map.
	if byPath, ok := m.reviewByServer["typescript"]; ok && len(byPath) == 0 {
		t.Fatal("an emptied review map was left behind; ResetReviews will report a change with nothing to clear")
	}
}

func TestFailMessageChoosesLexicographicallyFirstRootEvenWithEmptyRoot(t *testing.T) {
	failMap := map[clientKey]string{
		{name: "typescript", root: ""}:           "rootless failure",
		{name: "typescript", root: "/workspace"}: "workspace failure",
		{name: "gopls", root: "/workspace"}:      "other server failure",
	}
	if got := failMessage(failMap, "typescript"); got != "rootless failure" {
		t.Fatalf("failMessage() = %q, want %q", got, "rootless failure")
	}
}

// TestServerRootForPathFallsBackWhenMarkerAbsent is the end-to-end coverage
// decision behind root_markers: a file_types-matching file outside any marker
// directory still resolves to the project root instead of being rejected. The
// contrast with discoverWorkspaceRoot's own fallback test makes the rejection
// gate in serverRootForPath (the only place that could drop it) observable.
func TestServerRootForPathFallsBackWhenMarkerAbsent(t *testing.T) {
	root := t.TempDir()
	m := &Manager{projectRoot: root}
	cfg := config.LSPServerConfig{FileTypes: []string{".ts"}, RootMarkers: []string{"tsconfig.json"}}
	gotRoot, ok := m.serverRootForPath("sample", cfg, filepath.Join(root, "src", "app.ts"))
	if !ok {
		t.Fatal("serverRootForPath rejected a file outside any marker directory")
	}
	if gotRoot != root {
		t.Fatalf("serverRootForPath = %q, want project-root fallback %q", gotRoot, root)
	}
}

// TestStartFallsBackToProjectRootWhenNoMarkerMatches verifies Start actually
// launches a client rooted at the project root when no root marker matches,
// exercising the full Start -> serverRootForPath -> discoverWorkspaceRoot
// path rather than only the internal discovery function.
func TestStartFallsBackToProjectRootWhenNoMarkerMatches(t *testing.T) {
	root := t.TempDir()
	markerDir := filepath.Join(root, "nested")
	if err := os.MkdirAll(markerDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(markerDir, "tsconfig.json"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	m := NewManager(&config.Config{
		LSP: config.LSPConfig{
			"typescript": {
				Command:     "chord-no-such-typescript-server",
				FileTypes:   []string{".ts", ".tsx"},
				RootMarkers: []string{"tsconfig.json"},
			},
		},
	}, root, nil)

	// src/app.ts lives where no marker exists: discovery falls back to the
	// project root, and Start must still launch a client rooted there.
	target := filepath.Join(root, "src", "app.ts")
	key := clientKey{name: "typescript", root: root}
	m.Start(context.Background(), target)
	waitForLSPStartDone(t, m, key)
	m.startFailMu.Lock()
	_, failed := m.startFail[key]
	m.startFailMu.Unlock()
	if !failed {
		t.Fatalf("typescript was not started for %s (no fallback to project root)", target)
	}
}

func TestEffectiveRootMarkersForCommonServers(t *testing.T) {
	pythonMarkers := []string{"pyrightconfig.json", "pyproject.toml", "requirements.txt"}
	typescriptMarkers := []string{"tsconfig.json", "jsconfig.json", "package.json"}
	for _, test := range []struct {
		name    string
		server  string
		command string
		markers []string
		want    []string
	}{
		{name: "pyright command", command: "pyright-langserver", want: pythonMarkers},
		{name: "basedpyright command", command: "basedpyright-langserver", want: pythonMarkers},
		{name: "typescript command", command: "typescript-language-server", want: typescriptMarkers},
		{name: "command path", command: filepath.Join("bin", "pyright-langserver"), want: pythonMarkers},
		{name: "windows command", command: "PYRIGHT-LANGSERVER.CMD", want: pythonMarkers},
		{name: "python wrapper", server: "pyright", command: "python-wrapper", want: pythonMarkers},
		{name: "typescript wrapper", server: "typescript", command: "tsserver-wrapper", want: typescriptMarkers},
		{name: "unknown", command: "gopls"},
		{name: "substring", command: "not-pyright-langserver"},
		{name: "unknown wrapper", command: "typescript-wrapper"},
		{name: "override", server: "pyright", command: "pyright-langserver", markers: []string{"custom.toml"}, want: []string{"custom.toml"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg := config.LSPServerConfig{Command: test.command, RootMarkers: test.markers}
			if got := effectiveRootMarkers(test.server, cfg); !slices.Equal(got, test.want) {
				t.Fatalf("effectiveRootMarkers() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestDiscoverWorkspaceRootIgnoresOtherLanguageMarkers(t *testing.T) {
	for _, test := range []struct {
		server        string
		command       string
		marker        string
		foreignMarker string
		filename      string
	}{
		{server: "pyright", command: "python-wrapper", marker: "pyproject.toml", foreignMarker: "package.json", filename: "app.py"},
		{server: "typescript", command: "tsserver-wrapper", marker: "tsconfig.json", foreignMarker: "requirements.txt", filename: "app.ts"},
	} {
		t.Run(test.server, func(t *testing.T) {
			root := t.TempDir()
			project := filepath.Join(root, "package")
			nested := filepath.Join(project, "nested")
			if err := os.MkdirAll(nested, 0o755); err != nil {
				t.Fatal(err)
			}
			for _, path := range []string{filepath.Join(root, test.marker), filepath.Join(project, test.marker), filepath.Join(nested, test.foreignMarker)} {
				if err := os.WriteFile(path, nil, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			manager := &Manager{projectRoot: root}
			cfg := config.LSPServerConfig{Command: test.command}
			rootForFile, matched, ok := manager.discoverWorkspaceRoot(test.server, cfg, filepath.Join(nested, test.filename))
			if !ok || !matched || rootForFile != project {
				t.Fatalf("discoverWorkspaceRoot() = (%q, %t, %t), want (%q, true, true)", rootForFile, matched, ok, project)
			}
		})
	}
}
