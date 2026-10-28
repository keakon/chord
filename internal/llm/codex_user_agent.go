package llm

import (
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
)

// codexUserAgentProduct is the product name the Codex client reports in its
// User-Agent and in its originator header: the TUI entry point overrides the
// default originator with this name, so both wire fields carry the same value.
// Codex carries its version nowhere else on the wire — no version header, no
// body field — so emulating a Codex version means building this string and
// nothing else.
const codexUserAgentProduct = "codex-tui"

// codexUnknownValue is what the Codex client renders for a fact it could not
// discover about the machine.
const codexUnknownValue = "Unknown"

const codexSystemVersionPlist = "/System/Library/CoreServices/SystemVersion.plist"
const codexOSReleaseFile = "/etc/os-release"

// codexClientUserAgent renders the User-Agent the Codex client sends:
//
//	codex-tui/<version> (<OS> <os version>; <arch>) <terminal> (codex-tui; <version>)
//
// The version is the one piece the user configures; the rest is discovered from
// this machine the way the client's own OS and terminal probes do, so the same
// configuration reproduces what a Codex build would claim here. Both version
// slots take the same value, the way the client prints its own version twice.
func codexClientUserAgent(version string) string {
	return codexUserAgent(version, codexMachineOSInfo(), codexMachineTerminal())
}

// codexUserAgent formats the three discovered parts around the configured
// version. Split out from codexClientUserAgent so the exact string is testable
// without depending on what machine runs the test.
func codexUserAgent(version string, osInfo codexOSInfo, terminal string) string {
	v := sanitizeUserAgentProductVersion(version)
	return sanitizeCodexUserAgent(codexUserAgentProduct + "/" + v +
		" (" + osInfo.name + " " + osInfo.version + "; " + osInfo.arch + ") " +
		terminal + " (" + codexUserAgentProduct + "; " + v + ")")
}

// codexOSInfo is the discovered operating-system part of the User-Agent.
type codexOSInfo struct {
	name    string
	version string
	arch    string
}

// codexMachineOSInfo caches the probe: OS identity does not change during a
// process, and the Codex client reuses its discovery across requests too.
var codexMachineOSInfo = sync.OnceValue(discoverCodexOSInfo)

func discoverCodexOSInfo() codexOSInfo {
	var name, rawVersion string
	switch runtime.GOOS {
	case "darwin":
		rawVersion = codexDarwinProductVersion()
	case "linux":
		name, rawVersion = codexLinuxRelease()
	case "windows":
		rawVersion = codexWindowsVersion()
	}
	return codexOSInfoFor(runtime.GOOS, runtime.GOARCH, name, rawVersion)
}

// codexOSInfoFor turns the discovered facts into the User-Agent's OS part: a
// fixed name per platform except on Linux, where the distribution name from
// os-release wins, and a version normalized by the client's own version type.
func codexOSInfoFor(goos, goarch, discoveredName, rawVersion string) codexOSInfo {
	name := goos
	switch goos {
	case "darwin":
		name = "Mac OS"
	case "linux":
		if discoveredName != "" {
			name = discoveredName
		} else {
			name = "Linux"
		}
	case "windows":
		name = "Windows"
	}
	return codexOSInfo{name: name, version: renderCodexOSVersion(rawVersion), arch: codexArchitecture(goos, goarch)}
}

// codexDarwinProductVersion reads the system product version the same file the
// Codex client parses.
func codexDarwinProductVersion() string {
	data, err := os.ReadFile(codexSystemVersionPlist)
	if err != nil {
		return ""
	}
	return codexPlistString(data, "ProductVersion")
}

// codexPlistString returns the <string> value that follows <key>name</key> in an
// XML property list, or "" when the key or its string value is missing. The
// files Chord reads here are flat key/value plists, so a scan is enough and no
// plist decoder is needed.
func codexPlistString(data []byte, name string) string {
	text := string(data)
	key := "<key>" + name + "</key>"
	_, rest, found := strings.Cut(text, key)
	if !found {
		return ""
	}
	open := strings.Index(rest, "<string>")
	if open < 0 {
		return ""
	}
	rest = rest[open+len("<string>"):]
	end := strings.Index(rest, "</string>")
	if end < 0 {
		return ""
	}
	return strings.TrimSpace(rest[:end])
}

// codexLinuxRelease returns the distribution name and VERSION_ID read from
// os-release; both are empty when the file cannot be parsed at all.
func codexLinuxRelease() (name, version string) {
	data, err := os.ReadFile(codexOSReleaseFile)
	if err != nil {
		return "", ""
	}
	return codexLinuxReleaseFrom(data)
}

// codexLinuxReleaseFrom returns the distribution name and version. A missing
// field still yields the name, because the Codex client reports the
// distribution it identified even when the release string is absent.
func codexLinuxReleaseFrom(data []byte) (name, version string) {
	fields := codexShellAssignments(string(data))
	name = fields["NAME"]
	version = fields["VERSION_ID"]
	if name == "" && version == "" {
		return "", ""
	}
	if name == "" {
		name = "Linux"
	}
	return name, version
}

// codexShellAssignments parses KEY=VALUE lines, tolerating single or double
// quotes, and keeps the last assignment of a key the way sourcing the file
// would. Lines that are not assignments (comments, blank) are ignored.
func codexShellAssignments(text string) map[string]string {
	fields := make(map[string]string)
	for line := range strings.SplitSeq(text, "\n") {
		line = strings.TrimSpace(line)
		key, value, found := strings.Cut(line, "=")
		if !found || key == "" || strings.ContainsAny(key, " \t#") {
			continue
		}
		value = strings.TrimSpace(value)
		if len(value) >= 2 {
			if quote := value[0]; (quote == '"' || quote == '\'') && value[len(value)-1] == quote {
				value = value[1 : len(value)-1]
			}
		}
		fields[key] = value
	}
	return fields
}

