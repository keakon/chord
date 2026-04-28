package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/tools"
)

// ---------------------------------------------------------------------------
// Benchmarks for hot rendering paths that have been optimised with caching.
// Run with: go test ./internal/tui/ -bench=. -benchmem
//
// These benchmarks act as regression guards: a significant allocation or
// time increase compared to the baseline signals that a caching layer has
// been bypassed or removed.
// ---------------------------------------------------------------------------

func benchmarkAssistantBlock() *Block {
	return &Block{
		ID:   1,
		Type: BlockAssistant,
		Content: strings.Join([]string{
			"Here is a summary:",
			"",
			"- item one with a fairly long line to wrap through the assistant card renderer and exercise width calculations.",
			"- item two with some `inline code` and a code block below.",
			"",
			"```go",
			"func benchmarkAssistantRender(input string) string {",
			"\treturn strings.TrimSpace(input)",
			"}",
			"```",
		}, "\n"),
	}
}

func benchmarkViewportWithSpill() *Viewport {
	v := NewViewport(100, 12)
	v.maxHotBytes = 1024
	for i := 0; i < 10; i++ {
		content := strings.Repeat("block ", 180)
		if i == 9 {
			content = strings.Repeat("tail ", 40)
		}
		v.AppendBlock(&Block{ID: i + 1, Type: BlockAssistant, Content: content})
	}
	return v
}

func benchmarkAssistantStreamingBlock() *Block {
	return &Block{
		ID:        9,
		Type:      BlockAssistant,
		Streaming: true,
		Content: strings.Join([]string{
			"Streaming reply with pending markdown markers:",
			"- first bullet that wraps through the fallback path",
			"- second bullet with some extra words to force wrapping",
		}, "\n"),
	}
}

func benchmarkModelForView() Model {
	m := NewModel(&sessionControlAgent{providerModelRef: "anthropic/claude-opus-4.7"})
	m.width = 120
	m.height = 40
	m.layout = m.generateLayout(m.width, m.height)
	m.viewport = NewViewport(m.layout.main.Dx(), m.layout.main.Dy())
	for i := 0; i < 8; i++ {
		m.viewport.AppendBlock(&Block{ID: i + 1, Type: BlockAssistant, Content: strings.Repeat("view render block ", 60)})
	}
	m.recalcViewportSize()
	return m
}

func benchmarkToolBlock() *Block {
	return &Block{
		ID:            1,
		Type:          BlockToolCall,
		ToolName:      "Read",
		Content:       `{"path":"internal/tui/render_bench_test.go","limit":120}`,
		ResultContent: strings.Repeat("package tui\n", 40),
		ResultDone:    true,
		Collapsed:     false,
	}
}

func benchmarkCompactToolBlock() *Block {
	return &Block{
		ID:                     2,
		Type:                   BlockToolCall,
		ToolName:               "Bash",
		Content:                `{"command":"git diff -- internal/tui/block_tool.go && go test ./internal/tui -run TestTool -count=1","description":"Inspect tool card rendering","timeout":120,"workdir":"."}`,
		ResultContent:          strings.Repeat("diff line\n", 20),
		ResultDone:             true,
		Collapsed:              false,
		ToolCallDetailExpanded: true,
	}
}

func benchmarkAssistantStreamingTextBlock() *Block {
	b := benchmarkAssistantStreamingBlock()
	b.Content = strings.Repeat("streaming cheap path line with no markdown fences or bullets ", 8)
	b.Streaming = true
	b.InvalidateStreamingSettledCache()
	return b
}

// BenchmarkRenderInfoPanelCacheHit measures the cost when the fingerprint is
// unchanged (the common case during scrolling). Should be O(1) — just a string
// compare and a return of the cached string, with zero lipgloss work.
func BenchmarkRenderInfoPanelCacheHit(b *testing.B) {
	backend := newInfoPanelAgent()
	backend.contextCurrent = 50_000
	backend.contextLimit = 200_000
	backend.todos = []tools.TodoItem{
		{Status: "in_progress", Content: "Write unit tests"},
		{Status: "pending", Content: "Review PR"},
	}
	m := NewModel(backend)
	// Warm the cache.
	_ = m.renderInfoPanel(32, 40)
	b.ResetTimer()
	b.ReportAllocs()
	for b.Loop() {
		_ = m.renderInfoPanel(32, 40)
	}
}

