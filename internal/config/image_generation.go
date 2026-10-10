package config

import (
	"fmt"
	"net/url"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/keakon/chord/internal/imagegen"
)

// ImageGenerationConfig controls the independently routed image pool.
type ImageGenerationConfig struct {
	Enabled        bool   `json:"enabled" yaml:"enabled"`
	ModelPool      string `json:"model_pool,omitempty" yaml:"model_pool,omitempty"`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty" yaml:"timeout_seconds,omitempty"`
}

// ImageModelConfig describes an image-only model's wire contract.
type ImageModelConfig struct {
	Type    string `json:"type,omitempty" yaml:"type,omitempty"`
	BaseURL string `json:"base_url,omitempty" yaml:"base_url,omitempty"`
}

func (c ImageModelConfig) Target(provider, model string, providerConfig ProviderConfig) (imagegen.Target, error) {
	normalized, _, err := NormalizeProviderPreset(providerConfig)
	if err != nil {
		return imagegen.Target{}, err
	}
	base := c.BaseURL
	endpoint := base
	if endpoint == "" {
		endpoint = normalized.APIURL
	}
	if endpoint != "" {
		if err := imagegen.ValidateURL(endpoint); err != nil {
			return imagegen.Target{}, fmt.Errorf("invalid image API address: %w", err)
		}
	}
	imageRoot, isImagesURL := imageAPIResourceRoot(endpoint)
	nativeRoot, isGeminiEndpoint, err := geminiImageAPIResourceRoot(endpoint, model)
	if err != nil {
		return imagegen.Target{}, err
	}
	imageType := c.Type
	if imageType == "" {
		imageType = imagegen.InferPreset(model)
		switch {
		case isGeminiEndpoint || InferProviderTypeFromAPIURL(endpoint) == ProviderTypeGenerateContent || IsGeminiAPIURL(endpoint) && (imageType == imagegen.PresetGemini || EffectiveProviderType(normalized) == ProviderTypeGenerateContent):
			if imageType != "" && imageType != imagegen.PresetGemini {
				return imagegen.Target{}, fmt.Errorf("image model conflicts with Gemini API root; set image_generation.type and an appropriate endpoint explicitly")
			}
			imageType = imagegen.PresetGemini
		case isImagesURL:
			if imageType == imagegen.PresetGemini {
				return imagegen.Target{}, fmt.Errorf("gemini image model requires a native API root; set image_generation.type and an appropriate endpoint explicitly")
			}
			if imageType == "" {
				imageType = imagegen.PresetCompatible
			}
		}
		if imageType == "" {
			return imagegen.Target{}, fmt.Errorf("cannot infer image_generation.type from API URL or model ID; set it explicitly")
		}
	}
	if imageType != imagegen.PresetGemini && isImagesURL {
		base = imageRoot
	} else if imageType == imagegen.PresetGemini && (isGeminiEndpoint || IsGeminiAPIURL(endpoint)) {
		base = nativeRoot
	} else if base == "" && normalized.APIURL != "" {
		return imagegen.Target{}, fmt.Errorf("provider api_url is not a recognized image API address; use an Images root or /images/generations endpoint, a Gemini version root or :generateContent endpoint, or set image_generation.base_url explicitly")
	}
	if imageType == imagegen.PresetGemini {
		if base != "" && !IsGeminiAPIURL(base) {
			return imagegen.Target{}, fmt.Errorf("gemini image API root must end in /v1, /v1beta or /v1alpha")
		}
	}
	target, err := imagegen.ResolveTarget(imageType, model, base)
	target.Provider = provider
	return target, err
}

