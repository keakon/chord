// Package externalclaude speaks the Claude Code stream-json/control protocol:
// argv construction, event decoding, permission control frames and process
// lifecycle. It owns no Chord orchestration state.
package externalclaude

import (
	"fmt"
	"slices"
	"strings"
)

const (
	FlagPrint                = "-p"
	FlagOutputFormat         = "--output-format"
	FlagVerbose              = "--verbose"
	FlagInputFormat          = "--input-format"
	FlagSessionID            = "--session-id"
	FlagResume               = "--resume"
	FlagPermissionPromptTool = "--permission-prompt-tool"
	FlagPermissionMode       = "--permission-mode"
	FlagDisallowedTools      = "--disallowedTools"

	OutputFormatStreamJSON = "stream-json"
	InputFormatStreamJSON  = "stream-json"
	PermissionModeDefault  = "default"
	PermissionPromptStdio  = "stdio"
)

// LaunchOptions is the Chord-derived launch contract for one Claude Code process.
type LaunchOptions struct {
	SessionID       string
	Resume          bool
	DisallowedTools []string
	LaunchArgs      []string
}

// BuildArgs returns the full argv (excluding argv[0]) for one session.
func BuildArgs(opts LaunchOptions) ([]string, error) {
	user, err := parseLaunchArgs(opts.LaunchArgs)
	if err != nil {
		return nil, err
	}
	args := []string{FlagPrint, FlagOutputFormat, OutputFormatStreamJSON, FlagVerbose,
		FlagInputFormat, InputFormatStreamJSON,
		FlagPermissionPromptTool, PermissionPromptStdio, FlagPermissionMode, PermissionModeDefault,
		"--safe-mode", "--restricted", "--setting-sources", "", "--strict-mcp-config",
		"--mcp-config", `{"mcpServers":{}}`, "--tools", strings.Join(managedTools, ","),
		"--settings", managedSettings(),
	}
	if sessionID := strings.TrimSpace(opts.SessionID); sessionID != "" {
		if opts.Resume {
			args = append(args, FlagResume, sessionID)
		} else {
			args = append(args, FlagSessionID, sessionID)
		}
	}
	disallowed := append(slices.Clone(opts.DisallowedTools), user.disallowed...)
	slices.Sort(disallowed)
	disallowed = slices.Compact(disallowed)
	if len(disallowed) > 0 {
		args = append(args, FlagDisallowedTools, strings.Join(disallowed, ","))
	}
	args = append(args, user.args...)
	return args, nil
}

// ValidSessionID reports whether value is a canonical 8-4-4-4-12 hexadecimal
// UUID. Claude Code rejects anything else for --session-id.
func ValidSessionID(value string) bool {
	if len(value) != 36 {
		return false
	}
	for i, r := range value {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if r != '-' {
				return false
			}
			continue
		}
		if !isHex(r) {
			return false
		}
	}
	return true
}

func isHex(r rune) bool {
	return (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')
}

// NewSessionID formats a random UUIDv4. The caller supplies the 16 random
// bytes so this package stays independent of the ID source.
func NewSessionID(random [16]byte) string {
	random[6] = (random[6] & 0x0f) | 0x40
	random[8] = (random[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", random[0:4], random[4:6], random[6:8], random[8:10], random[10:16])
}
