package lsp

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/charmbracelet/x/powernap/pkg/lsp/protocol"
	powertransport "github.com/charmbracelet/x/powernap/pkg/transport"

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
func (f *fakePowernapClient) NotifyDidOpenTextDocument(context.Context, string, string, int, string) error {
	return nil
}
func (f *fakePowernapClient) NotifyDidChangeTextDocument(context.Context, string, int, []protocol.TextDocumentContentChangeEvent) error {
	return nil
}
func (f *fakePowernapClient) NotifyDidCloseTextDocument(_ context.Context, uri string) error {
	f.didCloseURIs = append(f.didCloseURIs, uri)
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

	got := discoverPythonInterpreterForGOOS(root, "linux")
	if got != pythonPath {
		t.Fatalf("discoverPythonInterpreterForGOOS() = %q, want %q", got, pythonPath)
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

	got := discoverPythonInterpreterForGOOS(root, "linux")
	if got != "" {
		t.Fatalf("discoverPythonInterpreterForGOOS() = %q, want empty", got)
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

	got := discoverPythonInterpreterForGOOS(root, "windows")
	if got != pythonPath {
		t.Fatalf("discoverPythonInterpreterForGOOS() = %q, want %q", got, pythonPath)
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

	got := prepareWorkspaceSettings("pyright", config.LSPServerConfig{Command: "pyright-langserver"}, root)
	want := map[string]any{"python": map[string]any{"pythonPath": pythonPath}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("prepareWorkspaceSettings() = %#v, want %#v", got, want)
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
	got := prepareWorkspaceSettings("pyright", config.LSPServerConfig{Command: "pyright-langserver", Options: explicit}, root)
	if !reflect.DeepEqual(got, explicit) {
		t.Fatalf("prepareWorkspaceSettings() = %#v, want %#v", got, explicit)
	}
}

func TestPrepareWorkspaceSettingsMakesExplicitRelativeInterpreterAbsolute(t *testing.T) {
	root := t.TempDir()
	explicit := map[string]any{"python": map[string]any{"pythonPath": ".venv/bin/python"}}
	got := prepareWorkspaceSettings("pyright", config.LSPServerConfig{Command: "pyright-langserver", Options: explicit}, root)
	want := map[string]any{"python": map[string]any{"pythonPath": filepath.Join(root, ".venv", "bin", "python")}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("prepareWorkspaceSettings() = %#v, want %#v", got, want)
	}
}

func TestPrepareWorkspaceSettingsMakesExplicitRelativeVenvPathAbsolute(t *testing.T) {
	root := t.TempDir()
	explicit := map[string]any{"python": map[string]any{"venvPath": ".venvs", "venv": "py311"}}
	got := prepareWorkspaceSettings("pyright", config.LSPServerConfig{Command: "pyright-langserver", Options: explicit}, root)
	want := map[string]any{"python": map[string]any{"venvPath": filepath.Join(root, ".venvs"), "venv": "py311"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("prepareWorkspaceSettings() = %#v, want %#v", got, want)
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
		clients: map[string]*Client{"gopls": client},
		waiters: map[string][]chan []Diagnostic{normalizeWaiterPath(path): {make(chan []Diagnostic, 1)}},
		diagByServer: map[string]map[string]diagCounts{
			"gopls": {string(uri): {errors: 1}},
		},
		touchedPaths: map[string]struct{}{
			normalizeWaiterPath(path): {},
		},
	}
	if err := mgr.DidCloseErr(path); err != nil {
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
	if _, ok := mgr.diagByServer["gopls"]; ok {
		t.Fatalf("diagByServer = %#v, want gopls entry removed", mgr.diagByServer)
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
