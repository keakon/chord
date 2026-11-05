package tui

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	tea "github.com/keakon/bubbletea/v2"

	"github.com/keakon/chord/internal/recovery"
	"github.com/keakon/chord/internal/tui/terminaltext"
	uitheme "github.com/keakon/chord/internal/tui/theme"
)

// A compaction checkpoint is the only card that names the archived history it
// replaced. These tests pin the entry it draws and the sources that entry
// opens: a checkpoint that names several archives must not be turned into
// "the newest snapshot of the session", and a checkpoint whose archive this
// session cannot read must say so instead of pointing somewhere else.

// compactionHistorySessionAgent is a session stub that also reports the
// session directory, so the history layer can find the snapshots on disk.
type compactionHistorySessionAgent struct {
	*sessionControlAgent
	dir string
}

func (a *compactionHistorySessionAgent) SessionDir() string { return a.dir }

// compactionCheckpointContent builds a checkpoint that names the given
// archives, in the shape the compaction writer produces.
func compactionCheckpointContent(refs ...string) string {
	var sb strings.Builder
	sb.WriteString("[Context Summary]\nEarlier conversation was compacted.\n")
	sb.WriteString("\n[Context compressed]\n")
	sb.WriteString("Archived history files (read the matching file with the read tool to recover exact details; paths accept ~ shorthand):\n")
	for _, ref := range refs {
		sb.WriteString("- ")
		sb.WriteString(ref)
		sb.WriteString("\n")
	}
	sb.WriteString("Each archive begins with a short message-segment index (after its header): read the index first.\n")
	return sb.String()
}

// compactionHistorySessionDir writes one applied snapshot into a temporary
// session directory and returns the directory.
func compactionHistorySessionDir(t *testing.T, indexes ...int) string {
	t.Helper()
	dir := t.TempDir()
	for _, index := range indexes {
		name := strconv.Itoa(index)
		if err := os.WriteFile(filepath.Join(dir, "main.pre-compress-"+name+".jsonl"), []byte("{}\n"), 0o600); err != nil {
			t.Fatalf("write snapshot %d: %v", index, err)
		}
		status := `{"status":"applied"}`
		if err := os.WriteFile(filepath.Join(dir, "history-"+name+".status.json"), []byte(status), 0o600); err != nil {
			t.Fatalf("write status %d: %v", index, err)
		}
	}
	return dir
}

// compactionHistoryModel builds a model whose agent reports the session
// directory, with one compaction summary card in the viewport.
func compactionHistoryModel(t *testing.T, dir, content string) (*Model, *Block) {
	t.Helper()
	ApplyTheme(uitheme.Default())
	backend := &compactionHistorySessionAgent{sessionControlAgent: &sessionControlAgent{}, dir: dir}
	m := NewModelWithSize(backend, 100, 30)
	m.mode = ModeNormal
	block := &Block{
		ID:                   1,
		Type:                 BlockCompactionSummary,
		Content:              content,
		CompactionSummaryRaw: content,
	}
	m.viewport.AppendBlock(block)
	m.layout = m.generateLayout(m.width, m.height)
	_ = m.viewport.Render("", nil, -1, -1, "")
	return &m, block
}

// clickCompactionHistoryEntry clicks the "view referenced history" entry of a
// compaction summary card.
func clickCompactionHistoryEntry(t *testing.T, m *Model, block *Block) {
	t.Helper()
	hit := block.compactionEntry
	if hit == nil {
		t.Fatal("the compaction summary card has no referenced-history entry")
	}
	m.Update(tea.MouseClickMsg{X: hit.start, Y: m.viewport.offset + hit.line, Button: tea.MouseLeft})
}

// scanCompactionHistory feeds the background scan result into the layer.
func scanCompactionHistory(m *Model, indexes ...int) {
	snapshots := make([]recovery.CompactionHistorySnapshot, 0, len(indexes))
	for _, index := range indexes {
		name := strconv.Itoa(index)
		snapshots = append(snapshots, recovery.CompactionHistorySnapshot{
			Index:      index,
			JSONLName:  "main.pre-compress-" + name + ".jsonl",
			StatusName: "history-" + name + ".status.json",
		})
	}
	m.handleCompactionHistoryScanned(compactionHistoryScannedMsg{
		generation: m.compactionHistory.generation,
		snapshots:  snapshots,
	})
}

