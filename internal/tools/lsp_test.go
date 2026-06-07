package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
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
		"Prefer it over grep/glob once the file path and cursor position are known",
		"Use grep/glob only to discover candidate files or positions when the location is not known yet.",
		"Use 1-based line and character from the raw file content",
		"count tabs in the source line as a single character",
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

func TestLspToolCharacterParameterExplainsRawSourceCounting(t *testing.T) {
	params := (LspTool{}).Parameters()
	props := params["properties"].(map[string]any)
	character := props["character"].(map[string]any)
	desc := character["description"].(string)
	for _, want := range []string{
		"raw source line",
		"Count tabs in the source line as a single character",
	} {
		if !strings.Contains(desc, want) {
			t.Fatalf("character description missing %q: %q", want, desc)
		}
	}
}

func TestGrepToolDescriptionExplainsDiscoveryRole(t *testing.T) {
	desc := (GrepTool{}).Description()
	for _, want := range []string{
		"pattern uses regex syntax, not glob syntax or plain text",
		"Use glob only to filter filenames by basename.",
		"Best for discovering candidate files, symbols, or text matches when the exact location is not known yet.",
		"For semantic navigation at a known position (definition, references, implementations), prefer the lsp tool",
	} {
		if !strings.Contains(desc, want) {
			t.Fatalf("Description() missing %q: %q", want, desc)
		}
	}
}

func TestGlobToolDescriptionExplainsDiscoveryRole(t *testing.T) {
	desc := (GlobTool{}).Description()
	for _, want := range []string{
		"pattern is a path glob, not a regular expression and not a file-contents search.",
		"Best for discovering candidate files by path or extension before using read, grep, or lsp.",
	} {
		if !strings.Contains(desc, want) {
			t.Fatalf("Description() missing %q: %q", want, desc)
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
