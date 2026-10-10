package config

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/keakon/chord/internal/imagegen"
)

func TestImageGenerationConfigLoadAndMerge(t *testing.T) {
	base := DefaultConfig()
	base.Providers = map[string]ProviderConfig{"sample": {Type: ProviderTypeResponses}}
	if base.ImageGeneration.Enabled {
		t.Fatal("enabled by default")
	}
	base.ImageGeneration = ImageGenerationConfig{Enabled: true, ModelPool: "images"}
	if base.ImageGeneration.Timeout() != 300*time.Second {
		t.Fatal("wrong default timeout")
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("image_generation:\n  timeout_seconds: 90\n"), 0600); err != nil {
		t.Fatal(err)
	}
	_, merged, err := MergeProjectConfig(base, path)
	if err != nil || merged.ImageGeneration.ModelPool != "images" || merged.ImageGeneration.Timeout() != 90*time.Second || base.ImageGeneration.TimeoutSeconds != 0 {
		t.Fatalf("merged=%+v err=%v", merged, err)
	}
	if err := os.WriteFile(path, []byte("image_generation:\n  enabled: false\n"), 0600); err != nil {
		t.Fatal(err)
	}
	_, merged, err = MergeProjectConfig(base, path)
	if err != nil || merged.ImageGeneration.Enabled {
		t.Fatal(err)
	}
	var cfg Config
	if err := yaml.Unmarshal([]byte("image_generation:\n  unexpected: true\n"), &cfg); err == nil {
		t.Fatal("unknown image field accepted")
	}

	base.Providers["sample"] = ProviderConfig{Type: ProviderTypeResponses, Models: map[string]ModelConfig{"gpt-image-1": {ImageGeneration: &ImageModelConfig{Type: "openai"}}}}
	base.ModelPools = map[string][]string{"images": {"sample/gpt-image-1"}}
	if err := base.ImageGeneration.Validate(base); err != nil {
		t.Fatal(err)
	}
	for _, c := range []ImageGenerationConfig{{Enabled: true}, {Enabled: true, ModelPool: "missing"}, {TimeoutSeconds: -1}, {TimeoutSeconds: 1801}} {
		if err := c.Validate(base); err == nil {
			t.Fatal("invalid configuration accepted")
		}
	}
	raw := "image_generation:\n  enabled: true\n  model_pool: images\nmodel_pools:\n  images: [sample/gpt-image-1]\nproviders:\n  sample:\n    type: responses\n    models:\n      gpt-image-1:\n        image_generation:\n          type: openai\n"
	if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadConfigFromPath(path)
	if err != nil || !loaded.ImageGeneration.Enabled {
		t.Fatal(err)
	}
	loaded.Providers["sample"] = ProviderConfig{Models: map[string]ModelConfig{"gpt-image-1": {}}}
	if err := loaded.ImageGeneration.Validate(loaded); err == nil {
		t.Fatal("text model accepted as image target")
	}
	for _, raw := range []string{"image_generation:\n  preset: openai\n", "image_generation:\n  provider: sample\n"} {
		if yaml.Unmarshal([]byte(raw), &cfg) == nil {
			t.Fatal("obsolete field accepted")
		}
	}
}

