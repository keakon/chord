package lsp

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/keakon/x/powernap/pkg/lsp/protocol"
	powertransport "github.com/keakon/x/powernap/pkg/transport"

	"github.com/keakon/chord/internal/config"
)

type fakePowernapClient struct {
	shutdownErr          error
	exitErr              error
	definitionResult     *protocol.Or_Result_textDocument_definition
	definitionErr        error
	implementationResult *protocol.Or_Result_textDocument_implementation
	implementationErr    error
	shutdowns            int
	exits                int
	kills                int
	didCloseURIs         []string
	didChangeURIs        []string
	didOpenURIs          []string
	watchedFileEvents    []protocol.FileEvent
	registeredHandlers   map[string]powertransport.Handler
	registeredNotifies   map[string]powertransport.NotificationHandler
	configNotifications  []any
	initializeHook       func(*fakePowernapClient) error
}

func (f *fakePowernapClient) Initialize(context.Context, bool) error {
	if f.initializeHook != nil {
		return f.initializeHook(f)
	}
	return nil
}
func (f *fakePowernapClient) Shutdown(ctx context.Context) error { f.shutdowns++; return f.shutdownErr }
func (f *fakePowernapClient) Exit() error                        { f.exits++; return f.exitErr }
func (f *fakePowernapClient) Kill()                              { f.kills++ }
func (f *fakePowernapClient) IsRunning() bool                    { return true }
func (f *fakePowernapClient) RegisterNotificationHandler(method string, handler powertransport.NotificationHandler) {
	if f.registeredNotifies == nil {
		f.registeredNotifies = make(map[string]powertransport.NotificationHandler)
	}
	f.registeredNotifies[method] = handler
}
func (f *fakePowernapClient) RegisterHandler(method string, handler powertransport.Handler) {
	if f.registeredHandlers == nil {
		f.registeredHandlers = make(map[string]powertransport.Handler)
	}
	f.registeredHandlers[method] = handler
}
func (f *fakePowernapClient) NotifyDidOpenTextDocument(_ context.Context, uri string, _ string, _ int, _ string) error {
	f.didOpenURIs = append(f.didOpenURIs, uri)
	return nil
}

// syncedURIs are the documents this server was told about, whether the client
// sent didOpen or didChange for them.
func (f *fakePowernapClient) syncedURIs() []string {
	return append(append([]string(nil), f.didOpenURIs...), f.didChangeURIs...)
}
func (f *fakePowernapClient) NotifyDidChangeTextDocument(_ context.Context, uri string, _ int, _ []protocol.TextDocumentContentChangeEvent) error {
	f.didChangeURIs = append(f.didChangeURIs, uri)
	return nil
}
func (f *fakePowernapClient) NotifyDidCloseTextDocument(_ context.Context, uri string) error {
	f.didCloseURIs = append(f.didCloseURIs, uri)
	return nil
}
func (f *fakePowernapClient) NotifyDidChangeWatchedFiles(_ context.Context, changes []protocol.FileEvent) error {
	f.watchedFileEvents = append(f.watchedFileEvents, changes...)
	return nil
}
func (f *fakePowernapClient) NotifyWorkspaceDidChangeConfiguration(_ context.Context, settings any) error {
	f.configNotifications = append(f.configNotifications, settings)
	return nil
}
func (f *fakePowernapClient) RequestHover(context.Context, string, protocol.Position) (*protocol.Hover, error) {
	return nil, nil
}
func (f *fakePowernapClient) RequestDefinition(context.Context, string, protocol.Position) (*protocol.Or_Result_textDocument_definition, error) {
	return f.definitionResult, f.definitionErr
}
func (f *fakePowernapClient) RequestImplementation(context.Context, string, protocol.Position) (*protocol.Or_Result_textDocument_implementation, error) {
	return f.implementationResult, f.implementationErr
}
func (f *fakePowernapClient) FindReferences(context.Context, string, int, int, bool) ([]protocol.Location, error) {
	return nil, nil
}

