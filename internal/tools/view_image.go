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

// ImageInputCapability reports whether the active model accepts image input.
// ViewImage uses it to hide itself from models that cannot consume images, so
// the model is never offered a tool whose output it could not use.
type ImageInputCapability interface {
	SupportsInput(modality string) bool
}

// ViewImageTool loads a local PNG/JPEG file into the conversation as an image
// part. The decoded bytes are pushed to the per-call ImageSink (see
// image_sink.go); the agent runtime re-injects the collected parts as a
// synthetic user message after the surrounding tool-call batch completes. The
// tool itself returns only a short textual confirmation.
type ViewImageTool struct {
	capability ImageInputCapability
}

// NewViewImageTool builds a ViewImageTool. capability gates visibility on the
// active model's image-input support; a nil capability keeps the tool hidden.
func NewViewImageTool(capability ImageInputCapability) *ViewImageTool {
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

// IsAvailable hides ViewImage unless the active model accepts image input.
func (t *ViewImageTool) IsAvailable() bool {
	return t.capability != nil && t.capability.SupportsInput("image")
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
			return "", fmt.Errorf("file not found: %s", a.Path)
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
