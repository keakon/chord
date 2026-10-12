package tui

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestFreeformPatchEnvelopePreviewBeforeClosingQuote(t *testing.T) {
	patch := "*** Begin Patch\n*** Update File: src/demo.go\n@@\n-old\n+new"
	for _, field := range []string{"input", "patch"} {
		t.Run(field, func(t *testing.T) {
			raw, err := json.Marshal(map[string]string{field: patch})
			if err != nil {
				t.Fatal(err)
			}
			partial := string(raw[:len(raw)-2])
			if got := freeformApplyPatchStreamingPreview(partial); got != patch {
				t.Fatalf("preview = %q, want %q", got, patch)
			}
			outer, err := json.Marshal(map[string]string{"patch": partial})
			if err != nil {
				t.Fatal(err)
			}
			if got := applyPatchStreamingPreview(string(outer[:len(outer)-2])); got != patch {
				t.Fatalf("nested preview = %q, want %q", got, patch)
			}
		})
	}
}

func TestFreeformPatchEnvelopePreviewCoalescesGrowth(t *testing.T) {
	input := `{"input":"*** Begin Patch\n*** Update File: src/demo.go\n@@\n+` + strings.Repeat("x", patchPreviewCoalesceBytes)
	b := &Block{}
	first := b.cachedApplyPatchStreamingArgs(input, true)
	if !strings.Contains(first, "*** Update File: src/demo.go") {
		t.Fatalf("missing target in preview: %q", first)
	}
	if got := b.cachedApplyPatchStreamingArgs(input+"tail", true); got != first {
		t.Fatal("sub-window growth refreshed the preview")
	}
	if got := b.cachedApplyPatchStreamingArgs(input+strings.Repeat("y", patchPreviewCoalesceBytes), true); !strings.Contains(got, "yyy") {
		t.Fatal("growth past the window did not refresh the preview")
	}
}
