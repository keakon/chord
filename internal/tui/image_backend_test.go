package tui

import (
	"os"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestDetectTerminalImageCapabilities(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		want TerminalImageCapabilities
	}{
		{
			name: "kitty term",
			env:  map[string]string{"TERM": "xterm-kitty"},
			want: TerminalImageCapabilities{Backend: ImageBackendKitty, SupportsInline: true, SupportsFullscreen: true, Reason: "kitty graphics protocol detected"},
		},
		{
			name: "ghostty term program",
			env:  map[string]string{"TERM_PROGRAM": "ghostty"},
			want: TerminalImageCapabilities{Backend: ImageBackendKitty, SupportsInline: true, SupportsFullscreen: true, Reason: "ghostty kitty graphics protocol detected"},
		},
		{
			name: "iterm2",
			env:  map[string]string{"TERM_PROGRAM": "iTerm.app"},
			want: TerminalImageCapabilities{Backend: ImageBackendITerm2, SupportsInline: true, SupportsFullscreen: true, Reason: "iTerm2 inline image protocol detected"},
		},
		{
			name: "tmux disables images",
			env:  map[string]string{"TERM": "xterm-kitty", "TMUX": "1"},
			want: TerminalImageCapabilities{Backend: ImageBackendNone, SupportsInline: false, SupportsFullscreen: false, Reason: "disabled inside terminal multiplexer"},
		},
		{
			name: "forced none override",
			env:  map[string]string{"TERM": "xterm-kitty", "CHORD_IMAGE_BACKEND": "none"},
			want: TerminalImageCapabilities{Backend: ImageBackendNone, SupportsInline: false, SupportsFullscreen: false, Reason: "disabled by CHORD_IMAGE_BACKEND=none"},
		},
		{
			name: "fullscreen override off",
			env:  map[string]string{"TERM": "xterm-kitty", "CHORD_IMAGE_FULLSCREEN": "0"},
			want: TerminalImageCapabilities{Backend: ImageBackendKitty, SupportsInline: true, SupportsFullscreen: false, Reason: "kitty graphics protocol detected"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := detectTerminalImageCapabilitiesFromMap(tt.env)
			if got != tt.want {
				t.Fatalf("detectTerminalImageCapabilitiesFromMap() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestMapFromEnvMsg(t *testing.T) {
	orig := os.Environ()
	_ = orig
	msg := tea.EnvMsg([]string{"TERM=xterm-kitty", "TERM_PROGRAM=ghostty"})
	got := mapFromEnvMsg(msg)
	if got["TERM"] != "xterm-kitty" || got["TERM_PROGRAM"] != "ghostty" {
		t.Fatalf("mapFromEnvMsg() = %#v", got)
	}
}
