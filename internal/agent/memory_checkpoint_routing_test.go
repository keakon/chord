package agent

import (
	"strings"
	"testing"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/tools"
)

const (
	routingMemoryMarker      = "## Memory\nThis project has historical memory"
	routingWriteContract     = "Never add or restate entries yourself"
	routingDeleteOnly        = "You may only delete an index line"
	routingExtractionNote    = "may be captured into memory automatically"
	routingLongSessionMarker = "## Long-session context management"
)

func routingAgent(t *testing.T, withMemory bool, extractEnabled bool) *MainAgent {
	t.Helper()
	var projectRoot string
	if withMemory {
		projectRoot = t.TempDir()
		writeProjectMemory(t, projectRoot, "# Project Memory\n\nRouting note.\n")
	} else {
		projectRoot = t.TempDir()
	}
	a := newTestMainAgent(t, projectRoot)
	a.projectConfig = &config.Config{Memory: config.MemoryConfig{Enabled: new(extractEnabled)}}
	a.memoryExtractEnabled.Store(a.effectiveMemoryExtractEnabled())
	if withMemory && !a.memoryIsActive() {
		t.Fatal("memory should be active with a MEMORY.md present")
	}
	if !withMemory && a.memoryIsActive() {
		t.Fatal("memory must be inactive without a MEMORY.md")
	}
	return a
}

func routingEnableCheckpoint(t *testing.T, a *MainAgent) {
	t.Helper()
	a.modelDrivenCompactionEnabled.Store(true)
	a.tools.Register(tools.NewCompactContextTool(tools.CompactContextValidator{ContinuationStateMaxTokens: CompactContinuationStateMaxTokens}))
	if !a.compactContextVisible() {
		t.Fatal("compact_context should be visible after enabling feature and registering the tool")
	}
}

func assertRoutingPrompt(t *testing.T, prompt string, wantMemory, wantExtract, wantLongSession bool) {
	t.Helper()
	if strings.Contains(prompt, routingMemoryMarker) != wantMemory {
		t.Fatalf("Memory discipline present = %v, want %v in:\n%s", !wantMemory, wantMemory, prompt)
	}
	if wantMemory && !strings.Contains(prompt, routingWriteContract) {
		t.Fatalf("Memory discipline must carry the unconditional write contract, got:\n%s", prompt)
	}
	if wantMemory && !strings.Contains(prompt, routingDeleteOnly) {
		t.Fatalf("Memory discipline must say the only allowed write is deleting an index line, got:\n%s", prompt)
	}
	if strings.Contains(prompt, routingExtractionNote) != wantExtract {
		t.Fatalf("extraction note present = %v, want %v in:\n%s", !wantExtract, wantExtract, prompt)
	}
	if strings.Contains(prompt, routingLongSessionMarker) != wantLongSession {
		t.Fatalf("long-session block present = %v, want %v in:\n%s", !wantLongSession, wantLongSession, prompt)
	}
	// No cross sentences, asserted on the sliced blocks rather than on the
	// constants or the whole prompt: other blocks (planner, capabilities) may
	// legitimately name tools or files, so only the owning block is pinned.
	if wantMemory {
		block := promptSection(prompt, routingMemoryMarker)
		for _, unwanted := range []string{"compact_context", ".chord/notes/", "Move a line up"} {
			if strings.Contains(block, unwanted) {
				t.Fatalf("Memory discipline block must not mention %q, got:\n%s", unwanted, block)
			}
		}
	}
	if wantLongSession {
		block := promptSection(prompt, routingLongSessionMarker)
		if strings.Contains(block, "MEMORY.md") {
			t.Fatalf("long-session block must not mention MEMORY.md, got:\n%s", block)
		}
	}
}

// promptSection slices the assembled prompt from the marker heading up to the
// next top-level heading (or the end), so assertions pin the owning block
// instead of the whole prompt surface.
func promptSection(prompt, marker string) string {
	_, rest, ok := strings.Cut(prompt, marker)
	if !ok {
		return ""
	}
	if next, _, ok := strings.Cut(rest, "\n## "); ok {
		return marker + next
	}
	return marker + rest
}

