package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/keakon/chord/internal/agent"
)

// Every clickable status region must cover exactly the columns its member is
// drawn in. The right-side group is composed member by member (compaction pill,
// path, session, jobs pill) and then placed against the activity lane, so a
// region derived from the group's left edge instead of the member's own offset
// lands one pill width too far left, and a group the lane or the row width pushed
// aside leaves an unclickable member behind a live region.
func TestStatusCopyRegionsMatchDrawnColumns(t *testing.T) {
	details := []string{
		"",
		"waiting for the first token",
		"streaming a long progress detail that eats the whole activity lane",
	}
	compactingBothVisible := false
	droppedWithDisplay := false
	for width := 30; width <= 200; width++ {
		for _, detail := range details {
			for _, compacting := range []bool{false, true} {
				backend := &sessionControlAgent{sessionSummary: &agent.SessionSummary{ID: "1775115074902"}}
				m := NewModelWithSize(backend, width, 24)
				m.workingDir = "/home/user/projects/checkout-name"
				m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityStreaming, AgentID: "main", Detail: detail}
				if compacting {
					m.compactionBgStatus = compactionBackgroundStatus{
						Active:    true,
						StartedAt: time.Now().Add(-95 * time.Second),
						Trigger:   agent.CompactionTriggerModelDriven,
						Bytes:     1234567,
						Events:    42,
					}
				}
				plain := stripANSI(m.renderStatusBar())
				if compacting && m.statusPath.display != "" && m.statusSession.display != "" {
					compactingBothVisible = true
				}
				for _, region := range []statusBarCopyRegionState{m.statusPath, m.statusSession} {
					if region.display != "" && region.endX <= region.startX {
						droppedWithDisplay = true
					}
					assertStatusRegionDrawn(t, width, detail, compacting, plain, region)
				}
			}
		}
	}
	if !compactingBothVisible {
		t.Fatal("no compacting layout kept both copy targets visible, so the region check proved nothing")
	}
	if !droppedWithDisplay {
		t.Fatal("no layout dropped a composed member, so the empty-region check proved nothing")
	}
}

// assertStatusRegionDrawn checks one recorded region against the rendered row: a
// live region must hold exactly the member's visible text, and a member the row
// does not draw must have no columns left to click.
func assertStatusRegionDrawn(t *testing.T, width int, detail string, compacting bool, plain string, region statusBarCopyRegionState) {
	t.Helper()
	if region.display == "" {
		return
	}
	where := fmt.Sprintf("width=%d detail=%q compacting=%v", width, detail, compacting)
	if region.endX <= region.startX {
		if strings.Contains(plain, region.display) {
			t.Fatalf("%s: region is empty but the row still shows %q (row %q)", where, region.display, plain)
		}
		return
	}
	visible := region.endX - region.startX
	displayWidth := ansi.StringWidth(region.display)
	want := ansi.Cut(region.display, displayWidth-visible, displayWidth)
	if got := ansi.Cut(plain, region.startX, region.endX); got != want {
		t.Fatalf("%s: region [%d,%d) drew %q, want %q (row %q)", where, region.startX, region.endX, got, want, plain)
	}
}

// The activity lane replaces the front of the right-side group in place: the
// members it leaves visible keep their own columns instead of shifting left, and
// a member the lane ate entirely loses its clickable columns. The status bar's
// own width never budgets a member to survive a cut, so this pins the placement
// contract on a synthetic row rather than through a layout.
func TestStatusBarRightPlacementRegionsFollowCutRow(t *testing.T) {
	const (
		leftWidth      = 4
		effectiveWidth = 40
		rightStart     = 10
		pillText       = "PILL"
		pathText       = "path/her"
		sessionText    = "SID 1234"
	)
	gap := lipgloss.Width(statusBarActivityPathGap)
	offsets := statusBarRightOffsets{
		path:    len(pillText) + gap,
		session: len(pillText) + gap + len(pathText) + gap,
		jobs:    -1,
	}
	group := pillText + statusBarActivityPathGap + pathText + statusBarActivityPathGap + sessionText
	if got := ansi.StringWidth(group); got != effectiveWidth-rightStart {
		t.Fatalf("fixture group width %d does not fill the row's right side %d", got, effectiveWidth-rightStart)
	}

	for _, tc := range []struct {
		name          string
		activityWidth int
		wantPill      string
	}{
		{name: "lane clips the pill's head", activityWidth: 8, wantPill: ""},
		{name: "lane leaves the pill's tail", activityWidth: 6, wantPill: "LL"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			line, placement := renderStatusBarPlacedLine("LEFT", leftWidth, rightStart, group, strings.Repeat("a", tc.activityWidth), tc.activityWidth, effectiveWidth)
			if !placement.drawn || placement.groupStart == 0 {
				t.Fatalf("setup produced no lane cut: %+v (row %q)", placement, line)
			}
			if placement.rowStart+placement.groupEnd-placement.groupStart != effectiveWidth {
				t.Fatalf("group does not end at the row's right edge: %+v (row %q)", placement, line)
			}
			for _, member := range []struct {
				name    string
				offset  int
				display string
				want    string
			}{
				{name: "pill", offset: 0, display: pillText, want: tc.wantPill},
				{name: "path", offset: offsets.path, display: pathText, want: pathText},
				{name: "session", offset: offsets.session, display: sessionText, want: sessionText},
			} {
				startX, endX := placement.region(member.offset, member.display)
				got := ""
				if endX > startX {
					got = ansi.Cut(line, startX-statusBarLeftMargin, endX-statusBarLeftMargin)
				}
				if got != member.want {
					t.Fatalf("%s: region [%d,%d) drew %q, want %q (row %q)", member.name, startX, endX, got, member.want, line)
				}
				// The lane cut the group's head in place, so a member it left whole
				// must still sit at the columns it had without the lane.
				if member.want == member.display {
					wantStart := statusBarLeftMargin + rightStart + member.offset
					if startX != wantStart || endX != wantStart+ansi.StringWidth(member.display) {
						t.Fatalf("%s: region [%d,%d) moved from its columns [%d,%d) (row %q)", member.name, startX, endX, wantStart, wantStart+ansi.StringWidth(member.display), line)
					}
				}
			}
			if tc.wantPill == "" && strings.Contains(stripANSI(line), pillText) {
				t.Fatalf("row still shows %q although the lane cut the whole pill: %q", pillText, line)
			}
		})
	}
}
