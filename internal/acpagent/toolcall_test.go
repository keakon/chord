package acpagent

import (
	"encoding/json"
	"strings"
	"testing"

	acp "github.com/coder/acp-go-sdk"

	"github.com/keakon/chord/internal/agent"
)

func TestToolKindMapping(t *testing.T) {
	tests := []struct {
		tool string
		want acp.ToolKind
	}{
		{"read", acp.ToolKindRead},
		{"read_artifact", acp.ToolKindRead},
		{"view_image", acp.ToolKindRead},
		{"write", acp.ToolKindEdit},
		{"edit", acp.ToolKindEdit},
		{"apply_patch", acp.ToolKindEdit},
		{"patch", acp.ToolKindEdit},
		{"delete", acp.ToolKindDelete},
		{"grep", acp.ToolKindSearch},
		{"glob", acp.ToolKindSearch},
		{"shell", acp.ToolKindExecute},
		{"job_output", acp.ToolKindExecute},
		{"job_list", acp.ToolKindExecute},
		{"job_kill", acp.ToolKindExecute},
		{"web_fetch", acp.ToolKindFetch},
		{"todo_write", acp.ToolKindThink},
		{"delegate", acp.ToolKindOther},
		{"", acp.ToolKindOther},
	}
	for _, tt := range tests {
		t.Run(tt.tool, func(t *testing.T) {
			if got := toolKind(tt.tool); got != tt.want {
				t.Fatalf("toolKind(%q) = %q, want %q", tt.tool, got, tt.want)
			}
		})
	}
}

func TestToolTitle(t *testing.T) {
	tests := []struct {
		name     string
		tool     string
		argsJSON string
		want     string
	}{
		{"path detail", "read", `{"path":"internal/acpagent/server.go"}`, "Read internal/acpagent/server.go"},
		{"command detail", "shell", `{"command":"go test ./..."}`, "Shell go test ./..."},
		{"pattern detail", "grep", `{"pattern":"TODO"}`, "Grep TODO"},
		{"no args", "todo_write", "", "Todo Write"},
		{"invalid json", "read", `{"path":`, "Read"},
		{"long detail is truncated", "shell", `{"command":"` + strings.Repeat("x", maxToolTitleDetail+10) + `"}`, "Shell " + strings.Repeat("x", maxToolTitleDetail) + "…"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := toolTitle(tt.tool, tt.argsJSON); got != tt.want {
				t.Fatalf("toolTitle(%q, %q) = %q, want %q", tt.tool, tt.argsJSON, got, tt.want)
			}
		})
	}
}

func TestToolLocationsOnlyForFileTools(t *testing.T) {
	if got := toolLocations("read", `{"path":"a.go"}`); len(got) != 1 || got[0].Path != "a.go" {
		t.Fatalf("toolLocations(read) = %#v", got)
	}
	if got := toolLocations("shell", `{"path":"a.go","command":"ls"}`); got != nil {
		t.Fatalf("toolLocations(shell) = %#v, want nil", got)
	}
	if got := toolLocations("read", `{"path":""}`); got != nil {
		t.Fatalf("toolLocations(read with empty path) = %#v, want nil", got)
	}
}

func TestToolProgressText(t *testing.T) {
	tests := []struct {
		name     string
		progress agent.ToolProgressSnapshot
		want     string
	}{
		{"text wins", agent.ToolProgressSnapshot{Text: "streaming", Label: "label", Current: 1, Total: 2}, "streaming"},
		{"counts", agent.ToolProgressSnapshot{Current: 3, Total: 9}, "3/9"},
		{"label", agent.ToolProgressSnapshot{Label: "waiting"}, "waiting"},
		{"empty", agent.ToolProgressSnapshot{}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := progressText(tt.progress); got != tt.want {
				t.Fatalf("progressText(%#v) = %q, want %q", tt.progress, got, tt.want)
			}
		})
	}
}

func TestRawInputKeepsValidJSONOnly(t *testing.T) {
	raw, ok := rawInput(` {"path": "a"} `).(json.RawMessage)
	if !ok {
		t.Fatalf("rawInput did not return json.RawMessage")
	}
	var decoded map[string]string
	if err := json.Unmarshal(raw, &decoded); err != nil || decoded["path"] != "a" {
		t.Fatalf("rawInput payload = %s (err %v)", raw, err)
	}
	for _, args := range []string{"", "   ", `{"path":`, "not json"} {
		if got := rawInput(args); got != nil {
			t.Fatalf("rawInput(%q) = %#v, want nil", args, got)
		}
	}
}
