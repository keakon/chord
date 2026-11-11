package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/keakon/x/ansi"

	"github.com/keakon/chord/internal/analytics"
)

func TestFormatUsageThroughput(t *testing.T) {
	second := int64(time.Second)
	cases := []struct {
		name              string
		tokens, requestNs int64
		want              string
	}{
		{"no tokens", 0, second, "—"},
		{"no duration", 100, 0, "—"},
		{"integer range", 80, second, "80"},
		{"large", 100, second, "100"},
		{"one decimal below ten", 5, second, "5.0"},
		{"slow one decimal", 1, 2 * second, "0.5"},
	}
	for _, tc := range cases {
		if got := formatUsageThroughput(tc.tokens, tc.requestNs); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestFormatUsageFirstOutput(t *testing.T) {
	cases := []struct {
		name             string
		totalNs, samples int64
		want             string
	}{
		{"no samples", int64(800 * time.Millisecond), 0, "—"},
		{"milliseconds", int64(800 * time.Millisecond), 1, "800ms"},
		{"seconds", int64(1200 * time.Millisecond), 1, "1.2s"},
		{"minutes", int64(90 * time.Second), 1, "1m30s"},
	}
	for _, tc := range cases {
		if got := formatUsageFirstOutput(tc.totalNs, tc.samples); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestUsageSpeedCells(t *testing.T) {
	second := int64(time.Second)
	cells := usageSpeedCells(100, second, 1, int64(300*time.Millisecond), 1)
	want := []string{"100", "300ms", "1"}
	for i, w := range want {
		if cells[i] != w {
			t.Fatalf("cell %d = %q, want %q", i, cells[i], w)
		}
	}

	empty := usageSpeedCells(0, 0, 0, 0, 0)
	if empty[0] != "—" || empty[1] != "—" || empty[2] != "0" {
		t.Fatalf("empty cells = %v, want dashes with zero samples", empty)
	}
}

func TestUsageSpeedColumns(t *testing.T) {
	columns := usageSpeedColumns()
	wantTitles := []string{"tok/s", "TTFT", "Samples"}
	if len(columns) != len(wantTitles) {
		t.Fatalf("columns = %d, want %d", len(columns), len(wantTitles))
	}
	for i, w := range wantTitles {
		if columns[i].Title != w {
			t.Fatalf("column %d title = %q, want %q", i, columns[i].Title, w)
		}
	}
}

func TestUsageModelItemsCarrySpeedCells(t *testing.T) {
	second := int64(time.Second)
	models := map[string]*analytics.ModelStats{
		"provider/model-1": {Calls: 2, OutputTokens: 200, ThroughputTokens: 200, ThroughputNs: 2 * second, ThroughputSamples: 2, FirstOutputTotalNs: int64(600 * time.Millisecond), FirstOutputSamples: 2},
		"provider/model-2": {Calls: 1, OutputTokens: 10},
	}
	items := usageModelItems(models)
	if len(items) != 2 {
		t.Fatalf("items = %d, want 2", len(items))
	}
	byLabel := map[string][]string{}
	for _, item := range items {
		byLabel[item.Label] = item.Cells
	}
	fast := byLabel["provider/model-1"]
	if len(fast) != 8 {
		t.Fatalf("fast model cells = %d, want 8", len(fast))
	}
	if fast[5] != "100" || fast[6] != "300ms" || fast[7] != "2" {
		t.Fatalf("speed cells = %v, want [100 300ms 2]", fast[5:])
	}
	slow := byLabel["provider/model-2"]
	if slow[5] != "—" || slow[6] != "—" || slow[7] != "0" {
		t.Fatalf("slow model speed cells = %v, want dashes with zero samples", slow[5:])
	}
}

func TestUsageModelsKeepNamesAndMetricsAcrossWidths(t *testing.T) {
	second := int64(time.Second)
	models := map[string]*analytics.ModelStats{
		"provider/sample-model": {Calls: 2, OutputTokens: 200, ThroughputTokens: 200, ThroughputNs: 2 * second, ThroughputSamples: 2, FirstOutputTotalNs: int64(600 * time.Millisecond), FirstOutputSamples: 2},
	}
	for _, width := range []int{40, 64, 80, 120} {
		lines := renderUsageModels(usageModelItems(models), width)
		text := ansi.Strip(strings.Join(lines, "\n"))
		for _, want := range []string{"provider/sample-model", "Calls", "Cost", "tok/s", "TTFT", "Samples", "100", "300ms"} {
			if !strings.Contains(text, want) {
				t.Errorf("width %d missing %q:\n%s", width, want, text)
			}
		}
		for _, line := range lines {
			if w := ansi.StringWidth(line); w > width {
				t.Fatalf("line width %d exceeds %d: %q", w, width, line)
			}
		}
	}
}

func TestUsageModelsAt80ColumnTerminal(t *testing.T) {
	stats := analytics.ModelStats{
		Calls: 1, InputTokens: 100, OutputTokens: 50,
		ThroughputTokens: 50, ThroughputNs: int64(time.Second), ThroughputSamples: 1,
		FirstOutputTotalNs: int64(300 * time.Millisecond), FirstOutputSamples: 1,
	}
	m := NewModel(&sessionControlAgent{usageStats: analytics.SessionStats{
		ByModel: map[string]*analytics.ModelStats{"provider/model-1": &stats},
	}})
	t.Cleanup(func() { _ = m.Close() })
	m.width, m.height = 80, 24
	m.usageStats.view = statsViewModels
	m.usageStats.projectReport = &analytics.ProjectUsageReport{
		ByModelRef: map[string]*analytics.UsageAggregate{
			"provider/model-1": {
				LLMCalls: 1, InputTokens: 100, OutputTokens: 50,
				ThroughputTokens: 50, ThroughputNs: int64(time.Second), ThroughputSamples: 1,
				FirstOutputTotalNs: int64(300 * time.Millisecond), FirstOutputSamples: 1,
			},
		},
	}
	for _, scope := range []statsScope{statsScopeSession, statsScopeProject} {
		m.usageStats.scope = scope
		m.usageStats.renderVersion++
		lines := m.usageStatsLines(m.usageStatsInnerWidth())
		text := ansi.Strip(strings.Join(lines, "\n"))
		for _, want := range []string{"provider/model-1", "tok/s 50", "TTFT 300ms", "Samples 1"} {
			if !strings.Contains(text, want) {
				t.Errorf("scope %s missing %q:\n%s", scope.label(), want, text)
			}
		}
		for _, line := range lines {
			if width := ansi.StringWidth(line); width > m.usageStatsInnerWidth() {
				t.Fatalf("scope %s: width %d exceeds inner width: %q", scope.label(), width, line)
			}
		}
	}
}
