package lsp

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	powernap "github.com/charmbracelet/x/powernap/pkg/lsp"
	"github.com/charmbracelet/x/powernap/pkg/lsp/protocol"
	powertransport "github.com/charmbracelet/x/powernap/pkg/transport"
	"github.com/keakon/chord/internal/config"
)

type lspProcessClient interface {
	Initialize(ctx context.Context, enableSnippets bool) error
	Shutdown(ctx context.Context) error
	Exit() error
	Kill()
	IsRunning() bool
	RegisterNotificationHandler(method string, handler powertransport.NotificationHandler)
	RegisterHandler(method string, handler powertransport.Handler)
	NotifyDidOpenTextDocument(ctx context.Context, uri string, languageID string, version int, text string) error
	NotifyDidChangeTextDocument(ctx context.Context, uri string, version int, changes []protocol.TextDocumentContentChangeEvent) error
	NotifyDidCloseTextDocument(ctx context.Context, uri string) error
	NotifyWorkspaceDidChangeConfiguration(ctx context.Context, settings any) error
	RequestHover(ctx context.Context, uri string, position protocol.Position) (*protocol.Hover, error)
	RequestDefinition(ctx context.Context, uri string, position protocol.Position) (*protocol.Or_Result_textDocument_definition, error)
	RequestImplementation(ctx context.Context, uri string, position protocol.Position) (*protocol.Or_Result_textDocument_implementation, error)
	FindReferences(ctx context.Context, filepath string, line, character int, includeDeclaration bool) ([]protocol.Location, error)
}

// Client wraps a powernap LSP client with per-file version tracking and diagnostic cache.
type Client struct {
	client lspProcessClient
	name   string
	cwd    string
	cfg    config.LSPServerConfig
	debug  bool

	// openFiles: path (normalized) -> version for didOpen/didChange
	openFiles   map[string]int32
	openFilesMu sync.Mutex

	// diagnostics cache: URI -> diagnostics (updated by publishDiagnostics handler)
	diagnostics   map[protocol.DocumentURI][]protocol.Diagnostic
	diagnosticsMu sync.RWMutex

	// onDiagnostics is called when diagnostics are received (manager sets it to broadcast + notify waiters).
	onDiagnostics func(uri string, serverID string, diags []protocol.Diagnostic)
}

// NewClient creates an LSP client and starts the server process. Call Initialize next.
func NewClient(ctx context.Context, name string, cfg config.LSPServerConfig, cwd string, debug bool) (*Client, error) {
	c := &Client{
		name:        name,
		cwd:         cwd,
		cfg:         cfg,
		debug:       debug,
		openFiles:   make(map[string]int32),
		diagnostics: make(map[protocol.DocumentURI][]protocol.Diagnostic),
	}
	if err := c.createPowernapClient(); err != nil {
		return nil, err
	}
	return c, nil
}

func (c *Client) createPowernapClient() error {
	rootURI := string(protocol.URIFromPath(c.cwd))
	env := c.cfg.Env
	if env == nil {
		env = make(map[string]string)
	}
	clientConfig := powernap.ClientConfig{
		Command:     c.cfg.Command,
		Args:        c.cfg.Args,
		RootURI:     rootURI,
		Environment: env,
		Settings:    c.cfg.Options,
		InitOptions: c.cfg.InitOptions,
		WorkspaceFolders: []protocol.WorkspaceFolder{
			{URI: rootURI, Name: filepath.Base(c.cwd)},
		},
	}
	pc, err := powernap.NewClient(clientConfig)
	if err != nil {
		return fmt.Errorf("create powernap client: %w", err)
	}
	c.client = pc
	return nil
}

// SetOnDiagnostics sets the callback invoked when textDocument/publishDiagnostics is received.
func (c *Client) SetOnDiagnostics(fn func(uri string, serverID string, diags []protocol.Diagnostic)) {
	c.onDiagnostics = fn
}