// BenchmarkRenderInfoPanelCacheMiss measures the full re-render path.
// This exercises all lipgloss work inside renderInfoPanel.
func BenchmarkRenderInfoPanelCacheMiss(b *testing.B) {
	backend := newInfoPanelAgent()
	backend.contextCurrent = 50_000
	backend.contextLimit = 200_000
	backend.todos = []tools.TodoItem{
		{Status: "in_progress", Content: "Write unit tests"},
		{Status: "pending", Content: "Review PR"},
	}
	m := NewModel(backend)
	b.ResetTimer()
	b.ReportAllocs()
	// Alternate heights to guarantee cache misses every iteration.
	heights := [2]int{40, 41}
	i := 0
	for b.Loop() {
		_ = m.renderInfoPanel(32, heights[i&1])
		i++
	}
}

// BenchmarkRenderAnimatedInputSeparatorCacheHit measures the separator cache
// hit path. Should be O(1) — just a few int comparisons and a string return.
func BenchmarkRenderAnimatedInputSeparatorCacheHit(b *testing.B) {
	m := NewModel(&sessionControlAgent{})
	m.width = 120
	// Warm the cache.
	_ = m.renderAnimatedInputSeparator(120)
	b.ResetTimer()
	b.ReportAllocs()
	for b.Loop() {
		_ = m.renderAnimatedInputSeparator(120)
	}
}

// BenchmarkRenderAnimatedInputSeparatorCacheMiss measures the full neon
// separator render path (color ramp + per-column ANSI writes).
func BenchmarkRenderAnimatedInputSeparatorCacheMiss(b *testing.B) {
	m := NewModel(&sessionControlAgent{})
	// Alternate widths to guarantee cache misses every iteration.
	widths := [2]int{120, 121}
	i := 0
	b.ResetTimer()
	b.ReportAllocs()
	for b.Loop() {
		_ = m.renderAnimatedInputSeparator(widths[i&1])
		i++
	}
}

// BenchmarkRenderStatusBarModelPillCacheHit measures the model pill cache hit
// path inside renderStatusBar. Scrolling should hit this every frame.
func BenchmarkRenderStatusBarModelPillCacheHit(b *testing.B) {
	backend := &sessionControlAgent{providerModelRef: "anthropic/claude-opus-4.7"}
	m := NewModel(backend)
	m.width = 120
	m.height = 40
	m.rightPanelVisible = false // forces model pill branch
	// Warm the cache.
	_ = m.renderStatusBar()
	b.ResetTimer()
	b.ReportAllocs()
	for b.Loop() {
		_ = m.renderStatusBar()
	}
}

// BenchmarkRenderStatusBarAgentSnapshotDirty measures the first render after
// the event-driven status-bar agent snapshot is marked dirty, while the visible
// footer text remains unchanged.
func BenchmarkRenderStatusBarAgentSnapshotDirty(b *testing.B) {
	backend := &sessionControlAgent{providerModelRef: "anthropic/claude-opus-4.7"}
	m := NewModel(backend)
	m.width = 120
	m.height = 40
	m.rightPanelVisible = false
	_ = m.renderStatusBar()
	b.ResetTimer()
	b.ReportAllocs()
	for b.Loop() {
		m.invalidateStatusBarAgentSnapshot()
		_ = m.renderStatusBar()
	}
}

type benchmarkSessionSummaryAgent struct {
	sessionControlAgent
	summaryCalls int
}

func (a *benchmarkSessionSummaryAgent) GetSessionSummary() *agent.SessionSummary {
	a.summaryCalls++
	return a.sessionSummary
}