func TestCompactionSummaryCardDrawsReferencedHistoryEntry(t *testing.T) {
	content := compactionCheckpointContent("/tmp/session/history-3.md")
	_, block := compactionHistoryModel(t, t.TempDir(), content)

	plain := terminaltext.StripANSI(strings.Join(block.Render(100, ""), "\n"))
	if !strings.Contains(plain, "View referenced history") {
		t.Fatalf("the card does not offer the referenced history:\n%s", plain)
	}
}

func TestCompactionSummaryCardWithoutArchiveHasNoEntry(t *testing.T) {
	content := "[Context Summary]\nEarlier conversation was compacted.\n"
	_, block := compactionHistoryModel(t, t.TempDir(), content)

	plain := terminaltext.StripANSI(strings.Join(block.Render(100, ""), "\n"))
	if strings.Contains(plain, "View referenced history") {
		t.Fatalf("a checkpoint without an archive must not advertise the entry:\n%s", plain)
	}
	if block.compactionEntry != nil {
		t.Fatal("a checkpoint without an archive must not build a hit region")
	}
}

func TestCompactionSummaryClickOpensTheSourceList(t *testing.T) {
	dir := compactionHistorySessionDir(t, 3)
	content := compactionCheckpointContent("/tmp/session/history-2.md", "/tmp/session/history-3.md")
	m, block := compactionHistoryModel(t, dir, content)

	clickCompactionHistoryEntry(t, m, block)
	if !m.compactionHistory.active {
		t.Fatal("clicking the entry did not open the history layer")
	}
	scanCompactionHistory(m, 3)

	if !m.compactionHistory.sourceListActive {
		t.Fatal("a checkpoint that names several archives must show the source list")
	}
	sources := m.compactionHistory.sources
	if len(sources) != 2 {
		t.Fatalf("source count = %d, want 2", len(sources))
	}
	if sources[0].available {
		t.Fatal("history-2.md has no local snapshot and must not be offered")
	}
	if !sources[1].available {
		t.Fatal("history-3.md has an applied local snapshot and must be offered")
	}
	// The list must not fall back to the newest snapshot of the session.
	if m.compactionHistory.pendingIndex != -1 {
		t.Fatalf("the source list opened snapshot %d, want no snapshot", m.compactionHistory.pendingIndex)
	}
}

func TestCompactionSummarySingleVerifiedArchiveOpensDirectly(t *testing.T) {
	dir := compactionHistorySessionDir(t, 3)
	content := compactionCheckpointContent("/tmp/session/history-3.md")
	m, block := compactionHistoryModel(t, dir, content)

	clickCompactionHistoryEntry(t, m, block)
	if !m.compactionHistory.active {
		t.Fatal("clicking the entry did not open the history layer")
	}
	scanCompactionHistory(m, 3)

	if m.compactionHistory.sourceListActive {
		t.Fatal("a single verified archive must open directly, without a source list")
	}
	if m.compactionHistory.pendingIndex != 0 {
		t.Fatalf("pending snapshot = %d, want 0", m.compactionHistory.pendingIndex)
	}
}

func TestCompactionSummaryMissingArchiveDoesNotFallBack(t *testing.T) {
	dir := compactionHistorySessionDir(t, 3)
	content := compactionCheckpointContent("/tmp/session/history-9.md")
	m, block := compactionHistoryModel(t, dir, content)

	clickCompactionHistoryEntry(t, m, block)
	scanCompactionHistory(m, 3)

	if !m.compactionHistory.sourceListActive {
		t.Fatal("an archive without a local snapshot must be shown as a source")
	}
	if len(m.compactionHistory.sources) != 1 || m.compactionHistory.sources[0].available {
		t.Fatal("history-9.md must be listed as unavailable")
	}
	if m.compactionHistory.pendingIndex != -1 {
		t.Fatalf("the layer opened snapshot %d, want no snapshot", m.compactionHistory.pendingIndex)
	}
	if !strings.Contains(m.compactionHistory.notice, "local snapshot") {
		t.Fatalf("notice = %q, want it to name the missing local snapshot", m.compactionHistory.notice)
	}
}

