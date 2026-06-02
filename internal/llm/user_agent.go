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

func setProviderLLMUserAgent(headerSetter interface{ Set(string, string) }, provider *ProviderConfig) {
	if provider != nil {
		if userAgent := provider.UserAgent(); userAgent != "" {
			headerSetter.Set(headerUserAgent, userAgent)
			return
		}
	}
	setDefaultLLMUserAgent(headerSetter)
}