func BenchmarkRenderStatusBarSessionSummaryCacheHit(b *testing.B) {
	backend := &benchmarkSessionSummaryAgent{}
	backend.providerModelRef = "anthropic/claude-opus-4.7"
	backend.sessionSummary = &agent.SessionSummary{ID: "123", ForkedFrom: "122"}
	m := NewModel(backend)
	m.width = 120
	m.height = 40
	m.rightPanelVisible = false
	_ = m.renderStatusBar()
	backend.summaryCalls = 0
	b.ResetTimer()
	b.ReportAllocs()
	for b.Loop() {
		_ = m.renderStatusBar()
	}
	if backend.summaryCalls != 0 {
		b.Fatalf("GetSessionSummary calls = %d, want 0 on cache hit", backend.summaryCalls)
	}
}

func BenchmarkRenderStatusBarSessionIDRightCacheHit(b *testing.B) {
	backend := &benchmarkSessionSummaryAgent{}
	backend.providerModelRef = "anthropic/claude-opus-4.7"
	backend.sessionSummary = &agent.SessionSummary{ID: "1775115074902", ForkedFrom: "122"}
	m := NewModel(backend)
	m.width = 180
	m.height = 40
	m.rightPanelVisible = false
	_ = m.renderStatusBar()
	backend.summaryCalls = 0
	b.ResetTimer()
	b.ReportAllocs()
	for b.Loop() {
		_ = m.renderStatusBar()
	}
	if backend.summaryCalls != 0 {
		b.Fatalf("GetSessionSummary calls = %d, want 0 on cache hit", backend.summaryCalls)
	}
}

// BenchmarkRenderStatusBarSessionSummaryDirty measures the first footer render
// after session-summary data changes and the status-bar snapshot is invalidated.
func BenchmarkRenderStatusBarSessionSummaryDirty(b *testing.B) {
	backend := &benchmarkSessionSummaryAgent{}
	backend.providerModelRef = "anthropic/claude-opus-4.7"
	backend.sessionSummary = &agent.SessionSummary{ID: "1775115074902", ForkedFrom: "122"}
	m := NewModel(backend)
	m.width = 180
	m.height = 40
	m.rightPanelVisible = false
	_ = m.renderStatusBar()
	backend.summaryCalls = 0
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; b.Loop(); i++ {
		if i&1 == 0 {
			backend.sessionSummary = &agent.SessionSummary{ID: "1775115074902", ForkedFrom: "122"}
		} else {
			backend.sessionSummary = &agent.SessionSummary{ID: "1775115074903", ForkedFrom: "122"}
		}
		m.invalidateStatusBarAgentSnapshot()
		_ = m.renderStatusBar()
	}
	if backend.summaryCalls != b.N {
		b.Fatalf("GetSessionSummary calls = %d, want %d on dirty render", backend.summaryCalls, b.N)
	}
}

// BenchmarkRenderStatusBarStreamingActivityCacheHit covers the centered
// activity-lane steady-state path that the generic cache-hit benchmarks miss.
func BenchmarkRenderStatusBarStreamingActivityCacheHit(b *testing.B) {
	backend := &sessionControlAgent{providerModelRef: "anthropic/claude-opus-4.7"}
	m := NewModel(backend)
	m.width = 120
	m.height = 40
	m.rightPanelVisible = false
	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityStreaming, AgentID: "main"}
	m.activityStartTime["main"] = time.Now().Add(-2 * time.Second)
	m.turnBusyStartedAt["main"] = time.Now().Add(-90 * time.Second)
	m.viewport.AppendBlock(&Block{ID: 1, Type: BlockAssistant, Content: "hi", SettledAt: time.Now().Add(-90 * time.Second)})
	_ = m.renderStatusBar()
	b.ResetTimer()
	b.ReportAllocs()
	for b.Loop() {
		_ = m.renderStatusBar()
	}
}

// BenchmarkRenderNeonSeparator measures the raw neon separator render
// (cache-miss path) to catch regressions in the ANSI-write optimisation.
func BenchmarkRenderNeonSeparator(b *testing.B) {
	ApplyTheme(DefaultTheme())
	b.ResetTimer()
	b.ReportAllocs()
	for b.Loop() {
		_ = renderNeonSeparator(120)
	}
}