// The two gates are independent: Memory discipline follows memoryIsActive (plus
// the extract flag for its note), the long-session block follows
// compactContextVisible. No sentence appears only when both are on.
func TestMemoryCheckpointRoutingMatrix(t *testing.T) {
	t.Run("neither", func(t *testing.T) {
		a := routingAgent(t, false, false)
		if a.memoryIsActive() || a.compactContextVisible() {
			t.Fatalf("active=%v visible=%v, want both false", a.memoryIsActive(), a.compactContextVisible())
		}
		assertRoutingPrompt(t, a.buildSystemPrompt(), false, false, false)
	})

	t.Run("checkpoint only", func(t *testing.T) {
		a := routingAgent(t, false, false)
		routingEnableCheckpoint(t, a)
		prompt := a.buildSystemPrompt()
		assertRoutingPrompt(t, prompt, false, false, true)
	})

	t.Run("memory only without extract", func(t *testing.T) {
		a := routingAgent(t, true, false)
		if a.compactContextVisible() {
			t.Fatal("checkpoint must stay invisible without the feature flag and tool")
		}
		prompt := a.buildSystemPrompt()
		assertRoutingPrompt(t, prompt, true, false, false)
	})

	t.Run("memory only with extract", func(t *testing.T) {
		a := routingAgent(t, true, true)
		if a.compactContextVisible() {
			t.Fatal("checkpoint must stay invisible without the feature flag and tool")
		}
		if !a.memoryExtractEnabled.Load() {
			t.Fatal("extract flag should be on")
		}
		assertRoutingPrompt(t, a.buildSystemPrompt(), true, true, false)
	})

	t.Run("memory and checkpoint without extract", func(t *testing.T) {
		a := routingAgent(t, true, false)
		routingEnableCheckpoint(t, a)
		assertRoutingPrompt(t, a.buildSystemPrompt(), true, false, true)
	})

	t.Run("both with extract", func(t *testing.T) {
		a := routingAgent(t, true, true)
		routingEnableCheckpoint(t, a)
		if !a.memoryExtractEnabled.Load() {
			t.Fatal("extract flag should be on")
		}
		assertRoutingPrompt(t, a.buildSystemPrompt(), true, true, true)
	})

	t.Run("extract without loaded memory stays silent", func(t *testing.T) {
		a := routingAgent(t, false, true)
		routingEnableCheckpoint(t, a)
		assertRoutingPrompt(t, a.buildSystemPrompt(), false, false, true)
	})
}

// Checkpoint invisibility has three independent causes; each must hide the
// long-session block through the real compactContextVisible gate.
func TestCheckpointInvisibleCauses(t *testing.T) {
	t.Run("feature disabled", func(t *testing.T) {
		a := routingAgent(t, false, false)
		a.tools.Register(tools.NewCompactContextTool(tools.CompactContextValidator{ContinuationStateMaxTokens: CompactContinuationStateMaxTokens}))
		a.modelDrivenCompactionEnabled.Store(false)
		if a.compactContextVisible() {
			t.Fatal("feature disabled must hide compact_context")
		}
		if strings.Contains(a.buildSystemPrompt(), routingLongSessionMarker) {
			t.Fatal("long-session block must be absent while the feature is disabled")
		}
	})

	t.Run("tool not registered", func(t *testing.T) {
		a := routingAgent(t, false, false)
		a.modelDrivenCompactionEnabled.Store(true)
		if a.compactContextVisible() {
			t.Fatal("missing registration must hide compact_context")
		}
		if strings.Contains(a.buildSystemPrompt(), routingLongSessionMarker) {
			t.Fatal("long-session block must be absent without a registered tool")
		}
	})

	t.Run("permission denied", func(t *testing.T) {
		a := routingAgent(t, false, false)
		routingEnableCheckpoint(t, a)
		a.ruleset = permissionRuleset(t, "\"*\": deny\ncompact_context: deny\n")
		if a.compactContextVisible() {
			t.Fatal("explicit deny must hide compact_context")
		}
		if strings.Contains(a.buildSystemPrompt(), routingLongSessionMarker) {
			t.Fatal("long-session block must be absent while compact_context is denied")
		}
	})
}
