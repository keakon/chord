package message

import "testing"

func TestToolResultSucceeded(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    bool
	}{
		{"empty", "", true},
		{"whitespace", " \n\t ", true},
		{"success", "ok", true},
		{"cancelled", "cancelled", false},
		{"cancelled with details", "cancelled\nuser stopped", false},
		{"error prefix", "Error: failed", false},
		{"embedded error block", "before\n\nError: failed", false},
		{"model stopped", "Model stopped before completing this tool call", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ToolResultSucceeded(tt.content); got != tt.want {
				t.Fatalf("ToolResultSucceeded(%q) = %v, want %v", tt.content, got, tt.want)
			}
		})
	}
}
