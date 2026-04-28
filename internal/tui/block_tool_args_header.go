package tui

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/keakon/chord/internal/tools"
)

func bashDescriptionSummary(vals map[string]string) string {
	if vals == nil {
		return ""
	}
	return strings.TrimSpace(vals["description"])
}

func bashSummaryParts(vals map[string]string) (mainPart, grayPart string) {
	if vals == nil {
		return "", ""
	}
	if desc := bashDescriptionSummary(vals); desc != "" {
		mainPart = desc
	} else {
		mainPart = firstDisplayLine(vals["command"])
	}
	if mainPart == "" {
		return "", ""
	}
	return mainPart, bashHeaderGrayPart(vals)
}

func bashCommandLines(command string) []string {
	command = strings.ReplaceAll(command, "\r\n", "\n")
	command = strings.ReplaceAll(command, "\r", "\n")
	if command == "" {
		return nil
	}
	return strings.Split(command, "\n")
}

func bashCommandPreviewLines(command string, maxLines int) (lines []string, hidden int) {
	all := bashCommandLines(command)
	if len(all) == 0 {
		return nil, 0
	}
	if maxLines <= 0 || len(all) <= maxLines {
		return all, 0
	}
	return all[:maxLines], len(all) - maxLines
}

func formatCollapsedBashHeaderPartsWithParsed(keys []string, vals map[string]string) (mainPart, grayPart string, ok bool) {
	if len(keys) == 0 {
		return "", "", false
	}
	mainPart, grayPart = bashSummaryParts(vals)
	if mainPart == "" {
		return "", "", false
	}
	return mainPart, grayPart, true
}

func bashHeaderGrayPart(vals map[string]string) string {
	timeoutInfo := tools.ResolveBashTimeoutValue(parseBashTimeoutValue(vals["timeout"]), vals["timeout"] != "")
	var opts []string
	if timeoutInfo.HasLimit && !timeoutInfo.UsesDefault {
		if timeoutInfo.Clamped {
			opts = append(opts, fmt.Sprintf("timeout=%d→%d", timeoutInfo.RequestedSec, timeoutInfo.EffectiveSec))
		} else {
			opts = append(opts, fmt.Sprintf("timeout=%d", timeoutInfo.EffectiveSec))
		}
	}
	if len(opts) == 0 {
		return ""
	}
	return "(" + strings.Join(opts, ", ") + ")"
}

func parseBashTimeoutValue(raw string) int {
	if raw == "" {
		return 0
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		return 0
	}
	return v
}

func parseDeleteHeaderPaths(vals map[string]string) []string {
	if raw := strings.TrimSpace(vals["paths"]); raw != "" {
		var parsed []string
		if err := json.Unmarshal([]byte(raw), &parsed); err == nil {
			out := make([]string, 0, len(parsed))
			for _, path := range parsed {
				path = strings.TrimSpace(path)
				if path != "" {
					out = append(out, path)
				}
			}
			if len(out) > 0 {
				return out
			}
		}
		for _, line := range strings.Split(raw, "\n") {
			line = strings.TrimSpace(strings.TrimPrefix(line, "- "))
			if line != "" {
				return []string{line}
			}
		}
	}
	return nil
}
