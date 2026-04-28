package mcp

import (
	"strings"
	"unicode"
)

// RegisteredMCPToolName is the tool id exposed to the LLM and TUI, distinct
// from built-in tools (e.g. mcp_exa_web_search vs Read).
func RegisteredMCPToolName(serverKey, remoteToolName string) string {
	sk := sanitizeMCPToken(serverKey)
	rt := sanitizeMCPToken(remoteToolName)
	if sk == "" {
		sk = "srv"
	}
	if rt == "" {
		rt = "tool"
	}
	return "mcp_" + sk + "_" + rt
}

func sanitizeMCPToken(s string) string {
	var b strings.Builder
	prevUnderscore := false
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			prevUnderscore = false
			continue
		}
		if !prevUnderscore && b.Len() > 0 {
			b.WriteByte('_')
			prevUnderscore = true
		}
	}
	out := strings.Trim(b.String(), "_")
	for strings.Contains(out, "__") {
		out = strings.ReplaceAll(out, "__", "_")
	}
	return out
}
