package tools

import (
	"strings"
	"testing"
)

func TestGrepToolDescriptionExplainsDiscoveryRole(t *testing.T) {
	desc := (GrepTool{}).Description()
	for _, want := range []string{
		"If pattern is not valid regex, it is safely searched as literal text",
		"Use paths for one or more files/directories",
		"includes for optional path globs",
		"Best for discovering candidate files, symbols, or text matches when the exact location is not known yet.",
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
		"Relative paths resolve from the session working directory",
		"Defaults to the session working directory",
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
		"Find files by path using glob syntax.",
		"Best for discovering candidate files by path or extension.",
	} {
		if !strings.Contains(desc, want) {
			t.Fatalf("Description() missing %q: %q", want, desc)
		}
	}
	// The patterns parameter owns the not-regex / not-contents boundary.
	if strings.Contains(desc, "regular expressions") {
		t.Fatalf("Description() should leave the regex boundary to the patterns parameter: %q", desc)
	}
}

func TestGlobToolParameterDescriptionsClarifyBasePathAndPatternScope(t *testing.T) {
	props := (GlobTool{}).Parameters()["properties"].(map[string]any)
	pathDesc := props["path"].(map[string]any)["description"].(string)
	patternDesc := props["patterns"].(map[string]any)["description"].(string)

	for _, want := range []string{
		"Single base directory to search from",
		"Relative paths resolve from the session working directory",
		"Supports ~",
		"Defaults to the session working directory",
		"Returned matches are relative to this base directory",
	} {
		if !strings.Contains(pathDesc, want) {
			t.Fatalf("path description missing %q: %q", want, pathDesc)
		}
	}
	for _, want := range []string{
		"Path globs relative to path",
		"Returned matches are also relative to path",
		"src/**/*.ts",
		"Supports **",
		"This is glob syntax, not regex and not a file-contents search.",
	} {
		if !strings.Contains(patternDesc, want) {
			t.Fatalf("pattern description missing %q: %q", want, patternDesc)
		}
	}
}
