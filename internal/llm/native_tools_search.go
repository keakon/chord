package llm

import (
	"encoding/json"
	"fmt"
	"slices"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/toolname"
)

// nativeWebSearchRequest is the adapter for the explicitly supported search
// contracts. Provider-specific configuration never enters the execution loop.
func nativeWebSearchRequest(cfg *config.NativeWebSearchConfig, provider *ProviderConfig) (*nativeToolRequest, error) {
	if err := cfg.Validate(provider.Type(), provider.APIURL()); err != nil {
		return nil, err
	}
	snapshot := *cfg
	snapshot.AllowedDomains = slices.Clone(cfg.AllowedDomains)
	snapshot.BlockedDomains = slices.Clone(cfg.BlockedDomains)
	if snapshot.MaxUses == 0 {
		snapshot.MaxUses = 8
	}
	constraints, err := json.Marshal(struct {
		AllowedDomains []string `json:"allowed_domains,omitempty"`
		BlockedDomains []string `json:"blocked_domains,omitempty"`
		MaxUses        int      `json:"max_uses"`
	}{snapshot.AllowedDomains, snapshot.BlockedDomains, snapshot.MaxUses})
	if err != nil {
		return nil, fmt.Errorf("encode native search constraints: %w", err)
	}
	return &nativeToolRequest{
		authorization:    message.NativeToolAuthorization{Tool: toolname.WebSearch, Contract: snapshot.Contract, Constraints: constraints},
		formatContent:    func(history *message.NativeToolHistory, content string) string { return history.CitedContent(content) },
		applyDeclaration: func(raw []byte) ([]byte, error) { return addNativeWebSearchDeclaration(raw, &snapshot) },
	}, nil
}

// addNativeWebSearchDeclaration edits only the request projection. Authorization
// has already been bound to this endpoint and immutable constraint snapshot.
func addNativeWebSearchDeclaration(raw []byte, cfg *config.NativeWebSearchConfig) ([]byte, error) {
	if cfg == nil {
		return raw, nil
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil, err
	}
	var defs []json.RawMessage
	if err := json.Unmarshal(body["tools"], &defs); err != nil && len(body["tools"]) > 0 {
		return nil, err
	}
	decl := map[string]any{"type": toolname.WebSearch}
	if cfg.Contract == config.NativeWebSearchMessages {
		decl["type"] = "web_search_20250305"
		decl["name"] = toolname.WebSearch
		decl["max_uses"] = cfg.MaxUses
		if len(cfg.AllowedDomains) > 0 {
			decl["allowed_domains"] = cfg.AllowedDomains
		}
		if len(cfg.BlockedDomains) > 0 {
			decl["blocked_domains"] = cfg.BlockedDomains
		}
	} else {
		filters := map[string]any{}
		if len(cfg.AllowedDomains) > 0 {
			filters["allowed_domains"] = cfg.AllowedDomains
		}
		if len(cfg.BlockedDomains) > 0 {
			filters["blocked_domains"] = cfg.BlockedDomains
		}
		if len(filters) > 0 {
			decl["filters"] = filters
		}
		body["max_tool_calls"], _ = json.Marshal(cfg.MaxUses)
		var include []string
		_ = json.Unmarshal(body["include"], &include)
		include = append(include, "web_search_call.action.sources")
		body["include"], _ = json.Marshal(include)
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
	return json.Marshal(body)
}