// BenchmarkRenderAssistantCard measures the hot assistant-card render path.
// It guards against reintroducing lipgloss Width(...).Render(...) over already
// wrapped multi-line content.
func BenchmarkRenderAssistantCard(b *testing.B) {
	ApplyTheme(DefaultTheme())
	block := benchmarkAssistantBlock()
	b.ResetTimer()
	b.ReportAllocs()
	for b.Loop() {
		block.InvalidateCache()
		_ = block.Render(100, "")
	}
}

// BenchmarkRenderAssistantStreamingCard measures the cheap streaming fallback
// path, which should stay noticeably lighter than settled glamour rendering.
func BenchmarkRenderAssistantStreamingCard(b *testing.B) {
	ApplyTheme(DefaultTheme())
	block := benchmarkAssistantStreamingBlock()
	b.ResetTimer()
	b.ReportAllocs()
	for b.Loop() {
		block.InvalidateCache()
		_ = block.Render(100, "")
	}
}

func BenchmarkRenderAssistantStreamingTextCard(b *testing.B) {
	ApplyTheme(DefaultTheme())
	block := benchmarkAssistantStreamingTextBlock()
	b.ResetTimer()
	b.ReportAllocs()
	for b.Loop() {
		block.InvalidateCache()
		_ = block.Render(100, "")
	}
}

// BenchmarkRenderToolCallCard measures the hot expanded tool card render path.
func BenchmarkRenderToolCallCard(b *testing.B) {
	ApplyTheme(DefaultTheme())
	block := benchmarkToolBlock()
	b.ResetTimer()
	b.ReportAllocs()
	for b.Loop() {
		block.InvalidateCache()
		_ = block.Render(100, "")
	}
}

// BenchmarkRenderCompactToolCallCard measures the compact-toggle tool card path
// where repeated JSON arg parsing previously happened inside a single render.
func BenchmarkRenderCompactToolCallCard(b *testing.B) {
	ApplyTheme(DefaultTheme())
	block := benchmarkCompactToolBlock()
	b.ResetTimer()
	b.ReportAllocs()
	for b.Loop() {
		block.InvalidateCache()
		_ = block.Render(100, "")
	}
}

// BenchmarkViewportVisibleWindowBlockIDs ensures spill visible-window
// computation stays on cached line counts instead of re-rendering every block.
func BenchmarkViewportVisibleWindowBlockIDs(b *testing.B) {
	ApplyTheme(DefaultTheme())
	v := benchmarkViewportWithSpill()
	v.ScrollToBottom()
	_ = v.Render("", nil, -1) // warm line-count caches
	v.hotBudgetDirty = true
	b.ResetTimer()
	b.ReportAllocs()
	for b.Loop() {
		v.hotBudgetDirty = true
		_ = v.visibleWindowBlockIDs()
	}
}

// BenchmarkFindMatchesAtWidth ensures search offset computation reuses block
// line-count caches instead of triggering cold MeasureLineCount renders.
func BenchmarkFindMatchesAtWidth(b *testing.B) {
	ApplyTheme(DefaultTheme())
	blocks := []*Block{
		benchmarkAssistantBlock(),
		{ID: 2, Type: BlockAssistant, Content: strings.Repeat("needle ", 80)},
		{ID: 3, Type: BlockToolCall, ToolName: "Read", Content: `{"path":"foo"}`, ResultContent: strings.Repeat("alpha beta gamma\n", 30), ResultDone: true},
	}
	for _, block := range blocks {
		block.LineCount(100)
	}
	b.ResetTimer()
	b.ReportAllocs()
	for b.Loop() {
		_ = FindMatchesAtWidth(blocks, "needle", 100)
	}
}

// BenchmarkModelViewCached measures repeated View() calls over a stable frame.
// This guards the new UV parse/draw caches and screen-buffer reuse path.
func BenchmarkModelViewCached(b *testing.B) {
	ApplyTheme(DefaultTheme())
	m := benchmarkModelForView()
	_ = m.View()
	b.ResetTimer()
	b.ReportAllocs()
	for b.Loop() {
		_ = m.View()
	}
}

