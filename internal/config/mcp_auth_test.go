package config

import (
	"strings"
	"testing"
)

func TestValidateMCPAuthCombinations(t *testing.T) {
	tests := []struct {
		name   string
		server MCPServerConfig
		valid  bool
	}{
		{"stdio", MCPServerConfig{Command: "sample"}, true},
		{"anonymous", MCPServerConfig{URL: "https://example.invalid/mcp"}, true},
		{"managed", MCPServerConfig{URL: "https://example.invalid/mcp", Auth: MCPAuthOAuth}, true},
		{"empty oauth", MCPServerConfig{URL: "https://example.invalid/mcp", OAuth: &MCPOAuthConfig{}}, true},
		{"static", MCPServerConfig{URL: "https://example.invalid/mcp", Headers: map[string]string{"authorization": "$SAMPLE_TOKEN"}}, true},
		{"neither", MCPServerConfig{}, false},
		{"both", MCPServerConfig{URL: "https://example.invalid/mcp", Command: "sample"}, false},
		{"unknown auth", MCPServerConfig{URL: "https://example.invalid/mcp", Auth: "invalid"}, false},
		{"stdio oauth", MCPServerConfig{Command: "sample", Auth: MCPAuthOAuth}, false},
		{"stdio block", MCPServerConfig{Command: "sample", OAuth: &MCPOAuthConfig{}}, false},
		{"none block", MCPServerConfig{URL: "https://example.invalid/mcp", Auth: MCPAuthNone, OAuth: &MCPOAuthConfig{}}, false},
		{"static managed", MCPServerConfig{URL: "https://example.invalid/mcp", Auth: MCPAuthOAuth, Headers: map[string]string{"aUtHoRiZaTiOn": "sample"}}, false},
		{"static auto block", MCPServerConfig{URL: "https://example.invalid/mcp", OAuth: &MCPOAuthConfig{}, Headers: map[string]string{"Authorization": "sample"}}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateMCPAuth(MCPConfig{"sample": tt.server})
			if (err == nil) != tt.valid {
				t.Fatalf("validation = %v, valid = %v", err, tt.valid)
			}
		})
	}
}

func TestValidateMCPOAuthFields(t *testing.T) {
	tests := []struct {
		name  string
		oauth MCPOAuthConfig
		valid bool
	}{
		{"issuer only", MCPOAuthConfig{Issuer: "https://auth.example.invalid"}, true},
		{"pre registered", MCPOAuthConfig{Issuer: "https://auth.example.invalid", ClientID: "sample"}, true},
		{"secret not resolved", MCPOAuthConfig{Issuer: "https://auth.example.invalid", ClientID: "sample", TokenEndpointAuthMethod: MCPOAuthAuthMethodClientSecretBasic, ClientSecretEnv: "CHORD_SAMPLE_UNSET_SECRET"}, true},
		{"issuer required", MCPOAuthConfig{ClientID: "sample"}, false},
		{"client required", MCPOAuthConfig{TokenEndpointAuthMethod: MCPOAuthAuthMethodNone}, false},
		{"secret client required", MCPOAuthConfig{ClientSecretEnv: "SAMPLE_SECRET"}, false},
		{"secret required", MCPOAuthConfig{Issuer: "https://auth.example.invalid", ClientID: "sample", TokenEndpointAuthMethod: MCPOAuthAuthMethodClientSecretPost}, false},
		{"secret forbidden", MCPOAuthConfig{Issuer: "https://auth.example.invalid", ClientID: "sample", ClientSecretEnv: "SAMPLE_SECRET"}, false},
		{"unknown method", MCPOAuthConfig{Issuer: "https://auth.example.invalid", ClientID: "sample", TokenEndpointAuthMethod: "invalid"}, false},
		{"zero port", MCPOAuthConfig{CallbackPort: new(0)}, false},
		{"high port", MCPOAuthConfig{CallbackPort: new(65536)}, false},
		{"negative port", MCPOAuthConfig{CallbackPort: new(-1)}, false},
		{"valid port", MCPOAuthConfig{CallbackPort: new(65535)}, true},
		{"empty scope", MCPOAuthConfig{Scopes: []string{""}}, false},
		{"space scope", MCPOAuthConfig{Scopes: []string{"files read"}}, false},
		{"control scope", MCPOAuthConfig{Scopes: []string{"files\x00read"}}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateMCPAuth(MCPConfig{"sample": {URL: "https://example.invalid/mcp", OAuth: &tt.oauth}})
			if (err == nil) != tt.valid {
				t.Fatalf("validation = %v, valid = %v", err, tt.valid)
			}
		})
	}
}

func TestNormalizeMCPAuthOwnsConfiguration(t *testing.T) {
	original := MCPServerConfig{URL: "https://example.invalid/mcp", Headers: map[string]string{"X-Sample": "original"}, OAuth: &MCPOAuthConfig{Issuer: "https://auth.example.invalid", ClientID: "sample", Scopes: []string{"z", "a", "z"}, CallbackPort: new(1234)}}
	normalized, err := NormalizeMCPAuth(original)
	if err != nil {
		t.Fatal(err)
	}
	if normalized.Auth != MCPAuthAuto || normalized.OAuth.TokenEndpointAuthMethod != MCPOAuthAuthMethodNone || strings.Join(normalized.OAuth.Scopes, ",") != "a,z" {
		t.Fatalf("unexpected normalized configuration: %#v", normalized)
	}
	normalized.Headers["X-Sample"] = "changed"
	normalized.OAuth.Scopes[0] = "changed"
	*normalized.OAuth.CallbackPort = 4321
	if original.Headers["X-Sample"] != "original" || original.OAuth.Scopes[0] != "z" || *original.OAuth.CallbackPort != 1234 {
		t.Fatal("normalization aliases input")
	}
}

func TestMCPAuthenticationConfigFailsClosed(t *testing.T) {
	for _, fields := range []string{"auth: invalid", "auth: [oauth]", "oauth: {callback_port: 0}", "oauth: {callback_port: wrong}", "oauth: {unknown: true}", "oauth: {scopes: ['bad scope']}"} {
		t.Run(fields, func(t *testing.T) {
			data := []byte("mcp:\n  sample:\n    url: https://example.invalid/mcp\n    " + fields + "\n")
			if _, err := loadConfigData("sample.yaml", data, false, nil); err == nil {
				t.Fatal("invalid authentication accepted by loader")
			}
			if _, _, err := stripTypeInvalidOverride("sample.yaml", data); err == nil {
				t.Fatal("invalid authentication accepted by project sanitizer")
			}
		})
	}
	data := []byte("mcp:\n  sample:\n    url: https://example.invalid/mcp\n    oauth: {}\n")
	cfg, err := loadConfigData("sample.yaml", data, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MCP["sample"].OAuth == nil || cfg.MCP["sample"].OAuth.CallbackPort != nil || cfg.MCP["sample"].Auth != MCPAuthAuto {
		t.Fatal("configuration presence/defaults not preserved")
	}
	if err := ValidateAgentMCP(map[string]*AgentConfig{"worker": {MCP: MCPConfig{"sample": {URL: "https://example.invalid/mcp", Auth: "invalid"}}}}, nil); err == nil {
		t.Fatal("private agent authentication accepted")
	}
}
