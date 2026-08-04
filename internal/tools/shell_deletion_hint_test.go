package tools

import (
	"strings"
	"testing"
)

func TestShellFileDeletionHintRoutesToVisibleTool(t *testing.T) {
	visibleSet := func(names ...string) map[string]struct{} {
		set := make(map[string]struct{}, len(names))
		for _, name := range names {
			set[name] = struct{}{}
		}
		return set
	}

	// Static descriptions without visibility information assume the default
	// surface, which includes delete.
	if got := shellFileDeletionHint(nil); !strings.Contains(got, "prefer `delete`") {
		t.Fatalf("nil visibility hint = %q, want delete routing", got)
	}

	if got := shellFileDeletionHint(visibleSet(NameShell, NameDelete, NameApplyPatch)); !strings.Contains(got, "prefer `delete`") {
		t.Fatalf("delete-visible hint = %q, want delete routing", got)
	}

	got := shellFileDeletionHint(visibleSet(NameShell, NameApplyPatch))
	if !strings.Contains(got, "prefer `apply_patch`") || !strings.Contains(got, "*** Delete File:") {
		t.Fatalf("patch-only hint = %q, want apply_patch Delete File routing", got)
	}

	if got := shellFileDeletionHint(visibleSet(NameShell, NameRead)); got != "" {
		t.Fatalf("no-deletion-tool hint = %q, want empty", got)
	}
}