func BenchmarkModelViewCachedSearchActive(b *testing.B) {
	ApplyTheme(DefaultTheme())
	m := benchmarkModelForView()
	m.search.State.Active = true
	m.search.State.Query = "render"
	m.search.State.Matches = []MatchPosition{{BlockID: 1, BlockIndex: 0}}
	m.search.State.Current = 0
	_ = m.View()
	b.ResetTimer()
	b.ReportAllocs()
	for b.Loop() {
		_ = m.View()
	}
}

// (no lipgloss calls, just data reads and string formatting).
func BenchmarkInfoPanelFingerprint(b *testing.B) {
	backend := newInfoPanelAgent()
	backend.contextCurrent = 80_000
	backend.contextLimit = 200_000
	backend.keysConfirmed = 3
	backend.keysTotal = 5
	backend.todos = []tools.TodoItem{
		{Status: "in_progress", Content: "task one"},
		{Status: "pending", Content: "task two"},
	}
	m := NewModel(backend)
	b.ResetTimer()
	b.ReportAllocs()
	for b.Loop() {
		_ = m.infoPanelFingerprint(32, 40)
	}
}

// TestInfoPanelCacheHitAllocsGuard verifies that a cache hit allocates very
// little — if this jumps above the threshold the caching layer was likely
// bypassed or the fingerprint was made more expensive.
func TestInfoPanelCacheHitAllocsGuard(t *testing.T) {
	backend := newInfoPanelAgent()
	backend.contextCurrent = 50_000
	backend.contextLimit = 200_000
	m := NewModel(backend)
	// Warm the cache.
	_ = m.renderInfoPanel(32, 40)
	// Measure allocations on a cache hit.
	hitAllocs := testing.AllocsPerRun(50, func() {
		_ = m.renderInfoPanel(32, 40)
	})
	// A cache hit goes through infoPanelFingerprint (fmt.Fprintf to strings.Builder)
	// then a string compare. ≤20 allocs is the expected ceiling; a full miss
	// produces hundreds.
	const maxHitAllocs = 20
	if hitAllocs > maxHitAllocs {
		t.Errorf("renderInfoPanel cache hit allocs = %.0f, want ≤%d (cache may be broken or fingerprint too expensive)",
			hitAllocs, maxHitAllocs)
	}
}

// TestSeparatorCacheHitAllocsGuard verifies the separator cache hit is
// allocation-free.
func TestSeparatorCacheHitAllocsGuard(t *testing.T) {
	m := NewModel(&sessionControlAgent{})
	// Warm the cache.
	_ = m.renderAnimatedInputSeparator(120)
	hitAllocs := testing.AllocsPerRun(50, func() {
		_ = m.renderAnimatedInputSeparator(120)
	})
	if hitAllocs > 0 {
		t.Errorf("separator cache hit allocs = %.0f, want 0", hitAllocs)
	}
}

// TestViewportVisibleWindowBlockIDsAllocsGuard verifies visible-window lookup
// uses cached spans after the first render.
func TestViewportVisibleWindowBlockIDsAllocsGuard(t *testing.T) {
	ApplyTheme(DefaultTheme())
	v := benchmarkViewportWithSpill()
	v.ScrollToBottom()
	_ = v.Render("", nil, -1)
	allocs := testing.AllocsPerRun(30, func() {
		v.hotBudgetDirty = true
		_ = v.visibleWindowBlockIDs()
	})
	const maxAllocs = 2
	if allocs > maxAllocs {
		t.Fatalf("visibleWindowBlockIDs allocs = %.0f, want ≤%d", allocs, maxAllocs)
	}
}

// TestFindMatchesAtWidthAllocsGuard verifies search line-offset calculation no
// longer re-renders blocks on each search pass.
func TestFindMatchesAtWidthAllocsGuard(t *testing.T) {
	ApplyTheme(DefaultTheme())
	blocks := []*Block{
		benchmarkAssistantBlock(),
		{ID: 2, Type: BlockAssistant, Content: strings.Repeat("needle ", 80)},
		{ID: 3, Type: BlockToolCall, ToolName: "Read", Content: `{"path":"foo"}`, ResultContent: strings.Repeat("alpha beta gamma\n", 30), ResultDone: true},
	}
	for _, block := range blocks {
		block.LineCount(100)
	}
	allocs := testing.AllocsPerRun(30, func() {
		_ = FindMatchesAtWidth(blocks, "needle", 100)
	})
	const maxAllocs = 40
	if allocs > maxAllocs {
		t.Fatalf("FindMatchesAtWidth allocs = %.0f, want ≤%d", allocs, maxAllocs)
	}
}

