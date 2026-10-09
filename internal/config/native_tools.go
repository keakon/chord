package config

import (
	"fmt"
	"strings"
)

const (
	NativeWebSearchResponses = "openai.responses.web_search"
	NativeWebSearchMessages  = "anthropic.messages.web_search_20250305"
)

// NativeWebSearchConfig is explicit request-scope authorization for one exact
// model endpoint. It is never borrowed from catalog config profiles or compat.
// The model chooses queries; these domain constraints apply before execution.
type NativeWebSearchConfig struct {
	Contract       string   `json:"contract" yaml:"contract"`
	APIURL         string   `json:"api_url" yaml:"api_url"`
	Preauthorized  bool     `json:"preauthorized" yaml:"preauthorized"`
	AllowedDomains []string `json:"allowed_domains,omitempty" yaml:"allowed_domains,omitempty"`
	BlockedDomains []string `json:"blocked_domains,omitempty" yaml:"blocked_domains,omitempty"`
	MaxUses        int      `json:"max_uses,omitempty" yaml:"max_uses,omitempty"`
}

func (c NativeWebSearchConfig) Validate(protocol, endpoint string) error {
	if c.APIURL == "" || c.APIURL != endpoint {
		return fmt.Errorf("native web search authorization does not match the request endpoint")
	}
	if (protocol != ProviderTypeResponses || c.Contract != NativeWebSearchResponses) && (protocol != ProviderTypeMessages || c.Contract != NativeWebSearchMessages) {
		return fmt.Errorf("native web search contract does not match the request protocol")
	}
	if len(c.AllowedDomains) > 0 && len(c.BlockedDomains) > 0 {
		return fmt.Errorf("native web search domain filters are mutually exclusive")
	}
	if c.MaxUses < 0 || c.MaxUses > 8 {
		return fmt.Errorf("native web search max_uses must be between 1 and 8, or omitted")
	}
	for _, domains := range [][]string{c.AllowedDomains, c.BlockedDomains} {
		if len(domains) > 100 {
			return fmt.Errorf("native web search accepts at most 100 domains")
		}
		for _, domain := range domains {
			if !strings.Contains(domain, ".") || strings.ContainsAny(domain, "/:?#@ \t\r\n*") || strings.HasPrefix(domain, ".") || strings.HasSuffix(domain, ".") || strings.Contains(domain, "..") {
				return fmt.Errorf("native web search requires bare domains")
			}
		}
	}
	return nil
}
