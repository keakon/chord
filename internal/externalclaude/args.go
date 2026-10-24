// Package externalclaude speaks the Claude Code stream-json/control protocol:
// argv construction, event decoding, permission control frames and process
// lifecycle. It owns no Chord orchestration state.
package externalclaude

import (
	"encoding/json"
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
	FlagAllowedTools         = "--allowedTools"
	FlagMCPConfig            = "--mcp-config"
	FlagStrictMCPConfig      = "--strict-mcp-config"

	OutputFormatStreamJSON = "stream-json"
	InputFormatStreamJSON  = "stream-json"
	PermissionModeDefault  = "default"
	PermissionPromptStdio  = "stdio"

	// MCPNotifyServerName and MCPNotifyToolName are the private MCP surface
	// Chord injects into Claude Code. --strict-mcp-config keeps every other
	// MCP server out of the process.
	MCPNotifyServerName = "chord"
	MCPNotifyToolName   = "mcp__chord__notify"
)

// LaunchOptions is the Chord-derived launch contract for one Claude Code process.
type LaunchOptions struct {
	SessionID       string
	Resume          bool
	DisallowedTools []string
	LaunchArgs      []string
	Notify          bool
}

// BuildArgs returns the full argv (excluding argv[0]) for one session.
func BuildArgs(opts LaunchOptions) ([]string, error) {
	user, err := parseLaunchArgs(opts.LaunchArgs)
	if err != nil {
		return nil, err
	}
	mcpConfig, err := buildMCPConfig(opts.Notify)
	if err != nil {
		return nil, err
	}
	// --safe-mode disables user-configured MCP servers, but an SDK MCP server
	// declared by --mcp-config is part of the control channel and still
	// connects. --bare is not usable here: it also disables OAuth and keychain
	// authentication, while safe mode leaves authentication unchanged.
	args := []string{FlagPrint, FlagOutputFormat, OutputFormatStreamJSON, FlagVerbose,
		FlagInputFormat, InputFormatStreamJSON,
		FlagPermissionPromptTool, PermissionPromptStdio, FlagPermissionMode, PermissionModeDefault,
		"--safe-mode", "--restricted", "--setting-sources", "", FlagStrictMCPConfig,
		FlagMCPConfig, mcpConfig, "--tools", strings.Join(managedTools, ","),
		"--settings", managedSettings(),
	}
	if opts.Notify {
		// The MCP tool is the one tool Chord wants available without a control
		// round-trip. Restricting the allow rule to its exact name keeps every
		// built-in call on the permission path.
		args = append(args, FlagAllowedTools, MCPNotifyToolName)
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

// buildMCPConfig renders the only MCP configuration a launch may use. Without
// notify the map stays empty; --strict-mcp-config then prevents any user,
// project, or plugin MCP server from leaking into the process.
func buildMCPConfig(notify bool) (string, error) {
	servers := map[string]any{}
	if notify {
		servers[MCPNotifyServerName] = map[string]string{
			"type": "sdk",
			"name": MCPNotifyServerName,
		}
	}
	data, err := json.Marshal(map[string]any{"mcpServers": servers})
	if err != nil {
		return "", fmt.Errorf("encode MCP configuration: %w", err)
	}
	return string(data), nil
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