// Initialize sends initialize + initialized to the server and registers handlers.
func (c *Client) Initialize(ctx context.Context) error {
	c.registerHandlers()
	if err := c.client.Initialize(ctx, false); err != nil {
		return fmt.Errorf("initialize lsp client: %w", err)
	}
	settings := c.workspaceSettings()
	if err := c.client.NotifyWorkspaceDidChangeConfiguration(ctx, settings); err != nil {
		return fmt.Errorf("notify workspace configuration: %w", err)
	}
	return nil
}

func (c *Client) registerHandlers() {
	c.client.RegisterNotificationHandler("textDocument/publishDiagnostics", func(_ context.Context, _ string, params json.RawMessage) {
		var par protocol.PublishDiagnosticsParams
		if err := json.Unmarshal(params, &par); err != nil {
			slog.Error("lsp: unmarshal publishDiagnostics", "error", err)
			return
		}
		c.diagnosticsMu.Lock()
		c.diagnostics[par.URI] = par.Diagnostics
		c.diagnosticsMu.Unlock()
		if c.onDiagnostics != nil {
			c.onDiagnostics(string(par.URI), c.name, par.Diagnostics)
		}
	})
	c.client.RegisterHandler("workspace/applyEdit", handleApplyEdit)
	c.client.RegisterHandler("workspace/configuration", c.handleWorkspaceConfiguration)
	c.client.RegisterHandler("client/registerCapability", handleRegisterCapability)
}

func (c *Client) handleWorkspaceConfiguration(_ context.Context, _ string, params json.RawMessage) (any, error) {
	var configParams protocol.ConfigurationParams
	if err := json.Unmarshal(params, &configParams); err != nil {
		return nil, err
	}
	settings := c.workspaceSettings()
	result := make([]any, len(configParams.Items))
	for i := range result {
		result[i] = settings
	}
	return result, nil
}

func (c *Client) workspaceSettings() map[string]any {
	if c.cfg.Options == nil {
		return map[string]any{}
	}
	return c.cfg.Options
}

// Kill terminates the client without graceful shutdown.
func (c *Client) Kill() { c.client.Kill() }

// Close shuts down and exits the client. If graceful shutdown does not finish
// before ctx expires, the client is killed because language servers are
// auxiliary, per-session processes and there is no value in blocking process
// exit on them.
func (c *Client) Close(ctx context.Context) error {
	if err := c.client.Shutdown(ctx); err != nil {
		slog.Warn("lsp: shutdown client", "error", err)
		if ctx != nil && ctx.Err() != nil {
			c.client.Kill()
			return ctx.Err()
		}
	}
	if err := c.client.Exit(); err != nil {
		slog.Warn("lsp: exit client", "error", err)
		if ctx != nil && ctx.Err() != nil {
			c.client.Kill()
			return ctx.Err()
		}
		return err
	}
	return nil
}

// IsRunning returns whether the connection is still active.
func (c *Client) IsRunning() bool {
	return c.client != nil && c.client.IsRunning()
}

// HandlesFile returns true if this client handles the given path by file type and cwd.
func (c *Client) HandlesFile(path string) bool {
	abs, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(c.cwd, abs)
	if err != nil || strings.HasPrefix(rel, "..") {
		return false
	}
	if len(c.cfg.FileTypes) == 0 {
		return true
	}
	ext := strings.ToLower(filepath.Ext(path))
	for _, ft := range c.cfg.FileTypes {
		e := ft
		if e != "" && e[0] != '.' {
			e = "." + e
		}
		if ext == strings.ToLower(e) {
			return true
		}
	}
	return false
}

// pathToURI returns file URI for the given path.
func (c *Client) pathToURI(path string) string {
	abs, _ := filepath.Abs(path)
	return string(protocol.URIFromPath(abs))
}

