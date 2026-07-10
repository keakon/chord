package main

import (
	"strings"

	"github.com/keakon/chord/internal/config"

	"gopkg.in/yaml.v3"
)

type initialSetupProviderKind string

const (
	initialSetupProviderAPIKey initialSetupProviderKind = "api-key"
	initialSetupProviderCodex  initialSetupProviderKind = "codex"
)

type initialSetupConfigInput struct {
	Kind            initialSetupProviderKind
	ProviderName    string
	ProviderType    string
	APIURL          string
	ModelName       string
	Proxy           string
	IMESwitchTarget string
	PreventSleep    *bool
	ContextLimit    int
	InputLimit      int
	OutputLimit     int
}

type initialSetupConfigYAML struct {
	Providers       map[string]initialSetupProviderYAML `yaml:"providers"`
	ModelPools      map[string][]string                 `yaml:"model_pools"`
	Proxy           string                              `yaml:"proxy,omitempty"`
	IMESwitchTarget string                              `yaml:"ime_switch_target,omitempty"`
	PreventSleep    *bool                               `yaml:"prevent_sleep,omitempty"`
}

type initialSetupProviderYAML struct {
	Type   string                           `yaml:"type,omitempty"`
	APIURL string                           `yaml:"api_url,omitempty"`
	Preset string                           `yaml:"preset,omitempty"`
	Models map[string]initialSetupModelYAML `yaml:"models"`
}

type initialSetupModelYAML struct {
	Limit initialSetupLimitYAML `yaml:"limit"`
}

type initialSetupLimitYAML struct {
	Context int `yaml:"context"`
	Input   int `yaml:"input,omitempty"`
	Output  int `yaml:"output"`
}

type initialSetupEndpointDefaults struct {
	ProviderName string
	ProviderType string
	APIURL       string
	ModelName    string
	ContextLimit int
	InputLimit   int
	OutputLimit  int
}

type initialSetupModelDefaults struct {
	Name         string
	ContextLimit int
	InputLimit   int
	OutputLimit  int
}

func buildInitialSetupConfigYAML(input initialSetupConfigInput) ([]byte, error) {
	providerName := strings.TrimSpace(input.ProviderName)
	if providerName == "" {
		providerName = "openai"
	}
	modelName := strings.TrimSpace(input.ModelName)
	if modelName == "" {
		modelName = "gpt-5.6"
	}

	provider := initialSetupProviderYAML{}
	modelPool := []string{}
	switch input.Kind {
	case initialSetupProviderCodex:
		provider.Preset = "codex"
		provider.Type = "responses"
		provider.Models = make(map[string]initialSetupModelYAML)
		for _, model := range initialSetupCodexModels() {
			provider.Models[model.Name] = initialSetupModelYAML{
				Limit: initialSetupLimitYAML{Context: model.ContextLimit, Input: model.InputLimit, Output: model.OutputLimit},
			}
			modelPool = append(modelPool, providerName+"/"+model.Name)
		}
	default:
		provider.Type = strings.TrimSpace(input.ProviderType)
		provider.APIURL = strings.TrimSpace(input.APIURL)
		provider.Models = map[string]initialSetupModelYAML{
			modelName: {Limit: initialSetupLimitYAML{Context: input.ContextLimit, Input: input.InputLimit, Output: input.OutputLimit}},
		}
		modelPool = []string{providerName + "/" + modelName}
	}

	cfg := initialSetupConfigYAML{
		Providers: map[string]initialSetupProviderYAML{providerName: provider},
		ModelPools: map[string][]string{
			"default": modelPool,
		},
	}
	if strings.TrimSpace(input.Proxy) != "" {
		cfg.Proxy = strings.TrimSpace(input.Proxy)
	}
	if strings.TrimSpace(input.IMESwitchTarget) != "" {
		cfg.IMESwitchTarget = strings.TrimSpace(input.IMESwitchTarget)
	}
	if input.PreventSleep != nil {
		cfg.PreventSleep = input.PreventSleep
	}
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return nil, err
	}
	return data, nil
}

