package toolname

import "testing"

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
		{"SpawnStatus", "SpawnStatus"},
		{"SpawnStop", "SpawnStop"},
		{"TodoWrite", "TodoWrite"},
		{"Question", "Question"},
		{"custom_tool", "custom_tool"},
		{"Spawn*", "Spawn*"},
		{"Spawn?", "Spawn?"},
		{"SpawnStatus*", "SpawnStatus*"},
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
