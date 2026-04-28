package shell

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// ShellType represents the type of shell to use for command execution
type ShellType int

const (
	ShellBash ShellType = iota
	ShellPowerShell
	ShellGitBash
	ShellPOSIX
)

func (s ShellType) String() string {
	switch s {
	case ShellBash:
		return "bash"
	case ShellPowerShell:
		return "powershell"
	case ShellGitBash:
		return "git-bash"
	case ShellPOSIX:
		return "posix"
	default:
		return "unknown"
	}
}

// DetectShell determines the best available shell for the current platform
func DetectShell() (ShellType, error) {
	switch runtime.GOOS {
	case "windows":
		return detectWindowsShell()
	default:
		return detectUnixShell()
	}
}

// detectUnixShell finds the best shell on macOS/Linux
func detectUnixShell() (ShellType, error) {
	// Check if bash is available
	if _, err := exec.LookPath("bash"); err == nil {
		return ShellBash, nil
	}

	// Fallback to sh (always available on Unix systems)
	return ShellPOSIX, nil
}

// detectWindowsShell finds the best shell on Windows
func detectWindowsShell() (ShellType, error) {
	// Check PowerShell 7+ (pwsh)
	if _, err := exec.LookPath("pwsh"); err == nil {
		return ShellPowerShell, nil
	}

	// Check Windows PowerShell (powershell)
	if _, err := exec.LookPath("powershell"); err == nil {
		return ShellPowerShell, nil
	}

	// Check Git Bash
	if gitBash, err := findGitBash(); err == nil {
		_ = gitBash // found
		return ShellGitBash, nil
	}

	// Fallback to POSIX shell simulation
	return ShellPOSIX, nil
}

// findGitBash locates Git Bash on Windows
func findGitBash() (string, error) {
	// Try git-bash in PATH
	if path, err := exec.LookPath("git-bash"); err == nil {
		return path, nil
	}

	// Try bash in Git for Windows installation
	// Common locations: C:\Program Files\Git\bin\bash.exe, C:\Program Files (x86)\Git\bin\bash.exe
	// Use system drive to support non-C: installs.
	drive := os.Getenv("SystemDrive")
	if drive == "" {
		drive = "C:"
	}
	commonPaths := []string{
		filepath.Join("Program Files", "Git", "bin", "bash.exe"),
		filepath.Join("Program Files (x86)", "Git", "bin", "bash.exe"),
	}

	for _, p := range commonPaths {
		fullPath := filepath.Join(drive, p)
		if _, err := exec.LookPath(fullPath); err == nil {
			return fullPath, nil
		}
	}

	return "", fmt.Errorf("git bash not found")
}

// GetShellCommand returns the command and arguments to execute the shell
func GetShellCommand(shellType ShellType, command string) (string, []string) {
	switch shellType {
	case ShellBash:
		return "bash", []string{"-c", command}
	case ShellPowerShell:
		// Try pwsh first, fallback to powershell
		if _, err := exec.LookPath("pwsh"); err == nil {
			return "pwsh", []string{"-Command", command}
		}
		return "powershell", []string{"-Command", command}
	case ShellGitBash:
		// Use bash from Git for Windows
		if gitBash, err := findGitBash(); err == nil {
			return gitBash, []string{"-c", command}
		}
		return "bash", []string{"-c", command}
	case ShellPOSIX:
		return "sh", []string{"-c", command}
	default:
		return "bash", []string{"-c", command}
	}
}

// GetShellDescription returns a description for the given shell type
func GetShellDescription(shellType ShellType) string {
	switch shellType {
	case ShellBash:
		return "Execute a shell command via bash."
	case ShellPowerShell:
		return "Execute a shell command via PowerShell."
	case ShellGitBash:
		return "Execute a shell command via Git Bash."
	case ShellPOSIX:
		return "Execute a shell command (POSIX sh; avoid bash-specific syntax like [[ ]])."
	default:
		return "Execute a shell command."
	}
}

// ParseShellType converts a shell type string (e.g. "bash", "powershell",
// "git-bash", "posix") back to a ShellType. Unknown values fall back to Bash.
func ParseShellType(s string) ShellType {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "powershell":
		return ShellPowerShell
	case "git-bash":
		return ShellGitBash
	case "posix":
		return ShellPOSIX
	default:
		return ShellBash
	}
}

// IsWindows returns true if running on Windows
func IsWindows() bool {
	return runtime.GOOS == "windows"
}
