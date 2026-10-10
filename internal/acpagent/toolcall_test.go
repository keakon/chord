package acpagent

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/png"
	"path/filepath"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/imagegen"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"

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
		{"web_search", acp.ToolKindSearch},
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

func TestGenerateImageACPContentIncludesOriginal(t *testing.T) {
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	img, err := imagegen.ValidateImage(t.Context(), buf.Bytes(), "")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	ref, err := tools.SaveImageArtifact(t.Context(), dir, img)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(tools.ImageGenerationSummary{State: imagegen.StateSaved, Images: []tools.GeneratedImage{{ArtifactRef: ref, Reference: tools.ImageArtifactPrefix + ref.RelPath, Width: 2, Height: 2}}})
	if err != nil {
		t.Fatal(err)
	}
	event := agent.ToolResultEvent{Name: tools.NameGenerateImage, Status: agent.ToolResultStatusSuccess, Payload: string(raw), Parts: []message.ContentPart{{Type: message.ContentPartImage, ImagePath: filepath.Join(dir, ref.RelPath), ArtifactID: ref.ID}}}
	content := toolResultContent(t.Context(), event)
	encoded, err := json.Marshal(content)
	if err != nil {
		t.Fatal(err)
	}
	if len(content) != 2 || !bytes.Contains(encoded, []byte(base64.StdEncoding.EncodeToString(img.Data))) {
		t.Fatal("ACP omitted original image bytes")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	cancelledContent := toolResultContent(ctx, event)
	cancelledJSON, err := json.Marshal(cancelledContent)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(cancelledJSON, []byte(base64.StdEncoding.EncodeToString(img.Data))) || !bytes.Contains(cancelledJSON, []byte("context canceled")) {
		t.Fatal("cancelled delivery published original or lost cancellation")
	}
	event.Status = agent.ToolResultStatusCancelled
	if len(toolResultContent(t.Context(), event)) != 1 {
		t.Fatal("cancelled result published image success")
	}
}
