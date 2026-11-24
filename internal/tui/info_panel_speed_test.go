package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/keakon/x/ansi"

	"github.com/keakon/chord/internal/analytics"
	"github.com/keakon/chord/internal/config"
)

func usageSpeedStats() analytics.SessionStats {
	return analytics.SessionStats{ByModel: map[string]*analytics.ModelStats{
		"provider/model-1":      {ThroughputTokens: 200, ThroughputNs: int64(time.Second), ThroughputSamples: 2, FirstOutputTotalNs: int64(4 * time.Second), FirstOutputSamples: 2},
		"provider/model-2":      {ThroughputTokens: 100, ThroughputNs: int64(time.Second), ThroughputSamples: 1, FirstOutputTotalNs: int64(2500 * time.Millisecond), FirstOutputSamples: 1},
		"other/model-1":         {ThroughputTokens: 50, ThroughputNs: int64(time.Second), ThroughputSamples: 1, FirstOutputTotalNs: int64(3 * time.Second), FirstOutputSamples: 1},
		"provider/model-1@high": {ThroughputTokens: 25, ThroughputNs: int64(time.Second), ThroughputSamples: 1, FirstOutputTotalNs: int64(5 * time.Second), FirstOutputSamples: 1},
		"provider/model-fast":   {FirstOutputTotalNs: int64(40 * time.Millisecond), FirstOutputSamples: 1},
		"provider/model-slow":   {FirstOutputTotalNs: int64(72 * time.Second), FirstOutputSamples: 1},
	}}
}

func stripInfoPanelLinePadding(s string) string {
	lines := strings.Split(ansi.Strip(s), "\n")
	for i := range lines {
		lines[i] = strings.TrimSpace(lines[i])
	}
	return strings.Join(lines, "\n")
}

func TestModelSpeedLineSelectsExactModelRef(t *testing.T) {
	for _, tt := range []struct {
		ref  string
		want string
	}{
		{"provider/model-1", "tok/s: 200\nTTFT: 2.0s"},
		{"provider/model-2", "tok/s: 100\nTTFT: 2.5s"},
		{"other/model-1", "tok/s: 50\nTTFT: 3.0s"},
		{"provider/model-1@high", "tok/s: 25\nTTFT: 5.0s"},
		{"provider/model-fast", "TTFT: 40ms"},
		{"provider/model-slow", "TTFT: 72s"},
	} {
		t.Run(tt.ref, func(t *testing.T) {
			text := stripInfoPanelLinePadding(renderModelSpeedLine(40, usageSpeedStats(), tt.ref))
			if text != tt.want {
				t.Fatalf("speed line = %q, want %q", text, tt.want)
			}
		})
	}
}

func TestModelSpeedLineTTFTUnitBoundaries(t *testing.T) {
	const ref = "provider/model"
	for _, tt := range []struct {
		name    string
		total   time.Duration
		samples int64
		want    string
	}{
		{"below 1ms", 2*time.Millisecond - time.Nanosecond, 2, "tok/s: 50"},
		{"at 1ms", 2 * time.Millisecond, 2, "tok/s: 50\nTTFT: 1ms"},
		{"fractional ms", 83 * time.Millisecond, 2, "tok/s: 50\nTTFT: 41ms"},
		{"below 100ms", 200*time.Millisecond - time.Nanosecond, 2, "tok/s: 50\nTTFT: 99ms"},
		{"at 100ms", 200 * time.Millisecond, 2, "tok/s: 50\nTTFT: 0.1s"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			stats := analytics.SessionStats{ByModel: map[string]*analytics.ModelStats{
				ref: {ThroughputTokens: 50, ThroughputNs: int64(time.Second), ThroughputSamples: 1, FirstOutputTotalNs: int64(tt.total), FirstOutputSamples: tt.samples},
			}}
			if text := stripInfoPanelLinePadding(renderModelSpeedLine(40, stats, ref)); text != tt.want {
				t.Fatalf("speed line = %q, want %q", text, tt.want)
			}
		})
	}
}

