package tui

import (
	"fmt"
	"strings"
	"testing"

	uitheme "github.com/keakon/chord/internal/tui/theme"
)

// newArchiveSpillTestModel builds a deferred transcript whose viewport holds
// the tail window and whose archive holds every block, matching the real
// deferred layout.
func newArchiveSpillTestModel(t *testing.T, total int) (*Model, *startupDeferredTranscriptState) {
	t.Helper()
	ApplyTheme(uitheme.Default())
	m := NewModelWithSize(nil, 80, 24)
	t.Cleanup(func() { _ = m.viewport.Close() })
	m.viewport.SetSize(80, 10)

	blocks := make([]*Block, total)
	meta := make([]startupDeferredBlockMeta, total)
	for i := range blocks {
		block := &Block{ID: i + 1, Type: BlockAssistant, Content: fmt.Sprintf("block-%03d needle-%03d payload", i, i)}
		blocks[i] = block
		meta[i] = startupDeferredBlockMeta{
			BlockID:    block.ID,
			Type:       block.Type,
			block:      block,
			LineCounts: map[int]int{80: block.LineCount(80)},
		}
	}
	windowStart := max(total-10, 0)
	state := &startupDeferredTranscriptState{
		allBlocks:   blocks,
		blockMeta:   meta,
		indexByID:   buildDeferredBlockIndex(blocks),
		windowStart: windowStart,
		windowEnd:   total,
	}
	m.startupDeferredTranscript = state
	m.viewport.ReplaceBlocks(blocks[windowStart:total])
	return &m, state
}

func drainStartupDeferredArchiveSpill(t *testing.T, m *Model, state *startupDeferredTranscriptState) int {
	t.Helper()
	total := 0
	for range len(state.allBlocks) + 1 {
		spilled, more := m.spillStartupDeferredArchiveBatch(state)
		total += spilled
		if !more {
			return total
		}
	}
	t.Fatal("archive spill did not converge")
	return 0
}

func TestStartupDeferredArchiveSpillKeepsWindowAndHaloHot(t *testing.T) {
	const total = 60
	m, state := newArchiveSpillTestModel(t, total)
	keepStart := max(state.windowStart-startupDeferredTranscriptPreheatHalo, 0)

	spilled := drainStartupDeferredArchiveSpill(t, m, state)
	if want := keepStart; spilled != want {
		t.Fatalf("spilled %d blocks, want %d outside the window/halo", spilled, want)
	}
	for i, block := range state.allBlocks {
		inside := i >= keepStart
		if inside {
			if block.spillCold {
				t.Fatalf("block %d inside the window/halo is cold", i)
			}
			if block.Content == "" {
				t.Fatalf("block %d inside the window/halo lost its content", i)
			}
			continue
		}
		if !block.spillCold {
			t.Fatalf("block %d outside the window/halo stayed hot", i)
		}
		if block.Content != "" {
			t.Fatalf("block %d content = %q, want it released", i, block.Content)
		}
		if got := block.spillLineCounts[80]; got != state.blockMeta[i].LineCounts[80] {
			t.Fatalf("block %d spill line count = %d, want %d from archive metadata", i, got, state.blockMeta[i].LineCounts[80])
		}
	}
}

func TestStartupDeferredArchiveColdBlockSearchAndHydration(t *testing.T) {
	const total = 60
	m, state := newArchiveSpillTestModel(t, total)
	drainStartupDeferredArchiveSpill(t, m, state)

	coldIndex := 0
	if !state.allBlocks[coldIndex].spillCold {
		t.Fatal("expected the oldest archive block to be spilled")
	}
	matches := findMatchesInStartupDeferredBlockMeta(state.blockMeta, "needle-000", 80)
	if len(matches) != 1 || matches[0].BlockIndex != coldIndex {
		t.Fatalf("search over cold archive returned %#v, want one match at block %d", matches, coldIndex)
	}

	laterIndex := 30
	laterMatches := findMatchesInStartupDeferredBlockMeta(state.blockMeta, fmt.Sprintf("needle-%03d", laterIndex), 80)
	if len(laterMatches) != 1 || laterMatches[0].BlockIndex != laterIndex {
		t.Fatalf("search over cold archive returned %#v, want one match at block %d", laterMatches, laterIndex)
	}
	wantOffset := 0
	for i := range laterIndex {
		wantOffset += startupDeferredBlockLineCount(state.blockMeta[i], 80)
	}
	if laterMatches[0].LineOffset != wantOffset {
		t.Fatalf("line offset = %d, want %d from archive metadata", laterMatches[0].LineOffset, wantOffset)
	}
	if !state.allBlocks[coldIndex].spillCold {
		t.Fatal("search hydrated a cold archive block")
	}

	rendered := strings.Join(state.allBlocks[laterIndex].Render(80, ""), "\n")
	if !strings.Contains(rendered, fmt.Sprintf("needle-%03d", laterIndex)) {
		t.Fatalf("hydrated render = %q, want the archived content", rendered)
	}
	if state.allBlocks[laterIndex].spillCold {
		t.Fatal("render left the archive block cold")
	}
}

