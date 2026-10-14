package tui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/tui/terminaltext"
)

func TestRenderInfoPanelEditedFilesStatsPreserveInfoPanelBackground(t *testing.T) {
	backend := newInfoPanelAgent()
	m := NewModel(backend)
	m.sidebar.Update(nil, "main", "builder")
	m.sidebar.AddFileEdit("main", "/tmp/foo.go", 2, 1)

	rendered := m.renderInfoPanel(48, 24)

	wantAdd := currentStyles.InfoPanelEditAddedStyle.Render("+2")
	wantSep := currentStyles.InfoPanelDim.Render(" ")
	wantRem := currentStyles.InfoPanelEditRemovedStyle.Render("-1")
	if !strings.Contains(rendered, wantAdd) {
		t.Fatalf("CHANGED FILES +N should use info-panel background; want segment %q in output", wantAdd)
	}
	if !strings.Contains(rendered, wantSep) {
		t.Fatalf("CHANGED FILES separator space should use info-panel background; want segment %q in output", wantSep)
	}
	if !strings.Contains(rendered, wantRem) {
		t.Fatalf("CHANGED FILES -N should use info-panel background; want segment %q in output", wantRem)
	}
	if !strings.Contains(rendered, wantAdd+wantSep+wantRem) {
		t.Fatalf("CHANGED FILES +N/-N sequence should preserve panel background across separator; want substring %q in output", wantAdd+wantSep+wantRem)
	}
	badAdd := currentStyles.SidebarAddedStyle.Render("+2")
	badRem := currentStyles.SidebarRemovedStyle.Render("-1")
	if strings.Contains(rendered, badAdd) {
		t.Fatalf("CHANGED FILES should not use sidebar-only add style (no panel bg); got bare segment %q", badAdd)
	}
	if strings.Contains(rendered, badRem) {
		t.Fatalf("CHANGED FILES should not use sidebar-only remove style (no panel bg); got bare segment %q", badRem)
	}

	section := infoPanelSectionLines(infoPanelPlainLines(rendered), "▼ CHANGED FILES")
	if len(section) == 0 || !strings.HasPrefix(section[0], "foo.go +2 -1") {
		t.Fatalf("CHANGED FILES rows should remain visible in plain section extraction; got %#v", section)
	}
}

func TestRenderInfoPanelEditedFilesPrioritizesStatsWhenNarrow(t *testing.T) {
	backend := newInfoPanelAgent()
	m := NewModel(backend)
	m.sidebar.Update(nil, "main", "builder")
	m.sidebar.AddFileEdit("main", "/tmp/app_update_input_row.go", 14, 4)

	block := terminaltext.StripANSI(m.buildInfoPanelFilesBlock(8))
	if !strings.Contains(block, "+14 -4") {
		t.Fatalf("narrow CHANGED FILES row should keep full stats, got %q", block)
	}
}

func TestRenderInfoPanelEditedFilesStatsDoNotTouchRightEdge(t *testing.T) {
	backend := newInfoPanelAgent()
	m := NewModel(backend)
	m.sidebar.Update(nil, "main", "builder")
	m.sidebar.AddFileEdit("main", "/tmp/long_fixture_filename_for_stats.go", 12, 34)

	block := terminaltext.StripANSI(m.buildInfoPanelFilesBlock(30))
	lines := strings.Split(block, "\n")
	if len(lines) < 2 {
		t.Fatalf("CHANGED FILES block should include a file row, got %q", block)
	}
	if !strings.Contains(lines[1], "+12 -34") {
		t.Fatalf("CHANGED FILES row should keep full stats, got %q", lines[1])
	}
	if !strings.HasSuffix(lines[1], "+12 -34 ") {
		t.Fatalf("CHANGED FILES stats should leave a trailing safety cell, got %q", lines[1])
	}
}

