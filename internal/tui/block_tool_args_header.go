package tui

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/keakon/chord/internal/tools"
	"github.com/keakon/chord/internal/tui/markdownutil"
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
	command = markdownutil.NormalizeNewlines(command)
	if command == "" {
		return nil
	}
	return strings.Split(command, "\n")
}

func bashCommandPreviewLines(command string, maxLines int) []string {
	all := bashCommandLines(command)
	if len(all) == 0 {
		return nil
	}
	if maxLines <= 0 || len(all) <= maxLines {
		return all
	}
	return all[:maxLines]
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
	var opts []string
	background := vals["run_in_background"] == "true"
	maxMs := tools.ShellMaxTimeoutMs
	if background {
		opts = append(opts, "background")
		maxMs = tools.ShellMaxBackgroundTimeoutMs
	}
	if raw := strings.TrimSpace(vals["timeout_ms"]); raw != "" {
		if ms, err := strconv.Atoi(raw); err == nil {
			switch {
			case ms <= 0:
				opts = append(opts, "no deadline")
			case ms > maxMs:
				// The runtime caps the deadline, so the header must not claim
				// the requested value as the effective one.
				opts = append(opts, "timeout="+formatToolMs(ms)+"→"+formatToolMs(maxMs))
			case background || ms != tools.ShellDefaultTimeoutMs:
				// A detached job has no default deadline, so an explicit value
				// — even the foreground default — is a deliberate deadline.
				opts = append(opts, "timeout="+formatToolMs(ms))
			}
		}
	}
	if raw := strings.TrimSpace(vals["yield_time_ms"]); raw != "" {
		if ms, err := strconv.Atoi(raw); err == nil {
			switch {
			case ms <= 0:
				opts = append(opts, "no promotion")
			case ms != tools.ShellDefaultYieldMs:
				opts = append(opts, "yield="+formatToolMs(ms))
			}
		}
	}
	if len(opts) == 0 {
		return ""
	}
	return "(" + strings.Join(opts, ", ") + ")"
}

// formatToolMs renders a millisecond argument value the way tool headers show
// deadlines: the largest exact unit ("2m", "45s"), never the zero-padded clock
// form FormatElapsed uses for a measured elapsed time.
func formatToolMs(ms int) string {
	if ms%3600000 == 0 {
		return fmt.Sprintf("%dh", ms/3600000)
	}
	if ms%60000 == 0 {
		return fmt.Sprintf("%dm", ms/60000)
	}
	if ms%1000 == 0 {
		return fmt.Sprintf("%ds", ms/1000)
	}
	return fmt.Sprintf("%dms", ms)
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
		for line := range strings.SplitSeq(raw, "\n") {
			line = strings.TrimSpace(strings.TrimPrefix(line, "- "))
			if line != "" {
				return []string{line}
			}
		}
	}
	return nil
}
