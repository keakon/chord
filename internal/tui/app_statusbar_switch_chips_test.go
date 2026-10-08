package tui

import (
	"maps"
	"regexp"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/agent"
)

// Status bar switches (LOOP / YOLO / MEMORY / PERSIST-FAIL) render as chips:
// a themed fill plus one space of padding groups each switch into one token,
// and they are the only status bar elements that carry a background.
var statusBarChipBackgroundPattern = regexp.MustCompile(`48;5;([0-9]+)`)

type statusBarChipSpec struct {
	padded string // plain chip text after padding is applied
	style  string // exact styled segment the status bar must embed
	bg     string // theme color index expected behind the chip
}

func statusBarChipBackgrounds(s string) map[string]bool {
	got := map[string]bool{}
	for _, match := range statusBarChipBackgroundPattern.FindAllStringSubmatch(s, -1) {
		got[match[1]] = true
	}
	return got
}

func assertStatusBarSwitchChips(t *testing.T, out string, chips ...statusBarChipSpec) {
	t.Helper()
	plain := stripANSI(out)
	wantBgs := map[string]bool{}
	for _, chip := range chips {
		if !strings.Contains(plain, chip.padded) {
			t.Fatalf("status bar plain text %q does not contain padded chip %q", plain, chip.padded)
		}
		if !strings.Contains(out, chip.style) {
			t.Fatalf("status bar does not contain styled chip %q; got %q", chip.style, out)
		}
		wantBgs[chip.bg] = true
	}
	gotBgs := statusBarChipBackgrounds(out)
	if !maps.Equal(gotBgs, wantBgs) {
		t.Fatalf("status bar background colors = %v, want exactly the chip surfaces %v; got %q", gotBgs, wantBgs, out)
	}
}

func TestStatusBarSwitchChipsCarryGroupingSurface(t *testing.T) {
	t.Run("enabled switches", func(t *testing.T) {
		backend := &sessionControlAgent{
			loopState:         agent.LoopStateExecuting,
			loopIteration:     2,
			loopMaxIterations: 5,
			yoloEnabled:       true,
			memoryEnabled:     true,
		}
		m := NewModelWithSize(backend, 180, 24)

		assertStatusBarSwitchChips(t, m.renderStatusBar(),
			statusBarChipSpec{
				padded: " LOOP 2/5 ",
				style:  StatusChipStyle.Render("LOOP 2/5"),
				bg:     currentTheme.StatusBg,
			},
			statusBarChipSpec{
				padded: " YOLO ",
				style:  StatusChipWarnStyle.Render("YOLO"),
				bg:     currentTheme.InfoPanelDiagWarnFg,
			},
			statusBarChipSpec{
				padded: " MEMORY ",
				style:  StatusChipStyle.Render("MEMORY"),
				bg:     currentTheme.StatusBg,
			},
		)
	})

	t.Run("degraded switches", func(t *testing.T) {
		backend := &sessionControlAgent{
			yoloEnabled:    true,
			memoryDegraded: true,
		}
		m := NewModelWithSize(backend, 180, 24)
		m.persistenceDegraded = true

		assertStatusBarSwitchChips(t, m.renderStatusBar(),
			statusBarChipSpec{
				padded: " YOLO ",
				style:  StatusChipWarnStyle.Render("YOLO"),
				bg:     currentTheme.InfoPanelDiagWarnFg,
			},
			statusBarChipSpec{
				padded: " MEMORY-FAIL ",
				style:  StatusChipErrorStyle.Render("MEMORY-FAIL"),
				bg:     currentTheme.ErrorFg,
			},
			statusBarChipSpec{
				padded: " PERSIST-FAIL ",
				style:  StatusChipErrorStyle.Render("PERSIST-FAIL"),
				bg:     currentTheme.ErrorFg,
			},
		)
	})
}