func TestRenderInfoPanelChangedFilesDeletedFileUsesStrikethroughWithoutStats(t *testing.T) {
	backend := newInfoPanelAgent()
	m := NewModel(backend)
	m.sidebar.Update(nil, "main", "builder")
	m.sidebar.AddFileDelete("main", "/tmp/obsolete.go")

	rendered := m.renderInfoPanel(48, 24)
	section := infoPanelSectionLines(infoPanelPlainLines(rendered), "▼ CHANGED FILES")
	if len(section) == 0 || !strings.HasPrefix(section[0], "obsolete.go") {
		t.Fatalf("deleted changed-file row missing; section=%#v", section)
	}
	if strings.Contains(section[0], "-1") || strings.Contains(section[0], "+0") {
		t.Fatalf("deleted changed-file row should not show fake stats: %#v", section)
	}
	if !strings.Contains(rendered, "\x1b[9m") && !strings.Contains(rendered, ";9m") {
		t.Fatalf("deleted changed-file row should render with strikethrough, got %q", rendered)
	}
}

func TestRenderInfoPanelEditedFilesKeepsRowsScrollable(t *testing.T) {
	backend := newInfoPanelAgent()
	m := NewModel(backend)
	m.sidebar.Update(nil, "main", "builder")
	// A sizeable but realistic turn: every file must stay rendered so the user
	// can reach it by scrolling the panel, instead of collapsing into an
	// "… and N more" dead end.
	const total = 30
	for i := 1; i <= total; i++ {
		m.sidebar.AddFileEdit("main", fmt.Sprintf("/tmp/file-%02d.go", i), i, 0)
	}

	// Height is generous so panel-level clipping does not hide rows from the
	// assertion; the point here is the section's own truncation policy.
	section := infoPanelSectionLines(infoPanelPlainLines(m.renderInfoPanel(48, 200)), "▼ CHANGED FILES")
	if got, want := len(section), total; got != want {
		t.Fatalf("CHANGED FILES rows = %d, want %d; section=%#v", got, want, section)
	}
	// Newest first, so the last-added file leads and the first-added trails.
	if got, want := section[0], fmt.Sprintf("file-%02d.go +%d", total, total); !strings.HasPrefix(got, want) {
		t.Fatalf("first row = %q, want newest file with prefix %q", got, want)
	}
	if got, want := section[total-1], "file-01.go +1"; !strings.HasPrefix(got, want) {
		t.Fatalf("last row = %q, want oldest file with prefix %q", got, want)
	}
	if joined := strings.Join(section, "\n"); strings.Contains(joined, "more") {
		t.Fatalf("CHANGED FILES should not add an overflow row at %d files; section=%#v", total, section)
	}
}

func TestRenderInfoPanelEditedFilesReeditMovesToNewestSlot(t *testing.T) {
	backend := newInfoPanelAgent()
	m := NewModel(backend)
	m.sidebar.Update(nil, "main", "builder")
	m.sidebar.AddFileEdit("main", "/tmp/one.go", 1, 0)
	m.sidebar.AddFileEdit("main", "/tmp/two.go", 1, 0)
	m.sidebar.AddFileEdit("main", "/tmp/one.go", 2, 0)

	section := infoPanelSectionLines(infoPanelPlainLines(m.renderInfoPanel(48, 40)), "▼ CHANGED FILES")
	if got, want := len(section), 2; got != want {
		t.Fatalf("CHANGED FILES rows = %d, want %d; section=%#v", got, want, section)
	}
	// one.go was touched last, so it leads and includes both edits.
	if !strings.HasPrefix(section[0], "one.go +3") {
		t.Fatalf("first row = %q, want most recently edited one.go", section[0])
	}
	if !strings.HasPrefix(section[1], "two.go") {
		t.Fatalf("second row = %q, want older entry two.go", section[1])
	}
}

func TestInfoPanelChangedFileUpdateInvalidatesCache(t *testing.T) {
	backend := newInfoPanelAgent()
	m := NewModel(backend)
	m.sidebar.Update(nil, "main", "builder")
	m.sidebar.AddFileEdit("main", "/tmp/main.go", 1, 0)
	first := infoPanelPlainLines(m.renderInfoPanel(48, 24))

	m.sidebar.AddFileEdit("main", "/tmp/main.go", 2, 0)
	second := infoPanelPlainLines(m.renderInfoPanel(48, 24))

	firstSection := infoPanelSectionLines(first, "▼ CHANGED FILES")
	secondSection := infoPanelSectionLines(second, "▼ CHANGED FILES")
	if len(firstSection) != 1 || !strings.Contains(firstSection[0], "+1") {
		t.Fatalf("initial changed-file section = %#v, want +1", firstSection)
	}
	if len(secondSection) != 1 || !strings.Contains(secondSection[0], "+3") {
		t.Fatalf("updated changed-file section = %#v, want +3", secondSection)
	}
}

