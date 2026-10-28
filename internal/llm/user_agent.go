package llm

import (
	"strings"

	"github.com/keakon/chord/internal/buildinfo"
)

func defaultLLMUserAgent() string {
	version := sanitizeUserAgentProductVersion(buildinfo.Current().Version)
	if version == "" {
		version = sanitizeUserAgentProductVersion(buildinfo.DefaultDevVersion)
	}
	return "chord/" + version
}

func sanitizeUserAgentProductVersion(version string) string {
	version = strings.TrimSpace(version)
	if version == "" {
		return ""
	}
	var b strings.Builder
	for _, r := range version {
		if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '.' || r == '-' || r == '_' || r == '+' || r == '~' {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	return b.String()
}

func setDefaultLLMUserAgent(headerSetter interface{ Set(string, string) }) {
	headerSetter.Set(headerUserAgent, defaultLLMUserAgent())
}

// setProviderLLMUserAgent sets the User-Agent for one request: an explicit
// provider user_agent wins, then the client identity the request's emulate
// selector resolves to (model override first), then Chord's own agent. An empty
// model resolves the provider-level selector only.
func setProviderLLMUserAgent(headerSetter interface{ Set(string, string) }, provider *ProviderConfig, model string) {
	if provider != nil {
		if userAgent := provider.UserAgent(); userAgent != "" {
			headerSetter.Set(headerUserAgent, userAgent)
			return
		}
		// A request presenting as the Codex client gets that client's own
		// header; an explicit provider user_agent still wins.
		if userAgent := provider.codexEmulationUserAgent(model); userAgent != "" {
			headerSetter.Set(headerUserAgent, userAgent)
			return
		}
		// The Claude Code agent is the Messages-wire counterpart of the Codex
		// one and mutually exclusive with it by provider type.
		if userAgent := provider.claudeEmulationUserAgent(model); userAgent != "" {
			headerSetter.Set(headerUserAgent, userAgent)
			return
		}
	}
	setDefaultLLMUserAgent(headerSetter)
}
