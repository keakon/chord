// Package convformat defines the plain-text format shared by session export and
// TUI card copy: block labels (User:, Assistant:, etc.) and separator. One format
// for both human and model consumption, token-efficient (no emoji/Markdown).
//
// Merged USER + local !shell cards use LabelUser + human-readable body for
// copy/model context, with an appended payload when persisted for restore.
package convformat

import (
	"encoding/json"
	"strings"
)

// BlockSep is the separator between blocks. Used by both /export and yy/y3y copy.
const BlockSep = "\n\n---\n\n"

// Label constants for export and copy. No trailing newline; caller adds "\n\n" before content.
const (
	LabelUser      = "User:"
	LabelAssistant = "Assistant:"
	LabelThinking  = "Thinking:"
	LabelBlock     = "Block:"
)

const (
	localShellPayloadPrefix  = "local_shell_payload: "
	localShellPayloadVersion = 2
)

type localShellPayload struct {
	Version  int    `json:"version"`
	UserLine string `json:"user_line"`
	Command  string `json:"command"`
	Output   string `json:"output"`
	Failed   bool   `json:"failed"`
}

// ToolCallLabel returns the label for a tool call block, e.g. "TOOL CALL (Edit):".
func ToolCallLabel(toolName string) string {
	if toolName == "" {
		return "TOOL CALL:"
	}
	return "TOOL CALL (" + toolName + "):"
}

// LabelLocalShell is the block label for TUI !shell runs (client-side bash -c).
// Not the same as TOOL CALL (Shell): no LLM/agent round-trip.
const LabelLocalShell = "LOCAL SHELL (!):"

// LocalShellCopyBody builds the plain body after LabelLocalShell.
// command is the string passed to bash -c; output is combined stdout/stderr
// (and any trailing error text the TUI appends). If failed, a final status line is added.
func LocalShellCopyBody(command, output string, failed bool) string {
	return localShellReadableBody(command, output, failed)
}

// LocalShellBlockString returns a full copy/export-shaped block for one !shell run.
func LocalShellBlockString(command, output string, failed bool) string {
	return BlockString(LabelLocalShell, LocalShellCopyBody(command, output, failed))
}

func localShellReadableBody(command, output string, failed bool) string {
	if command == "" {
		command = "(empty)"
	}
	var b strings.Builder
	b.WriteString("command:\n")
	b.WriteString(command)
	b.WriteString("\n\noutput:\n")
	b.WriteString(output)
	if failed {
		b.WriteString("\n\nstatus: error")
	}
	return b.String()
}

// UserShellReadableBody is the human-readable body under "User:" for merged
// !shell cards used by copy/export/model context.
func UserShellReadableBody(userLine, cmd, output string, failed bool) string {
	userLine = strings.TrimSpace(userLine)
	if userLine == "" {
		userLine = "!"
	}
	return userLine + "\n\n" + localShellReadableBody(cmd, output, failed)
}

// UserShellPersistedBody appends a machine-readable payload after the
// human-readable body so session restore can roundtrip multiline content.
func UserShellPersistedBody(userLine, cmd, output string, failed bool) string {
	readable := UserShellReadableBody(userLine, cmd, output, failed)
	payloadBytes, _ := json.Marshal(localShellPayload{
		Version:  localShellPayloadVersion,
		UserLine: userLine,
		Command:  cmd,
		Output:   output,
		Failed:   failed,
	})
	return readable + "\n\n" + localShellPayloadPrefix + string(payloadBytes)
}

// ToolResultLabel returns the label for a tool result block, e.g. "TOOL RESULT (Read):".
func ToolResultLabel(toolName string) string {
	if toolName == "" {
		return "TOOL RESULT (unknown):"
	}
	return "TOOL RESULT (" + toolName + "):"
}

// ToolCallMarkdown formats a tool call as Markdown with optional arguments,
// result, and diff sections.
func ToolCallMarkdown(toolName, args, result, diff string) string {
	name := strings.TrimSpace(toolName)
	if name == "" {
		name = "unknown"
	}
	parts := []string{"# Tool call: " + name}
	if args = strings.TrimSpace(args); args != "" && args != "{}" {
		parts = append(parts, "## Arguments\n\n```json\n"+args+"\n```")
	}
	if result = strings.TrimSpace(result); result != "" {
		parts = append(parts, "## Result\n\n"+result)
	}
	if diff = strings.TrimSpace(diff); diff != "" {
		parts = append(parts, "## Diff\n\n```diff\n"+diff+"\n```")
	}
	return strings.Join(parts, "\n\n")
}

