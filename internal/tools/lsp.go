package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/rivo/uniseg"

	"github.com/keakon/chord/internal/lsp"
)

// LspTool exposes LSP code intelligence (definition, find references, implementations) to the agent.
type LspTool struct {
	LSP     *lsp.Manager // nil when LSP not configured
	BaseDir string       // session working directory for relative paths; empty keeps process cwd behavior
}

type lspArgs struct {
	Operation   string `json:"operation"` // "definition", "references", or "implementation"
	Path        string `json:"path"`
	Line        int    `json:"line"`                          // 1-based
	Character   int    `json:"character"`                     // 1-based
	IncludeDecl *bool  `json:"include_declaration,omitempty"` // for references; default true
}

func (t LspTool) Name() string { return NameLsp }

func (t LspTool) IsAvailable() bool { return t.LSP != nil }

func (t LspTool) ConcurrencyPolicy(args json.RawMessage) ConcurrencyPolicy {
	return normalizeConcurrencyPolicy(NameLsp, fileToolConcurrencyPolicyInDir(args, true, t.BaseDir))
}

func (t LspTool) Description() string {
	base := "Semantic code navigation via LSP. Use this tool first for definition, references, and implementation at a known file position. Prefer it over text or file search once the file path and cursor position are known and the file type has LSP coverage. Use available discovery tools only to discover candidate files or positions when the location is not known yet. Put the cursor on the identifier itself, not file start or whitespace. If references fails because no identifier is found, inspect the file and retry on the symbol name. Use 1-based line and character from the raw file content; count Unicode grapheme clusters (user-perceived characters) in the source line, including tabs as a single character, and prefer the start of the target identifier. Returned locations use the same line/character counting."
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
				"description": "One of: definition, references, implementation. Prefer these for semantic navigation and analysis once the file path and position are known. Use definition to jump to the symbol definition, references to find usages, and implementation to find concrete implementations. For references/implementation, place the cursor on the identifier, not on empty space or the file beginning.",
				"enum":        []string{"definition", "references", "implementation"},
			},
			"path": map[string]any{
				"type":        "string",
				"description": "Absolute or relative path to the verified file. Relative paths resolve from the session working directory. Supports ~ for the current user's home directory. Do not guess paths.",
			},
			"line": map[string]any{
				"type":        "integer",
				"description": "1-based line number.",
			},
			"character": map[string]any{
				"type":        "integer",
				"description": "1-based character offset on the raw source line, counted as Unicode grapheme clusters (user-perceived characters). Count tabs as a single character. For symbol queries, this should point to a character inside the identifier.",
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

func (LspTool) ConcurrencySafeReadOnly(json.RawMessage) bool { return true }

func utf16UnitsForRunes(runes []rune) int {
	units := 0
	for _, r := range runes {
		if r >= 0x10000 {
			units += 2
		} else {
			units++
		}
	}
	return units
}

func lspCharacterToUTF16Offset(lines []string, line, character int) int {
	if character <= 0 {
		return 0
	}
	if line < 0 || line >= len(lines) {
		return character
	}
	utf16Offset := 0
	charIndex := 0
	graphemes := uniseg.NewGraphemes(lines[line])
	for graphemes.Next() {
		if charIndex >= character {
			break
		}
		utf16Offset += utf16UnitsForRunes(graphemes.Runes())
		charIndex++
	}
	return utf16Offset
}

func utf16OffsetToLspCharacter(lines []string, line, utf16Offset int) int {
	if utf16Offset <= 0 {
		return 0
	}
	if line < 0 || line >= len(lines) {
		return utf16Offset
	}
	units := 0
	character := 0
	graphemes := uniseg.NewGraphemes(lines[line])
	for graphemes.Next() {
		if units >= utf16Offset {
			break
		}
		units += utf16UnitsForRunes(graphemes.Runes())
		character++
	}
	return character
}

func lspSourceLinesForPath(path string, sourceLinesByPath map[string][]string) ([]string, bool) {
	if lines, ok := sourceLinesByPath[path]; ok {
		return lines, true
	}
	decoded, err := ReadDecodedTextFile(path)
	if err != nil {
		return nil, false
	}
	lines := splitReadToolLines(decoded.Text)
	sourceLinesByPath[path] = lines
	return lines, true
}

func formatLspLocations(locs []lsp.RefLocation, sourceLinesByPath map[string][]string) string {
	var b strings.Builder
	for i, loc := range locs {
		if i > 0 {
			b.WriteString("\n")
		}
		col := loc.Col
		if lines, ok := lspSourceLinesForPath(loc.Path, sourceLinesByPath); ok {
			col = utf16OffsetToLspCharacter(lines, loc.Line, loc.Col)
		}
		b.WriteString(fmt.Sprintf("%s:%d:%d", loc.Path, loc.Line+1, col+1))
	}
	return b.String()
}

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
	absPath, err := resolveToolPathAbsInDir(a.Path, t.BaseDir)
	if err != nil {
		return "", fmt.Errorf("resolve path: %w", err)
	}
	sourceLinesByPath := make(map[string][]string)
	if decoded, readErr := ReadDecodedTextFile(absPath); readErr == nil {
		sourceLinesByPath[absPath] = splitReadToolLines(decoded.Text)
	}
	// LSP uses 0-based line and character.
	line, char := a.Line-1, a.Character-1
	if line < 0 {
		line = 0
	}
	if char < 0 {
		char = 0
	}
	if lines, ok := sourceLinesByPath[absPath]; ok {
		char = lspCharacterToUTF16Offset(lines, line, char)
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
		return formatLspLocations(locs, sourceLinesByPath), nil
	case "references":
		locs, err := client.FindReferences(ctx, absPath, line, char, includeDecl)
		if err != nil {
			return "", fmt.Errorf("references: %w", err)
		}
		if len(locs) == 0 {
			return "No references found.", nil
		}
		return formatLspLocations(locs, sourceLinesByPath), nil
	case "implementation":
		locs, err := client.FindImplementations(ctx, absPath, line, char)
		if err != nil {
			return "", fmt.Errorf("implementation: %w", err)
		}
		if len(locs) == 0 {
			return "No implementations found.", nil
		}
		return formatLspLocations(locs, sourceLinesByPath), nil
	default:
		return "", fmt.Errorf("operation must be definition, references, or implementation, got %q", a.Operation)
	}
}
