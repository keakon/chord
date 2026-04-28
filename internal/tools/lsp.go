package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/keakon/chord/internal/lsp"
)

// LspTool exposes LSP code intelligence (definition, find references, implementations) to the agent.
type LspTool struct {
	LSP *lsp.Manager // nil when LSP not configured
}

type lspArgs struct {
	Operation   string `json:"operation"` // "definition", "references", or "implementation"
	Path        string `json:"path"`
	Line        int    `json:"line"`                          // 1-based
	Character   int    `json:"character"`                     // 1-based
	IncludeDecl *bool  `json:"include_declaration,omitempty"` // for references; default true
}

func (t LspTool) Name() string { return "Lsp" }

func (LspTool) ConcurrencyPolicy(args json.RawMessage) ConcurrencyPolicy {
	return normalizeConcurrencyPolicy("Lsp", fileToolConcurrencyPolicy(args, true))
}

func (t LspTool) Description() string {
	base := "Semantic code navigation via LSP. Use this tool first for definition, references, and implementation at a known file position. Prefer it over Grep/Glob once the file path and cursor position are known and the file type has LSP coverage. Use Grep/Glob only to discover candidate files or positions when the location is not known yet. Use 1-based line and character from the raw file content. If the position comes from Read output, do not count Read's left line-number gutter or separator tab; count tabs in the source line as a single character, and prefer the start of the target identifier."
	if t.LSP == nil {
		return base
	}
	servers := t.LSP.ConfiguredServers()
	if len(servers) == 0 {
		return base
	}
	var parts []string
	for _, s := range servers {
		if len(s.FileTypes) == 0 {
			parts = append(parts, s.Name+" (any file type)")
		} else {
			parts = append(parts, s.Name+" ("+strings.Join(s.FileTypes, ", ")+")")
		}
	}
	return base + "\n\nConfigured LSP servers: " + strings.Join(parts, ", ")
}

func (t LspTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"operation": map[string]any{
				"type":        "string",
				"description": "One of: definition, references, implementation. Prefer these for semantic navigation and analysis once the file path and position are known. Use definition to jump to the symbol definition, references to find usages, and implementation to find concrete implementations.",
				"enum":        []string{"definition", "references", "implementation"},
			},
			"path": map[string]any{
				"type":        "string",
				"description": "Absolute or relative path to the file.",
			},
			"line": map[string]any{
				"type":        "integer",
				"description": "1-based line number.",
			},
			"character": map[string]any{
				"type":        "integer",
				"description": "1-based character offset on the raw source line. If the position comes from Read output, do not count Read's left line-number gutter or separator tab; count tabs in the source line as a single character.",
			},
			"include_declaration": map[string]any{
				"type":        "boolean",
				"description": "For references: include the declaration. Default true.",
			},
		},
		"required":             []string{"operation", "path", "line", "character"},
		"additionalProperties": false,
	}
}

func (t LspTool) IsReadOnly() bool { return true }

func (t LspTool) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	if t.LSP == nil {
		return "", fmt.Errorf("LSP not configured")
	}
	var a lspArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	if a.Path == "" {
		return "", fmt.Errorf("path is required")
	}
	absPath, err := filepath.Abs(a.Path)
	if err != nil {
		return "", fmt.Errorf("resolve path: %w", err)
	}
	// LSP uses 0-based line and character.
	line, char := a.Line-1, a.Character-1
	if line < 0 {
		line = 0
	}
	if char < 0 {
		char = 0
	}
	includeDecl := a.IncludeDecl == nil || *a.IncludeDecl

	client, ok := t.LSP.ClientForPath(ctx, absPath)
	if !ok {
		return "", fmt.Errorf("no LSP server for this file (check file type and config)")
	}

	switch strings.ToLower(a.Operation) {
	case "definition":
		locs, err := client.GoToDefinition(ctx, absPath, line, char)
		if err != nil {
			return "", fmt.Errorf("definition: %w", err)
		}
		if len(locs) == 0 {
			return "No definition found.", nil
		}
		var b strings.Builder
		for i, loc := range locs {
			if i > 0 {
				b.WriteString("\n")
			}
			b.WriteString(fmt.Sprintf("%s:%d:%d", loc.Path, loc.Line+1, loc.Col+1))
		}
		return b.String(), nil
	case "references":
		locs, err := client.FindReferences(ctx, absPath, line, char, includeDecl)
		if err != nil {
			return "", fmt.Errorf("references: %w", err)
		}
		if len(locs) == 0 {
			return "No references found.", nil
		}
		var b strings.Builder
		for i, loc := range locs {
			if i > 0 {
				b.WriteString("\n")
			}
			b.WriteString(fmt.Sprintf("%s:%d:%d", loc.Path, loc.Line+1, loc.Col+1))
		}
		return b.String(), nil
	case "implementation":
		locs, err := client.FindImplementations(ctx, absPath, line, char)
		if err != nil {
			return "", fmt.Errorf("implementation: %w", err)
		}
		if len(locs) == 0 {
			return "No implementations found.", nil
		}
		var b strings.Builder
		for i, loc := range locs {
			if i > 0 {
				b.WriteString("\n")
			}
			b.WriteString(fmt.Sprintf("%s:%d:%d", loc.Path, loc.Line+1, loc.Col+1))
		}
		return b.String(), nil
	default:
		return "", fmt.Errorf("operation must be definition, references, or implementation, got %q", a.Operation)
	}
}
