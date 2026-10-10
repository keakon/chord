package config

import (
	"fmt"

	"github.com/keakon/chord/internal/imagegen"
	"github.com/keakon/chord/internal/modelcatalog"
)

const NativeImageGenerationResponses = modelcatalog.ServerToolContractResponsesImageGeneration

// NativeImageGenerationConfig authorizes server-side generation on one endpoint.
type NativeImageGenerationConfig struct {
	Contract      string `json:"contract" yaml:"contract"`
	APIURL        string `json:"api_url" yaml:"api_url"`
	Preauthorized bool   `json:"preauthorized" yaml:"preauthorized"`
	Model         string `json:"model" yaml:"model"`
	Size          string `json:"size,omitempty" yaml:"size,omitempty"`
	Quality       string `json:"quality,omitempty" yaml:"quality,omitempty"`
	Background    string `json:"background,omitempty" yaml:"background,omitempty"`
	OutputFormat  string `json:"output_format,omitempty" yaml:"output_format,omitempty"`
	MaxUses       int    `json:"max_uses,omitempty" yaml:"max_uses,omitempty"`
}

func (c NativeImageGenerationConfig) Validate(protocol, endpoint string) error {
	if protocol != ProviderTypeResponses || c.Contract != NativeImageGenerationResponses || c.APIURL == "" || c.APIURL != endpoint {
		return fmt.Errorf("native image generation authorization does not match the request endpoint and protocol")
	}
	if c.MaxUses < 0 || c.MaxUses > imagegen.MaxImages {
		return fmt.Errorf("native image generation max_uses must be between 1 and %d, or omitted", imagegen.MaxImages)
	}
	t, err := imagegen.ResolveTarget(imagegen.PresetOpenAI, c.Model, "")
	if err != nil {
		return err
	}
	r := imagegen.Request{Prompt: "Validate configured image options", Operation: imagegen.Generate, Size: c.Size, Quality: c.Quality, Background: c.Background, OutputFormat: c.OutputFormat}
	return t.Validate(&r)
}
