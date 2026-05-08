package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestSummarizeMCPControlErrorFlattensJoinedErrorsForToast(t *testing.T) {
	err := errors.Join(
		fmt.Errorf(`unknown MCP server %q`, "missing"),
		fmt.Errorf(`MCP server %q is not manual`, "auto-empty"),
		fmt.Errorf(`enable MCP %q: %w`, "manual-empty", fmt.Errorf("must specify either command or url")),
	)

	got := summarizeMCPControlError(err)
	if strings.Contains(got, "\n") {
		t.Fatalf("summary contains newline: %q", got)
	}
	for _, want := range []string{
		`unknown MCP server "missing"`,
		`MCP server "auto-empty" is not manual`,
		`enable MCP "manual-empty": must specify either command or url`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("summary %q missing %q", got, want)
		}
	}
	if !strings.Contains(got, "; ") {
		t.Fatalf("summary %q should use semicolon separators", got)
	}
}

func TestSummarizeMCPControlErrorCollapsesSingleErrorWhitespace(t *testing.T) {
	got := summarizeMCPControlError(fmt.Errorf("  first line\nsecond line\tthird line  "))
	if got != "first line second line third line" {
		t.Fatalf("summary = %q, want %q", got, "first line second line third line")
	}
}

func TestSummarizeMCPControlErrorIgnoresCanceledBranches(t *testing.T) {
	if got := summarizeMCPControlError(context.Canceled); got != "" {
		t.Fatalf("canceled summary = %q, want empty", got)
	}
	got := summarizeMCPControlError(errors.Join(context.Canceled, fmt.Errorf("other failure")))
	if got != "other failure" {
		t.Fatalf("mixed summary = %q, want %q", got, "other failure")
	}
}
