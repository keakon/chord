package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestViewportSpillStoreAppendLoad(t *testing.T) {
	store, err := newViewportSpillStore()
	if err != nil {
		t.Fatalf("newViewportSpillStore: %v", err)
	}

	orig := &Block{
		ID:            7,
		Type:          BlockAssistant,
		Content:       "hello spill",
		ThinkingParts: []string{"thought-1", "thought-2"},
	}
	ref, err := store.Append(orig)
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	got, err := store.Load(ref)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.ID != orig.ID || got.Content != orig.Content {
		t.Fatalf("loaded block mismatch: %#v", got)
	}
	if len(got.ThinkingParts) != 2 {
		t.Fatalf("loaded thinking parts mismatch: %#v", got.ThinkingParts)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close(): %v", err)
	}
}

func TestViewportSpillStoreDoesNotUseWorkingDirRuntimeRoot(t *testing.T) {
	cwd := t.TempDir()
	nested := filepath.Join(cwd, "internal", "tui")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("MkdirAll(nested): %v", err)
	}
	prevWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd(): %v", err)
	}
	if err := os.Chdir(nested); err != nil {
		t.Fatalf("Chdir(nested): %v", err)
	}
	defer func() { _ = os.Chdir(prevWD) }()

	store, err := newViewportSpillStore()
	if err != nil {
		t.Fatalf("newViewportSpillStore(): %v", err)
	}
	defer func() { _ = store.Close() }()

	if strings.Contains(store.path, filepath.Join(nested, ".chord", "runtime")) {
		t.Fatalf("spill store path = %q, should not live under cwd/.chord/runtime", store.path)
	}
}

func TestViewportSpillPreservesSearchAndHydration(t *testing.T) {
	v := NewViewport(40, 4)
	v.maxHotBytes = 1024

	v.AppendBlock(&Block{ID: 1, Type: BlockUser, Content: strings.Repeat("alpha ", 600)})
	v.AppendBlock(&Block{ID: 2, Type: BlockAssistant, Content: strings.Repeat("beta ", 600)})
	v.AppendBlock(&Block{ID: 3, Type: BlockAssistant, Content: "tail"})

	if !v.blocks[0].spillCold {
		t.Fatalf("expected oldest block to spill, got spillCold=%v", v.blocks[0].spillCold)
	}

	matches := FindMatchesAtWidth(v.visibleBlocks(), "alpha", 40)
	if len(matches) == 0 || matches[0].BlockIndex != 0 {
		t.Fatalf("expected search hit on spilled block, got %#v", matches)
	}

	block := v.GetFocusedBlock(1)
	if block == nil {
		t.Fatal("expected spilled block to hydrate")
	}
	if block.spillCold {
		t.Fatal("expected hydrated block to be hot")
	}
	if !strings.Contains(block.Content, "alpha") {
		t.Fatalf("unexpected hydrated content: %q", block.Content)
	}
}

func TestViewportSpillHydratePreservesToolDisplayWorkingDir(t *testing.T) {
	v := NewViewport(80, 6)
	v.SetWorkingDir(filepath.Join(string(os.PathSeparator), "tmp", "workspace"))

	tool := &Block{
		ID:       1,
		Type:     BlockToolCall,
		ToolName: "Read",
		Content:  `{"path":"` + filepath.Join(v.workingDir, "internal", "tui", "block_tool.go") + `","limit":20,"offset":5}`,
	}
	v.AppendBlock(tool)
	abs := filepath.Join(v.workingDir, "internal", "tui", "block_tool.go")

	if !v.spillBlock(tool) {
		t.Fatal("expected spillBlock(tool) to succeed")
	}
	if !tool.spillCold {
		t.Fatalf("expected tool block to spill, got spillCold=%v", tool.spillCold)
	}

	inspected, temporary := tool.inspectionBlock()
	if !temporary {
		t.Fatal("expected inspectionBlock() to load a temporary hydrated block")
	}
	if inspected == nil {
		t.Fatal("expected inspectionBlock() result")
	}
	if inspected.displayWorkingDir != v.workingDir {
		t.Fatalf("inspectionBlock() lost displayWorkingDir: got %q want %q", inspected.displayWorkingDir, v.workingDir)
	}
	inspected.InvalidateCache()

	if err := tool.ensureMaterialized(); err != nil {
		t.Fatalf("ensureMaterialized(): %v", err)
	}
	if tool.spillCold {
		t.Fatal("expected tool block to hydrate")
	}
	if tool.displayWorkingDir != v.workingDir {
		t.Fatalf("displayWorkingDir lost after hydrate: got %q want %q", tool.displayWorkingDir, v.workingDir)
	}

	joined := stripANSI(strings.Join(tool.Render(120, ""), "\n"))
	want := filepath.Join("internal", "tui", "block_tool.go") + " (limit=20, offset=5)"
	if !strings.Contains(joined, want) {
		t.Fatalf("expected hydrated tool block to keep relative path, got:\n%s", joined)
	}
	if strings.Contains(joined, abs) {
		t.Fatalf("expected hydrated tool block not to fall back to absolute path, got:\n%s", joined)
	}
}

func TestViewportRenderSkipsHydratingOffscreenSpilledBlocks(t *testing.T) {
	v := NewViewport(40, 4)
	v.maxHotBytes = 1024

	v.AppendBlock(&Block{ID: 1, Type: BlockUser, Content: strings.Repeat("alpha ", 600)})
	v.AppendBlock(&Block{ID: 2, Type: BlockAssistant, Content: strings.Repeat("beta ", 600)})
	v.AppendBlock(&Block{ID: 3, Type: BlockAssistant, Content: "tail"})
	v.ScrollToBottom()

	if !v.blocks[0].spillCold {
		t.Fatalf("expected oldest block to spill, got spillCold=%v", v.blocks[0].spillCold)
	}

	before := v.blocks[0].spillCold
	_ = v.Render("", nil, -1)
	if v.blocks[0].spillCold != before {
		t.Fatalf("expected off-screen spilled block to remain cold after render, got spillCold=%v", v.blocks[0].spillCold)
	}
}

func TestViewportSpillMissingFileFallsBackWithoutCrash(t *testing.T) {
	v := NewViewport(40, 4)
	v.maxHotBytes = 1024
	v.SetSpillRecovery(func() []*Block {
		return []*Block{
			{ID: 1, Type: BlockAssistant, Content: strings.Repeat("gamma ", 600)},
			{ID: 2, Type: BlockAssistant, Content: strings.Repeat("delta ", 600)},
			{ID: 3, Type: BlockAssistant, Content: "tail"},
		}
	})

	v.AppendBlock(&Block{ID: 1, Type: BlockAssistant, Content: strings.Repeat("gamma ", 600)})
	v.AppendBlock(&Block{ID: 2, Type: BlockAssistant, Content: strings.Repeat("delta ", 600)})
	v.AppendBlock(&Block{ID: 3, Type: BlockAssistant, Content: "tail"})

	if !v.blocks[0].spillCold {
		t.Fatalf("expected spilled block, got spillCold=%v", v.blocks[0].spillCold)
	}
	if err := v.spill.file.Close(); err != nil {
		t.Fatalf("close spill file: %v", err)
	}
	v.spill.file = nil
	if err := os.Remove(v.spill.path); err != nil {
		t.Fatalf("remove spill file: %v", err)
	}

	block := v.GetFocusedBlock(1)
	if block == nil {
		t.Fatal("expected block to be rebuilt after spill file removal")
	}
	if !strings.Contains(block.Content, "gamma") {
		t.Fatalf("expected rebuilt content after spill file removal, got %q", block.Content)
	}
}