func TestImageModelTypeInferenceAndProviderRoot(t *testing.T) {
	for _, tc := range []struct {
		name, model, endpoint, providerType, explicitType, override, wantType, wantRoot string
		wantError                                                                       bool
	}{
		{name: "shared native provider", model: "gemini-3.1-flash-image-preview", endpoint: "https://example.invalid/proxy/v1beta/", wantType: imagegen.PresetGemini, wantRoot: "https://example.invalid/proxy/v1beta"},
		{name: "Gemini generation endpoint", model: "gemini-3.1-flash-image-preview", endpoint: "https://example.invalid/proxy/v1beta/models/gemini-3.1-flash-image-preview:generateContent", wantType: imagegen.PresetGemini, wantRoot: "https://example.invalid/proxy/v1beta"},
		{name: "Gemini v1 generation endpoint", model: "gemini-2.5-flash-image", endpoint: "https://example.invalid/proxy/v1/models/gemini-2.5-flash-image:generateContent", wantType: imagegen.PresetGemini, wantRoot: "https://example.invalid/proxy/v1"},
		{name: "explicit Gemini generation endpoint", model: "gemini-2.5-flash-image", explicitType: imagegen.PresetGemini, endpoint: "https://example.invalid/v1alpha/models/gemini-2.5-flash-image:generateContent", wantType: imagegen.PresetGemini, wantRoot: "https://example.invalid/v1alpha"},
		{name: "Gemini generation override", model: "gemini-2.5-flash-image", endpoint: "https://example.invalid/v1/responses", override: "https://example.invalid/proxy/v1beta/models/gemini-2.5-flash-image:generateContent", wantType: imagegen.PresetGemini, wantRoot: "https://example.invalid/proxy/v1beta"},
		{name: "explicit native v1 proxy", model: "gemini-2.5-flash-image", endpoint: "https://example.invalid/v1/", providerType: ProviderTypeGenerateContent, wantType: imagegen.PresetGemini, wantRoot: "https://example.invalid/v1"},
		{name: "known GPT model", model: "gpt-image-2.5-sunburst", endpoint: "https://example.invalid/v1/images/", wantType: imagegen.PresetOpenAI, wantRoot: "https://example.invalid/v1/images"},
		{name: "GPT generation endpoint", model: "gpt-image-1", endpoint: "https://example.invalid/proxy/v1/images/generations", wantType: imagegen.PresetOpenAI, wantRoot: "https://example.invalid/proxy/v1/images"},
		{name: "Grok generation endpoint", model: "grok-imagine-image", endpoint: "https://example.invalid/proxy/v1/images/generations", wantType: imagegen.PresetXAI, wantRoot: "https://example.invalid/proxy/v1/images"},
		{name: "Seedream generation endpoint", model: "doubao-seedream-4-5-251128", endpoint: "https://example.invalid/proxy/api/v3/images/generations", wantType: imagegen.PresetSeedream, wantRoot: "https://example.invalid/proxy/api/v3/images"},
		{name: "Qwen generation endpoint", model: "qwen-image-3.0", endpoint: "https://example.invalid/proxy/v1/images/generations", wantType: imagegen.PresetQwen, wantRoot: "https://example.invalid/proxy/v1/images"},
		{name: "explicit Grok generation endpoint", model: "grok-imagine-image", endpoint: "https://example.invalid/v1/images/generations/", explicitType: imagegen.PresetXAI, wantType: imagegen.PresetXAI, wantRoot: "https://example.invalid/v1/images"},
		{name: "unknown Images alias", model: "sample-image", endpoint: "https://example.invalid/v1/images/", wantType: imagegen.PresetCompatible, wantRoot: "https://example.invalid/v1/images"},
		{name: "unknown generation alias", model: "sample-image", endpoint: "https://example.invalid/proxy/images/generations", wantType: imagegen.PresetCompatible, wantRoot: "https://example.invalid/proxy/images"},
		{name: "generation override", model: "grok-imagine-image", endpoint: "https://example.invalid/v1/responses", override: "https://example.invalid/proxy/images/generations", wantType: imagegen.PresetXAI, wantRoot: "https://example.invalid/proxy/images"},
		{name: "custom explicit resource root", model: "gpt-image-1", endpoint: "https://example.invalid/v1/responses", override: "https://example.invalid/v1", wantType: imagegen.PresetOpenAI, wantRoot: "https://example.invalid/v1"},
		{name: "explicit type wins", model: "gpt-image-1", endpoint: "https://example.invalid/v1beta/", explicitType: imagegen.PresetCompatible, override: "https://example.invalid/custom/images", wantType: imagegen.PresetCompatible, wantRoot: "https://example.invalid/custom/images"},
		{name: "unknown endpoint and model", model: "sample-image", endpoint: "https://example.invalid/custom", wantError: true},
		{name: "known model with unknown endpoint", model: "grok-imagine-image", endpoint: "https://example.invalid/custom", wantError: true},
		{name: "GPT model with unknown endpoint", model: "gpt-image-1", endpoint: "https://example.invalid/custom", wantError: true},
		{name: "Gemini model with unknown endpoint", model: "gemini-2.5-flash-image", endpoint: "https://example.invalid/custom", wantError: true},
		{name: "Seedream model with unknown endpoint", model: "doubao-seedream-4-5-251128", endpoint: "https://example.invalid/custom", wantError: true},
		{name: "Qwen model with unknown endpoint", model: "qwen-image-3.0", endpoint: "https://example.invalid/custom", wantError: true},
		{name: "explicit type with unknown endpoint", model: "grok-imagine-image", explicitType: imagegen.PresetXAI, endpoint: "https://example.invalid/custom", wantError: true},
		{name: "known model with chat endpoint", model: "gpt-image-1", endpoint: "https://example.invalid/v1/responses", wantError: true},
		{name: "invalid image endpoint scheme", model: "grok-imagine-image", endpoint: "ftp://example.invalid/v1/images/generations", wantError: true},
		{name: "image endpoint with query", model: "grok-imagine-image", endpoint: "https://example.invalid/v1/images/generations?key=sample", wantError: true},
		{name: "image endpoint with credentials", model: "grok-imagine-image", endpoint: "https://sample@example.invalid/v1/images/generations", wantError: true},
		{name: "unknown native model", model: "sample-image", endpoint: "https://example.invalid/v1beta/", wantError: true},
		{name: "native URL with GPT model", model: "gpt-image-1", endpoint: "https://example.invalid/v1beta/", wantError: true},
		{name: "Images URL with Gemini model", model: "gemini-2.5-flash-image", endpoint: "https://example.invalid/v1/images/", wantError: true},
		{name: "generation URL with Gemini model", model: "gemini-2.5-flash-image", endpoint: "https://example.invalid/v1/images/generations", wantError: true},
		{name: "Gemini endpoint model mismatch", model: "gemini-2.5-flash-image", endpoint: "https://example.invalid/v1beta/models/gemini-3.1-flash-image-preview:generateContent", wantError: true},
		{name: "explicit Gemini endpoint model mismatch", model: "gemini-2.5-flash-image", explicitType: imagegen.PresetGemini, endpoint: "https://example.invalid/v1beta/models/gemini-3.1-flash-image-preview:generateContent", wantError: true},
		{name: "Gemini endpoint without version root", model: "gemini-2.5-flash-image", endpoint: "https://example.invalid/proxy/models/gemini-2.5-flash-image:generateContent", wantError: true},
		{name: "Gemini streaming image endpoint", model: "gemini-2.5-flash-image", endpoint: "https://example.invalid/v1beta/models/gemini-2.5-flash-image:streamGenerateContent", wantError: true},
		{name: "GPT model at Gemini generation endpoint", model: "gpt-image-1", endpoint: "https://example.invalid/v1beta/models/gpt-image-1:generateContent", wantError: true},
		{name: "native resource path rejected", model: "gemini-2.5-flash-image", endpoint: "https://example.invalid/v1beta/models", wantError: true},
		{name: "unknown native alias does not inherit capabilities", model: "gemini-sample-image", endpoint: "https://example.invalid/v1beta/", wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := ImageModelConfig{Type: tc.explicitType, BaseURL: tc.override}
			target, err := c.Target("sample", tc.model, ProviderConfig{APIURL: tc.endpoint, Type: tc.providerType})
			if tc.wantError {
				if err == nil {
					t.Fatal("invalid configuration accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if target.Preset != tc.wantType || target.BaseURL != tc.wantRoot {
				t.Fatalf("unexpected target: %+v", target)
			}
			req, _, err := imagegen.BuildRequest(context.Background(), target, imagegen.Request{Prompt: "A tree"}, "test-key")
			if err != nil {
				t.Fatal(err)
			}
			defer req.Body.Close()
			want := tc.wantRoot + "/generations"
			if tc.wantType == imagegen.PresetGemini {
				want = tc.wantRoot + "/models/" + tc.model + ":generateContent"
			}
			if req.URL.String() != want {
				t.Fatalf("request URL = %s, want %s", req.URL, want)
			}
			if tc.wantType == imagegen.PresetCompatible && target.Edit {
				t.Fatal("inferred alias granted editing")
			}
		})
	}
	var model ModelConfig
	if err := yaml.Unmarshal([]byte("image_generation: {}\n"), &model); err != nil || model.ImageGeneration == nil {
		t.Fatalf("image-only declaration lost: %v", err)
	}
	target, err := model.ImageGeneration.Target("sample", "gemini-2.5-flash-image", ProviderConfig{Preset: "gemini"})
	if err != nil || target.BaseURL != "https://generativelanguage.googleapis.com/v1beta" {
		t.Fatalf("preset root: %+v, %v", target, err)
	}
}

func TestImageModelRootAndChatIsolation(t *testing.T) {
	for _, root := range []string{"https://example.invalid/v1/images", "https://example.invalid/v1/images/"} {
		c := ImageModelConfig{Type: "openai"}
		target, err := c.Target("sample", "gpt-image-2.5-sunburst", ProviderConfig{APIURL: root})
		if err != nil || target.BaseURL != "https://example.invalid/v1/images" {
			t.Fatalf("image root inheritance: %v", err)
		}
	}
	c := ImageModelConfig{Type: "openai", BaseURL: "https://example.invalid/custom/images"}
	target, err := c.Target("sample", "gpt-image-1.5", ProviderConfig{APIURL: "https://example.invalid/v1/responses"})
	if err != nil || target.BaseURL != c.BaseURL {
		t.Fatal("explicit root lost")
	}
	c.BaseURL = ""
	target, err = c.Target("sample", "gpt-image-1.5", ProviderConfig{APIURL: "https://example.invalid/v1/responses"})
	if err == nil {
		t.Fatal("chat URL silently replaced with the default image endpoint")
	}
}

func TestImageGenerationEndpointRoutesEdits(t *testing.T) {
	target, err := (ImageModelConfig{}).Target("sample", "gpt-image-1", ProviderConfig{APIURL: "https://example.invalid/proxy/v1/images/generations"})
	if err != nil {
		t.Fatal(err)
	}
	req, _, err := imagegen.BuildRequest(t.Context(), target, imagegen.Request{Prompt: "Blue leaves", Operation: imagegen.Edit}, "test-key")
	if err != nil {
		t.Fatal(err)
	}
	defer req.Body.Close()
	if want := "https://example.invalid/proxy/v1/images/edits"; req.URL.String() != want {
		t.Fatalf("edit URL = %s, want %s", req.URL, want)
	}
}
