package tui

import (
	"strings"
	"testing"
)

func TestRenderUserTerminalUsesTerminalBadge(t *testing.T) {
	block := &Block{
		ID:                   1,
		Type:                 BlockUser,
		Content:              "!uv venv",
		UserLocalShellCmd:    "uv venv",
		UserLocalShellResult: "error: failed",
		UserLocalShellFailed: true,
	}

	lines := block.Render(80, "")
	if len(lines) == 0 {
		t.Fatal("expected rendered lines")
	}

	var plain []string
	for _, line := range lines {
		plain = append(plain, stripANSI(line))
	}
	joined := strings.Join(plain, "\n")
	if !strings.Contains(joined, "TERMINAL") {
		t.Fatalf("expected TERMINAL badge, got %q", joined)
	}
	if strings.Contains(joined, "TERMINAL · LOOP") {
		t.Fatalf("unexpected loop suffix in terminal badge, got %q", joined)
	}
}

func TestRenderUserTerminalShowsExpandHintForCollapsedOutput(t *testing.T) {
	block := &Block{
		ID:                   1,
		Type:                 BlockUser,
		Content:              "!printf 'a\\nb\\nc\\n'",
		UserLocalShellCmd:    "printf 'a\\nb\\nc\\n'",
		UserLocalShellResult: "a\nb\nc",
		Collapsed:            true,
	}

	joined := stripANSI(strings.Join(block.Render(80, ""), "\n"))
	if !strings.Contains(joined, "TERMINAL") {
		t.Fatalf("expected TERMINAL badge, got:\n%s", joined)
	}
	if !strings.Contains(joined, "[space] toggle expand/collapse") {
		t.Fatalf("expected collapsed terminal output to show expand hint, got:\n%s", joined)
	}
	if !strings.Contains(joined, "2 more lines") {
		t.Fatalf("expected collapsed terminal output to report hidden lines, got:\n%s", joined)
	}
}