func TestModelSpeedLineHiddenWithoutSamples(t *testing.T) {
	if line := renderModelSpeedLine(40, analytics.SessionStats{}, "provider/model-1"); line != "" {
		t.Fatalf("empty stats rendered %q, want no line", line)
	}
	stats := analytics.SessionStats{ByModel: map[string]*analytics.ModelStats{
		"provider/model-1": {Calls: 3, OutputTokens: 500},
	}}
	if line := renderModelSpeedLine(40, stats, "provider/model-1"); line != "" {
		t.Fatalf("no throughput samples rendered %q, want no line", line)
	}
	stats.ByModel["provider/model-1"].FirstOutputTotalNs = int64(2400 * time.Millisecond)
	stats.ByModel["provider/model-1"].FirstOutputSamples = 1
	if text := stripInfoPanelLinePadding(renderModelSpeedLine(40, stats, "provider/model-1")); text != "TTFT: 2.4s" {
		t.Fatalf("TTFT-only sample rendered %q, want TTFT: 2.4s", text)
	}
	stats.ByModel["provider/model-1"].FirstOutputTotalNs = int64(10 * time.Second)
	if text := stripInfoPanelLinePadding(renderModelSpeedLine(40, stats, "provider/model-1")); text != "TTFT: 10s" {
		t.Fatalf("10-second TTFT rendered %q, want TTFT: 10s", text)
	}
	stats.ByModel["provider/model-1"].FirstOutputTotalNs = int64(9900 * time.Millisecond)
	if text := stripInfoPanelLinePadding(renderModelSpeedLine(40, stats, "provider/model-1")); text != "TTFT: 9.9s" {
		t.Fatalf("TTFT below 10 seconds rendered %q, want TTFT: 9.9s", text)
	}
	stats.ByModel["provider/model-1"].FirstOutputTotalNs = int64(11 * time.Second)
	if text := stripInfoPanelLinePadding(renderModelSpeedLine(40, stats, "provider/model-1")); text != "TTFT: 11s" {
		t.Fatalf("TTFT above 10 seconds rendered %q, want TTFT: 11s", text)
	}
	if line := renderModelSpeedLine(40, usageSpeedStats(), "provider/model-3"); line != "" {
		t.Fatalf("unmeasured model rendered %q, want no line", line)
	}
	if line := renderModelSpeedLine(40, usageSpeedStats(), "provider/model-1@low"); line != "" {
		t.Fatalf("unmeasured variant rendered %q, want no line", line)
	}
}

func TestInfoPanelModelSpeedFollowsDisplayRef(t *testing.T) {
	backend := newAttemptRefAgent("provider/model-1", "provider/model-2")
	backend.state.PoolName = "default"
	backend.state.PoolNames = []string{"default"}
	backend.state.KeysConfirmed = 2
	backend.state.KeysTotal = 2
	backend.state.ServiceTier = config.ServiceTierFast
	backend.state.EffectiveTier = config.ServiceTierFast
	backend.usage = usageSpeedStats()
	backend.usage.OutputTokens = 375
	backend.usage.LLMCalls = 5
	m := NewModelWithSize(backend, 100, 24)

	text := stripInfoPanelLinePadding(m.buildInfoPanelModelBlock(40, backend.usage))
	if !strings.HasSuffix(text, "tok/s: 100\nTTFT: 2.5s") {
		t.Fatalf("model block = %q, want displayed model speed after pool, keys and tier", text)
	}
	for _, label := range []string{"Pool: default", "Keys: 2/2", "tier: fast"} {
		if !strings.Contains(text, label) {
			t.Fatalf("model block = %q, want %q before speed", text, label)
		}
	}
	if usage := ansi.Strip(m.buildInfoPanelUsageBlock(40, 34, backend.usage)); strings.Contains(usage, "tok/s") || !strings.Contains(usage, "TOKENS") {
		t.Fatalf("usage block = %q, want tokens without model speed", usage)
	}

	backend.state.DisplayRef = "other/model-1"
	text = stripInfoPanelLinePadding(m.buildInfoPanelModelBlock(40, backend.usage))
	if !strings.HasSuffix(text, "tok/s: 50\nTTFT: 3.0s") {
		t.Fatalf("model block after provider switch = %q, want other provider metrics", text)
	}
	backend.state.DisplayRef = "provider/model-3"
	if text := ansi.Strip(m.buildInfoPanelModelBlock(40, backend.usage)); strings.Contains(text, "tok/s") {
		t.Fatalf("model block without samples = %q, want hidden speed", text)
	}
}

func TestInfoPanelModelSpeedInvalidatesCachedPanel(t *testing.T) {
	backend := newAttemptRefAgent("provider/model-1", "provider/model-1")
	backend.usage = usageSpeedStats()
	m := NewModelWithSize(backend, 100, 40)
	first := m.renderInfoPanel(40, 40)
	backend.usage.ByModel["provider/model-1"].ThroughputNs = int64(2 * time.Second)
	second := m.renderInfoPanel(40, 40)
	if first == second || !strings.Contains(ansi.Strip(second), "tok/s: 100") {
		t.Fatalf("updated panel = %q, want speed refreshed to 100", ansi.Strip(second))
	}
	backend.usage.ByModel["provider/model-1"].FirstOutputTotalNs = int64(6 * time.Second)
	third := m.renderInfoPanel(40, 40)
	if second == third || !strings.Contains(ansi.Strip(third), "TTFT: 3.0s") {
		t.Fatalf("updated panel = %q, want TTFT refreshed to 3.0s", ansi.Strip(third))
	}
	backend.usage.ByModel["provider/model-1"].ThroughputSamples = 0
	if text := ansi.Strip(m.renderInfoPanel(40, 40)); strings.Contains(text, "tok/s") || !strings.Contains(text, "TTFT: 3.0s") {
		t.Fatalf("panel without throughput samples = %q, want TTFT only", text)
	}
	backend.usage.ByModel["provider/model-1"].FirstOutputSamples = 0
	if text := ansi.Strip(m.renderInfoPanel(40, 40)); strings.Contains(text, "TTFT") {
		t.Fatalf("panel without TTFT samples = %q, want TTFT hidden", text)
	}
}
