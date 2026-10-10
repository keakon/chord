// Package imagegen implements bounded, single-request image generation.
package imagegen

import (
	"fmt"
	"slices"
	"strings"
)

const (
	Generate          = "generate"
	Edit              = "edit"
	PresetOpenAI      = "openai"
	PresetCompatible  = "openai-compatible"
	PresetGemini      = "gemini"
	PresetSeedream    = "seedream"
	PresetXAI         = "xai"
	PresetQwen        = "qwen"
	MaxImageBytes     = 32 << 20
	MaxResponseBytes  = 96 << 20
	MaxImages         = 5
	MaxReferences     = 5
	MaxPixels         = 36_000_000
	MaxImageDimension = 12000
)

type Request struct {
	Prompt          string   `json:"prompt"`
	Operation       string   `json:"operation"`
	ReferenceImages []string `json:"reference_images,omitempty"`
	AspectRatio     string   `json:"aspect_ratio,omitempty"`
	Size            string   `json:"size,omitempty"`
	Quality         string   `json:"quality,omitempty"`
	Background      string   `json:"background,omitempty"`
	OutputFormat    string   `json:"output_format,omitempty"`
	OutputPath      string   `json:"output_path,omitempty"`
	References      []Image  `json:"-"`
}

type Image struct {
	Data   []byte `json:"-"`
	MIME   string `json:"mime_type,omitempty"`
	Width  int    `json:"width,omitempty"`
	Height int    `json:"height,omitempty"`
}

type Candidate struct {
	Image
	URL           string `json:"url,omitempty"`
	RevisedPrompt string `json:"revised_prompt,omitempty"`
}

type Result struct {
	Images    []Candidate    `json:"images"`
	RequestID string         `json:"request_id,omitempty"`
	Usage     map[string]any `json:"usage,omitempty"`
	Text      string         `json:"text,omitempty"`
	Warnings  []string       `json:"warnings,omitempty"`
}

type Target struct {
	Provider    string
	Preset      string
	Model       string
	BaseURL     string
	UserAgent   string
	Edit        bool
	Sizes       []string
	Ratios      []string
	Qualities   []string
	Formats     []string
	Backgrounds []string
}

// InferPreset returns the implemented image contract for an exact wire model ID.
// Unknown aliases require an explicit type; prefixes do not grant capabilities.
func InferPreset(model string) string {
	switch model {
	case "gpt-image-1", "gpt-image-1-mini", "gpt-image-1.5", "gpt-image-2", "gpt-image-2.5-sunburst", "gpt-image-2.5-flare":
		return PresetOpenAI
	case "gemini-2.5-flash-image", "gemini-3-pro-image-preview", "gemini-3.1-flash-image-preview":
		return PresetGemini
	case "doubao-seedream-4-0-250828", "doubao-seedream-4-5-251128":
		return PresetSeedream
	case "grok-imagine-image", "grok-imagine-image-pro", "grok-imagine-image-2.0":
		return PresetXAI
	case "qwen-image-3.0-pro", "qwen-image-3.0", "qwen-image-2.1-pro", "qwen-image-2.1-turbo":
		return PresetQwen
	default:
		return ""
	}
}

