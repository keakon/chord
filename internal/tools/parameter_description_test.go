package tools

import (
	"strings"
	"testing"
)

func parameterDescription(t *testing.T, tool Tool, path ...string) string {
	t.Helper()
	params := tool.Parameters()
	current := any(params)
	for _, name := range path {
		obj, ok := current.(map[string]any)
		if !ok {
			t.Fatalf("%s parameter path %v is not an object before %q", tool.Name(), path, name)
		}
		if name == "items" {
			current, ok = obj["items"]
			if !ok {
				t.Fatalf("%s parameter path %v missing items", tool.Name(), path)
			}
			continue
		}
		props, ok := obj["properties"].(map[string]any)
		if !ok {
			t.Fatalf("%s parameter path %v missing properties before %q", tool.Name(), path, name)
		}
		current, ok = props[name]
		if !ok {
			t.Fatalf("%s parameters missing property path %v", tool.Name(), path)
		}
	}
	prop, ok := current.(map[string]any)
	if !ok {
		t.Fatalf("%s parameter path %v is not an object", tool.Name(), path)
	}
	desc, ok := prop["description"].(string)
	if !ok {
		t.Fatalf("%s.%v description missing or not string", tool.Name(), path)
	}
	return desc
}

func TestToolParameterDescriptionsMentionDefaults(t *testing.T) {
	tests := []struct {
		name string
		tool Tool
		path []string
		want string
	}{
		{name: "read offset", tool: ReadTool{}, path: []string{"offset"}, want: "Defaults to 0."},
		{name: "read limit", tool: ReadTool{}, path: []string{"limit"}, want: "Defaults to 2000."},
		{name: "glob path", tool: GlobTool{}, path: []string{"path"}, want: "Defaults to the session working directory."},
		{name: "grep paths", tool: GrepTool{}, path: []string{"paths"}, want: "Defaults to the session working directory when omitted."},
		{name: "lsp include declaration", tool: LspTool{}, path: []string{"include_declaration"}, want: "Default true."},
		{name: "edit replace all", tool: EditTool{}, path: []string{"replace_all"}, want: "Default is false."},
		{name: "shell workdir", tool: NewShellTool(""), path: []string{"workdir"}, want: "Defaults to the session working directory."},
		{name: "shell timeout", tool: NewShellTool(""), path: []string{"timeout"}, want: "default 30 seconds"},
		{name: "spawn timeout", tool: NewSpawnTool(""), path: []string{"timeout"}, want: "Defaults to no timeout"},
		{name: "spawn workdir", tool: NewSpawnTool(""), path: []string{"workdir"}, want: "Defaults to the session working directory."},
		{name: "web fetch raw", tool: WebFetchTool{}, path: []string{"raw"}, want: "Default false."},
		{name: "web fetch timeout", tool: WebFetchTool{}, path: []string{"timeout"}, want: "Default 30"},
		{name: "view image label", tool: NewViewImageTool(nil), path: []string{"label"}, want: "Defaults to the image filename."},
		{name: "save artifact type", tool: SaveArtifactTool{}, path: []string{"type"}, want: "Defaults to handoff_note."},
		{name: "save artifact mime type", tool: SaveArtifactTool{}, path: []string{"mime_type"}, want: "defaults to text/markdown"},
		{name: "save artifact mode", tool: SaveArtifactTool{}, path: []string{"mode"}, want: "default"},
		{name: "question multiple", tool: NewQuestionTool(nil), path: []string{"questions", "items", "multiple"}, want: "Defaults to false."},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			desc := parameterDescription(t, tc.tool, tc.path...)
			if !strings.Contains(desc, tc.want) {
				t.Fatalf("description = %q, want substring %q", desc, tc.want)
			}
		})
	}
}