func TestRenderInfoPanelEditedFilesReeditProtectsFileFromEviction(t *testing.T) {
	backend := newInfoPanelAgent()
	m := NewModel(backend)
	m.sidebar.Update(nil, "main", "builder")
	for i := range sidebarMaxEditedFiles {
		m.sidebar.AddFileEdit("main", fmt.Sprintf("/tmp/file-%05d.go", i), 1, 0)
	}
	m.sidebar.AddFileEdit("main", "/tmp/file-00000.go", 2, 0)
	m.sidebar.AddFileEdit("main", "/tmp/newest.go", 1, 0)

	section := infoPanelSectionLines(infoPanelPlainLines(m.renderInfoPanel(48, sidebarMaxEditedFiles+50)), "▼ CHANGED FILES")
	if got, want := len(section), sidebarMaxEditedFiles; got != want {
		t.Fatalf("CHANGED FILES rows = %d, want %d", got, want)
	}
	if !strings.HasPrefix(section[0], "newest.go") || !strings.HasPrefix(section[1], "file-00000.go +3") {
		t.Fatalf("newest rows = %#v, want newest.go then re-edited file-00000.go", section[:2])
	}
	if joined := strings.Join(section, "\n"); strings.Contains(joined, "file-00001.go") {
		t.Fatalf("oldest untouched file should be evicted; section tail=%#v", section[len(section)-3:])
	}
}

// The sidebar cap is the only ceiling: the panel must render every file the
// sidebar still holds, so nothing is hidden behind a second truncation.
func TestRenderInfoPanelEditedFilesSidebarCapIsOnlyCeiling(t *testing.T) {
	if infoPanelEditedFilesHardLimit != sidebarMaxEditedFiles {
		t.Fatalf("infoPanelEditedFilesHardLimit = %d, want %d; the two layers must share one ceiling",
			infoPanelEditedFilesHardLimit, sidebarMaxEditedFiles)
	}

	backend := newInfoPanelAgent()
	m := NewModel(backend)
	m.sidebar.Update(nil, "main", "builder")
	const dropped = 50
	total := sidebarMaxEditedFiles + dropped
	for i := 1; i <= total; i++ {
		m.sidebar.AddFileEdit("main", fmt.Sprintf("/tmp/file-%05d.go", i), i, 0)
	}

	// Height is generous so panel-level clipping does not hide rows here.
	section := infoPanelSectionLines(infoPanelPlainLines(m.renderInfoPanel(48, total+50)), "▼ CHANGED FILES")
	if got, want := len(section), sidebarMaxEditedFiles; got != want {
		t.Fatalf("CHANGED FILES rows = %d, want %d; section=%#v", got, want, section)
	}
	// The sidebar keeps the newest entries and the panel renders them first, so
	// the evicted prefix is missing from the tail, not from the top of the list.
	if got, want := section[0], fmt.Sprintf("file-%05d.go +%d", total, total); !strings.HasPrefix(got, want) {
		t.Fatalf("first row = %q, want newest file with prefix %q", got, want)
	}
	if got, want := section[len(section)-1], fmt.Sprintf("file-%05d.go +%d", dropped+1, dropped+1); !strings.HasPrefix(got, want) {
		t.Fatalf("last row = %q, want oldest survivor with prefix %q", got, want)
	}
	if joined := strings.Join(section, "\n"); strings.Contains(joined, "more") {
		t.Fatalf("panel must not add an overflow row on top of the sidebar cap; section tail=%#v", section[len(section)-3:])
	}
}

func TestRenderInfoPanelCollapsedEditedFilesShowsCountOnly(t *testing.T) {
	backend := newInfoPanelAgent()
	m := NewModel(backend)
	m.sidebar.Update(nil, "main", "builder")
	m.sidebar.AddFileEdit("main", "/tmp/foo.go", 2, 1)
	m.sidebar.AddFileEdit("main", "/tmp/bar.go", 1, 0)
	m.infoPanelCollapsedSections[infoPanelSectionFiles] = true

	section := infoPanelSectionLines(infoPanelPlainLines(m.renderInfoPanel(48, 24)), "▶ CHANGED FILES")
	if len(section) != 0 {
		t.Fatalf("collapsed CHANGED FILES section should not render body lines, got %#v", section)
	}
}
