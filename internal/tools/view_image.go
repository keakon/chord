package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/keakon/chord/internal/imageutil"
	"github.com/keakon/chord/internal/message"
)

// ViewImageCapability reports whether the session can expose the view_image tool.
type ViewImageCapability interface {
	SupportsViewImageTool() bool
}

// ViewImageTool loads a local PNG/JPEG file into the conversation as an image
// part attached to the surrounding tool result.
type ViewImageTool struct {
	capability ViewImageCapability
}

// NewViewImageTool builds a ViewImageTool. capability gates visibility; a nil
// capability keeps the tool hidden.
func NewViewImageTool(capability ViewImageCapability) *ViewImageTool {
	return &ViewImageTool{capability: capability}
}

type viewImageArgs struct {
	Path  string `json:"path"`
	Label string `json:"label,omitempty"`
}

func (*ViewImageTool) Name() string { return NameViewImage }

func (*ViewImageTool) Description() string {
	return "View a local PNG or JPEG image by loading its contents into the conversation so you can see it directly. " +
		"Use this to inspect a screenshot, diagram, or rendered output on disk (for example to verify a UI change you just made). " +
		"The path must point to a PNG or JPEG file readable on this machine. " +
		"Note: the image content enters the model context and may be sent to a remote provider."
}

func (*ViewImageTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{
				"type":        "string",
				"description": "Absolute or relative path to a PNG or JPEG file. Supports ~ for the current user's home directory.",
			},
			"label": map[string]any{
				"type":        "string",
				"description": "Optional short label describing the image, echoed back in the confirmation text.",
			},
		},
		"required":             []string{"path"},
		"additionalProperties": false,
	}
}

func (*ViewImageTool) IsReadOnly() bool { return true }

func (*ViewImageTool) ConcurrencySafeReadOnly(json.RawMessage) bool { return true }

func (*ViewImageTool) CanRenderBeforeToolUseEnd(json.RawMessage) bool { return true }

// IsAvailable hides ViewImage unless the owning agent can safely expose it.
func (t *ViewImageTool) IsAvailable() bool {
	return t.capability != nil && t.capability.SupportsViewImageTool()
}

func (t *ViewImageTool) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	var a viewImageArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	a.Path = strings.TrimSpace(a.Path)
	a.Label = strings.TrimSpace(a.Label)
	if a.Path == "" {
		return "", fmt.Errorf("path is required")
	}

	sink, ok := ImageSinkFromContext(ctx)
	if !ok || sink == nil {
		return "", fmt.Errorf("image input is not available in this context")
	}

	resolvedPath, _, err := resolveExistingToolPath(a.Path, PathTargetRegularFile, "read")
	if err != nil {
		if strings.Contains(err.Error(), "path not found") {
			return "", fileNotFoundErrorWithPathSuggestions(a.Path, PathTargetRegularFile)
		}
		return "", err
	}

	// Read and (for PNG) compress; ReadImageFile rejects non-PNG/JPEG inputs and
	// enforces the shared size limit.
	data, mimeType, err := imageutil.ReadImageFile(resolvedPath)
	if err != nil {
		return "", err
	}

	// Leave ImagePath empty so the recovery layer persists Data to the session
	// store; the original on-disk file may be transient (e.g. a screenshot).
	sink.AddImage(message.ContentPart{
		Type:     "image",
		MimeType: mimeType,
		Data:     data,
		FileName: filepath.Base(resolvedPath),
	})

	label := a.Label
	if label == "" {
		label = filepath.Base(resolvedPath)
	}
	return fmt.Sprintf("Loaded image %q into context.", label), nil
}
