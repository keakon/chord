package llm

import (
	"encoding/json"
	"errors"
	"slices"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/toolname"
)

func nativeImageRequest(cfg *config.NativeImageGenerationConfig, provider *ProviderConfig) (*nativeToolRequest, error) {
	if err := cfg.Validate(provider.Type(), provider.APIURL()); err != nil {
		return nil, err
	}
	snapshot := *cfg
	if snapshot.MaxUses == 0 {
		snapshot.MaxUses = 1
	}
	constraints, err := json.Marshal(snapshot)
	if err != nil {
		return nil, err
	}
	return &nativeToolRequest{authorization: message.NativeToolAuthorization{Tool: toolname.GenerateImage, Contract: snapshot.Contract, Constraints: constraints}, applyDeclaration: func(raw []byte) ([]byte, error) {
		var body map[string]json.RawMessage
		if err := json.Unmarshal(raw, &body); err != nil {
			return nil, err
		}
		var defs []json.RawMessage
		if len(body["tools"]) > 0 {
			if err := json.Unmarshal(body["tools"], &defs); err != nil {
				return nil, err
			}
		}
		decl := map[string]any{"type": "image_generation", "model": snapshot.Model, "partial_images": 0}
		for k, v := range map[string]string{"size": snapshot.Size, "quality": snapshot.Quality, "background": snapshot.Background, "output_format": snapshot.OutputFormat} {
			if v != "" {
				decl[k] = v
			}
		}
		encoded, err := json.Marshal(decl)
		if err != nil {
			return nil, err
		}
		defs = append(defs, encoded)
		body["tools"], err = json.Marshal(defs)
		if err != nil {
			return nil, err
		}
		body["max_tool_calls"], _ = json.Marshal(snapshot.MaxUses)
		return json.Marshal(body)
	}}, nil
}

// A failed server request may use the local tool only after its durable journal
// proves rejection, without earlier native executions or stream output.
func nativeImageFallbackAllowed(err error) bool {
	api, ok := errors.AsType[*APIError](err)
	if !ok || api.Origin != APIErrorOriginHTTPResponse {
		return false
	}
	return apiErrorSignalEquals(api, "rate_limit_error", "rate_limit_exceeded", "insufficient_quota", "invalid_api_key", "authentication_error", "unsupported_tool", "unsupported_value", "unsupported_parameter")
}

func nativeImageFallbackDefinitions(defs []message.ToolDefinition, provider *ProviderConfig, model string, messages []message.Message) []message.ToolDefinition {
	var requirements string
	mc, _ := provider.GetModel(model)
	if cfg := mc.NativeImageGeneration; cfg != nil {
		options := map[string]string{}
		for key, value := range map[string]string{"size": cfg.Size, "quality": cfg.Quality, "background": cfg.Background, "output_format": cfg.OutputFormat} {
			if value != "" {
				options[key] = value
			}
		}
		if len(options) > 0 {
			encoded, _ := json.Marshal(options)
			requirements = " Preserve the configured output requirements: " + string(encoded) + "."
		}
	}
	// Native server IDs cannot become local edit references. Expose the saved
	// originals from prior native receipts so explicit edits keep their inputs.
	for _, msg := range messages {
		if msg.NativeTools == nil {
			continue
		}
		for _, call := range msg.NativeTools.Calls {
			if call.Kind == message.HostedCallKindImageGeneration && len(call.Parts) > 0 && len(call.Result) <= 8192 {
				var receipt struct {
					Images []struct {
						Reference string `json:"reference"`
					} `json:"images"`
				}
				if json.Unmarshal(call.Result, &receipt) == nil {
					for _, img := range receipt.Images {
						requirements += " Saved native original reference: " + img.Reference + "."
					}
				}
			}
		}
	}
	out := slices.Clone(defs)
	for i := range out {
		if out[i].Name == toolname.GenerateImage {
			out[i].Description += " The configured server image tool rejected the request before execution. Use this local tool to complete the original image request, preserving its requirements." + requirements
		}
	}
	return out
}
