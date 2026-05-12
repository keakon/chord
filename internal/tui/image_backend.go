package tui

import (
	"os"
	"strings"

	tea "charm.land/bubbletea/v2"
)

// ImageBackend describes the terminal image protocol backend available in the
// current terminal session.
type ImageBackend int

type terminalNotificationProtocol int

const (
	ImageBackendNone ImageBackend = iota
	ImageBackendKitty
	ImageBackendITerm2
)

const (
	terminalNotificationOSC9 terminalNotificationProtocol = iota
	terminalNotificationOSC777
)

func (b ImageBackend) String() string {
	switch b {
	case ImageBackendKitty:
		return "kitty"
	case ImageBackendITerm2:
		return "iterm2"
	default:
		return "none"
	}
}

// TerminalImageCapabilities summarizes the image features available to the TUI.
type TerminalImageCapabilities struct {
	Backend            ImageBackend
	SupportsInline     bool
	SupportsFullscreen bool
	Reason             string
}

var currentTerminalImageCapabilities = TerminalImageCapabilities{
	Backend: ImageBackendNone,
	Reason:  "terminal image support not initialized",
}

func setCurrentTerminalImageCapabilities(caps TerminalImageCapabilities) {
	currentTerminalImageCapabilities = caps
}

func currentImageCapabilities() TerminalImageCapabilities {
	return currentTerminalImageCapabilities
}

func detectTerminalImageCapabilitiesFromMap(env map[string]string) TerminalImageCapabilities {
	getenv := func(key string) string {
		if env == nil {
			return ""
		}
		return env[key]
	}

	backendOverride := strings.ToLower(strings.TrimSpace(getenv("CHORD_IMAGE_BACKEND")))
	inlineOverride, inlineSet := parseImageBoolOverride(getenv("CHORD_IMAGE_INLINE"))
	fullscreenOverride, fullscreenSet := parseImageBoolOverride(getenv("CHORD_IMAGE_FULLSCREEN"))

	finalize := func(backend ImageBackend, reason string) TerminalImageCapabilities {
		caps := TerminalImageCapabilities{Backend: backend, Reason: reason}
		switch backend {
		case ImageBackendKitty:
			caps.SupportsInline = true
			caps.SupportsFullscreen = true
		case ImageBackendITerm2:
			caps.SupportsInline = true
			caps.SupportsFullscreen = true
		default:
			caps.SupportsInline = false
			caps.SupportsFullscreen = false
		}
		if inlineSet {
			if !inlineOverride {
				caps.SupportsInline = false
			} else if caps.Backend == ImageBackendNone {
				caps.SupportsInline = false
				if caps.Reason == "" {
					caps.Reason = "inline image override ignored without supported backend"
				}
			}
		}
		if fullscreenSet {
			caps.SupportsFullscreen = fullscreenOverride && caps.Backend != ImageBackendNone
		}
		if caps.Backend == ImageBackendNone {
			caps.SupportsInline = false
			caps.SupportsFullscreen = false
		}
		return caps
	}

	switch backendOverride {
	case "none":
		return finalize(ImageBackendNone, "disabled by CHORD_IMAGE_BACKEND=none")
	case "kitty":
		return finalize(ImageBackendKitty, "forced by CHORD_IMAGE_BACKEND=kitty")
	case "iterm2":
		return finalize(ImageBackendITerm2, "forced by CHORD_IMAGE_BACKEND=iterm2")
	case "auto", "":
		// auto-detect below
	default:
		return finalize(ImageBackendNone, "invalid CHORD_IMAGE_BACKEND override")
	}

	if strings.TrimSpace(getenv("TMUX")) != "" || strings.TrimSpace(getenv("ZELLIJ")) != "" {
		return finalize(ImageBackendNone, "disabled inside terminal multiplexer")
	}

	term := strings.TrimSpace(getenv("TERM"))
	termProgram := strings.TrimSpace(getenv("TERM_PROGRAM"))
	if term == "xterm-kitty" || strings.TrimSpace(getenv("KITTY_WINDOW_ID")) != "" {
		return finalize(ImageBackendKitty, "kitty graphics protocol detected")
	}
	if term == "xterm-ghostty" || strings.EqualFold(termProgram, "ghostty") {
		return finalize(ImageBackendKitty, "ghostty kitty graphics protocol detected")
	}
	if strings.EqualFold(termProgram, "WezTerm") {
		return finalize(ImageBackendITerm2, "WezTerm iTerm2 inline image protocol detected")
	}
	if termProgram == "iTerm.app" || strings.TrimSpace(getenv("ITERM_SESSION_ID")) != "" {
		return finalize(ImageBackendITerm2, "iTerm2 inline image protocol detected")
	}
	return finalize(ImageBackendNone, "terminal does not advertise a supported image protocol")
}

func detectTerminalNotificationProtocolFromMap(env map[string]string) terminalNotificationProtocol {
	if env == nil {
		return terminalNotificationOSC9
	}
	termProgram := strings.TrimSpace(env["TERM_PROGRAM"])
	if term := strings.TrimSpace(env["TERM"]); term == "xterm-ghostty" || strings.EqualFold(termProgram, "ghostty") {
		return terminalNotificationOSC777
	}
	if strings.EqualFold(termProgram, "WezTerm") {
		return terminalNotificationOSC777
	}
	if strings.TrimSpace(env["WT_SESSION"]) != "" {
		return terminalNotificationOSC777
	}
	return terminalNotificationOSC9
}

func detectTerminalNotificationProtocolFromProcessEnv() terminalNotificationProtocol {
	env := make(map[string]string, len(os.Environ()))
	for _, kv := range os.Environ() {
		key, value, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		env[key] = value
	}
	return detectTerminalNotificationProtocolFromMap(env)
}

func detectTerminalImageCapabilitiesFromProcessEnv() TerminalImageCapabilities {
	env := make(map[string]string, len(os.Environ()))
	for _, kv := range os.Environ() {
		key, value, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		env[key] = value
	}
	return detectTerminalImageCapabilitiesFromMap(env)
}

func detectFocusResizeFreezeFromEnv() bool {
	env := make(map[string]string, len(os.Environ()))
	for _, kv := range os.Environ() {
		key, value, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		env[key] = value
	}
	return detectFocusResizeFreezeFromMap(env)
}

func detectFocusResizeFreezeFromMap(env map[string]string) bool {
	if env == nil {
		return false
	}
	if strings.TrimSpace(env["CMUX_SOCKET_PATH"]) != "" || strings.TrimSpace(env["CMUX_SOCKET"]) != "" {
		return true
	}
	term := strings.TrimSpace(env["TERM"])
	termProgram := strings.TrimSpace(env["TERM_PROGRAM"])
	return term == "xterm-ghostty" || strings.EqualFold(termProgram, "ghostty") || strings.EqualFold(termProgram, "iTerm.app")
}

func mapFromEnvMsg(msg tea.EnvMsg) map[string]string {
	env := make(map[string]string, len(msg))
	for _, kv := range msg {
		key, value, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		env[key] = value
	}
	return env
}

func parseImageBoolOverride(raw string) (bool, bool) {
	switch strings.TrimSpace(raw) {
	case "0", "false", "FALSE", "False", "no", "NO", "No":
		return false, true
	case "1", "true", "TRUE", "True", "yes", "YES", "Yes":
		return true, true
	default:
		return false, false
	}
}
