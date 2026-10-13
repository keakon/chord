package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/url"
	"slices"
	"strings"
	"unicode"

	"gopkg.in/yaml.v3"
)

const (
	MCPAuthAuto                         = "auto"
	MCPAuthOAuth                        = "oauth"
	MCPAuthNone                         = "none"
	MCPOAuthAuthMethodNone              = "none"
	MCPOAuthAuthMethodClientSecretBasic = "client_secret_basic"
	MCPOAuthAuthMethodClientSecretPost  = "client_secret_post"
	DefaultMCPOAuthCallbackPort         = 41937
)

// MCPOAuthConfig describes managed OAuth without containing client secrets.
type MCPOAuthConfig struct {
	Issuer                  string   `json:"issuer,omitempty" yaml:"issuer,omitempty"`
	ClientID                string   `json:"client_id,omitempty" yaml:"client_id,omitempty"`
	TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method,omitempty" yaml:"token_endpoint_auth_method,omitempty"`
	ClientSecretEnv         string   `json:"client_secret_env,omitempty" yaml:"client_secret_env,omitempty"`
	Scopes                  []string `json:"scopes,omitempty" yaml:"scopes,omitempty"`
	CallbackPort            *int     `json:"callback_port,omitempty" yaml:"callback_port,omitempty"`
}

// ValidateMCPAuth rejects invalid connection and authentication combinations.
// Environment secrets are deliberately not read during configuration validation.
func ValidateMCPAuth(servers MCPConfig) error {
	for _, name := range slices.Sorted(maps.Keys(servers)) {
		if err := validateMCPServerAuth(servers[name]); err != nil {
			return fmt.Errorf("MCP server %q: %w", name, err)
		}
	}
	return nil
}

func validateMCPServerAuth(s MCPServerConfig) error {
	if (s.Command != "" && strings.TrimSpace(s.Command) == "") || (s.URL != "" && strings.TrimSpace(s.URL) == "") {
		return fmt.Errorf("command and url must not be blank")
	}
	if (strings.TrimSpace(s.Command) == "") == (strings.TrimSpace(s.URL) == "") {
		return fmt.Errorf("exactly one of command or url is required")
	}
	mode := s.Auth
	if mode == "" {
		mode = MCPAuthAuto
	}
	if mode != MCPAuthAuto && mode != MCPAuthOAuth && mode != MCPAuthNone {
		return fmt.Errorf("auth must be auto, oauth, or none")
	}
	if s.Command != "" && (mode == MCPAuthOAuth || s.OAuth != nil) {
		return fmt.Errorf("stdio does not support managed OAuth")
	}
	if mode == MCPAuthNone && s.OAuth != nil {
		return fmt.Errorf("auth none does not allow an oauth block")
	}
	for key := range s.Headers {
		if strings.EqualFold(key, "Authorization") && (mode == MCPAuthOAuth || (mode == MCPAuthAuto && s.OAuth != nil)) {
			return fmt.Errorf("static Authorization conflicts with managed OAuth configuration")
		}
	}
	o := s.OAuth
	if o == nil {
		return nil
	}
	if o.Issuer != "" {
		u, err := url.Parse(o.Issuer)
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.Fragment != "" || u.RawQuery != "" {
			return fmt.Errorf("oauth issuer must be an absolute HTTP(S) URL without userinfo, query, or fragment")
		}
	}
	if o.ClientID != "" && o.Issuer == "" {
		return fmt.Errorf("oauth client_id requires issuer")
	}
	if o.ClientID == "" && (o.ClientSecretEnv != "" || o.TokenEndpointAuthMethod != "") {
		return fmt.Errorf("oauth client_secret_env and token_endpoint_auth_method require client_id")
	}
	method := o.TokenEndpointAuthMethod
	if method == "" {
		method = MCPOAuthAuthMethodNone
	}
	switch method {
	case MCPOAuthAuthMethodNone:
		if o.ClientSecretEnv != "" {
			return fmt.Errorf("oauth auth method none does not allow client_secret_env")
		}
	case MCPOAuthAuthMethodClientSecretBasic, MCPOAuthAuthMethodClientSecretPost:
		if o.ClientSecretEnv == "" {
			return fmt.Errorf("oauth client_secret_env is required for the configured authentication method")
		}
	default:
		return fmt.Errorf("unsupported oauth token_endpoint_auth_method")
	}
	if strings.ContainsAny(o.ClientSecretEnv, "=\x00") || strings.IndexFunc(o.ClientSecretEnv, unicode.IsSpace) >= 0 {
		return fmt.Errorf("oauth client_secret_env must be an environment variable name")
	}
	if o.CallbackPort != nil && (*o.CallbackPort < 1 || *o.CallbackPort > 65535) {
		return fmt.Errorf("oauth callback_port must be between 1 and 65535")
	}
	for _, scope := range o.Scopes {
		if scope == "" || strings.IndexFunc(scope, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 {
			return fmt.Errorf("oauth scopes must be nonempty and contain no whitespace or control characters")
		}
	}
	return nil
}

// NormalizeMCPAuth returns an independent configuration with effective defaults
// and canonical scope ordering. A missing callback port remains distinguishable
// from an explicitly configured port; consumers use DefaultMCPOAuthCallbackPort.
func NormalizeMCPAuth(server MCPServerConfig) (MCPServerConfig, error) {
	if err := validateMCPServerAuth(server); err != nil {
		return MCPServerConfig{}, err
	}
	server.Args = slices.Clone(server.Args)
	server.Env = slices.Clone(server.Env)
	server.AllowedTools = slices.Clone(server.AllowedTools)
	server.Headers = maps.Clone(server.Headers)
	if server.Auth == "" {
		server.Auth = MCPAuthAuto
	}
	if server.OAuth != nil {
		oauth := *server.OAuth
		oauth.Scopes = slices.Clone(oauth.Scopes)
		slices.Sort(oauth.Scopes)
		oauth.Scopes = slices.Compact(oauth.Scopes)
		if oauth.ClientID != "" && oauth.TokenEndpointAuthMethod == "" {
			oauth.TokenEndpointAuthMethod = MCPOAuthAuthMethodNone
		}
		if oauth.CallbackPort != nil {
			oauth.CallbackPort = new(*oauth.CallbackPort)
		}
		server.OAuth = &oauth
	}
	return server, nil
}

// MCP configuration cannot participate in tolerant field dropping: a mistyped
// authentication field must never turn a managed connection into an anonymous one.
func validateMCPConfigYAML(data []byte) error {
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return err
	}
	if len(root.Content) == 0 || root.Content[0].Kind != yaml.MappingNode {
		return nil
	}
	hasMCP := false
	for i := 0; i+1 < len(root.Content[0].Content); i += 2 {
		if root.Content[0].Content[i].Value == "mcp" {
			hasMCP = true
			break
		}
	}
	if !hasMCP {
		return nil
	}

	var envelope struct {
		MCP   MCPConfig      `yaml:"mcp"`
		Other map[string]any `yaml:",inline"`
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&envelope); err != nil {
		if errors.Is(err, io.EOF) {
			return nil
		}
		return fmt.Errorf("invalid MCP configuration: %w", err)
	}
	return ValidateMCPAuth(envelope.MCP)
}
