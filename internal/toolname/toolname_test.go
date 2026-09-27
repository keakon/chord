package toolname

import (
	"strings"
	"testing"
)

func TestNormalizeTrimsAndPreservesToolNames(t *testing.T) {
	tests := []struct {
		name string
		want string
	}{
		{"", ""},
		{" edit ", Edit},
		{"Edit", "Edit"},
		{"ApplyPatch", "ApplyPatch"},
		{"WebFetch", "WebFetch"},
		{"JobOutput", "JobOutput"},
		{"JobList", "JobList"},
		{"JobKill", "JobKill"},
		{"TodoWrite", "TodoWrite"},
		{"Question", "Question"},
		{"custom_tool", "custom_tool"},
		{"Job*", "Job*"},
		{"Job?", "Job?"},
		{"JobOutput*", "JobOutput*"},
		{"WebFetch?", "WebFetch?"},
		{"TodoWrite*", "TodoWrite*"},
		{"SaveArtifact:*", "SaveArtifact:*"},
		{"custom_tool*", "custom_tool*"},
	}

	for _, tt := range tests {
		if got := Normalize(tt.name); got != tt.want {
			t.Fatalf("Normalize(%q) = %q, want %q", tt.name, got, tt.want)
		}
	}
}

func TestIsValidToolName(t *testing.T) {
	tests := []struct {
		name string
		want bool
	}{
		{"shell", true},
		{"mcp_chrome-devtools_take_screenshot", true},
		{"mcp_翻译服务器_网页搜索", true},
		{"", false},
		{"shell\n<｜｜DSML｜｜ invoke name=", false},
		{"tool/name", false},
		{"mcp_翻译 服务器_搜索", false},
		{strings.Repeat("a", 128), true},
		{strings.Repeat("a", 129), false},
		{strings.Repeat("字", 42), true},
		{strings.Repeat("字", 43), false},
	}
	for _, tt := range tests {
		if got := IsValid(tt.name); got != tt.want {
			t.Errorf("IsValid(%q) = %v, want %v", tt.name, got, tt.want)
		}
	}
}