func TestStartupDeferredWindowSwitchHydratesOnlyVisibleColdBlocks(t *testing.T) {
	const total = 60
	m, state := newArchiveSpillTestModel(t, total)
	drainStartupDeferredArchiveSpill(t, m, state)

	if !m.applyStartupDeferredTranscriptWindow(5, 15, "test") {
		t.Fatal("window switch was rejected")
	}
	if !m.startupDeferredPreheatPending {
		t.Fatal("window switch should request a preheat restart")
	}
	for i := 5; i < 15; i++ {
		if !state.allBlocks[i].spillCold {
			t.Fatalf("window switch hydrated off-screen block %d before it was rendered", i)
		}
	}

	// Window navigation repositions the offset; only the rendered part of the
	// new window should hydrate.
	m.viewport.offset = 0
	m.viewport.clampOffset()
	_ = m.viewport.Render("", nil, -1, -1, "")
	if state.allBlocks[5].spillCold {
		t.Fatal("visible block 5 stayed cold after render")
	}
	if state.allBlocks[5].Content == "" {
		t.Fatal("visible block 5 lost its content after hydrate")
	}
	if !state.allBlocks[0].spillCold {
		t.Fatal("render hydrated an archive block that is not in the window")
	}
}

func TestStartupDeferredPreheatMeasuresColdHaloWithoutHydrating(t *testing.T) {
	const total = 60
	m, state := newArchiveSpillTestModel(t, total)
	drainStartupDeferredArchiveSpill(t, m, state)

	index := 20
	block := state.allBlocks[index]
	if !block.spillCold {
		t.Fatalf("block %d should be cold for the preheat test", index)
	}
	state.blockMeta[index].LineCounts = nil
	if !preheatStartupDeferredBlock(state, index, 80) {
		t.Fatal("preheat should report work for a halo block without a line count")
	}
	if got := state.blockMeta[index].LineCounts[80]; got <= 0 {
		t.Fatalf("preheat recorded line count %d, want > 0", got)
	}
	if !block.spillCold || block.Content != "" {
		t.Fatal("preheat hydrated a cold halo block")
	}
	if preheatStartupDeferredBlock(state, index, 80) {
		t.Fatal("preheat should not redo a halo block that already has a count")
	}
}

func TestStartupDeferredColdSearchRunsOffUpdate(t *testing.T) {
	const total = 60
	m, state := newArchiveSpillTestModel(t, total)
	drainStartupDeferredArchiveSpill(t, m, state)
	index := 20
	block := state.allBlocks[index]
	if !block.spillCold {
		t.Fatalf("block %d should be cold", index)
	}
	cmd := m.executeSearchAgainstCurrentTranscript(fmt.Sprintf("needle-%03d", index))
	if cmd == nil {
		t.Fatal("cold archive search should return an asynchronous command")
	}
	if !block.spillCold {
		t.Fatal("starting a cold archive search hydrated its target")
	}
	msg, ok := cmd().(startupDeferredSearchResultMsg)
	if !ok {
		t.Fatalf("search command returned %T, want startupDeferredSearchResultMsg", msg)
	}
	if len(msg.matches) == 0 {
		t.Fatal("asynchronous cold search returned no matches")
	}
	if !block.spillCold {
		t.Fatal("searching a cold archive block hydrated it in the command")
	}
}

func TestStartupDeferredPreheatSkipsOversizedColdBlocks(t *testing.T) {
	const total = 60
	m, state := newArchiveSpillTestModel(t, total)
	index := 20
	block := state.allBlocks[index]
	block.Content = strings.Repeat("x", (4<<20)+1024)
	block.lineCache = []string{"cached"}
	block.lineCacheWidth = 80
	block.lineCountCache = 1
	if !m.viewport.spillBlock(block) {
		t.Fatal("failed to spill the oversized test block")
	}
	if !block.spillCold {
		t.Fatal("oversized test block should be cold")
	}
	state.blockMeta[index].LineCounts = nil
	if preheatStartupDeferredBlock(state, index, 80) {
		t.Fatal("preheat should skip a cold block that exceeds the per-block budget")
	}
	if !block.spillCold {
		t.Fatal("preheat hydrated an oversized cold block")
	}
	if state.blockMeta[index].LineCounts != nil {
		t.Fatal("preheat measured an oversized cold block")
	}
}

