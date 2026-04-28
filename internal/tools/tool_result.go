package tools

import (
	"fmt"
	"strings"
)

func NormalizeEmptySuccessOutput(toolName, result string, err error) string {
	if err != nil || result != "" {
		return result
	}
	name := strings.TrimSpace(toolName)
	if name == "" {
		name = "Tool"
	}
	return fmt.Sprintf("(%s completed with no output)", name)
}

func AppendArtifactGuidance(content string, truncated TruncateResult, guidance string) string {
	if !truncated.Truncated {
		return content
	}
	extra := make([]string, 0, 2)
	if ref := strings.TrimSpace(truncated.ArtifactReference); ref != "" && !strings.Contains(content, ref) {
		extra = append(extra, ref)
	}
	if guidance = strings.TrimSpace(guidance); guidance != "" && !strings.Contains(content, guidance) {
		extra = append(extra, guidance)
	}
	if len(extra) == 0 {
		return content
	}
	if strings.TrimSpace(content) == "" {
		return strings.Join(extra, "\n")
	}
	return content + "\n\n" + strings.Join(extra, "\n")
}