// DidOpen sends didOpen for the file; if already open, no-op. Version is maintained by client.
func (c *Client) DidOpen(ctx context.Context, path string, content string) error {
	c.openFilesMu.Lock()
	if _, ok := c.openFiles[path]; ok {
		c.openFilesMu.Unlock()
		return c.DidChange(ctx, path, content)
	}
	c.openFiles[path] = 1
	c.openFilesMu.Unlock()

	uri := c.pathToURI(path)
	lang := string(powernap.DetectLanguage(path))
	if lang == "" {
		lang = "plaintext"
	}
	if err := c.client.NotifyDidOpenTextDocument(ctx, uri, lang, 1, content); err != nil {
		return err
	}
	return nil
}

// DidChange sends didChange for the file. Caller may pass version 0 to let client use its own counter.
func (c *Client) DidChange(ctx context.Context, path string, content string) error {
	uri := c.pathToURI(path)
	c.openFilesMu.Lock()
	v, ok := c.openFiles[path]
	if !ok {
		c.openFilesMu.Unlock()
		return c.DidOpen(ctx, path, content)
	}
	v++
	c.openFiles[path] = v
	c.openFilesMu.Unlock()

	changes := []protocol.TextDocumentContentChangeEvent{
		{Value: protocol.TextDocumentContentChangeWholeDocument{Text: content}},
	}
	return c.client.NotifyDidChangeTextDocument(ctx, uri, int(v), changes)
}

// DidClose sends didClose for the file if it is open, then forgets the local open-file version.
func (c *Client) DidClose(ctx context.Context, path string) error {
	c.openFilesMu.Lock()
	_, ok := c.openFiles[path]
	if ok {
		delete(c.openFiles, path)
	}
	c.openFilesMu.Unlock()
	if !ok {
		return nil
	}
	return c.client.NotifyDidCloseTextDocument(ctx, c.pathToURI(path))
}

// GetDiagnostics returns a copy of diagnostics for the given path (or all if path is empty).
func (c *Client) GetDiagnostics(path string) []protocol.Diagnostic {
	c.diagnosticsMu.RLock()
	defer c.diagnosticsMu.RUnlock()
	if path != "" {
		uri := protocol.DocumentURI(c.pathToURI(path))
		return append([]protocol.Diagnostic(nil), c.diagnostics[uri]...)
	}
	var out []protocol.Diagnostic
	for _, d := range c.diagnostics {
		out = append(out, d...)
	}
	return out
}

func (c *Client) clearDiagnosticsForPath(path string) {
	uri := protocol.DocumentURI(c.pathToURI(path))
	c.diagnosticsMu.Lock()
	delete(c.diagnostics, uri)
	c.diagnosticsMu.Unlock()
}

// CloseAllFiles sends didClose for all open files.
func (c *Client) CloseAllFiles(ctx context.Context) {
	c.openFilesMu.Lock()
	paths := make([]string, 0, len(c.openFiles))
	for p := range c.openFiles {
		paths = append(paths, p)
	}
	c.openFiles = make(map[string]int32)
	c.openFilesMu.Unlock()
	for _, p := range paths {
		_ = c.client.NotifyDidCloseTextDocument(ctx, c.pathToURI(p))
	}
}

