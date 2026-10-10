package tools

import (
	"strings"

	"github.com/keakon/chord/internal/imagegen"
)

// ImageGenerationUsageGuidance is shared by the tool and visibility-aware prompt.
const ImageGenerationUsageGuidance = "When generate_image appears in the actual request's tool declarations, use it to fulfill image generation or editing requests. This configured image service is independent of the conversation model; do not invent temporary-chat or image-capability restrictions. Omit optional size, quality, aspect_ratio, background and output_format unless required by the user. All supplied options must be supported by one target. Eligible targets and API keys are tried internally. Successful results automatically include an image preview for image-capable conversation models; do not call a tool again just to view or confirm it. Text-only models receive saved file references and must not claim visual inspection. Chord completes downloads and saving internally, including bounded retries of temporary delivery errors. If an image was generated but delivery could not finish, report that distinction; do not issue a new generation request. Never retry an outcome_unknown; remote execution and billing are unconfirmed. After a quota, credential or all-keys-cooling failure, report the actionable reason or retry timing; do not use shell sleep or repeatedly regenerate."

func (t *GenerateImageTool) Description() string {
	var description strings.Builder
	description.WriteString(ImageGenerationUsageGuidance + " Generate one image or edit explicitly named reference images using the configured image service. Requires a specific prompt. Originals are saved as session artifacts; optional output_path publishes a workspace copy without overwriting. Only implemented target parameters are accepted. An outcome_unknown means the paid request may have executed: do not retry generation or switch services. Use view_image only when an additional saved image needs inspection.")
	if t.Backend != nil {
		targets := []imagegen.Target{t.Backend.Target()}
		if catalog, ok := t.Backend.(interface{ Targets() []imagegen.Target }); ok {
			targets = catalog.Targets()
		}
		for _, target := range targets {
			options := []string{}
			for _, option := range imageGenerationOptions(target) {
				if len(option.values) > 0 {
					options = append(options, option.key+"=["+strings.Join(option.values, ",")+"]")
				}
			}
			description.WriteString(" Target ")
			description.WriteString(target.Model)
			description.WriteString(" (")
			description.WriteString(target.Preset)
			description.WriteString("): ")
			description.WriteString(strings.Join(options, "; "))
			description.WriteByte('.')
		}
	}
	return description.String()
}
func (t *GenerateImageTool) Parameters() map[string]any {
	target := imagegen.Target{}
	if t.Backend != nil {
		target = t.Backend.Target()
	}
	operations := []string{imagegen.Generate}
	if target.Edit {
		operations = append(operations, imagegen.Edit)
	}
	props := map[string]any{
		"prompt":      map[string]any{"type": "string", "minLength": 1, "maxLength": 32000},
		"operation":   map[string]any{"type": "string", "enum": operations},
		"output_path": map[string]any{"type": "string", "description": "Optional path within the workspace, in an existing directory. Existing files are never overwritten."},
	}
	if target.Edit {
		props["reference_images"] = map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "maxItems": imagegen.MaxReferences, "description": "Explicit local image paths or artifact: session-relative references. Required for edit."}
	}
	for _, option := range imageGenerationOptions(target) {
		if len(option.values) > 0 {
			props[option.key] = map[string]any{"type": "string", "enum": option.values, "description": "Set only when the user explicitly requires this option; otherwise omit and use the target default. Optional parameters restrict eligible targets; all supplied values must fit one target."}
		}
	}
	return map[string]any{"type": "object", "properties": props, "required": []string{"prompt", "operation"}, "additionalProperties": false}
}

type imageGenerationOption struct {
	key    string
	values []string
}

func imageGenerationOptions(target imagegen.Target) []imageGenerationOption {
	return []imageGenerationOption{{"size", target.Sizes}, {"aspect_ratio", target.Ratios}, {"quality", target.Qualities}, {"background", target.Backgrounds}, {"output_format", target.Formats}}
}