// TestStreamingAssistantCheapPathAllocsGuard verifies streaming assistant cards
// stay on the cheap wrapText path instead of glamour.
func TestStreamingAssistantCheapPathAllocsGuard(t *testing.T) {
	ApplyTheme(DefaultTheme())
	block := benchmarkAssistantStreamingBlock()
	allocs := testing.AllocsPerRun(20, func() {
		block.InvalidateCache()
		_ = block.Render(100, "")
	})
	const maxAllocs = 1500
	if allocs > maxAllocs {
		t.Fatalf("streaming assistant render allocs = %.0f, want ≤%d", allocs, maxAllocs)
	}
}

// TestModelViewCachedAllocsGuard verifies stable frames reuse UV parse caches
// instead of re-decoding every ANSI string on each View() call.
func TestModelViewCachedAllocsGuard(t *testing.T) {
	ApplyTheme(DefaultTheme())
	m := benchmarkModelForView()
	_ = m.View()
	allocs := testing.AllocsPerRun(10, func() {
		_ = m.View()
	})
	const maxAllocs = 900
	if allocs > maxAllocs {
		t.Fatalf("cached View allocs = %.0f, want ≤%d", allocs, maxAllocs)
	}
}

// ---------------------------------------------------------------------------
// Cache-correctness smoke tests
// ---------------------------------------------------------------------------

// TestInfoPanelCacheHitReturnsSameString verifies that a cache hit returns the
// exact same string as the first render (not a new allocation).
func TestInfoPanelCacheHitReturnsSameString(t *testing.T) {
	m := NewModel(newInfoPanelAgent())
	first := m.renderInfoPanel(32, 30)
	second := m.renderInfoPanel(32, 30)
	if first != second {
		t.Fatal("cache hit returned a different string from the first render")
	}
}

// TestInfoPanelCacheInvalidatesOnDataChange verifies that changing agent data
// (e.g. token count) causes a re-render rather than returning stale content.
func TestInfoPanelCacheInvalidatesOnDataChange(t *testing.T) {
	backend := newInfoPanelAgent()
	backend.contextCurrent = 1000
	backend.contextLimit = 200_000
	m := NewModel(backend)
	before := m.renderInfoPanel(32, 30)

	backend.contextCurrent = 99_000
	after := m.renderInfoPanel(32, 30)

	if before == after {
		t.Fatal("expected re-render after data change, but got same output")
	}
	if !strings.Contains(stripANSI(after), "99") {
		t.Fatalf("re-rendered panel should contain updated token count; got %q", stripANSI(after))
	}
}

func TestInfoPanelCacheInvalidatesOnCollapseStateChange(t *testing.T) {
	backend := newInfoPanelAgent()
	backend.todos = []tools.TodoItem{{ID: "1", Content: "Investigate spacing", Status: "in_progress"}}
	m := NewModel(backend)
	before := m.renderInfoPanel(32, 30)

	m.toggleInfoPanelSection(infoPanelSectionTodos)
	after := m.renderInfoPanel(32, 30)

	if before == after {
		t.Fatal("expected re-render after collapse state change, but got same output")
	}
	if !strings.Contains(stripANSI(after), "▶ TODOS") || !strings.Contains(stripANSI(after), "0/1") {
		t.Fatalf("collapsed panel should contain collapsed TODOS header with progress; got %q", stripANSI(after))
	}
}

// TestSeparatorCacheHitReturnsSameString verifies separator cache correctness.
func TestSeparatorCacheHitReturnsSameString(t *testing.T) {
	m := NewModel(&sessionControlAgent{})
	first := m.renderAnimatedInputSeparator(80)
	second := m.renderAnimatedInputSeparator(80)
	if first != second {
		t.Fatal("separator cache hit returned different string")
	}
}