func TestStartupDeferredArchiveSpillKeepsOversizedBlocksHot(t *testing.T) {
	const total = 60
	m, state := newArchiveSpillTestModel(t, total)
	block := state.allBlocks[0]
	block.Content = strings.Repeat("x", (4<<20)+1024)
	state.blockMeta[0].LineCounts = map[int]int{80: 1}
	drainStartupDeferredArchiveSpill(t, m, state)
	if block.spillCold {
		t.Fatal("archive spill spilled a block that exceeds the large-block exemption")
	}
	if block.Content == "" {
		t.Fatal("archive spill cleared an oversized block that should have stayed hot")
	}
}

func TestStartupDeferredArchiveSpillAllowsOversizedDuringIdle(t *testing.T) {
	const total = 60
	m, state := newArchiveSpillTestModel(t, total)
	block := state.allBlocks[0]
	block.Content = strings.Repeat("x", (4<<20)+1024)
	state.blockMeta[0].LineCounts = map[int]int{80: 1}
	state.archiveSpillAllowOversized = true
	m.displayState = stateBackground
	drainStartupDeferredArchiveSpill(t, m, state)
	if !block.spillCold {
		t.Fatal("idle archive spill should reclaim a block above the active exemption")
	}
}

func TestStartupDeferredArchiveSpillSkipsBlocksAboveHardCeiling(t *testing.T) {
	const total = 60
	m, state := newArchiveSpillTestModel(t, total)
	block := state.allBlocks[0]
	block.Content = strings.Repeat("x", startupDeferredArchiveSpillMaxBlockBytes+1)
	state.blockMeta[0].LineCounts = map[int]int{80: 1}
	state.archiveSpillAllowOversized = true
	m.displayState = stateBackground

	drainStartupDeferredArchiveSpill(t, m, state)
	if block.spillCold {
		t.Fatal("archive spill wrote a block above the synchronous hard ceiling")
	}
	if block.Content == "" {
		t.Fatal("archive spill cleared a block above the synchronous hard ceiling")
	}
}

func TestStartupDeferredArchiveSpillCursorAdvancesAndResets(t *testing.T) {
	const total = 60
	m, state := newArchiveSpillTestModel(t, total)
	spilled, more := m.spillStartupDeferredArchiveBatch(state)
	if spilled == 0 || !more {
		t.Fatalf("first archive spill batch = (%d, %t), want a bounded batch with more work", spilled, more)
	}
	if state.archiveSpillCursor <= 0 {
		t.Fatal("archive spill cursor did not advance past the first batch")
	}
	if !m.applyStartupDeferredTranscriptWindow(5, 15, "test") {
		t.Fatal("window switch was rejected")
	}
	if state.archiveSpillCursor != 0 {
		t.Fatal("window switch did not reset the archive spill cursor")
	}
}

func TestStartupDeferredArchiveSpillSchedulerCoalescesRequests(t *testing.T) {
	stubTUITicks(t)
	const total = 60
	m, state := newArchiveSpillTestModel(t, total)

	if cmd := m.maybeScheduleStartupDeferredArchiveSpill(); cmd != nil {
		t.Fatal("spill should not be scheduled without a pending request")
	}
	m.markStartupDeferredArchiveSpillPending()
	cmd := m.maybeScheduleStartupDeferredArchiveSpill()
	if cmd == nil {
		t.Fatal("pending spill request should schedule a tick")
	}
	if !m.startupDeferredArchiveSpillScheduled {
		t.Fatal("scheduled flag not set")
	}
	if again := m.maybeScheduleStartupDeferredArchiveSpill(); again != nil {
		t.Fatal("a second request should not schedule a duplicate tick")
	}

	generation := m.startupDeferredArchiveSpillGeneration
	msgs := testCmdMsgs(cmd)
	tick, ok := testFindMsg[startupDeferredArchiveSpillTickMsg](msgs)
	if !ok || tick.generation != generation {
		t.Fatalf("tick = %#v, want generation %d", msgs, generation)
	}
	if cmd := m.handleStartupDeferredArchiveSpillTick(startupDeferredArchiveSpillTickMsg{generation: generation - 1}); cmd != nil {
		t.Fatal("stale spill tick should be ignored")
	}
	next := m.handleStartupDeferredArchiveSpillTick(tick)
	if next == nil {
		t.Fatal("remaining archive work should re-arm the spill tick")
	}
	if !m.startupDeferredArchiveSpillScheduled {
		t.Fatal("re-armed spill tick should be marked scheduled")
	}
	if !m.startupDeferredArchiveSpillPending {
		t.Fatal("remaining archive work should keep the pending request")
	}
	if !state.allBlocks[0].spillCold {
		t.Fatal("spill tick did not spill the archive")
	}
}

