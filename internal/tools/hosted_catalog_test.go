package tools

import (
	"testing"

	"github.com/keakon/chord/internal/config"
)

func TestHostedCatalogValidatesResolvedDeclarations(t *testing.T) {
	valid := func() config.HostedToolConfig {
		return config.HostedToolConfig{Declarations: map[string]config.HostedToolDeclarationConfig{
			config.ProviderTypeResponses: {Tool: map[string]any{"type": "sample_tool"}},
		}}
	}
	for _, tc := range []struct {
		name   string
		mutate func(*config.HostedToolConfig)
	}{
		{"missing declaration", func(c *config.HostedToolConfig) { c.Declarations = nil }},
		{"negative timeout", func(c *config.HostedToolConfig) { c.TimeoutSeconds = -1 }},
		{"unsupported family", func(c *config.HostedToolConfig) {
			c.Declarations[config.ProviderTypeChatCompletions] = c.Declarations[config.ProviderTypeResponses]
		}},
		{"empty tool", func(c *config.HostedToolConfig) {
			c.Declarations[config.ProviderTypeResponses] = config.HostedToolDeclarationConfig{Tool: map[string]any{}}
		}},
		{"non-string type", func(c *config.HostedToolConfig) {
			c.Declarations[config.ProviderTypeResponses].Tool["type"] = 1
		}},
		{"non-object schema", func(c *config.HostedToolConfig) { c.Parameters = map[string]any{"type": "array"} }},
		{"non-JSON schema", func(c *config.HostedToolConfig) {
			c.Parameters = map[string]any{"type": "object", "invalid": make(chan int)}
		}},
		{"non-JSON declaration", func(c *config.HostedToolConfig) {
			c.Declarations[config.ProviderTypeResponses].Tool["invalid"] = make(chan int)
		}},
		{"invalid force", func(c *config.HostedToolConfig) {
			decl := c.Declarations[config.ProviderTypeResponses]
			decl.Force = true
			c.Declarations[config.ProviderTypeResponses] = decl
		}},
		{"protected header", func(c *config.HostedToolConfig) {
			decl := c.Declarations[config.ProviderTypeResponses]
			decl.Headers = map[string]string{"aUtHoRiZaTiOn": "sample"}
			c.Declarations[config.ProviderTypeResponses] = decl
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			entry := valid()
			tc.mutate(&entry)
			if err := ValidateHostedToolCatalog(map[string]config.HostedToolConfig{"sample_tool": entry}, nil); err == nil {
				t.Fatal("invalid catalog accepted")
			}
		})
	}
	if err := ValidateHostedToolCatalog(map[string]config.HostedToolConfig{
		"sample_tool": valid(),
		NameWebSearch: {Declarations: map[string]config.HostedToolDeclarationConfig{
			config.ProviderTypeMessages: {Headers: map[string]string{"Anthropic-Beta": "sample-beta"}},
		}},
	}, nil); err != nil {
		t.Fatalf("valid catalog with partial built-in override: %v", err)
	}
}

func TestHostedDeclarationHeadersMergeCaseInsensitively(t *testing.T) {
	decl := mergeHostedDeclaration(
		config.HostedToolDeclarationConfig{Headers: map[string]string{"anthropic-beta": "base", "x-sample": "keep"}},
		config.HostedToolDeclarationConfig{Headers: map[string]string{"Anthropic-Beta": "override"}},
	)
	if len(decl.Headers) != 2 || decl.Headers["Anthropic-Beta"] != "override" || decl.Headers["X-Sample"] != "keep" {
		t.Fatalf("headers = %#v", decl.Headers)
	}
}