// renderCodexOSVersion normalizes a discovered version the way the Codex
// client's version type does: up to three dot-separated numbers are rendered as
// major.minor.patch with zeros filling the missing parts, anything else is kept
// verbatim, and an empty value becomes the unknown marker.
func renderCodexOSVersion(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return codexUnknownValue
	}
	parts := strings.Split(strings.TrimSuffix(raw, "."), ".")
	if len(parts) > 3 {
		return raw
	}
	numbers := make([]string, 3)
	for i, part := range parts {
		n, err := strconv.ParseUint(part, 10, 64)
		if err != nil {
			return raw
		}
		numbers[i] = strconv.FormatUint(n, 10)
	}
	for i := len(parts); i < 3; i++ {
		numbers[i] = "0"
	}
	return fmt.Sprintf("%s.%s.%s", numbers[0], numbers[1], numbers[2])
}

// codexArchitecture maps Go's architecture name to the machine string the Codex
// client reports from uname or the native system info API. Apple Silicon is
// "arm64" there while every other platform calls the same instruction set
// "aarch64".
func codexArchitecture(goos, goarch string) string {
	switch goarch {
	case "arm64":
		if goos == "darwin" {
			return "arm64"
		}
		return "aarch64"
	case "amd64":
		return "x86_64"
	case "386":
		if goos == "windows" {
			return "x86"
		}
		return "i686"
	default:
		return goarch
	}
}

// codexMachineTerminal caches the terminal token: the environment a process
// started in does not change, and the Codex client resolves it once as well.
var codexMachineTerminal = sync.OnceValue(func() string { return codexTerminalToken(os.Getenv) })

// codexTerminalToken identifies the terminal the way the Codex client does and
// renders it for the User-Agent. The probe order mirrors the client's own
// terminal detection: TERM_PROGRAM with its version first — unless it names the
// tmux multiplexer, which sits in front of the real terminal and would mask it
// — then the per-terminal markers that survive a multiplexer, then the TERM
// capability string, then the unknown marker.
//
// The client tests a handful of these markers for presence rather than for a
// value; an empty-but-set variable is treated as unset here, which only
// diverges for a shell that exports one of them blank. Values are trimmed where
// the client keeps them raw; a padded value is not worth reproducing.
func codexTerminalToken(lookup func(string) string) string {
	env := func(key string) string { return strings.TrimSpace(lookup(key)) }
	if program := env("TERM_PROGRAM"); program != "" && !codexIsTmuxTermProgram(program) {
		return sanitizeCodexTerminalToken(program + codexTerminalVersionSuffix(env("TERM_PROGRAM_VERSION")))
	}
	switch {
	case env("GHOSTTY_RESOURCES_DIR") != "":
		return "Ghostty"
	case env("WEZTERM_VERSION") != "":
		return "WezTerm" + codexTerminalVersionSuffix(env("WEZTERM_VERSION"))
	case env("ITERM_SESSION_ID") != "" || env("ITERM_PROFILE") != "" || env("ITERM_PROFILE_NAME") != "":
		return "iTerm.app"
	case env("KITTY_WINDOW_ID") != "" || strings.Contains(env("TERM"), "kitty"):
		return "kitty"
	case env("ALACRITTY_SOCKET") != "" || env("TERM") == "alacritty":
		return "Alacritty"
	case env("KONSOLE_VERSION") != "":
		return "Konsole" + codexTerminalVersionSuffix(env("KONSOLE_VERSION"))
	case env("GNOME_TERMINAL_SCREEN") != "":
		return "gnome-terminal"
	case env("VTE_VERSION") != "":
		return "VTE" + codexTerminalVersionSuffix(env("VTE_VERSION"))
	case env("WT_SESSION") != "":
		return "WindowsTerminal"
	}
	if term := env("TERM"); term != "" {
		return sanitizeCodexTerminalToken(term)
	}
	return "unknown"
}

// codexIsTmuxTermProgram reports whether TERM_PROGRAM names the tmux
// multiplexer. tmux re-exports TERM_PROGRAM as itself, so using it would report
// the multiplexer instead of the terminal running underneath it.
func codexIsTmuxTermProgram(program string) bool {
	return strings.EqualFold(program, "tmux")
}

// codexTerminalVersionSuffix renders the "/<version>" part of a terminal token,
// or nothing when the terminal did not publish a version.
func codexTerminalVersionSuffix(version string) string {
	if version == "" {
		return ""
	}
	return "/" + version
}

// sanitizeCodexTerminalToken narrows a terminal token to the characters the
// Codex client allows there: letters, digits, dash, underscore, dot and slash.
// The User-Agent as a whole is sanitized more loosely afterwards, so this only
// tightens the one token the client also tightens.
func sanitizeCodexTerminalToken(value string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			return r
		case r == '-', r == '_', r == '.', r == '/':
			return r
		default:
			return '_'
		}
	}, value)
}

// sanitizeCodexUserAgent replaces characters a header value cannot carry with
// '_', matching the sanitization the Codex client applies to its own User-Agent.
func sanitizeCodexUserAgent(value string) string {
	return strings.Map(func(r rune) rune {
		if r < ' ' || r > '~' {
			return '_'
		}
		return r
	}, value)
}