func TestSpillOffScreenOversizedBlocksOnlySpillsOffScreen(t *testing.T) {
	const oversizedBytes = 4<<20 + 1024
	newOversized := func(id int) *Block {
		return &Block{
			ID:      id,
			Type:    BlockAssistant,
			Content: strings.Repeat("x", oversizedBytes),
			// Pre-seed one measured line so the test does not pay a 4 MiB wrap.
			lineCache:      []string{"cached"},
			lineCacheWidth: 80,
			lineCountCache: 1,
		}
	}

	v := NewViewport(80, 6)
	t.Cleanup(func() { _ = v.Close() })
	offScreen := newOversized(1)
	v.AppendBlock(offScreen)
	for i := range 10 {
		v.AppendBlock(&Block{ID: 10 + i, Type: BlockAssistant, Content: fmt.Sprintf("small-%d", i)})
	}
	v.ScrollToBottom()
	if !v.blockTooLargeToSpill(offScreen) {
		t.Fatal("test block is not classified as oversized")
	}

	if spilled, more := v.SpillOffScreenOversizedBlocksBatch(1, 0); spilled != 1 || more {
		t.Fatalf("overflow spill = (%d, %t), want (1, false)", spilled, more)
	}
	if !offScreen.spillCold {
		t.Fatal("off-screen oversized block stayed hot during idle")
	}
	if v.blocks[1].spillCold {
		t.Fatal("idle oversized spill touched a block that fits the normal budget")
	}

	onScreen := newOversized(2)
	v.AppendBlock(onScreen)
	v.ScrollToBottom()
	v.SpillOffScreenOversizedBlocksBatch(1, 0)
	if onScreen.spillCold {
		t.Fatal("visible oversized block was spilled during idle")
	}
}

func TestFocusRestoreKeepsDeferredHotBudget(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	state := &startupDeferredTranscriptState{}
	m.startupDeferredTranscript = state
	m.configureStartupDeferredTranscriptRetention(state)
	if m.viewport.maxHotBytes != startupDeferredTranscriptAggressiveHotBytes {
		t.Fatalf("maxHotBytes = %d, want %d while deferred", m.viewport.maxHotBytes, startupDeferredTranscriptAggressiveHotBytes)
	}

	_ = m.handleFocusMsg()
	if m.viewport.maxHotBytes != startupDeferredTranscriptAggressiveHotBytes {
		t.Fatalf("focus restore reset maxHotBytes to %d, want the deferred budget %d", m.viewport.maxHotBytes, startupDeferredTranscriptAggressiveHotBytes)
	}
	if m.viewport.baseHotBytes != state.originalViewportBudget {
		t.Fatalf("baseHotBytes = %d, want the pre-deferred budget %d", m.viewport.baseHotBytes, state.originalViewportBudget)
	}
}

func TestStartupDeferredLiveSyncMaterializesColdArchiveBlock(t *testing.T) {
	const total = 60
	m, state := newArchiveSpillTestModel(t, total)
	drainStartupDeferredArchiveSpill(t, m, state)

	index := 30
	cold := state.allBlocks[index]
	if !cold.spillCold {
		t.Fatalf("block %d should be cold before the live sync", index)
	}
	m.syncStartupDeferredTranscriptBlock(cold)

	synced := state.allBlocks[index]
	if synced == cold {
		t.Fatal("live sync should replace the archive entry with a fresh clone")
	}
	if synced.spillCold || synced.Content == "" {
		t.Fatalf("synced block cold=%v content=%q, want it materialized", synced.spillCold, synced.Content)
	}
	if !strings.Contains(synced.Content, fmt.Sprintf("needle-%03d", index)) {
		t.Fatalf("synced content = %q, want the archived payload", synced.Content)
	}
	if state.blockMeta[index].block != synced {
		t.Fatal("archive metadata still points at the replaced cold block")
	}
	if !m.startupDeferredArchiveSpillPending {
		t.Fatal("live sync should request another archive spill pass")
	}
}