func TestCompactionHistorySourceListOpensTheChosenArchive(t *testing.T) {
	dir := compactionHistorySessionDir(t, 3)
	content := compactionCheckpointContent("/tmp/session/history-2.md", "/tmp/session/history-3.md")
	m, block := compactionHistoryModel(t, dir, content)

	clickCompactionHistoryEntry(t, m, block)
	scanCompactionHistory(m, 3)

	// The first source has no local snapshot: Enter must not open anything.
	m.handleCompactionHistoryKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	if m.compactionHistory.sourceListActive != true {
		t.Fatal("an unavailable source must not close the source list")
	}
	if m.compactionHistory.pendingIndex != -1 {
		t.Fatalf("an unavailable source opened snapshot %d", m.compactionHistory.pendingIndex)
	}

	m.handleCompactionHistoryKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyDown}))
	m.handleCompactionHistoryKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	if m.compactionHistory.sourceListActive {
		t.Fatal("choosing an available source must leave the source list")
	}
	if m.compactionHistory.pendingIndex != 0 {
		t.Fatalf("pending snapshot = %d, want 0", m.compactionHistory.pendingIndex)
	}
}

func TestCompactionSummaryEntrySurvivesSpilledContent(t *testing.T) {
	dir := compactionHistorySessionDir(t, 3)
	content := compactionCheckpointContent("/tmp/session/history-3.md")
	m, block := compactionHistoryModel(t, dir, content)

	// A cold spill clears the hot Content and drops the render caches, while
	// CompactionSummaryRaw survives (viewport_spill.go preserveMutableBlockState).
	// The entry is parsed from the raw checkpoint and its hit region is rebuilt
	// on the next render, so it must still work.
	block.Content = ""
	block.compactionEntry = nil
	block.InvalidateCache()
	lines := block.Render(m.viewport.width, "")
	_ = m.viewport.Render("", nil, -1, -1, "")

	plain := terminaltext.StripANSI(strings.Join(lines, "\n"))
	if !strings.Contains(plain, "View referenced history") {
		t.Fatalf("a spilled checkpoint lost its history entry:\n%s", plain)
	}
	if block.compactionEntry == nil {
		t.Fatal("a spilled checkpoint did not rebuild the entry hit region")
	}

	clickCompactionHistoryEntry(t, m, block)
	scanCompactionHistory(m, 3)
	if !m.compactionHistory.active {
		t.Fatal("clicking the entry of a spilled card did not open history")
	}
	if m.compactionHistory.sourceListActive {
		t.Fatal("a single verified archive must open directly after a spill")
	}
	if m.compactionHistory.pendingIndex != 0 {
		t.Fatalf("pending snapshot = %d, want 0", m.compactionHistory.pendingIndex)
	}
}

func TestCompactionSummaryBodyClickDoesNotOpenHistory(t *testing.T) {
	dir := compactionHistorySessionDir(t, 3)
	content := compactionCheckpointContent("/tmp/session/history-3.md")
	m, block := compactionHistoryModel(t, dir, content)

	lines := block.Render(m.viewport.width, "")
	line := -1
	for i, l := range lines {
		if strings.Contains(terminaltext.StripANSI(l), "Earlier conversation was compacted") {
			line = i
			break
		}
	}
	if line < 0 {
		t.Fatalf("no rendered line contains the summary body in %#v", lines)
	}
	m.Update(tea.MouseClickMsg{X: 4, Y: m.viewport.offset + line, Button: tea.MouseLeft})
	if m.compactionHistory.active {
		t.Fatal("clicking the card body must not open the history layer")
	}
}