func TestClientInitializeRegistersHandlersBeforeInitializeAndSyncsWorkspaceConfig(t *testing.T) {
	fake := &fakePowernapClient{
		initializeHook: func(f *fakePowernapClient) error {
			if _, ok := f.registeredHandlers["workspace/configuration"]; !ok {
				t.Fatal("workspace/configuration handler should be registered before Initialize")
			}
			if _, ok := f.registeredNotifies["textDocument/publishDiagnostics"]; !ok {
				t.Fatal("publishDiagnostics handler should be registered before Initialize")
			}
			return nil
		},
	}
	options := map[string]any{"python": map[string]any{"analysis": map[string]any{"typeCheckingMode": "strict"}}}
	c := &Client{
		client: fake,
		cfg:    config.LSPServerConfig{Options: options},
	}

	if err := c.Initialize(context.Background()); err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}
	if len(fake.configNotifications) != 1 {
		t.Fatalf("config notification count = %d, want 1", len(fake.configNotifications))
	}
	if !reflect.DeepEqual(fake.configNotifications[0], options) {
		t.Fatalf("config notification = %#v, want %#v", fake.configNotifications[0], options)
	}

	handler := fake.registeredHandlers["workspace/configuration"]
	params, err := json.Marshal(protocol.ConfigurationParams{
		Items: []protocol.ConfigurationItem{{Section: "python"}, {Section: "python.analysis"}, {Section: "missing"}},
	})
	if err != nil {
		t.Fatalf("Marshal(ConfigurationParams): %v", err)
	}
	got, err := handler(context.Background(), "", params)
	if err != nil {
		t.Fatalf("workspace/configuration handler error = %v", err)
	}
	want := []any{
		map[string]any{"analysis": map[string]any{"typeCheckingMode": "strict"}},
		map[string]any{"typeCheckingMode": "strict"},
		map[string]any{},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("workspace/configuration result = %#v, want %#v", got, want)
	}
}

