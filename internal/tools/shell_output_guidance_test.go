package tools

import (
	"strings"
	"testing"
	"time"
)

func TestShellOutputFilterGuidance(t *testing.T) {
	for _, tc := range []struct {
		command   string
		shellType string
		filtered  bool
	}{
		{"producer | tail -10", "bash", true},
		{"producer |& grep error | sort", "bash", true},
		{"producer | /usr/bin/head -5", "git-bash", true},
		{"producer | tail -10", "powershell", false},
		{"producer > saved.log; tail -10 saved.log", "bash", false},
		{"grep error saved.log", "bash", false},
		{"printf '%s' 'producer | tail -10'", "bash", false},
		{"helper() { producer | tail -10; }; other-command", "bash", false},
		{"producer | sort", "bash", false},
		{"producer |", "bash", false},
	} {
		t.Run(tc.command+"/"+tc.shellType, func(t *testing.T) {
			const output = "original output"
			got := appendShellOutputFilterNote(output, tc.command, tc.shellType, 10*time.Second)
			if (got != output) != tc.filtered {
				t.Fatalf("guidance = %q", got)
			}
			if !strings.HasPrefix(got, output) {
				t.Fatal("guidance altered command output")
			}
			if fast := appendShellOutputFilterNote(output, tc.command, tc.shellType, time.Millisecond); fast != output {
				t.Fatal("short command annotated")
			}
		})
	}
}

// PowerShell pipelines are not POSIX text filters, so the description must not
// advertise tail/grep there. The advice lives only in the tool description.
func TestShellOutputFilterGuidanceExcludesPowerShell(t *testing.T) {
	for _, shellType := range []string{"bash", "powershell"} {
		got := shellToolDescription(nil, shellType)
		if !strings.Contains(got, "Long results are saved with a bounded preview") || !strings.Contains(got, "instead of rerunning the command") {
			t.Fatalf("%s description lacks saved output guidance: %q", shellType, got)
		}
	}
	if got := shellToolDescription(nil, "powershell"); strings.Contains(got, "tail or grep") {
		t.Fatalf("PowerShell description advertises tail/grep guidance: %q", got)
	}
	if got := shellToolDescription(nil, "bash"); !strings.Contains(got, "tail or grep") {
		t.Fatalf("bash description should keep the tail/grep guidance: %q", got)
	}

	commandDescription := func(shellType string) string {
		props := NewShellTool(shellType).Parameters()["properties"].(map[string]any)
		desc, _ := props["command"].(map[string]any)["description"].(string)
		return desc
	}
	for _, shellType := range []string{"bash", "powershell"} {
		if desc := commandDescription(shellType); strings.Contains(desc, "tail") || strings.Contains(desc, "grep") {
			t.Fatalf("%s command description repeats the tool-level tail/grep guidance: %q", shellType, desc)
		}
	}
}
