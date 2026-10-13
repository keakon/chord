package mcp

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"
	"sync/atomic"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/permission"
)

// Target is a resolved connection configuration. Consumers must not mutate it;
// Snapshot creates an independent copy of all configurable collections.
type Target struct {
	Ref              ServerRef
	Config           ServerConfig
	TargetID         string
	ProfileID        string
	ConfigGeneration uint64
}

func (t Target) Snapshot() Target { t.Config = cloneServerConfig(t.Config); return t }

func cloneServerConfig(c ServerConfig) ServerConfig {
	c.Args = slices.Clone(c.Args)
	c.Env = slices.Clone(c.Env)
	c.Headers = maps.Clone(c.Headers)
	c.AllowedTools = slices.Clone(c.AllowedTools)
	if c.OAuth != nil {
		o := *c.OAuth
		o.Scopes = slices.Clone(o.Scopes)
		if o.CallbackPort != nil {
			o.CallbackPort = new(*o.CallbackPort)
		}
		c.OAuth = &o
	}
	return c
}

// ResolveTargetOptions supplies already merged configurations and the effective
// ruleset of the selected owner. Agent selection includes inherited main servers.
type ResolveTargetOptions struct {
	Main        config.MCPConfig
	Agents      map[string]*config.AgentConfig
	Agent       string
	Ruleset     permission.Ruleset
	ContentRoot string
	ConfigHome  string
}

var targetGeneration atomic.Uint64

func ResolveTargets(ctx context.Context, opts ResolveTargetOptions) ([]Target, error) {
	if err := config.ValidateMCPAuth(opts.Main); err != nil {
		return nil, err
	}
	if err := config.ValidateAgentMCP(opts.Agents, opts.Main); err != nil {
		return nil, err
	}
	if opts.Agent != "" && opts.Agents[opts.Agent] == nil {
		return nil, fmt.Errorf("unknown MCP agent %q", opts.Agent)
	}
	workspace, err := ResolveWorkspaceID(ctx, opts.ContentRoot)
	if err != nil {
		return nil, err
	}
	type ownedConfig struct {
		owner, name string
		cfg         config.MCPServerConfig
	}
	entries := make([]ownedConfig, 0, len(opts.Main))
	for name, cfg := range opts.Main {
		entries = append(entries, ownedConfig{MainOwner, name, cfg})
	}
	if opts.Agent != "" {
		for name, cfg := range opts.Agents[opts.Agent].MCP {
			entries = append(entries, ownedConfig{"agent:" + opts.Agent, name, cfg})
		}
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].owner != entries[j].owner {
			return entries[i].owner < entries[j].owner
		}
		return entries[i].name < entries[j].name
	})
	generation := targetGeneration.Add(1)
	targets := make([]Target, 0, len(entries))
	for _, entry := range entries {
		prefix := strings.TrimSuffix(RegisteredMCPToolName(entry.name, ""), "tool")
		var nested []string
		for _, other := range entries {
			p := strings.TrimSuffix(RegisteredMCPToolName(other.name, ""), "tool")
			if p != prefix && strings.HasPrefix(p, prefix) {
				nested = append(nested, p)
			}
		}
		if opts.Ruleset.DeniesAllWithPrefix(prefix, nested...) {
			continue
		}
		cfg, err := config.NormalizeMCPAuth(entry.cfg)
		if err != nil {
			return nil, err
		}
		converted := ServerConfigsFromConfig(config.MCPConfig{entry.name: cfg})[0]
		t := Target{Ref: ServerRef{workspace, entry.owner, entry.name}, Config: converted, ConfigGeneration: generation}
		if cfg.URL != "" {
			endpoint, err := CanonicalEndpoint(cfg.URL)
			if err != nil {
				return nil, fmt.Errorf("MCP server %q: %w", entry.name, err)
			}
			t.Config.URL = endpoint
			t.TargetID = identityHash(struct{ Workspace, Owner, Endpoint string }{workspace, entry.owner, endpoint})
			p := profileForTarget(t)
			t.ProfileID = identityHash(p)
		}
		targets = append(targets, t)
	}
	return targets, nil
}

type credentialProfile struct {
	IssuerHint         string `json:"issuer_hint"`
	RegistrationPolicy string `json:"registration_policy"`
	ConfiguredClientID string `json:"configured_client_id"`
	TokenAuthMethod    string `json:"token_auth_method"`
	ClientSecretEnv    string `json:"client_secret_env"`
	RedirectURI        string `json:"redirect_uri"`
}

func profileForTarget(t Target) credentialProfile {
	p := credentialProfile{RegistrationPolicy: "automatic", TokenAuthMethod: config.MCPOAuthAuthMethodNone, RedirectURI: fmt.Sprintf("http://127.0.0.1:%d/oauth/callback", config.DefaultMCPOAuthCallbackPort)}
	if t.Config.OAuth != nil {
		p.IssuerHint = t.Config.OAuth.Issuer
		p.ConfiguredClientID = t.Config.OAuth.ClientID
		p.ClientSecretEnv = t.Config.OAuth.ClientSecretEnv
		if t.Config.OAuth.TokenEndpointAuthMethod != "" {
			p.TokenAuthMethod = t.Config.OAuth.TokenEndpointAuthMethod
		}
		if t.Config.OAuth.CallbackPort != nil {
			p.RedirectURI = fmt.Sprintf("http://127.0.0.1:%d/oauth/callback", *t.Config.OAuth.CallbackPort)
		}
		if p.ConfiguredClientID != "" {
			p.RegistrationPolicy = "pre_registered"
		}
	}
	return p
}