func TestPrepareWorkspaceSettingsDiscoversPyrightUnixVirtualenv(t *testing.T) {
	root := t.TempDir()
	pythonPath := filepath.Join(root, ".venv", "bin", "python")
	if err := os.MkdirAll(filepath.Dir(pythonPath), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(pythonPath, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	got := discoverPythonInterpreterBoundedForGOOS(root, root, "linux")
	if got != pythonPath {
		t.Fatalf("discoverPythonInterpreterBoundedForGOOS() = %q, want %q", got, pythonPath)
	}
}

func TestPrepareWorkspaceSettingsDoesNotDiscoverWindowsVirtualenvOnUnix(t *testing.T) {
	root := t.TempDir()
	pythonPath := filepath.Join(root, ".venv", "Scripts", "python.exe")
	if err := os.MkdirAll(filepath.Dir(pythonPath), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(pythonPath, []byte("MZ\x90\x00"), 0o755); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	got := discoverPythonInterpreterBoundedForGOOS(root, root, "linux")
	if got != "" {
		t.Fatalf("discoverPythonInterpreterBoundedForGOOS() = %q, want empty", got)
	}
}

func TestPrepareWorkspaceSettingsDiscoversPyrightWindowsVirtualenv(t *testing.T) {
	root := t.TempDir()
	pythonPath := filepath.Join(root, ".venv", "Scripts", "python.exe")
	if err := os.MkdirAll(filepath.Dir(pythonPath), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(pythonPath, []byte("MZ\x90\x00"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	got := discoverPythonInterpreterBoundedForGOOS(root, root, "windows")
	if got != pythonPath {
		t.Fatalf("discoverPythonInterpreterBoundedForGOOS() = %q, want %q", got, pythonPath)
	}
}

func TestPrepareWorkspaceSettingsUsesDiscoveredPyrightVirtualenv(t *testing.T) {
	root := t.TempDir()
	pythonPath := filepath.Join(root, ".venv", "bin", "python")
	if err := os.MkdirAll(filepath.Dir(pythonPath), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(pythonPath, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	got := prepareWorkspaceSettingsBounded("pyright", config.LSPServerConfig{Command: "pyright-langserver"}, root, root)
	want := map[string]any{"python": map[string]any{"pythonPath": pythonPath}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("prepareWorkspaceSettingsBounded() = %#v, want %#v", got, want)
	}
}

func TestPrepareWorkspaceSettingsKeepsExplicitInterpreter(t *testing.T) {
	root := t.TempDir()
	pythonPath := filepath.Join(root, ".venv", "bin", "python")
	if err := os.MkdirAll(filepath.Dir(pythonPath), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(pythonPath, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	explicit := map[string]any{"python": map[string]any{"pythonPath": "/custom/python"}}
	got := prepareWorkspaceSettingsBounded("pyright", config.LSPServerConfig{Command: "pyright-langserver", Options: explicit}, root, root)
	if !reflect.DeepEqual(got, explicit) {
		t.Fatalf("prepareWorkspaceSettingsBounded() = %#v, want %#v", got, explicit)
	}
}

func TestPrepareWorkspaceSettingsMakesExplicitRelativeInterpreterAbsolute(t *testing.T) {
	root := t.TempDir()
	explicit := map[string]any{"python": map[string]any{"pythonPath": ".venv/bin/python"}}
	got := prepareWorkspaceSettingsBounded("pyright", config.LSPServerConfig{Command: "pyright-langserver", Options: explicit}, root, root)
	want := map[string]any{"python": map[string]any{"pythonPath": filepath.Join(root, ".venv", "bin", "python")}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("prepareWorkspaceSettingsBounded() = %#v, want %#v", got, want)
	}
}

func TestPrepareWorkspaceSettingsMakesExplicitRelativeVenvPathAbsolute(t *testing.T) {
	root := t.TempDir()
	explicit := map[string]any{"python": map[string]any{"venvPath": ".venvs", "venv": "py311"}}
	got := prepareWorkspaceSettingsBounded("pyright", config.LSPServerConfig{Command: "pyright-langserver", Options: explicit}, root, root)
	want := map[string]any{"python": map[string]any{"venvPath": filepath.Join(root, ".venvs"), "venv": "py311"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("prepareWorkspaceSettingsBounded() = %#v, want %#v", got, want)
	}
}

func TestCloneSettingsDeepCopiesSlices(t *testing.T) {
	original := map[string]any{
		"python": map[string]any{
			"analysis": map[string]any{
				"extraPaths": []any{"src", "lib"},
			},
		},
	}
	cloned := cloneSettings(original)
	nestedClone := cloned["python"].(map[string]any)["analysis"].(map[string]any)["extraPaths"].([]any)
	nestedClone[0] = "mutated"
	nestedOriginal := original["python"].(map[string]any)["analysis"].(map[string]any)["extraPaths"].([]any)
	if nestedOriginal[0] != "src" {
		t.Fatalf("cloneSettings slice mutation leaked to original: %#v", nestedOriginal)
	}
}

func TestClientDidCloseClosesTrackedFileAndClearsVersion(t *testing.T) {
	fake := &fakePowernapClient{}
	c := &Client{client: fake, openFiles: map[string]int32{"/tmp/main.go": 2}}
	if err := c.DidClose(context.Background(), "/tmp/main.go"); err != nil {
		t.Fatalf("DidClose() error = %v", err)
	}
	if len(fake.didCloseURIs) != 1 {
		t.Fatalf("didClose count = %d, want 1", len(fake.didCloseURIs))
	}
	if _, ok := c.openFiles["/tmp/main.go"]; ok {
		t.Fatal("expected open file entry removed")
	}
}

func TestClientDidCloseNoOpWhenFileNotOpen(t *testing.T) {
	fake := &fakePowernapClient{}
	c := &Client{client: fake, openFiles: map[string]int32{}}
	if err := c.DidClose(context.Background(), "/tmp/main.go"); err != nil {
		t.Fatalf("DidClose() error = %v", err)
	}
	if len(fake.didCloseURIs) != 0 {
		t.Fatalf("didClose count = %d, want 0", len(fake.didCloseURIs))
	}
}

func TestManagerDidCloseErrClearsDiagnosticsAndNotifiesClients(t *testing.T) {
	fake := &fakePowernapClient{}
	path := "/tmp/main.go"
	client := &Client{
		client:      fake,
		cwd:         "/tmp",
		openFiles:   map[string]int32{path: 1},
		diagnostics: map[protocol.DocumentURI][]protocol.Diagnostic{},
	}
	uri := protocol.DocumentURI(client.pathToURI(path))
	client.diagnostics[uri] = []protocol.Diagnostic{{Message: "boom"}}
	mgr := &Manager{
		clients: map[clientKey]*Client{{name: "gopls"}: client},
		waiters: map[string][]chan diagnosticsEvent{normalizeWaiterPath(path): {make(chan diagnosticsEvent, 1)}},
		diagByServer: map[clientKey]map[string]diagCounts{
			{name: "gopls"}: {string(uri): {errors: 1}},
		},
		touchedPaths: map[string]struct{}{
			normalizeWaiterPath(path): {},
		},
	}
	if err := mgr.DidCloseErr(context.Background(), path); err != nil {
		t.Fatalf("DidCloseErr() error = %v", err)
	}
	if len(fake.didCloseURIs) != 1 {
		t.Fatalf("didClose count = %d, want 1", len(fake.didCloseURIs))
	}
	if diags := client.GetDiagnostics(path); len(diags) != 0 {
		t.Fatalf("diagnostics = %v, want empty", diags)
	}
	if _, ok := mgr.waiters[normalizeWaiterPath(path)]; ok {
		t.Fatal("expected waiters for path removed")
	}
	if _, ok := mgr.diagByServer[clientKey{name: "gopls"}]; ok {
		t.Fatalf("diagByServer = %#v, want gopls entry removed", mgr.diagByServer)
	}
}

func TestClientDidOpenAlreadyOpenSendsDidChangeWithBumpedVersion(t *testing.T) {
	fake := &fakePowernapClient{}
	c := &Client{client: fake, openFiles: make(map[string]int32)}
	path := "/tmp/a.go"

	v1, err := c.DidOpen(context.Background(), path, "one")
	if err != nil {
		t.Fatalf("first DidOpen() error = %v", err)
	}
	if v1 != 1 {
		t.Fatalf("first DidOpen() version = %d, want 1", v1)
	}

	// The already-open branch used to double-unlock openFilesMu and read the
	// map without the lock (panic + data race); it must send didChange with the
	// bumped version instead.
	v2, err := c.DidOpen(context.Background(), path, "two")
	if err != nil {
		t.Fatalf("second DidOpen() error = %v", err)
	}
	if v2 != 2 {
		t.Fatalf("second DidOpen() version = %d, want 2", v2)
	}
	if len(fake.didOpenURIs) != 1 {
		t.Fatalf("didOpen count = %d, want 1", len(fake.didOpenURIs))
	}
	if len(fake.didChangeURIs) != 1 {
		t.Fatalf("didChange count = %d, want 1", len(fake.didChangeURIs))
	}
}

func TestClientDidChangeUnopenedSendsDidOpenWithVersionOne(t *testing.T) {
	fake := &fakePowernapClient{}
	c := &Client{client: fake, openFiles: make(map[string]int32)}
	path := "/tmp/a.go"

	v, err := c.DidChange(context.Background(), path, "one")
	if err != nil {
		t.Fatalf("DidChange() error = %v", err)
	}
	if v != 1 {
		t.Fatalf("DidChange() version = %d, want 1", v)
	}
	if len(fake.didOpenURIs) != 1 {
		t.Fatalf("didOpen count = %d, want 1", len(fake.didOpenURIs))
	}
	if len(fake.didChangeURIs) != 0 {
		t.Fatalf("didChange count = %d, want 0", len(fake.didChangeURIs))
	}
}

func TestClientCloseKillsOnShutdownTimeout(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	fake := &fakePowernapClient{shutdownErr: context.Canceled}
	c := &Client{client: fake}

	err := c.Close(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Close() error = %v, want context.Canceled", err)
	}
	if fake.shutdowns != 1 {
		t.Fatalf("shutdowns = %d, want 1", fake.shutdowns)
	}
	if fake.kills != 1 {
		t.Fatalf("kills = %d, want 1", fake.kills)
	}
	if fake.exits != 0 {
		t.Fatalf("exits = %d, want 0", fake.exits)
	}
}

func TestClientCloseKillsOnExitTimeout(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	fake := &fakePowernapClient{exitErr: context.Canceled}
	c := &Client{client: fake}
	cancel()

	err := c.Close(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Close() error = %v, want context.Canceled", err)
	}
	if fake.shutdowns != 1 {
		t.Fatalf("shutdowns = %d, want 1", fake.shutdowns)
	}
	if fake.exits != 1 {
		t.Fatalf("exits = %d, want 1", fake.exits)
	}
	if fake.kills != 1 {
		t.Fatalf("kills = %d, want 1", fake.kills)
	}
}

func TestGoToDefinitionUsesDefinitionLocationResult(t *testing.T) {
	fake := &fakePowernapClient{
		definitionResult: &protocol.Or_Result_textDocument_definition{Value: protocol.Definition{Value: protocol.Location{
			URI:   protocol.DocumentURI("file:///tmp/main.go"),
			Range: protocol.Range{Start: protocol.Position{Line: 4, Character: 7}},
		}}},
	}
	c := &Client{client: fake}
	got, err := c.GoToDefinition(context.Background(), "/tmp/input.go", 1, 2)
	if err != nil {
		t.Fatalf("GoToDefinition() error = %v", err)
	}
	want := []RefLocation{{Path: "/tmp/main.go", Line: 4, Col: 7}}
	if len(got) != len(want) || got[0] != want[0] {
		t.Fatalf("GoToDefinition() = %#v, want %#v", got, want)
	}
}

func TestGoToDefinitionUsesDefinitionLocationSliceResult(t *testing.T) {
	fake := &fakePowernapClient{
		definitionResult: &protocol.Or_Result_textDocument_definition{Value: protocol.Definition{Value: []protocol.Location{{
			URI:   protocol.DocumentURI("file:///tmp/main.go"),
			Range: protocol.Range{Start: protocol.Position{Line: 4, Character: 7}},
		}}}},
	}
	c := &Client{client: fake}
	got, err := c.GoToDefinition(context.Background(), "/tmp/input.go", 1, 2)
	if err != nil {
		t.Fatalf("GoToDefinition() error = %v", err)
	}
	want := []RefLocation{{Path: "/tmp/main.go", Line: 4, Col: 7}}
	if len(got) != len(want) || got[0] != want[0] {
		t.Fatalf("GoToDefinition() = %#v, want %#v", got, want)
	}
}

func TestGoToDefinitionUsesDefinitionLinks(t *testing.T) {
	fake := &fakePowernapClient{
		definitionResult: &protocol.Or_Result_textDocument_definition{Value: []protocol.DefinitionLink{{
			TargetURI:            protocol.DocumentURI("file:///tmp/impl.go"),
			TargetSelectionRange: protocol.Range{Start: protocol.Position{Line: 8, Character: 3}},
		}}},
	}
	c := &Client{client: fake}
	got, err := c.GoToDefinition(context.Background(), "/tmp/input.go", 1, 2)
	if err != nil {
		t.Fatalf("GoToDefinition() error = %v", err)
	}
	want := []RefLocation{{Path: "/tmp/impl.go", Line: 8, Col: 3}}
	if len(got) != len(want) || got[0] != want[0] {
		t.Fatalf("GoToDefinition() = %#v, want %#v", got, want)
	}
}

func TestGoToDefinitionNullResultReturnsEmpty(t *testing.T) {
	fake := &fakePowernapClient{definitionResult: &protocol.Or_Result_textDocument_definition{}}
	c := &Client{client: fake}
	got, err := c.GoToDefinition(context.Background(), "/tmp/input.go", 1, 2)
	if err != nil {
		t.Fatalf("GoToDefinition() error = %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("GoToDefinition() = %#v, want empty", got)
	}
}

func TestGoToDefinitionUnknownNestedShapeReturnsError(t *testing.T) {
	fake := &fakePowernapClient{definitionResult: &protocol.Or_Result_textDocument_definition{Value: protocol.Definition{Value: 123}}}
	c := &Client{client: fake}
	if _, err := c.GoToDefinition(context.Background(), "/tmp/input.go", 1, 2); err == nil {
		t.Fatal("GoToDefinition() error = nil, want error")
	}
}

func TestGoToDefinitionUnknownShapeReturnsError(t *testing.T) {
	fake := &fakePowernapClient{definitionResult: &protocol.Or_Result_textDocument_definition{Value: 123}}
	c := &Client{client: fake}
	if _, err := c.GoToDefinition(context.Background(), "/tmp/input.go", 1, 2); err == nil {
		t.Fatal("GoToDefinition() error = nil, want error")
	}
}

func TestGoToDefinitionResultMatchesJSONUnmarshalShape(t *testing.T) {
	data := []byte(`{"uri":"file:///tmp/main.go","range":{"start":{"line":4,"character":7},"end":{"line":4,"character":8}}}`)
	var res protocol.Or_Result_textDocument_definition
	if err := json.Unmarshal(data, &res); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	got, err := definitionResultToRefLocations(&res)
	if err != nil {
		t.Fatalf("definitionResultToRefLocations() error = %v", err)
	}
	want := []RefLocation{{Path: "/tmp/main.go", Line: 4, Col: 7}}
	if len(got) != len(want) || got[0] != want[0] {
		t.Fatalf("definitionResultToRefLocations() = %#v, want %#v", got, want)
	}
}

func TestFindImplementationsUsesLocationSliceResult(t *testing.T) {
	fake := &fakePowernapClient{
		implementationResult: &protocol.Or_Result_textDocument_implementation{Value: protocol.Definition{Value: []protocol.Location{{
			URI:   protocol.DocumentURI("file:///tmp/impl.go"),
			Range: protocol.Range{Start: protocol.Position{Line: 2, Character: 9}},
		}}}},
	}
	c := &Client{client: fake}
	got, err := c.FindImplementations(context.Background(), "/tmp/input.go", 1, 2)
	if err != nil {
		t.Fatalf("FindImplementations() error = %v", err)
	}
	want := []RefLocation{{Path: "/tmp/impl.go", Line: 2, Col: 9}}
	if len(got) != len(want) || got[0] != want[0] {
		t.Fatalf("FindImplementations() = %#v, want %#v", got, want)
	}
}

func TestFindImplementationsUsesDefinitionLinks(t *testing.T) {
	fake := &fakePowernapClient{
		implementationResult: &protocol.Or_Result_textDocument_implementation{Value: []protocol.DefinitionLink{{
			TargetURI:            protocol.DocumentURI("file:///tmp/impl.go"),
			TargetSelectionRange: protocol.Range{Start: protocol.Position{Line: 6, Character: 4}},
		}}},
	}
	c := &Client{client: fake}
	got, err := c.FindImplementations(context.Background(), "/tmp/input.go", 1, 2)
	if err != nil {
		t.Fatalf("FindImplementations() error = %v", err)
	}
	want := []RefLocation{{Path: "/tmp/impl.go", Line: 6, Col: 4}}
	if len(got) != len(want) || got[0] != want[0] {
		t.Fatalf("FindImplementations() = %#v, want %#v", got, want)
	}
}

func TestFindImplementationsResultMatchesJSONUnmarshalShape(t *testing.T) {
	data := []byte(`{"uri":"file:///tmp/impl.go","range":{"start":{"line":2,"character":9},"end":{"line":2,"character":10}}}`)
	var res protocol.Or_Result_textDocument_implementation
	if err := json.Unmarshal(data, &res); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	got, err := implementationResultToRefLocations(&res)
	if err != nil {
		t.Fatalf("implementationResultToRefLocations() error = %v", err)
	}
	want := []RefLocation{{Path: "/tmp/impl.go", Line: 2, Col: 9}}
	if len(got) != len(want) || got[0] != want[0] {
		t.Fatalf("implementationResultToRefLocations() = %#v, want %#v", got, want)
	}
}

func TestFindImplementationsUnknownNestedShapeReturnsError(t *testing.T) {
	fake := &fakePowernapClient{implementationResult: &protocol.Or_Result_textDocument_implementation{Value: protocol.Definition{Value: 123}}}
	c := &Client{client: fake}
	if _, err := c.FindImplementations(context.Background(), "/tmp/input.go", 1, 2); err == nil {
		t.Fatal("FindImplementations() error = nil, want error")
	}
}

func TestFindImplementationsUnknownShapeReturnsError(t *testing.T) {
	fake := &fakePowernapClient{implementationResult: &protocol.Or_Result_textDocument_implementation{Value: 123}}
	c := &Client{client: fake}
	if _, err := c.FindImplementations(context.Background(), "/tmp/input.go", 1, 2); err == nil {
		t.Fatal("FindImplementations() error = nil, want error")
	}
}

func TestFindImplementationsNullResultReturnsEmpty(t *testing.T) {
	fake := &fakePowernapClient{implementationResult: &protocol.Or_Result_textDocument_implementation{}}
	c := &Client{client: fake}
	got, err := c.FindImplementations(context.Background(), "/tmp/input.go", 1, 2)
	if err != nil {
		t.Fatalf("FindImplementations() error = %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("FindImplementations() = %#v, want empty", got)
	}
}

func TestDiscoverPythonInterpreterWalksUpToNearestEnvironment(t *testing.T) {
	root := t.TempDir()
	child := filepath.Join(root, "api", "app")
	if err := os.MkdirAll(child, 0o755); err != nil {
		t.Fatal(err)
	}
	pythonPath := filepath.Join(root, "api", ".venv", "bin", "python")
	if err := os.MkdirAll(filepath.Dir(pythonPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pythonPath, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := discoverPythonInterpreterBoundedForGOOS(child, root, "darwin"); got != pythonPath {
		t.Fatalf("got %q, want %q", got, pythonPath)
	}
}

func TestDiscoverPythonInterpreterDoesNotCrossProjectRoot(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "project")
	child := filepath.Join(root, "src")
	if err := os.MkdirAll(child, 0o755); err != nil {
		t.Fatal(err)
	}
	pythonPath := filepath.Join(parent, ".venv", "bin", "python")
	if err := os.MkdirAll(filepath.Dir(pythonPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pythonPath, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := discoverPythonInterpreterBoundedForGOOS(child, root, "darwin"); got != "" {
		t.Fatalf("got %q, want no interpreter", got)
	}
}

func TestDiscoverPythonInterpreterRejectsOutsideWorkspace(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	pythonPath := filepath.Join(outside, ".venv", "bin", "python")
	if err := os.MkdirAll(filepath.Dir(pythonPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pythonPath, nil, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := discoverPythonInterpreterBoundedForGOOS(outside, root, "linux"); got != "" {
		t.Fatalf("got %q for workspace outside project root", got)
	}
}

func TestDiscoverPythonInterpreterRejectsUnresolvablePaths(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("removing the working directory is unsupported on Windows")
	}
	root := t.TempDir()
	pythonPath := filepath.Join(root, ".venv", "bin", "python")
	if err := os.MkdirAll(filepath.Dir(pythonPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pythonPath, nil, 0o755); err != nil {
		t.Fatal(err)
	}
	workingDir := t.TempDir()
	t.Chdir(workingDir)
	if err := os.Remove(workingDir); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name string
		cwd  string
		root string
	}{
		{name: "workspace", cwd: ".", root: root},
		{name: "project", cwd: root, root: "."},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := discoverPythonInterpreterBoundedForGOOS(test.cwd, test.root, "linux"); got != "" {
				t.Fatalf("got %q for unresolvable path", got)
			}
		})
	}
}

func TestDiscoverPythonInterpreterStopsAtCwdWithoutProjectRoot(t *testing.T) {
	// Without a configured project root the search must never climb above the
	// workspace root: an environment in the workspace's parent (e.g. a
	// home-level ~/.venv for a workspace that is not inside the home
	// directory) would otherwise satisfy a workspace it does not contain.
	parent := t.TempDir()
	workspace := filepath.Join(parent, "workspace")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	parentVenv := filepath.Join(parent, ".venv", "bin", "python")
	if err := os.MkdirAll(filepath.Dir(parentVenv), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(parentVenv, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := discoverPythonInterpreterBoundedForGOOS(workspace, "", "linux"); got != "" {
		t.Fatalf("got %q for an environment above the workspace; empty project root must stop the walk at cwd", got)
	}

	// A venv inside the workspace still resolves with no project root.
	innerVenv := filepath.Join(workspace, ".venv", "bin", "python")
	if err := os.MkdirAll(filepath.Dir(innerVenv), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(innerVenv, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := discoverPythonInterpreterBoundedForGOOS(workspace, "", "linux"); got != innerVenv {
		t.Fatalf("got %q, want in-workspace %q", got, innerVenv)
	}
}

// TestManagerStopWaitsForInFlightStart is the regression test for Stop
// returning while a startServer goroutine is still in flight: that goroutine
// could finish afterwards and register a new client into a manager Stop had
// already drained. Stop must wait until every launch it observed settles, so
// when it returns no client can appear later.
func TestManagerStopWaitsForInFlightStart(t *testing.T) {
	root := t.TempDir()
	mgr := NewManager(&config.Config{
		LSP: config.LSPConfig{
			"gopls": {Command: "gopls", FileTypes: []string{".go"}},
		},
	}, root, nil)
	key := clientKey{name: "gopls", root: root}
	entry := &startEntry{ctx: context.Background(), done: make(chan struct{})}
	mgr.clientsMu.Lock()
	mgr.starting[key] = true
	mgr.launches[key] = entry
	mgr.clientsMu.Unlock()

	stopCtx, cancelStop := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelStop()
	stopped := make(chan struct{})
	go func() {
		mgr.Stop(stopCtx)
		close(stopped)
	}()

	// A fixed Stop blocks until the in-flight launch exits; the pre-fix Stop
	// returned immediately here, letting the launch register afterwards.
	select {
	case <-stopped:
		t.Fatal("Stop returned while an in-flight launch was still running")
	case <-time.After(100 * time.Millisecond):
	}

	// The launch finishes after Stop began.
	mgr.clientsMu.Lock()
	delete(mgr.starting, key)
	delete(mgr.launches, key)
	mgr.clientsMu.Unlock()
	close(entry.done)

	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop did not return after the in-flight launch settled")
	}

	// Stop must be safe to call again once everything settled.
	mgr.Stop(stopCtx)
	mgr.clientsMu.RLock()
	defer mgr.clientsMu.RUnlock()
	if len(mgr.clients) != 0 || len(mgr.launches) != 0 {
		t.Fatalf("manager still holds clients=%d launches=%d after Stop", len(mgr.clients), len(mgr.launches))
	}
}

// TestManagerStopPreventsInFlightStartFromRegistering verifies the admission
// gate: a launch whose start era was cancelled by a Stop must not register its
// finished client (the caller closes it instead), while a launch on a live era
// registers normally.
func TestManagerStopPreventsInFlightStartFromRegistering(t *testing.T) {
	root := t.TempDir()
	mgr := NewManager(&config.Config{
		LSP: config.LSPConfig{
			"gopls": {Command: "gopls", FileTypes: []string{".go"}},
		},
	}, root, nil)
	key := clientKey{name: "gopls", root: root}

	era, cancelEra := context.WithCancel(context.Background())
	cancelEra() // a Stop cancelled this launch's era
	fake := &fakePowernapClient{}
	lateClient := &Client{client: fake, cwd: root, cfg: config.LSPServerConfig{FileTypes: []string{".go"}}}
	entry := &startEntry{ctx: era, done: make(chan struct{})}

	mgr.clientsMu.Lock()
	closeMe, _, admitted := mgr.admitStartedClientLocked(key, entry, lateClient)
	mgr.clientsMu.Unlock()
	if admitted {
		t.Fatal("a launch whose era was cancelled by Stop was admitted")
	}
	if len(closeMe) != 0 {
		t.Fatalf("refused admission returned eviction work: %v", closeMe)
	}
	if _, ok := mgr.clients[key]; ok {
		t.Fatal("late client was registered after Stop")
	}

	// A launch on a live era still registers.
	liveClient := &Client{client: &fakePowernapClient{}, cwd: root, cfg: config.LSPServerConfig{FileTypes: []string{".go"}}}
	liveEntry := &startEntry{ctx: context.Background(), done: make(chan struct{})}
	mgr.clientsMu.Lock()
	_, _, admitted = mgr.admitStartedClientLocked(key, liveEntry, liveClient)
	mgr.clientsMu.Unlock()
	if !admitted {
		t.Fatal("a launch on a live era was refused")
	}
	if got := mgr.clients[key]; got != liveClient {
		t.Fatalf("clients[%v] = %p, want the live-launch client %p", key, got, liveClient)
	}
}

// TestManagerStopConcurrentWithStartSettles drives Stop and Start against the
// same manager from many goroutines (the idle-unload versus cold-start shape)
// and then requires the manager to settle with no registered client and no
// lingering launch. Run under -race this exercises the era and launches bookkeeping.
func TestManagerStopConcurrentWithStartSettles(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "main.ts")
	mgr := NewManager(&config.Config{
		LSP: config.LSPConfig{
			"typescript": {Command: "chord-no-such-typescript-server", FileTypes: []string{".ts"}},
		},
	}, root, nil)

	var wg sync.WaitGroup
	for range 30 {
		wg.Add(2)
		go func() {
			defer wg.Done()
			mgr.Start(context.Background(), path)
		}()
		go func() {
			defer wg.Done()
			stopCtx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
			defer cancel()
			mgr.Stop(stopCtx)
		}()
	}
	wg.Wait()

	// All launches use a nonexistent command and fail fast; a Stop waiting on
	// a wedged launch would surface here as a settle timeout.
	deadline := time.Now().Add(5 * time.Second)
	for {
		mgr.clientsMu.RLock()
		pending := len(mgr.launches)
		clients := len(mgr.clients)
		mgr.clientsMu.RUnlock()
		if pending == 0 && clients == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("manager did not settle after concurrent Start/Stop: pending=%d clients=%d", pending, clients)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestManagerStartAfterStopLaunchesAgain pins the unload semantics: Stop is
// an idle unload, not a terminal state — the manager must accept launches
// afterwards, or language servers would never reload after an idle unload.
func TestManagerStartAfterStopLaunchesAgain(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "main.ts")
	key := clientKey{name: "typescript", root: root}
	mgr := NewManager(&config.Config{
		LSP: config.LSPConfig{
			"typescript": {Command: "chord-no-such-typescript-server", FileTypes: []string{".ts"}},
		},
	}, root, nil)

	mgr.Start(context.Background(), path)
	waitForLSPStartDone(t, mgr, key)

	stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	mgr.Stop(stopCtx)
	cancel()

	// A later Start must launch again; the nonexistent command fails fast and
	// records a start failure, proving the launch actually ran.
	mgr.Start(context.Background(), path)
	waitForLSPStartDone(t, mgr, key)
	mgr.startFailMu.Lock()
	_, failed := mgr.startFail[key]
	mgr.startFailMu.Unlock()
	if !failed {
		t.Fatal("Start after Stop did not launch a server; the manager must stay usable after an unload")
	}
	mgr.clientsMu.RLock()
	defer mgr.clientsMu.RUnlock()
	if len(mgr.clients) != 0 || len(mgr.launches) != 0 {
		t.Fatalf("manager holds clients=%d launches=%d after Start-after-Stop settled", len(mgr.clients), len(mgr.launches))
	}
}
