package agent

import (
	"strings"
	"testing"
)

func TestStartupConfigIssuesNotice(t *testing.T) {
	if got := startupConfigIssuesNotice(1); !strings.Contains(got, "1 problem") || !strings.Contains(got, "chord doctor config") {
		t.Fatalf("notice(1) = %q", got)
	}
	if got := startupConfigIssuesNotice(3); !strings.Contains(got, "3 problems") || !strings.Contains(got, "chord doctor config") {
		t.Fatalf("notice(3) = %q", got)
	}
}

func TestStartupConfigIssuesSetAndConsumeOnce(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.SetStartupConfigIssues([]string{"one", "two"})
	if got := a.consumeStartupConfigIssues(); len(got) != 2 || got[0] != "one" || got[1] != "two" {
		t.Fatalf("consume = %v, want [one two]", got)
	}
	// One-shot: a second consume must not re-report the same issues.
	if got := a.consumeStartupConfigIssues(); len(got) != 0 {
		t.Fatalf("second consume = %v, want empty", got)
	}
}
