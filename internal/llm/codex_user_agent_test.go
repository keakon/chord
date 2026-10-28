package llm

import (
	"strings"
	"testing"
)

func TestCodexUserAgentShape(t *testing.T) {
	got := codexUserAgent("0.50.0", codexOSInfo{name: "Mac OS", version: "15.1.2", arch: "arm64"}, "vscode/1.90.2")
	if want := "codex-tui/0.50.0 (Mac OS 15.1.2; arm64) vscode/1.90.2 (codex-tui; 0.50.0)"; got != want {
		t.Fatalf("codexUserAgent = %q, want %q", got, want)
	}
}

func TestCodexUserAgentSanitizesVersionAndHeader(t *testing.T) {
	got := codexUserAgent("0.50.0 rc 1", codexOSInfo{name: "Ubuntu", version: "24.04.1", arch: "x86_64"}, "xterm\r\n")
	want := "codex-tui/0.50.0-rc-1 (Ubuntu 24.04.1; x86_64) xterm__ (codex-tui; 0.50.0-rc-1)"
	if got != want {
		t.Fatalf("codexUserAgent = %q, want %q", got, want)
	}
}

func TestCodexOSInfoFor(t *testing.T) {
	tests := []struct {
		name           string
		goos           string
		goarch         string
		discoveredName string
		rawVersion     string
		want           codexOSInfo
	}{
		{"macos", "darwin", "arm64", "", "15.1.2", codexOSInfo{name: "Mac OS", version: "15.1.2", arch: "arm64"}},
		{"macos without plist", "darwin", "x86_64", "", "", codexOSInfo{name: "Mac OS", version: "Unknown", arch: "x86_64"}},
		{"linux distro", "linux", "amd64", "Ubuntu", "24.04", codexOSInfo{name: "Ubuntu", version: "24.4.0", arch: "x86_64"}},
		{"linux no os-release", "linux", "arm64", "", "", codexOSInfo{name: "Linux", version: "Unknown", arch: "aarch64"}},
		{"windows", "windows", "amd64", "", "10.0.22631", codexOSInfo{name: "Windows", version: "10.0.22631", arch: "x86_64"}},
		{"unknown platform", "plan9", "mips", "", "", codexOSInfo{name: "plan9", version: "Unknown", arch: "mips"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := codexOSInfoFor(tc.goos, tc.goarch, tc.discoveredName, tc.rawVersion); got != tc.want {
				t.Fatalf("codexOSInfoFor = %#v, want %#v", got, tc.want)
			}
		})
	}
}

func TestRenderCodexOSVersion(t *testing.T) {
	tests := []struct {
		raw  string
		want string
	}{
		{"", "Unknown"},
		{"  ", "Unknown"},
		{"1", "1.0.0"},
		{"1.", "1.0.0"},
		{"22.04", "22.4.0"},
		{"1.2.3", "1.2.3"},
		{"1.2.3.", "1.2.3"},
		{"01.02.03", "1.2.3"},
		{"1.2.3.4", "1.2.3.4"},
		{"Rolling Release", "Rolling Release"},
		{"24.04 LTS", "24.04 LTS"},
	}
	for _, tc := range tests {
		if got := renderCodexOSVersion(tc.raw); got != tc.want {
			t.Errorf("renderCodexOSVersion(%q) = %q, want %q", tc.raw, got, tc.want)
		}
	}
}

func TestCodexArchitecture(t *testing.T) {
	tests := []struct {
		goos   string
		goarch string
		want   string
	}{
		{"darwin", "arm64", "arm64"},
		{"linux", "arm64", "aarch64"},
		{"windows", "arm64", "aarch64"},
		{"darwin", "amd64", "x86_64"},
		{"linux", "386", "i686"},
		{"windows", "386", "x86"},
		{"linux", "riscv64", "riscv64"},
	}
	for _, tc := range tests {
		if got := codexArchitecture(tc.goos, tc.goarch); got != tc.want {
			t.Errorf("codexArchitecture(%q, %q) = %q, want %q", tc.goos, tc.goarch, got, tc.want)
		}
	}
}