// geminiImageAPIResourceRoot accepts a version root or a generateContent URL
// for the configured model. A mismatched model must never be silently replaced.
func geminiImageAPIResourceRoot(endpoint, model string) (string, bool, error) {
	endpoint = strings.TrimRight(endpoint, "/")
	i := strings.LastIndex(endpoint, "/models/")
	if i < 0 || !strings.HasSuffix(endpoint, ":generateContent") {
		return endpoint, false, nil
	}
	root := endpoint[:i]
	if !IsGeminiAPIURL(root) {
		return "", false, fmt.Errorf("gemini image endpoint requires a version root ending in /v1, /v1beta or /v1alpha")
	}
	endpointModel, err := url.PathUnescape(strings.TrimSuffix(endpoint[i+len("/models/"):], ":generateContent"))
	if err != nil {
		return "", false, fmt.Errorf("decode gemini image endpoint model: %w", err)
	}
	if endpointModel != model {
		return "", false, fmt.Errorf("gemini image endpoint model does not match the configured model")
	}
	return root, true, nil
}

// imageAPIResourceRoot resolves an Images root or generation endpoint without
// changing the configured service or any proxy path prefix.
func imageAPIResourceRoot(endpoint string) (string, bool) {
	root := strings.TrimRight(endpoint, "/")
	if APIURLPathHasSuffix(root, "/images/generations") {
		return strings.TrimSuffix(root, "/generations"), true
	}
	return root, APIURLPathHasSuffix(root, "/images")
}

func (c ImageGenerationConfig) Validate(cfg *Config) error {
	if c.TimeoutSeconds < 0 || c.TimeoutSeconds > 1800 {
		return fmt.Errorf("image_generation.timeout_seconds must be between 0 and 1800")
	}
	for name, provider := range cfg.Providers {
		for model, mc := range provider.Models {
			if mc.ImageGeneration != nil {
				if _, err := mc.ImageGeneration.Target(name, model, provider); err != nil {
					return fmt.Errorf("image model %s/%s: %w", name, model, err)
				}
			}
			if mc.NativeImageGeneration != nil {
				if mc.ImageGeneration != nil {
					return fmt.Errorf("model %s/%s cannot be both image-only and a native tool caller", name, model)
				}
				normalized, _, err := NormalizeProviderPreset(provider)
				if err != nil {
					return err
				}
				if err := mc.NativeImageGeneration.Validate(EffectiveProviderType(normalized), normalized.APIURL); err != nil {
					return fmt.Errorf("native image model %s/%s: %w", name, model, err)
				}
			}
		}
	}
	if !c.Enabled {
		return nil
	}
	refs := cfg.ModelPools[c.ModelPool]
	if c.ModelPool == "" || len(refs) == 0 {
		return fmt.Errorf("image_generation.model_pool must reference a non-empty configured pool")
	}
	for _, ref := range refs {
		provider, model, variant, providerConfig, mc, err := ResolveConfiguredModelRef(cfg.Providers, ref)
		if err != nil {
			return fmt.Errorf("image pool reference %q: %w", ref, err)
		}
		if variant != "" || mc.ImageGeneration == nil {
			return fmt.Errorf("image pool reference %q must be an image model without a text variant", ref)
		}
		if _, err := mc.ImageGeneration.Target(provider, model, providerConfig); err != nil {
			return fmt.Errorf("image pool reference %q: %w", ref, err)
		}
	}
	return nil
}

func (c ImageGenerationConfig) Timeout() time.Duration {
	if c.TimeoutSeconds > 0 {
		return time.Duration(c.TimeoutSeconds) * time.Second
	}
	return 300 * time.Second
}

func (c *ImageGenerationConfig) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.MappingNode {
		return fmt.Errorf("image_generation must be a mapping")
	}
	type plain ImageGenerationConfig
	var value plain
	for i := 0; i < len(node.Content); i += 2 {
		switch node.Content[i].Value {
		case "enabled", "model_pool", "timeout_seconds":
		default:
			return fmt.Errorf("unknown image_generation field %q", node.Content[i].Value)
		}
	}
	if err := node.Decode(&value); err != nil {
		return err
	}
	*c = ImageGenerationConfig(value)
	return nil
}
