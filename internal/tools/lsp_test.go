package tools

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/lsp"
)

func TestLspToolParametersIncludeDefinitionAndImplementation(t *testing.T) {
	params := (LspTool{}).Parameters()
	props := params["properties"].(map[string]any)
	op := props["operation"].(map[string]any)
	enum := op["enum"].([]string)
	want := []string{"definition", "references", "implementation"}
	if len(enum) != len(want) {
		t.Fatalf("operation enum len = %d, want %d (%v)", len(enum), len(want), enum)
	}
	for i := range want {
		if enum[i] != want[i] {
			t.Fatalf("operation enum = %v, want %v", enum, want)
		}
	}
}

func TestLspToolDescriptionGuidesRoutingWithoutHover(t *testing.T) {
	desc := (LspTool{}).Description()
	for _, want := range []string{
		"Use this tool first for definition, references, and implementation at a known file position.",
		"Prefer it over text or file search once the file path and cursor position are known",
		"Use available discovery tools only to discover candidate files or positions when the location is not known yet.",
		"Use 1-based line and character from the raw file content",
		"count Unicode grapheme clusters",
		"tabs as a single character",
		"Returned locations use the same line/character counting.",
		"prefer the start of the target identifier",
	} {
		if !strings.Contains(desc, want) {
			t.Fatalf("Description() missing %q: %q", want, desc)
		}
	}
	if strings.Contains(desc, "hover") {
		t.Fatalf("Description() should not mention hover: %q", desc)
	}
}

func TestLspToolIsAvailableRequiresManager(t *testing.T) {
	if (LspTool{}).IsAvailable() {
		t.Fatal("LspTool without manager should not be available")
	}
}

func TestLspToolCharacterParameterExplainsRawSourceCounting(t *testing.T) {
	params := (LspTool{}).Parameters()
	props := params["properties"].(map[string]any)
	character := props["character"].(map[string]any)
	desc := character["description"].(string)
	for _, want := range []string{
		"raw source line",
		"Unicode grapheme clusters",
		"Count tabs as a single character",
	} {
		if !strings.Contains(desc, want) {
			t.Fatalf("character description missing %q: %q", want, desc)
		}
	}
}

func TestLspToolCharacterConversionsUseGraphemeClusters(t *testing.T) {
	lines := []string{"a🏳️‍🌈\tb"}
	if got := lspCharacterToUTF16Offset(lines, 0, 1); got != 1 {
		t.Fatalf("lspCharacterToUTF16Offset after ASCII = %d, want 1", got)
	}
	if got := lspCharacterToUTF16Offset(lines, 0, 2); got != 7 {
		t.Fatalf("lspCharacterToUTF16Offset after emoji grapheme = %d, want 7", got)
	}
	if got := lspCharacterToUTF16Offset(lines, 0, 3); got != 8 {
		t.Fatalf("lspCharacterToUTF16Offset after tab = %d, want 8", got)
	}
	if got := utf16OffsetToLspCharacter(lines, 0, 7); got != 2 {
		t.Fatalf("utf16OffsetToLspCharacter after emoji grapheme = %d, want 2", got)
	}
	if got := utf16OffsetToLspCharacter(lines, 0, 8); got != 3 {
		t.Fatalf("utf16OffsetToLspCharacter after tab = %d, want 3", got)
	}
}

func TestFormatLspLocationsConvertsUTF16ColumnsToGraphemeCharacters(t *testing.T) {
	path := filepath.Join(t.TempDir(), "unicode.go")
	lines := []string{"a🏳️‍🌈\tb"}
	got := formatLspLocations([]lsp.RefLocation{{Path: path, Line: 0, Col: 8}}, map[string][]string{path: lines})
	want := path + ":1:4"
	if got != want {
		t.Fatalf("formatLspLocations = %q, want %q", got, want)
	}
}

func TestLspToolPathDescriptionWarnsAgainstGuessing(t *testing.T) {
	props := (LspTool{}).Parameters()["properties"].(map[string]any)
	desc := props["path"].(map[string]any)["description"].(string)
	for _, want := range []string{"verified file", "Do not guess paths"} {
		if !strings.Contains(desc, want) {
			t.Fatalf("path description missing %q: %q", want, desc)
		}
	}
}

func TestGrepToolDescriptionExplainsDiscoveryRole(t *testing.T) {
	desc := (GrepTool{}).Description()
	for _, want := range []string{
		"If pattern is not valid regex, it is safely searched as literal text",
		"Use paths for one or more files/directories",
		"includes for optional path globs",
		"Best for discovering candidate files, symbols, or text matches when the exact location is not known yet.",
		"For semantic navigation at a known position (definition, references, implementations), prefer the lsp tool",
	} {
		if !strings.Contains(desc, want) {
			t.Fatalf("Description() missing %q: %q", want, desc)
		}
	}
}

func TestGrepToolParameterDescriptionsClarifyPathsAndIncludes(t *testing.T) {
	props := (GrepTool{}).Parameters()["properties"].(map[string]any)
	pathDesc := props["paths"].(map[string]any)["description"].(string)
	includeDesc := props["includes"].(map[string]any)["description"].(string)

	for _, want := range []string{
		"One or more files/directories to search",
		"Defaults to the current directory",
	} {
		if !strings.Contains(pathDesc, want) {
			t.Fatalf("paths description missing %q: %q", want, pathDesc)
		}
	}
	for _, want := range []string{
		"path glob filters",
		"**/*.go",
		"internal/**/*.ts",
	} {
		if !strings.Contains(includeDesc, want) {
			t.Fatalf("includes description missing %q: %q", want, includeDesc)
		}
	}
}

func TestGlobToolDescriptionExplainsDiscoveryRole(t *testing.T) {
	desc := (GlobTool{}).Description()
	for _, want := range []string{
		"patterns are path globs, not regular expressions and not file-contents searches.",
		"Best for discovering candidate files by path or extension before using read, grep, or lsp.",
	} {
		if !strings.Contains(desc, want) {
			t.Fatalf("Description() missing %q: %q", want, desc)
		}
	}
}

func TestGlobToolParameterDescriptionsClarifyBasePathAndPatternScope(t *testing.T) {
	props := (GlobTool{}).Parameters()["properties"].(map[string]any)
	pathDesc := props["path"].(map[string]any)["description"].(string)
	patternDesc := props["patterns"].(map[string]any)["description"].(string)

	for _, want := range []string{
		"Single base directory to search from",
		"Supports ~",
	} {
		if !strings.Contains(pathDesc, want) {
			t.Fatalf("path description missing %q: %q", want, pathDesc)
		}
	}
	for _, want := range []string{
		"Path globs relative to path",
		"src/**/*.ts",
		"Supports **",
	} {
		if !strings.Contains(patternDesc, want) {
			t.Fatalf("pattern description missing %q: %q", want, patternDesc)
		}
	}
}

func TestLspToolReturnsNotConfiguredBeforeOperationValidation(t *testing.T) {
	_, err := (LspTool{}).Execute(context.Background(), json.RawMessage(`{"operation":"unknown","path":"x","line":1,"character":1}`))
	if err == nil {
		t.Fatal("Execute() error = nil, want error")
	}
	if got := err.Error(); got != "LSP not configured" {
		t.Fatalf("Execute() error = %q, want %q", got, "LSP not configured")
	}
}