func initialSetupCodexModels() []initialSetupModelDefaults {
	return []initialSetupModelDefaults{
		{Name: "gpt-5.6-sol", ContextLimit: 1050000, OutputLimit: 128000},
		{Name: "gpt-5.6-terra", ContextLimit: 1050000, OutputLimit: 128000},
		{Name: "gpt-5.6-luna", ContextLimit: 1050000, OutputLimit: 128000},
		{Name: "gpt-5.2", ContextLimit: 400000, InputLimit: 272000, OutputLimit: 128000},
		{Name: "gpt-5.3-codex", ContextLimit: 400000, InputLimit: 272000, OutputLimit: 128000},
		{Name: "gpt-5.4", ContextLimit: 1050000, InputLimit: 922000, OutputLimit: 128000},
		{Name: "gpt-5.5", ContextLimit: 400000, InputLimit: 272000, OutputLimit: 128000},
	}
}

func inferProviderTypeFromAPIURL(apiURL string) string {
	switch {
	case config.APIURLPathHasSuffix(apiURL, "/responses"):
		return "responses"
	case config.APIURLPathHasSuffix(apiURL, "/chat/completions"):
		return "chat-completions"
	case config.APIURLPathHasSuffix(apiURL, "/messages"):
		return "messages"
	case config.APIURLPathHasSuffix(apiURL, "/models"):
		return "generate-content"
	default:
		return ""
	}
}

func defaultAPIKeyEnvVar(providerName string) string {
	providerName = strings.TrimSpace(providerName)
	if providerName == "" {
		return "OPENAI_API_KEY"
	}
	var b strings.Builder
	lastUnderscore := false
	for _, r := range providerName {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			lastUnderscore = false
			continue
		}
		if !lastUnderscore {
			b.WriteByte('_')
			lastUnderscore = true
		}
	}
	name := strings.Trim(b.String(), "_")
	if name == "" {
		name = "OPENAI"
	}
	return strings.ToUpper(name) + "_API_KEY"
}

func defaultAPIURLForProviderType(providerType string) string {
	return initialSetupDefaultsForProviderType(providerType).APIURL
}

func initialSetupDefaultsForProviderType(providerType string) initialSetupEndpointDefaults {
	switch strings.TrimSpace(providerType) {
	case "chat-completions":
		return initialSetupEndpointDefaults{
			ProviderName: "openai",
			ProviderType: "chat-completions",
			APIURL:       "https://gateway.example.com/v1/chat/completions",
			ModelName:    "gpt-5.6",
			ContextLimit: 128000,
			OutputLimit:  32768,
		}
	case "messages":
		return initialSetupEndpointDefaults{
			ProviderName: "anthropic",
			ProviderType: "messages",
			APIURL:       "https://api.anthropic.com/v1/messages",
			ModelName:    "claude-opus-4.8",
			ContextLimit: 1000000,
			OutputLimit:  64000,
		}
	case "generate-content":
		return initialSetupEndpointDefaults{
			ProviderName: "gemini",
			ProviderType: "generate-content",
			APIURL:       "https://generativelanguage.googleapis.com/v1beta/models",
			ModelName:    "gemini-3.5-flash",
			ContextLimit: 1048576,
			OutputLimit:  65536,
		}
	case "responses":
		fallthrough
	default:
		return initialSetupEndpointDefaults{
			ProviderName: "openai",
			ProviderType: "responses",
			APIURL:       "https://api.openai.com/v1/responses",
			ModelName:    "gpt-5.6",
			ContextLimit: 1050000,
			OutputLimit:  128000,
		}
	}
}

func initialSetupDefaultsForAPIURL(apiURL string) initialSetupEndpointDefaults {
	return initialSetupDefaultsForProviderType(inferProviderTypeFromAPIURL(apiURL))
}