// ToolResultMarkdown formats a standalone tool result as Markdown.
func ToolResultMarkdown(toolName, result, diff string) string {
	name := strings.TrimSpace(toolName)
	if name == "" {
		name = "unknown"
	}
	parts := []string{"# Tool result: " + name}
	if result = strings.TrimSpace(result); result != "" {
		parts = append(parts, "## Result\n\n"+result)
	}
	if diff = strings.TrimSpace(diff); diff != "" {
		parts = append(parts, "## Diff\n\n```diff\n"+diff+"\n```")
	}
	return strings.Join(parts, "\n\n")
}

// DoneToolCallMarkdown formats the Done tool copy/export representation. When
// result is a rejection, the reason is shown in a separate paragraph without the
// raw "Done rejected:" prefix.
func DoneToolCallMarkdown(report, result string) string {
	parts := []string{"# Tool call: Done"}
	if report = strings.TrimSpace(report); report != "" {
		parts = append(parts, "## Report\n\n"+report)
	}
	if reason := DoneRejectedReason(result); reason != "" {
		parts = append(parts, "## Rejection reason\n\n"+reason)
	} else if result = strings.TrimSpace(result); result != "" && !strings.EqualFold(result, "Done") && !strings.EqualFold(result, "Done approved") {
		parts = append(parts, "## Result\n\n"+result)
	}
	return strings.Join(parts, "\n\n")
}

// DoneRejectedReason extracts the user/model-facing reason from a Done rejection.
func DoneRejectedReason(result string) string {
	trimmed := strings.TrimSpace(result)
	lower := strings.ToLower(trimmed)
	if strings.HasPrefix(lower, "done rejected automatically:") {
		return strings.TrimSpace(trimmed[len("Done rejected automatically:"):])
	}
	if strings.HasPrefix(lower, "done rejected:") {
		return strings.TrimSpace(trimmed[len("Done rejected:"):])
	}
	return ""
}

// BlockString returns a single block string "label\n\ncontent" for export/copy.
func BlockString(label, content string) string {
	return label + "\n\n" + content
}

// JoinBlocks joins block strings with BlockSep.
func JoinBlocks(parts []string) string {
	return strings.Join(parts, BlockSep)
}

// TryParseUserShellPersistedMessage detects content saved by AppendContextMessage
// after a local !shell run.
// When ok, caller can rebuild the merged USER+bash TUI block.
func TryParseUserShellPersistedMessage(content string) (userLine, cmd, output string, failed bool, ok bool) {
	c := content
	if strings.HasPrefix(c, LabelUser+"\n\n") {
		c = c[len(LabelUser)+2:]
	}
	lines := strings.Split(c, "\n")
	lastIdx := -1
	for idx := len(lines) - 1; idx >= 0; idx-- {
		if strings.TrimSpace(lines[idx]) == "" {
			continue
		}
		lastIdx = idx
		break
	}
	if lastIdx < 0 {
		return "", "", "", false, false
	}
	lastLine := lines[lastIdx]
	if !strings.HasPrefix(lastLine, localShellPayloadPrefix) {
		return "", "", "", false, false
	}
	var payload localShellPayload
	if err := json.Unmarshal([]byte(strings.TrimPrefix(lastLine, localShellPayloadPrefix)), &payload); err != nil {
		return "", "", "", false, false
	}
	if payload.Version != localShellPayloadVersion {
		return "", "", "", false, false
	}
	if strings.TrimSpace(payload.UserLine) == "" || payload.UserLine[0] != '!' {
		return "", "", "", false, false
	}
	readable := strings.TrimRight(strings.Join(lines[:lastIdx], "\n"), "\n")
	if readable != UserShellReadableBody(payload.UserLine, payload.Command, payload.Output, payload.Failed) {
		return "", "", "", false, false
	}
	return payload.UserLine, payload.Command, payload.Output, payload.Failed, true
}