// ResolveTarget grants only the operations implemented for these model families.
func ResolveTarget(preset, model, baseURL string) (Target, error) {
	t := Target{Preset: preset, Model: model, BaseURL: strings.TrimRight(baseURL, "/")}
	switch preset {
	case PresetOpenAI:
		if InferPreset(model) != preset {
			return t, fmt.Errorf("openai image type requires an implemented GPT Image model")
		}
		t.Edit = true
		t.Sizes = []string{"auto", "1024x1024", "1536x1024", "1024x1536"}
		t.Qualities = []string{"auto", "low", "medium", "high"}
		if model == "gpt-image-2" || strings.HasPrefix(model, "gpt-image-2.5-") {
			t.Sizes = append(t.Sizes, "2048x2048", "2048x1152", "3840x2160", "2160x3840")
		}
		if strings.HasPrefix(model, "gpt-image-2.5-") {
			t.Qualities = append(t.Qualities, "xhigh", "max")
		}
		t.Formats = []string{"png", "jpeg", "webp"}
		t.Backgrounds = []string{"auto", "opaque", "transparent"}
		if t.BaseURL == "" {
			t.BaseURL = "https://api.openai.com/v1/images"
		}
	case PresetCompatible:
		t.Sizes = []string{"256x256", "512x512", "1024x1024", "1024x1536", "1536x1024"}
	case PresetGemini:
		if InferPreset(model) != preset {
			return t, fmt.Errorf("gemini image preset requires an implemented image-output model")
		}
		t.Edit = true
		t.Ratios = []string{"1:1", "2:3", "3:2", "3:4", "4:3", "4:5", "5:4", "9:16", "16:9", "21:9"}
		if model != "gemini-2.5-flash-image" {
			t.Sizes = []string{"1K", "2K", "4K"}
		}
		if t.BaseURL == "" {
			t.BaseURL = "https://generativelanguage.googleapis.com/v1beta"
		}
	case PresetSeedream:
		if InferPreset(model) != preset {
			return t, fmt.Errorf("seedream image preset requires doubao-seedream-4-0-250828 or doubao-seedream-4-5-251128")
		}
		t.Sizes = []string{"2K", "4K"}
		if t.BaseURL == "" {
			t.BaseURL = "https://ark.cn-beijing.volces.com/api/v3/images"
		}
	case PresetXAI:
		if InferPreset(model) != preset {
			return t, fmt.Errorf("xai image preset requires an implemented grok-imagine-image model")
		}
		t.Sizes = []string{"1k", "2k"}
		t.Ratios = []string{"1:1", "16:9", "9:16", "4:3", "3:4", "3:2", "2:3", "2:1", "1:2", "19.5:9", "9:19.5", "20:9", "9:20"}
		if t.BaseURL == "" {
			t.BaseURL = "https://api.x.ai/v1/images"
		}
	case PresetQwen:
		if InferPreset(model) != preset {
			return t, fmt.Errorf("qwen image preset requires an implemented OpenAI-compatible image model")
		}
		t.Sizes = []string{"auto", "1024x1024", "1024x1536", "1536x1024", "2048x2048"}
	default:
		return t, fmt.Errorf("unknown image generation preset %q", preset)
	}
	if model == "" || strings.ContainsAny(model, "/?#") {
		return t, fmt.Errorf("image model must be a non-empty wire model ID")
	}
	if t.BaseURL == "" {
		return t, fmt.Errorf("image generation base_url is required for %s", preset)
	}
	if err := ValidateURL(t.BaseURL); err != nil {
		return t, err
	}
	return t, nil
}

func (t Target) Validate(r *Request) error {
	r.Prompt = strings.TrimSpace(r.Prompt)
	if r.Prompt == "" || len(r.Prompt) > 32000 {
		return fmt.Errorf("prompt must contain 1 to 32000 bytes")
	}
	if r.Operation == "" {
		r.Operation = Generate
	}
	if r.Operation != Generate && r.Operation != Edit {
		return fmt.Errorf("operation must be generate or edit")
	}
	if r.Operation == Edit && !t.Edit {
		return fmt.Errorf("target does not support editing")
	}
	if len(r.ReferenceImages) > MaxReferences {
		return fmt.Errorf("at most %d reference images are allowed", MaxReferences)
	}
	if r.Operation == Edit && len(r.ReferenceImages) == 0 {
		return fmt.Errorf("edit requires explicit reference_images")
	}
	if len(r.ReferenceImages) > 0 && !t.Edit {
		return fmt.Errorf("target does not support reference images")
	}
	for _, v := range []struct {
		name, value string
		allowed     []string
	}{{"size", r.Size, t.Sizes}, {"aspect_ratio", r.AspectRatio, t.Ratios}, {"quality", r.Quality, t.Qualities}, {"background", r.Background, t.Backgrounds}, {"output_format", r.OutputFormat, t.Formats}} {
		if v.value != "" && !slices.Contains(v.allowed, v.value) {
			return fmt.Errorf("%s %q is not supported by this target", v.name, v.value)
		}
	}
	if r.Background == "transparent" && r.OutputFormat == "jpeg" {
		return fmt.Errorf("transparent background requires png or webp")
	}
	return nil
}