// OpenFileOnDemand opens the file with the LSP server if not already open (reads from disk).
func (c *Client) OpenFileOnDemand(ctx context.Context, path string) error {
	c.openFilesMu.Lock()
	_, ok := c.openFiles[path]
	c.openFilesMu.Unlock()
	if ok {
		return nil
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return c.DidOpen(ctx, path, string(content))
}

// NotifyChange reads the file and sends didChange (file must already be open).
func (c *Client) NotifyChange(ctx context.Context, path string) error {
	content, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return c.DidChange(ctx, path, string(content))
}

func handleApplyEdit(_ context.Context, _ string, params json.RawMessage) (any, error) {
	var par protocol.ApplyWorkspaceEditParams
	if err := json.Unmarshal(params, &par); err != nil {
		return protocol.ApplyWorkspaceEditResult{Applied: false, FailureReason: err.Error()}, nil
	}
	if err := applyWorkspaceEdit(par.Edit); err != nil {
		slog.Error("lsp: applyWorkspaceEdit", "error", err)
		return protocol.ApplyWorkspaceEditResult{Applied: false, FailureReason: err.Error()}, nil
	}
	return protocol.ApplyWorkspaceEditResult{Applied: true}, nil
}

func applyWorkspaceEdit(edit protocol.WorkspaceEdit) error {
	if edit.Changes != nil {
		for uri, edits := range edit.Changes {
			path, err := uri.Path()
			if err != nil {
				return err
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			content := string(data)
			// Sort edits in reverse position order so earlier edits don't shift
			// the byte offsets of later ones.
			sort.Slice(edits, func(i, j int) bool {
				si, sj := edits[i].Range.Start, edits[j].Range.Start
				if si.Line != sj.Line {
					return si.Line > sj.Line
				}
				return si.Character > sj.Character
			})
			for _, te := range edits {
				content = applyTextEdit(content, te)
			}
			if err := os.WriteFile(path, []byte(content), 0644); err != nil {
				return err
			}
		}
	}
	return nil
}

func applyTextEdit(content string, te protocol.TextEdit) string {
	lines := splitLines(content)
	startL, startC := int(te.Range.Start.Line), int(te.Range.Start.Character)
	endL, endC := int(te.Range.End.Line), int(te.Range.End.Character)
	if startL >= len(lines) {
		return content + te.NewText
	}
	startByte := lineCharToByte(lines, startL, startC)
	endByte := lineCharToByte(lines, endL, endC)
	return content[:startByte] + te.NewText + content[endByte:]
}

// splitLines splits s on '\n'. CRLF files are handled correctly: the '\r'
// is retained as the last byte of each line, which matches what LSP servers
// report for Character offsets on CRLF content (each '\r' counts as one
// UTF-16 code unit, just as it does in utf16CharToByteOffset).
func splitLines(s string) []string {
	var out []string
	for len(s) > 0 {
		i := 0
		for i < len(s) && s[i] != '\n' {
			i++
		}
		out = append(out, s[:i])
		if i < len(s) {
			i++
		}
		s = s[i:]
	}
	return out
}

func lineCharToByte(lines []string, line, char int) int {
	byteOffset := 0
	for i := 0; i < line && i < len(lines); i++ {
		byteOffset += len(lines[i]) + 1
	}
	if line < len(lines) {
		byteOffset += utf16CharToByteOffset(lines[line], char)
	}
	return byteOffset
}

// utf16CharToByteOffset converts a UTF-16 code unit offset to a byte offset
// within a string. LSP positions use UTF-16 offsets, so surrogate pairs
// (runes >= U+10000) consume 2 UTF-16 code units.
func utf16CharToByteOffset(s string, utf16Chars int) int {
	consumed := 0
	for i, r := range s {
		if consumed >= utf16Chars {
			return i
		}
		if r >= 0x10000 {
			consumed += 2 // surrogate pair
		} else {
			consumed++
		}
	}
	return len(s)
}

func handleRegisterCapability(_ context.Context, _ string, _ json.RawMessage) (any, error) {
	return nil, nil
}

// HoverResult is a simplified hover result for tools (no dependency on LSP protocol).
type HoverResult struct {
	Contents string
}

// RefLocation is a single reference location for tools.
type RefLocation struct {
	Path string
	Line int
	Col  int
}

func definitionResultToRefLocations(res *protocol.Or_Result_textDocument_definition) ([]RefLocation, error) {
	if res == nil || res.Value == nil {
		return nil, nil
	}
	switch v := res.Value.(type) {
	case protocol.Definition:
		switch dv := v.Value.(type) {
		case nil:
			return nil, nil
		case protocol.Location:
			return []RefLocation{locationToRefLocation(dv)}, nil
		case []protocol.Location:
			return locationsToRefLocations(dv), nil
		default:
			return nil, fmt.Errorf("unsupported definition nested result type %T", v.Value)
		}
	case []protocol.DefinitionLink:
		return definitionLinksToRefLocations(v), nil
	default:
		return nil, fmt.Errorf("unsupported definition result type %T", res.Value)
	}
}

func implementationResultToRefLocations(res *protocol.Or_Result_textDocument_implementation) ([]RefLocation, error) {
	if res == nil || res.Value == nil {
		return nil, nil
	}
	switch v := res.Value.(type) {
	case protocol.Definition:
		switch dv := v.Value.(type) {
		case nil:
			return nil, nil
		case protocol.Location:
			return []RefLocation{locationToRefLocation(dv)}, nil
		case []protocol.Location:
			return locationsToRefLocations(dv), nil
		default:
			return nil, fmt.Errorf("unsupported implementation definition result type %T", v.Value)
		}
	case []protocol.DefinitionLink:
		return definitionLinksToRefLocations(v), nil
	default:
		return nil, fmt.Errorf("unsupported implementation result type %T", res.Value)
	}
}

func locationsToRefLocations(locs []protocol.Location) []RefLocation {
	out := make([]RefLocation, 0, len(locs))
	for _, loc := range locs {
		out = append(out, locationToRefLocation(loc))
	}
	return out
}

func locationToRefLocation(loc protocol.Location) RefLocation {
	p, _ := loc.URI.Path()
	return RefLocation{
		Path: p,
		Line: int(loc.Range.Start.Line),
		Col:  int(loc.Range.Start.Character),
	}
}

func definitionLinksToRefLocations(links []protocol.DefinitionLink) []RefLocation {
	out := make([]RefLocation, 0, len(links))
	for _, link := range links {
		p, _ := link.TargetURI.Path()
		out = append(out, RefLocation{
			Path: p,
			Line: int(link.TargetSelectionRange.Start.Line),
			Col:  int(link.TargetSelectionRange.Start.Character),
		})
	}
	return out
}

// Hover returns hover information at the given position (line and character are 0-based).
func (c *Client) Hover(ctx context.Context, path string, line, character int) (*HoverResult, error) {
	uri := c.pathToURI(path)
	pos := protocol.Position{Line: uint32(line), Character: uint32(character)}
	h, err := c.client.RequestHover(ctx, uri, pos)
	if err != nil {
		return nil, err
	}
	if h == nil {
		return &HoverResult{}, nil
	}
	return &HoverResult{Contents: h.Contents.Value}, nil
}

// GoToDefinition returns definition locations for the symbol at the given position (line and character 0-based).
func (c *Client) GoToDefinition(ctx context.Context, path string, line, character int) ([]RefLocation, error) {
	uri := c.pathToURI(path)
	pos := protocol.Position{Line: uint32(line), Character: uint32(character)}
	res, err := c.client.RequestDefinition(ctx, uri, pos)
	if err != nil {
		return nil, err
	}
	return definitionResultToRefLocations(res)
}

// FindReferences returns all references to the symbol at the given position (line and character 0-based).
func (c *Client) FindReferences(ctx context.Context, path string, line, character int, includeDeclaration bool) ([]RefLocation, error) {
	locs, err := c.client.FindReferences(ctx, path, line, character, includeDeclaration)
	if err != nil {
		return nil, err
	}
	out := make([]RefLocation, 0, len(locs))
	for _, loc := range locs {
		p, _ := loc.URI.Path()
		out = append(out, RefLocation{
			Path: p,
			Line: int(loc.Range.Start.Line),
			Col:  int(loc.Range.Start.Character),
		})
	}
	return out, nil
}

// FindImplementations returns implementation locations for the symbol at the given position (line and character 0-based).
func (c *Client) FindImplementations(ctx context.Context, path string, line, character int) ([]RefLocation, error) {
	uri := c.pathToURI(path)
	pos := protocol.Position{Line: uint32(line), Character: uint32(character)}
	res, err := c.client.RequestImplementation(ctx, uri, pos)
	if err != nil {
		return nil, err
	}
	return implementationResultToRefLocations(res)
}

// WaitForServerReady polls IsRunning until true or timeout.
func (c *Client) WaitForServerReady(ctx context.Context, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if c.IsRunning() {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
	return fmt.Errorf("timeout waiting for LSP server %s", c.name)
}
