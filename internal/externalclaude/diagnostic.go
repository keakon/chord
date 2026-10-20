package externalclaude

import (
	"os"
	"strings"
)

// RedactDiagnostic removes authentication values from inherited and configured
// environment entries before CLI diagnostics cross into the host log.
func RedactDiagnostic(line string, overrides map[string]string) string {
	for _, entry := range os.Environ() {
		key, value, _ := strings.Cut(entry, "=")
		if sensitiveEnvironmentKey(key) && value != "" {
			line = strings.ReplaceAll(line, value, "[redacted]")
		}
	}
	for key, value := range overrides {
		if sensitiveEnvironmentKey(key) && value != "" {
			line = strings.ReplaceAll(line, value, "[redacted]")
		}
	}
	return line
}

func sensitiveEnvironmentKey(key string) bool {
	return strings.Contains(key, "TOKEN") || strings.Contains(key, "KEY") || strings.Contains(key, "SECRET") ||
		strings.Contains(key, "PASSWORD") || strings.HasSuffix(key, "_PROXY") || strings.HasSuffix(key, "_proxy")
}