func TestCodexTerminalToken(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		want string
	}{
		{"program with version", map[string]string{"TERM_PROGRAM": "vscode", "TERM_PROGRAM_VERSION": "1.90.2", "TERM": "xterm-256color"}, "vscode/1.90.2"},
		{"program without version", map[string]string{"TERM_PROGRAM": "iTerm.app"}, "iTerm.app"},
		{"term fallback", map[string]string{"TERM": "xterm-256color"}, "xterm-256color"},
		{"blank values are skipped", map[string]string{"TERM_PROGRAM": "  ", "TERM": "dumb"}, "dumb"},
		{"nothing set", map[string]string{}, "unknown"},

		// tmux re-exports TERM_PROGRAM as itself; using it would report the
		// multiplexer instead of the terminal underneath it.
		{"tmux program is masked", map[string]string{"TERM_PROGRAM": "tmux", "TERM_PROGRAM_VERSION": "3.5a", "TERM": "tmux-256color"}, "tmux-256color"},
		{"tmux program falls through to a probe", map[string]string{"TERM_PROGRAM": "TMUX", "ITERM_SESSION_ID": "w0t0p0"}, "iTerm.app"},

		{"ghostty", map[string]string{"GHOSTTY_RESOURCES_DIR": "/Applications/Ghostty.app"}, "Ghostty"},
		{"wezterm", map[string]string{"WEZTERM_VERSION": "20240203"}, "WezTerm/20240203"},
		{"iterm session wins over TERM", map[string]string{"ITERM_SESSION_ID": "w0t0p0", "TERM": "xterm-256color"}, "iTerm.app"},
		{"iterm profile", map[string]string{"ITERM_PROFILE": "Default"}, "iTerm.app"},
		{"kitty window id", map[string]string{"KITTY_WINDOW_ID": "1"}, "kitty"},
		{"kitty from TERM", map[string]string{"TERM": "xterm-kitty"}, "kitty"},
		{"alacritty socket", map[string]string{"ALACRITTY_SOCKET": "/tmp/Alacritty.sock"}, "Alacritty"},
		{"alacritty from TERM", map[string]string{"TERM": "alacritty"}, "Alacritty"},
		{"konsole", map[string]string{"KONSOLE_VERSION": "230804"}, "Konsole/230804"},
		{"gnome terminal", map[string]string{"GNOME_TERMINAL_SCREEN": "/org/gnome/Terminal"}, "gnome-terminal"},
		{"vte", map[string]string{"VTE_VERSION": "6003"}, "VTE/6003"},
		{"windows terminal", map[string]string{"WT_SESSION": "8a1b"}, "WindowsTerminal"},

		// The probe order is the client's: TERM_PROGRAM first, then the
		// markers, then TERM.
		{"program wins over a marker", map[string]string{"TERM_PROGRAM": "vscode", "WEZTERM_VERSION": "1"}, "vscode"},
		{"marker wins over TERM", map[string]string{"GHOSTTY_RESOURCES_DIR": "/x", "TERM": "xterm-256color"}, "Ghostty"},
		{"terminal token is sanitized", map[string]string{"TERM": "xterm+256color"}, "xterm_256color"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			lookup := func(key string) string { return tc.env[key] }
			if got := codexTerminalToken(lookup); got != tc.want {
				t.Fatalf("codexTerminalToken = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestCodexLinuxReleaseFrom(t *testing.T) {
	tests := []struct {
		name        string
		content     string
		wantName    string
		wantVersion string
	}{
		{"quoted fields", "NAME=\"Ubuntu\"\nVERSION_ID=\"24.04\"\n", "Ubuntu", "24.04"},
		{"unquoted fields", "NAME=Debian\nVERSION_ID=12.5\n", "Debian", "12.5"},
		{"version only", `VERSION_ID="42"`, "Linux", "42"},
		{"name only", "NAME=Fedora", "Fedora", ""},
		{"comments and blanks", "# a comment\n\n  \nNAME=Arch\nVERSION_ID=rolling\n", "Arch", "rolling"},
		{"last assignment wins", "NAME=Old\nNAME=New\n", "New", ""},
		{"no useful fields", "ID=sample\nPRETTY_NAME=\"Sample Linux\"\n", "", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			name, version := codexLinuxReleaseFrom([]byte(tc.content))
			if name != tc.wantName || version != tc.wantVersion {
				t.Fatalf("codexLinuxReleaseFrom = (%q, %q), want (%q, %q)", name, version, tc.wantName, tc.wantVersion)
			}
		})
	}
}

func TestCodexPlistString(t *testing.T) {
	plist := `<plist><dict><key>ProductVersion</key><string>15.1.2</string></dict></plist>`
	if got := codexPlistString([]byte(plist), "ProductVersion"); got != "15.1.2" {
		t.Fatalf("codexPlistString = %q, want 15.1.2", got)
	}
	if got := codexPlistString([]byte(plist), "BuildVersion"); got != "" {
		t.Fatalf("codexPlistString for a missing key = %q, want empty", got)
	}
	if got := codexPlistString([]byte("<key>ProductVersion</key><array/>"), "ProductVersion"); got != "" {
		t.Fatalf("codexPlistString without a string value = %q, want empty", got)
	}
}

func TestSanitizeCodexUserAgent(t *testing.T) {
	if got := sanitizeCodexUserAgent("a b~c\x7f"); got != "a b~c_" {
		t.Fatalf("sanitizeCodexUserAgent = %q, want %q", got, "a b~c_")
	}
}

func TestCodexClientUserAgentUsesMachineFacts(t *testing.T) {
	ua := codexClientUserAgent("0.50.0")
	info := codexMachineOSInfo()
	if !strings.HasPrefix(ua, "codex-tui/0.50.0 ("+info.name+" "+info.version+"; "+info.arch+") ") {
		t.Fatalf("codexClientUserAgent = %q, want the discovered OS part %#v", ua, info)
	}
	if !strings.HasSuffix(ua, " (codex-tui; 0.50.0)") {
		t.Fatalf("codexClientUserAgent = %q, want the client suffix", ua)
	}
	if strings.HasSuffix(ua, " ") || ua == "" {
		t.Fatalf("codexClientUserAgent = %q, want a terminal token before the suffix", ua)
	}
}
