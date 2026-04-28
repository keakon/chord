package shell

import (
	"testing"
)

func TestDetectShell(t *testing.T) {
	shellType, err := DetectShell()
	if err != nil {
		t.Fatalf("DetectShell() failed: %v", err)
	}

	// Should detect a valid shell type
	validShells := map[string]bool{
		"bash":       true,
		"powershell": true,
		"git-bash":   true,
		"posix":      true,
	}

	if !validShells[shellType.String()] {
		t.Errorf("Detected invalid shell type: %s", shellType.String())
	}

	t.Logf("Detected shell: %s", shellType.String())
}

func TestGetShellDescription(t *testing.T) {
	testCases := []struct {
		shellType    ShellType
		expectedDesc string
	}{
		{ShellBash, "Execute a shell command via bash."},
		{ShellPowerShell, "Execute a shell command via PowerShell."},
		{ShellGitBash, "Execute a shell command via Git Bash."},
		{ShellPOSIX, "Execute a shell command (POSIX sh; avoid bash-specific syntax like [[ ]])."},
	}

	for _, tc := range testCases {
		desc := GetShellDescription(tc.shellType)
		if desc != tc.expectedDesc {
			t.Errorf("GetShellDescription(%v) = %q, want %q", tc.shellType, desc, tc.expectedDesc)
		}
	}
}

func TestGetShellCommand(t *testing.T) {
	testCases := []struct {
		shellType       ShellType
		command         string
		expectArgsStart string
	}{
		{ShellBash, "ls -la", "-c"},
		{ShellPowerShell, "Get-ChildItem", "-Command"},
		{ShellPOSIX, "ls -la", "-c"},
		{ShellGitBash, "ls -la", "-c"}, // falls back to bash -c on non-Windows
	}

	for _, tc := range testCases {
		cmd, args := GetShellCommand(tc.shellType, tc.command)
		if len(args) < 2 {
			t.Errorf("GetShellCommand(%v, %s) args = %v, want at least 2 args", tc.shellType, tc.command, args)
			continue
		}
		if args[0] != tc.expectArgsStart {
			t.Errorf("GetShellCommand(%v, %s) args[0] = %q, want %q", tc.shellType, tc.command, args[0], tc.expectArgsStart)
		}
		t.Logf("GetShellCommand(%v, %s) = %s %v", tc.shellType, tc.command, cmd, args)
	}
}

func TestParseShellType(t *testing.T) {
	testCases := []struct {
		input    string
		expected ShellType
	}{
		{"bash", ShellBash},
		{"Bash", ShellBash},
		{"powershell", ShellPowerShell},
		{"PowerShell", ShellPowerShell},
		{"git-bash", ShellGitBash},
		{"posix", ShellPOSIX},
		{"", ShellBash},         // unknown falls back to bash
		{"zsh", ShellBash},      // unknown falls back to bash
		{"  bash  ", ShellBash}, // trimmed
	}

	for _, tc := range testCases {
		got := ParseShellType(tc.input)
		if got != tc.expected {
			t.Errorf("ParseShellType(%q) = %v, want %v", tc.input, got, tc.expected)
		}
	}
}
